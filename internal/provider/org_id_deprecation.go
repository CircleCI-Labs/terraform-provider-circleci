// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/ephemeralvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	ephschema "github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// This file holds everything to do with accepting an organization under two
// attribute names, `organization_id` (deprecated) and `org_id`, while the old name
// is retired.
//
// It is one file on purpose. The pair appears on more than thirty resources, and
// when the deprecation ends the removal should be a small, obvious change rather
// than an audit of thirty schemas: delete deprecatedOrgIDAttribute, drop
// orgIDConfigValidator, make orgIDAttribute Required, and follow the compiler.
//
// WHY THE ATTRIBUTES LOOK THE WAY THEY DO
//
// `organization_id` carries RequiresReplace on most resources, because CircleCI has
// no route that moves an object between organizations. That makes the obvious
// implementation of this deprecation actively destructive: add `org_id` as a plain
// Optional attribute, and a practitioner who follows the deprecation notice removes
// `organization_id` from their configuration, Terraform plans it as null, sees a
// change, and destroys and recreates the resource. For circleci_project that deletes
// the project and its build history. The "non-breaking" migration would then be more
// destructive than the breaking rename it was meant to avoid.
//
// Two things prevent it, and both are necessary:
//
//   - Computed. When the attribute leaves the configuration, Terraform retains the
//     prior state value instead of planning null, so there is no change to react to.
//   - RequiresReplaceIfConfigured rather than RequiresReplace. It skips replacement
//     when the configuration value is null, while still replacing on a genuine change
//     from one organization to another.
//
// TestProjectResourceUnit_SwitchingOrgAttributeDoesNotReplace asserts a no-op plan
// across the switch, and the naive version fails it with
// "expected NoOp, got action(s): [delete create]".

// orgIDDeprecationMessage is the warning a practitioner sees on every plan that
// still uses the old name. It says explicitly that switching is safe, because the
// honest worry when told to rename something load-bearing is whether it will destroy
// anything.
const orgIDDeprecationMessage = "Use org_id instead. organization_id still works and " +
	"will keep working until the next major release; switching to org_id does not " +
	"replace this resource."

// orgIDDescription describes the organization attribute. Callers pass what the
// organization owns so the sentence reads naturally per resource.
func orgIDDescription(owns string) string {
	return "The unique identifier (UUID) of the organization that owns " + owns + ".\n\n" +
		"This is the same field as the deprecated `organization_id`; set exactly one of " +
		"the two."
}

// deprecatedOrgIDAttribute returns the `organization_id` attribute.
//
// replaces reports whether changing the organization replaces the resource, which is
// true wherever CircleCI has no route to move the object. Where it is false the
// attribute still needs to be Optional+Computed, so that switching names is not seen
// as a change.
func deprecatedOrgIDAttribute(owns string, replaces bool) schema.StringAttribute {
	attribute := schema.StringAttribute{
		MarkdownDescription: "The unique identifier (UUID) of the organization that owns " +
			owns + ".\n\n" +
			"~> **Deprecated in favour of `org_id`**, which matches CircleCI's own naming. " +
			"Both work and mean the same thing; set exactly one. Switching from this " +
			"attribute to `org_id` does not replace the resource.",
		DeprecationMessage: orgIDDeprecationMessage,
		Optional:           true,
		Computed:           true,
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.UseStateForUnknown(),
		},
	}

	if replaces {
		attribute.PlanModifiers = append(
			attribute.PlanModifiers,
			stringplanmodifier.RequiresReplaceIfConfigured(),
		)
	}

	return attribute
}

// orgIDAttribute returns the `org_id` attribute, the name to use going forward.
func orgIDAttribute(owns string, replaces bool) schema.StringAttribute {
	attribute := schema.StringAttribute{
		MarkdownDescription: orgIDDescription(owns) + "\n\nChanging this value forces a new " +
			"resource to be created.",
		Optional: true,
		Computed: true,
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.UseStateForUnknown(),
		},
	}

	if !replaces {
		attribute.MarkdownDescription = orgIDDescription(owns)

		return attribute
	}

	attribute.PlanModifiers = append(
		attribute.PlanModifiers,
		stringplanmodifier.RequiresReplaceIfConfigured(),
	)

	return attribute
}

