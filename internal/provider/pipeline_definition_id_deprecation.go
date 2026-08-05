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
//
// WHY CHANGING THE DEFINITION REPLACES THE TRIGGER
//
// The definition id appears in exactly one route, the create:
//
//	POST  /projects/{project_id}/pipeline-definitions/{pipeline_definition_id}/triggers
//	PATCH /projects/{project_id}/triggers/{trigger_id}          <- no slot for a definition
//	GET   /projects/{project_id}/triggers/{trigger_id}           <- does not return one
//
// So a changed definition id used to plan an in-place Update, and the PATCH body has
// nowhere to put it: the API accepted the request, ignored the field, and reported
// success while the trigger stayed attached to the old definition. Read cannot
// detect that drift either — the definition is never returned, which is why the
// provider carries the value forward from state — so state confidently reported a
// value the API had discarded, for ever. Issue #29.
//
// RequiresReplaceIfConfigured, not RequiresReplace. Both replace on a genuine
// change, and neither replaces when the practitioner merely renames the attribute —
// but for different reasons, and only one of them is load-bearing on its own.
// RequiresReplaceIfConfigured declines because the configuration value for the name
// that was not written is null. Plain RequiresReplace declines only because
// UseStateForUnknown ran first and copied the prior value over the planned unknown,
// prior value included when that value is null. So plain RequiresReplace makes the
// no-op depend on the order of two plan modifiers on the same attribute, and the
// case where it bites is not hypothetical: `pipeline_definition_id` is null in every
// state written before 0.5.0, so with UseStateForUnknown removed, any plan that
// changes anything at all on such a trigger replaces it. Verified both ways in
// TestTriggerPlanUpgradingFrom04StateDoesNotReplace. Same reasoning, and the same
// choice, as org_id_deprecation.go.
//
// TestTriggerResourceUnit_SwitchingPipelineAttributeIsNoop is what holds the rename
// in place; TestTriggerResourceUnit_ChangingPipelineDefinitionIDReplaces is what
// proves a real change is no longer silently dropped.

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
			"`pipeline_definition_id` does not replace the trigger.\n\n" +
			"Changing the definition *id* does: a trigger is created under a definition and " +
			"there is no route that moves it to another one.",
		DeprecationMessage: pipelineDefinitionIDDeprecationMessage,
		Optional:           true,
		Computed:           true,
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.UseStateForUnknown(),
			// Only when this name is the one written: see the header comment.
			stringplanmodifier.RequiresReplaceIfConfigured(),
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
			"deprecated `pipeline_id`; set exactly one of the two.\n\n" +
			"~> **Changing this forces the trigger to be replaced.** A trigger is created " +
			"under a pipeline definition and CircleCI has no route that moves it to another " +
			"one — the update endpoint has no field for it. Replacement produces a new trigger " +
			"id, and for a `webhook` event source a new `event_source_web_hook_url`, so " +
			"anything posting to the old URL has to be repointed. Switching between this " +
			"attribute and the deprecated `pipeline_id` is not a change and replaces nothing.",
		Optional: true,
		Computed: true,
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.UseStateForUnknown(),
			// Only when this name is the one written: see the header comment.
			stringplanmodifier.RequiresReplaceIfConfigured(),
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
