// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// This file holds the write-only value pair, `value_wo` + `value_wo_version`,
// shared by circleci_context_environment_variable and
// circleci_project_environment_variable. Both resources exist to put a secret
// into CircleCI, and `value` records that secret in Terraform state in
// cleartext; `value_wo` is the same argument with nothing persisted.
//
// It is one file for the same reason org_id_deprecation.go is: the pair has to
// mean exactly the same thing on both resources, and three of the four
// decisions below are decisions *not* to do something obvious, which only stay
// made if they are written down once where both call sites can see them.
//
// WHY THE ATTRIBUTES LOOK THE WAY THEY DO
//
//   - No PreferWriteOnlyAttribute() plan modifier. The framework offers one, and
//     it emits a warning on every plan that uses `value` rather than `value_wo`.
//     Terraform core has asked providers not to: it fires on every run, and a
//     practitioner consuming a shared module cannot act on it. `value` keeps
//     working, undeprecated.
//
//   - No RequiresReplace on `value_wo`. The framework's own documentation says a
//     write-only attribute can carry it; that documentation is wrong for the
//     standard modifier. stringplanmodifier's requires_replace_if.go opens with
//     "if req.PlanValue.Equal(req.StateValue) { return }", and a write-only
//     attribute is null in both plan and state always, so the modifier never
//     fires and the resource silently keeps the old secret. Replacement, where
//     it is needed, is driven from `value_wo_version`, which is persisted.
//
//   - No Default on `value_wo_version`. Google's provider defaulted it to 0 and
//     had to undo that after it produced spurious recreations; it is Optional
//     with no default here, and required alongside `value_wo` instead.
//
//   - `value_wo` cannot be Computed. The framework rejects the combination.

// writeOnlyValueAttribute returns the `value_wo` attribute.
//
// replaces reports whether a rotation replaces the resource, which is true
// wherever CircleCI has no route that overwrites the value in place.
func writeOnlyValueAttribute(replaces bool) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: "The value of the environment variable, as a write-only argument: " +
			"Terraform sends it to CircleCI but never records it in state or in a plan file. " +
			"Requires Terraform 1.11 or later.\n\n" +
			"Because nothing derived from the value is stored, Terraform cannot see that it " +
			"changed. `value_wo_version` is required alongside it, and must be incremented every " +
			"time this value changes, or the new value is never sent — " +
			rotationEffect(replaces) + "\n\n" +
			"Set exactly one of `value` and `value_wo`.",
		Optional:  true,
		WriteOnly: true,
		Sensitive: true,
		Validators: []validator.String{
			// Both directions are required, not just version-implies-value. A
			// `value_wo` with no version can be created but can never be rotated:
			// every later edit to it is a no-op that Terraform reports as no change
			// at all. Refusing the configuration is the only way that surfaces.
			stringvalidator.AlsoRequires(path.MatchRoot("value_wo_version")),
		},
	}
}

// writeOnlyValueVersionAttribute returns the `value_wo_version` attribute.
//
// The name is `_wo_version`, not `_wo_revision`: `_wo` is HashiCorp's documented
// naming convention and every provider that shipped this pair except Kubernetes
// spells the counter "version".
func writeOnlyValueVersionAttribute(replaces bool) schema.Int64Attribute {
	attribute := schema.Int64Attribute{
		MarkdownDescription: "Rotation counter for `value_wo`. Increment it whenever `value_wo` " +
			"changes: a write-only value leaves no trace in state, so this is the only thing " +
			"Terraform has to compare, and changing `value_wo` on its own is not a change as " +
			"far as Terraform is concerned.\n\n" +
			"Required when `value_wo` is set, and must be at least 1. " +
			"Incrementing it " + rotationEffect(replaces),
		Optional: true,
		Validators: []validator.Int64{
			int64validator.AtLeast(1),
			// A version without a value is never useful. There is no Default: 0 to
			// make this optional-with-a-fallback — see the note at the top of this
			// file.
			int64validator.AlsoRequires(path.MatchRoot("value_wo")),
		},
	}

	if replaces {
		attribute.PlanModifiers = []planmodifier.Int64{
			int64planmodifier.RequiresReplace(),
		}
	}

	return attribute
}

// rotationEffect describes what bumping the version does, which differs by
// resource because only one of the two APIs has a route that updates in place.
func rotationEffect(replaces bool) string {
	if replaces {
		return "replaces the resource, because CircleCI has no route that updates a project " +
			"environment variable in place."
	}

	return "rewrites the value in place."
}

// envVarValueConfigValidator requires exactly one of `value` and `value_wo`.
//
// Exactly one rather than at least one: both set would be ambiguous if they
// disagreed, and neither leaves the provider with no secret to send. This is
// what makes it safe for `value` to have become Optional.
func envVarValueConfigValidator() resource.ConfigValidator {
	return resourcevalidator.ExactlyOneOf(
		path.MatchRoot("value"),
		path.MatchRoot("value_wo"),
	)
}

// resolveEnvVarValue returns the value to send to CircleCI, from whichever of
// the two attributes the configuration set.
//
// config, not plan. A write-only attribute is null in the plan by design — the
// framework nullifies it there and in state — so req.Plan would hand back an
// empty string and this would quietly write an empty secret. The configuration
// is the only place the value exists.
//
// version is used only to explain the failure: it is persisted, so its presence
// identifies a resource already being managed through `value_wo`.
func resolveEnvVarValue(
	ctx context.Context,
	config tfsdk.Config,
	value types.String,
	version types.Int64,
	diagnostics *diag.Diagnostics,
) (string, bool) {
	if !value.IsNull() && !value.IsUnknown() {
		return value.ValueString(), true
	}

	var writeOnly types.String

	diagnostics.Append(config.GetAttribute(ctx, path.Root("value_wo"), &writeOnly)...)

	if diagnostics.HasError() {
		return "", false
	}

	if writeOnly.IsNull() || writeOnly.IsUnknown() {
		// Erroring out is the whole point of this branch. envVarValueConfigValidator
		// already rejects a configuration setting neither attribute, so reaching
		// here means the value went missing between validation and apply — and
		// carrying on would send a request with no value in it, overwriting a live
		// secret with an empty string.
		detail := "Neither `value` nor `value_wo` has a value at apply time, so there is " +
			"nothing to send to CircleCI."
		if !version.IsNull() {
			detail = "`value_wo_version` is set, so this environment variable is managed " +
				"through `value_wo`, but `value_wo` has no value at apply time. Terraform " +
				"supplies a write-only value only during apply and stores nothing, so there " +
				"is no previous value to fall back on."
		}

		diagnostics.AddError("Missing environment variable value", detail)

		return "", false
	}

	return writeOnly.ValueString(), true
}

// envVarWrittenOutsideTerraform reports whether CircleCI's current updated_at
// is later than the one recorded after the provider's own last write.
//
// Both timestamps are compared as instants rather than as strings: they are read
// from two different routes, and a difference in formatting (fractional seconds,
// offset spelling) between them would otherwise read as drift on every refresh.
// Unparseable input falls back to inequality, which over-reports rather than
// letting a rotation through unnoticed.
func envVarWrittenOutsideTerraform(recorded, remote string) bool {
	recordedAt, recordedErr := time.Parse(time.RFC3339, recorded)
	remoteAt, remoteErr := time.Parse(time.RFC3339, remote)

	if recordedErr != nil || remoteErr != nil {
		return recorded != remote
	}

	return remoteAt.After(recordedAt)
}