// orgIDConfigValidator requires exactly one of the two names in configuration.
//
// Exactly one rather than at least one: both set would be ambiguous if they
// disagreed, and neither leaves the resource unaddressable. This inspects the
// *configuration*, so the Computed mirror value on the unused attribute does not
// count as set.
func orgIDConfigValidator() resource.ConfigValidator {
	return resourcevalidator.ExactlyOneOf(
		path.MatchRoot("organization_id"),
		path.MatchRoot("org_id"),
	)
}

// effectiveOrgID returns whichever of the two attributes the configuration set.
//
// Every code path that needs the organization must go through this. Reading only one
// of the two would silently break whichever half of the userbase used the other.
func effectiveOrgID(deprecated, current types.String) string {
	if !current.IsNull() && !current.IsUnknown() && current.ValueString() != "" {
		return current.ValueString()
	}

	return deprecated.ValueString()
}

// setOrgIDs writes the resolved organization onto both attributes.
//
// Both, not just the one the practitioner wrote. They are Computed, so leaving the
// unused one unknown would leave an unknown value in state after apply — which
// Terraform rejects — and populating only the configured one would make switching
// names look like a change.
func setOrgIDs(deprecated, current *types.String, orgID string) {
	*deprecated = types.StringValue(orgID)
	*current = types.StringValue(orgID)
}

// --- data sources and ephemeral resources ------------------------------------
//
// These need their own builders because the schema types are distinct packages,
// and because the hazard that shapes the resource attributes does not exist here:
// a data source is read fresh on every plan and an ephemeral resource holds no
// state, so there is nothing to replace and no prior value to retain. That makes
// these the simple case — both attributes plain Optional, exactly one required.

// deprecatedOrgIDDataSourceAttribute returns the `organization_id` attribute for a
// data source.
func deprecatedOrgIDDataSourceAttribute(reads string) dsschema.StringAttribute {
	return dsschema.StringAttribute{
		MarkdownDescription: "The unique identifier (UUID) of the organization to read " +
			reads + " from.\n\n" +
			"~> **Deprecated in favour of `org_id`**, which matches CircleCI's own naming. " +
			"Both work and mean the same thing; set exactly one.",
		DeprecationMessage: orgIDDeprecationMessage,
		Optional:           true,
	}
}

// orgIDDataSourceAttribute returns the `org_id` attribute for a data source.
func orgIDDataSourceAttribute(reads string) dsschema.StringAttribute {
	return dsschema.StringAttribute{
		MarkdownDescription: "The unique identifier (UUID) of the organization to read " +
			reads + " from.\n\nThis is the same field as the deprecated " +
			"`organization_id`; set exactly one of the two.",
		Optional: true,
	}
}

// orgIDDataSourceConfigValidator requires exactly one of the two names.
func orgIDDataSourceConfigValidator() datasource.ConfigValidator {
	return datasourcevalidator.ExactlyOneOf(
		path.MatchRoot("organization_id"),
		path.MatchRoot("org_id"),
	)
}

// deprecatedOrgIDEphemeralAttribute returns the `organization_id` attribute for an
// ephemeral resource.
func deprecatedOrgIDEphemeralAttribute(owns string) ephschema.StringAttribute {
	return ephschema.StringAttribute{
		MarkdownDescription: "The unique identifier (UUID) of the organization that owns " +
			owns + ".\n\n" +
			"~> **Deprecated in favour of `org_id`**, which matches CircleCI's own naming. " +
			"Both work and mean the same thing; set exactly one.",
		DeprecationMessage: orgIDDeprecationMessage,
		Optional:           true,
	}
}

