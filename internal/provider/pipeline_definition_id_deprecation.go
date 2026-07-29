// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// `pipeline_id` -> `pipeline_definition_id` on circleci_trigger, while the old name is
// retired.
//
// A trigger is created under a definition
// (`/projects/{project_id}/pipeline-definitions/{pipeline_id}/triggers`), so this
// attribute has always held a pipeline *definition* id. The same name on the
// run-scoped data sources held a pipeline *run* id, since renamed to `run_id`, so
// `pipeline_id` meant two opposite things depending on where it was written, and both
// are UUIDs, so nothing rejected the mix-up. The new name matches
// circleci_pipeline_definition, which is where the value comes from.
//
// Only the resource needs the pair. circleci_triggers has never been released, so its
// attribute is simply named `pipeline_definition_id` outright — see
// triggers_data_source.go.
//
// One file, like org_id_deprecation.go: ending the deprecation is deleting it and
// following the compiler.
//
// WHY THE RESOURCE ATTRIBUTES ARE Optional+Computed
//
// Plain Optional would be actively hostile. `pipeline_id` is Required today, so a
// practitioner following the deprecation notice deletes it, and Terraform plans the
// old attribute as null — a change on an attribute no Update can move a trigger
// across. Computed makes Terraform retain the prior state value instead, so the
// migration plans as nothing at all.
//
// That retention is also why reconcilePipelineDefinitionIDPlan is not optional:
// changing definitions plans the configured name as the new id and the other as the
// stale retained one, leaving state naming two different definitions. The plan is the
// only place that can be corrected, and the value has to come from *configuration*,
// which is the only thing that knows which of the two names was written.

// pipelineDefinitionIDDeprecationMessage is the warning shown on every plan that
// still uses the old name. It says switching destroys nothing, because that is the
// honest worry when told to rename something load-bearing.
const pipelineDefinitionIDDeprecationMessage = "Use pipeline_definition_id instead. " +
	"This attribute identifies a pipeline *definition*, not a pipeline run, and the old " +
	"name means a run id elsewhere in this provider. pipeline_id still works until the " +
	"next major release; switching to pipeline_definition_id does not replace anything."

// deprecatedTriggerPipelineIDAttribute returns the resource's `pipeline_id`.
func deprecatedTriggerPipelineIDAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: "Unique identifier (UUID) of the pipeline definition this trigger " +
			"creates pipeline runs from.\n\n" +
			"~> **Deprecated in favour of `pipeline_definition_id`.** This attribute has " +
			"always taken the id of a " +
			"[`circleci_pipeline_definition`](pipeline_definition), not of a pipeline run, " +
			"which is what `pipeline_id` means on the run-scoped data sources. Both names " +
			"work and mean the same thing; set exactly one. Switching to " +
			"`pipeline_definition_id` does not replace the trigger.",
		DeprecationMessage: pipelineDefinitionIDDeprecationMessage,
		Optional:           true,
		Computed:           true,
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.UseStateForUnknown(),
		},
	}
}

// triggerPipelineDefinitionIDAttribute returns the resource's
// `pipeline_definition_id`, the name to use going forward.
func triggerPipelineDefinitionIDAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: "Unique identifier (UUID) of the " +
			"[`circleci_pipeline_definition`](pipeline_definition) this trigger creates " +
			"pipeline runs from.\n\nThis is a pipeline **definition** — where to check out " +
			"code and where to find configuration — not a pipeline run. Same field as the " +
			"deprecated `pipeline_id`; set exactly one of the two.",
		Optional: true,
		Computed: true,
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.UseStateForUnknown(),
		},
	}
}

// pipelineDefinitionIDConfigValidator requires exactly one of the two names.
//
// Exactly one rather than at least one: both set would be ambiguous if they
// disagreed, and neither leaves the trigger with nothing to attach to. It inspects
// the *configuration*, so the Computed mirror value does not count as set.
func pipelineDefinitionIDConfigValidator() resource.ConfigValidator {
	return resourcevalidator.ExactlyOneOf(
		path.MatchRoot("pipeline_id"),
		path.MatchRoot("pipeline_definition_id"),
	)
}

// effectivePipelineDefinitionID returns whichever of the two names carries a value.
//
// Every path that needs the definition id must go through this, or half the userbase
// silently breaks.
func effectivePipelineDefinitionID(deprecated, current types.String) string {
	if !current.IsNull() && !current.IsUnknown() && current.ValueString() != "" {
		return current.ValueString()
	}

	if deprecated.IsUnknown() {
		return ""
	}

	return deprecated.ValueString()
}

// setPipelineDefinitionIDs writes the resolved definition id onto both attributes.
//
// Both, not just the configured one: they are Computed, so leaving the other unknown
// after apply is a value Terraform rejects, and leaving it stale makes switching names
// look like a change.
func setPipelineDefinitionIDs(deprecated, current *types.String, pipelineDefinitionID string) {
	*deprecated = types.StringValue(pipelineDefinitionID)
	*current = types.StringValue(pipelineDefinitionID)
}

// pipelineDefinitionIDPlanNeedsReconcile reports whether the reconciler should run.
//
// Call this BEFORE reading the configuration. A destroy plans a null object *and* a
// null configuration, and Get-ing that into the model fails with a value-conversion
// error naming the model type, which reads like a schema bug rather than a missing
// guard. Asking first is the whole reason this is a separate function.
func pipelineDefinitionIDPlanNeedsReconcile(req resource.ModifyPlanRequest) bool {
	return !req.Plan.Raw.IsNull() && !req.Config.Raw.IsNull()
}

// reconcilePipelineDefinitionIDPlan makes the two attributes agree in the plan.
func reconcilePipelineDefinitionIDPlan(
	ctx context.Context,
	resp *resource.ModifyPlanResponse,
	deprecated, current types.String,
) {
	pipelineDefinitionID := effectivePipelineDefinitionID(deprecated, current)
	if pipelineDefinitionID == "" {
		// Not knowable yet — it depends on something else in this plan. Terraform
		// re-plans before apply with the value resolved, so reconcile then.
		return
	}

	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("pipeline_id"), pipelineDefinitionID)...)
	resp.Diagnostics.Append(
		resp.Plan.SetAttribute(ctx, path.Root("pipeline_definition_id"), pipelineDefinitionID)...,
	)
}