// orgIDEphemeralAttribute returns the `org_id` attribute for an ephemeral resource.
func orgIDEphemeralAttribute(owns string) ephschema.StringAttribute {
	return ephschema.StringAttribute{
		MarkdownDescription: "The unique identifier (UUID) of the organization that owns " +
			owns + ".\n\nThis is the same field as the deprecated `organization_id`; set " +
			"exactly one of the two.",
		Optional: true,
	}
}

// orgIDEphemeralConfigValidator requires exactly one of the two names.
func orgIDEphemeralConfigValidator() ephemeral.ConfigValidator {
	return ephemeralvalidator.ExactlyOneOf(
		path.MatchRoot("organization_id"),
		path.MatchRoot("org_id"),
	)
}

// reconcileOrgIDPlan makes the two organization attributes agree in the plan.
//
// REQUIRED on any resource built with `replaces: false`. Without it that resource is
// broken, and the failure is not obvious from reading the schema.
//
// Both attributes are Optional+Computed, so the one the practitioner did not write is
// planned as its retained prior value. On a resource where changing the organization
// forces replacement that never matters, because a genuine change is a destroy and
// create. Where it does not force replacement, it is fatal: changing the organization
// from A to B plans `organization_id = B` and `org_id = A`, and no Update can satisfy
// that plan. Writing B to both contradicts the planned `org_id`; writing A to both
// contradicts the planned `organization_id`; persisting it verbatim leaves state
// naming two different organizations, and the next Read resolves to the stale one.
// Terraform reports:
//
//	Error: Provider produced inconsistent result after apply
//	.organization_id: was cty.StringVal("7777…") but now cty.StringVal("6666…")
//
// The plan is the only place the retained value can be corrected, and the value has
// to come from *config* — the plan cannot say which of the two names was written.
// orgIDPlanNeedsReconcile reports whether reconcileOrgIDPlan should run.
//
// Call this BEFORE reading the configuration. A destroy plans a null object *and* a
// null configuration, so `req.Config.Get` into a model struct fails there with a
// value-conversion error naming the model type — which reads like a schema bug rather
// than a missing guard. Asking first is the whole reason this is a separate function.
func orgIDPlanNeedsReconcile(req resource.ModifyPlanRequest) bool {
	return !req.Plan.Raw.IsNull() && !req.Config.Raw.IsNull()
}

func reconcileOrgIDPlan(
	ctx context.Context,
	resp *resource.ModifyPlanResponse,
	deprecated, current types.String,
) {
	organizationID := effectiveOrgID(deprecated, current)
	if organizationID == "" {
		// Not knowable yet — it depends on something else in this plan. Terraform
		// re-plans before apply with the value resolved, so reconcile then.
		return
	}

	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("organization_id"), organizationID)...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("org_id"), organizationID)...)
}

// computedOrgIDDataSourceAttributes returns the pair for a data source where the
// organization is an *output*, not an argument.
//
// Two data sources are like this: circleci_project and
// circleci_ios_signing_certificate both look up by something else and report the
// organization they found. The Optional builders above would be wrong here — they
// would make an output settable and break the lookup — but the pair still has to
// exist under both names, or dropping `organization_id` at the next major would
// silently remove an attribute with nothing to replace it.
//
// Both are Computed and always carry the same value. `organization_id` is not marked
// deprecated: a Computed attribute is something a practitioner *reads*, and warning
// them on every plan about a value they cannot control would be noise. It is simply
// documented as superseded.
func computedOrgIDDataSourceAttributes(of string) (deprecated, current dsschema.StringAttribute) {
	return dsschema.StringAttribute{
			MarkdownDescription: "Unique identifier (UUID) of the organization that owns " +
				of + ".\n\nSuperseded by `org_id`, which reports the same value and matches " +
				"CircleCI's own naming. Both are reported; prefer `org_id` in new " +
				"configurations.",
			Computed: true,
		}, dsschema.StringAttribute{
			MarkdownDescription: "Unique identifier (UUID) of the organization that owns " +
				of + ".",
			Computed: true,
		}
}
