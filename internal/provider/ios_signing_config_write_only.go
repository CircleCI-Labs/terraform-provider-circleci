// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
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

	"terraform-provider-circleci/internal/circleci"
)

// This file holds the write-only pair `provisioning_profiles_wo` +
// `provisioning_profiles_wo_version` for circleci_ios_signing_config.
//
// It is the sibling of environment_variable_write_only.go,
// webhook_write_only.go and otel_exporter_write_only.go, and deliberately the
// same shape. The decisions written up at the top of
// environment_variable_write_only.go apply here unchanged and are not repeated:
//
//   - no PreferWriteOnlyAttribute() plan modifier;
//   - no RequiresReplace on the write-only attribute itself (it is null in both
//     plan and state, so the standard modifier's "if req.PlanValue.Equal(
//     req.StateValue) { return }" means it never fires) — replacement is driven
//     from `provisioning_profiles_wo_version`, which is persisted;
//   - no Default on the version;
//   - the write-only attribute cannot be Computed.
//
// WHY THERE IS A PARALLEL LIST RATHER THAN A WRITE-ONLY `blob`
//
// The secret is `provisioning_profiles[*].blob`, nested one level down inside a
// ListNestedAttribute. It cannot be made write-only where it is. From
// terraform-plugin-framework v1.19.0, resource/schema/list_nested_attribute.go:
//
//	if a.IsWriteOnly() && !fwschema.ContainsAllWriteOnlyChildAttributes(a) {
//	    resp.Diagnostics.Append(fwschema.InvalidWriteOnlyNestedAttributeDiag(req.Path))
//	}
//
// (lines 317-319; the diagnostic itself is in
// internal/fwschema/write_only_nested_attribute_validation.go, and reads "Every
// child attribute of a WriteOnly nested attribute must also have WriteOnly set
// to true".) Marking the *list* write-only therefore forces `file_name` to be
// write-only too, and marking only `blob` write-only inside a non-write-only
// list is the mirror image of the same problem: Terraform Core requires every
// write-only value to be null in the response, and a nested object cannot be
// half-nulled.
//
// The alternative to a parallel list would have been hoisting `file_name` and
// `blob` out of the nested block into two top-level attributes, which is the
// workaround the ecosystem reaches for. It is breaking, it caps the resource at
// one profile unless the two lists are zipped by index, and it makes the
// state-backed spelling worse in order to add the write-only one. A parallel
// list is additive and matches how every other write-only pair in this provider
// is introduced.
//
// ONE VERSION FOR THE WHOLE LIST
//
// `provisioning_profiles_wo_version` counts rotations of the set of profiles,
// not of any single profile. That follows from what the resource already is:
// `provisioning_profiles` carries listplanmodifier.RequiresReplace and
// listvalidator.SizeAtLeast(1), so adding, removing, renewing or reordering a
// profile is already one indivisible change that replaces the resource. A
// per-entry version would have to live inside the nested object, where it would
// itself have to be write-only — and a write-only version is useless, because
// nothing persists it to compare against.
//
// LOSING `file_name` FROM STATE COSTS NOTHING HERE
//
// Because the whole nested list must be write-only, `file_name` leaves state on
// this path too. Nothing depends on it being there:
//
//   - Read never sets it. setIOSSigningConfigState deliberately leaves
//     `provisioning_profiles` alone, because the API reports only `file_name` and
//     overwriting the list with entries carrying a null `blob` would contradict
//     the configured, non-null one.
//   - Import never sets it either, for the same reason `blob` is left null; the
//     resource's own test already passes `ImportStateVerifyIgnore:
//     []string{"provisioning_profiles"}` for the whole list rather than one field
//     of it.
//   - No computed attribute is derived from it. `certificate_file_name` and
//     `certificate_type` come from the referenced certificate, via
//     data.references.signing_certificate.
//   - The plural data source `circleci_ios_signing_configs` reports profile
//     names, but it reads them from the API's list response
//     (signingConfigProfileAttributes in internal/circleci/signing_config.go), not
//     from this resource's state, so it is unaffected by which spelling created
//     the configuration.
//
// vault#2900 DOES NOT APPLY
//
// There is no update route for a signing configuration at all: the routes served
// is GET/POST /signing/configs and DELETE /signing/configs/{id},
// iosSigningConfigResource.Update is an empty method, and every attribute
// carries RequiresReplace. Only Create ever sends a profile, so there is no
// "update triggered by an unrelated field" for a version-gated send to omit the
// blobs from. Nothing here is gated on the version anyway, which keeps the rule
// the same as on the other three resources.

// iosSigningProfilesWriteOnlyAttribute returns the `provisioning_profiles_wo`
// attribute: the same list as `provisioning_profiles`, with both of its children
// write-only because the framework requires all of them to be.
func iosSigningProfilesWriteOnlyAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "The provisioning profiles paired with the certificate, as a " +
			"write-only argument: Terraform sends them to CircleCI but never records them in state " +
			"or in a plan file. Requires Terraform 1.11 or later.\n\n" +
			"Because nothing derived from the list is stored, Terraform cannot see that it " +
			"changed. `provisioning_profiles_wo_version` is required alongside it, and must be " +
			"incremented every time any profile changes, or the new profiles are never sent — " +
			"incrementing it forces a new resource to be created, exactly as editing " +
			"`provisioning_profiles` does, since there is no update route.\n\n" +
			"Set exactly one of `provisioning_profiles` and `provisioning_profiles_wo`.\n\n" +
			"~> **`file_name` is write-only here too, and not by choice.** Every child of a " +
			"write-only nested attribute must itself be write-only, so `file_name` leaves state " +
			"on this path along with `blob`. Nothing in this provider depends on it being there: " +
			"the API never reports profile content back, this resource's `Read` and import both " +
			"leave the list alone, and the `circleci_ios_signing_configs` data source reads " +
			"profile names from CircleCI rather than from state.",
		Optional:  true,
		WriteOnly: true,
		Validators: []validator.List{
			listvalidator.SizeAtLeast(1),
			// Both directions are required, not just version-implies-profiles. A
			// `provisioning_profiles_wo` with no version can be created but can never be
			// rotated: every later edit to it is a no-op that Terraform reports as no
			// change at all. Refusing the configuration is the only way that surfaces.
			listvalidator.AlsoRequires(path.MatchRoot("provisioning_profiles_wo_version")),
		},
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"file_name": schema.StringAttribute{
					MarkdownDescription: "A display name for the profile, for example " +
						"`release.mobileprovision`. Limited to 40 characters by the API.",
					Required:  true,
					WriteOnly: true,
					Validators: []validator.String{
						stringvalidator.LengthBetween(1, 40),
					},
				},
				"blob": schema.StringAttribute{
					MarkdownDescription: "The profile's `.mobileprovision` file, base64-encoded " +
						"(standard encoding), for example " +
						"`filebase64(\"release.mobileprovision\")`.",
					Required:  true,
					WriteOnly: true,
					Sensitive: true,
				},
			},
		},
	}
}

// iosSigningProfilesWriteOnlyVersionAttribute returns the
// `provisioning_profiles_wo_version` attribute.
//
// The name is `_wo_version`, not `_wo_revision`: `_wo` is HashiCorp's documented
// naming convention, and the counter is spelled "version" by every provider that
// has shipped this pair except Kubernetes.
//
// It carries RequiresReplace, which is not a new behaviour on the write-only
// path but the existing one preserved: `provisioning_profiles` already forces
// replacement, because there is no update route. The modifier belongs on the
// version rather than on the list, whose plan and state values are both always
// null.
func iosSigningProfilesWriteOnlyVersionAttribute() schema.Int64Attribute {
	return schema.Int64Attribute{
		MarkdownDescription: "Rotation counter for `provisioning_profiles_wo`. Increment it " +
			"whenever anything in that list changes: a write-only value leaves no trace in state, " +
			"so this is the only thing Terraform has to compare, and editing " +
			"`provisioning_profiles_wo` on its own is not a change as far as Terraform is " +
			"concerned.\n\n" +
			"One counter covers the whole list, because the whole list is one unit: adding, " +
			"removing, renewing or reordering a profile already replaces the resource rather than " +
			"updating an entry.\n\n" +
			"Required when `provisioning_profiles_wo` is set, and must be at least 1. " +
			"Incrementing it forces a new resource to be created, with a new `id`.",
		Optional: true,
		PlanModifiers: []planmodifier.Int64{
			int64planmodifier.RequiresReplace(),
		},
		Validators: []validator.Int64{
			int64validator.AtLeast(1),
			// A version with no profiles is never useful, and there is no Default: 0 that
			// would make it optional-with-a-fallback.
			int64validator.AlsoRequires(path.MatchRoot("provisioning_profiles_wo")),
		},
	}
}

// iosSigningProfilesConfigValidator requires exactly one of
// `provisioning_profiles` and `provisioning_profiles_wo`.
//
// Exactly one rather than at most one, unlike circleci_otel_exporter's headers:
// a signing configuration with no provisioning profiles is not a thing CircleCI
// accepts, which is why `provisioning_profiles` was Required and carries
// SizeAtLeast(1). Making it Optional only moves where the refusal comes from.
func iosSigningProfilesConfigValidator() resource.ConfigValidator {
	return resourcevalidator.ExactlyOneOf(
		path.MatchRoot("provisioning_profiles"),
		path.MatchRoot("provisioning_profiles_wo"),
	)
}

// resolveIOSSigningProfiles returns the provisioning profiles to send to
// CircleCI, from whichever of the two attributes the configuration set.
//
// config, not plan. A write-only attribute is null in the plan by design — the
// framework nullifies it there and in state — so req.Plan hands back an empty
// slice for `provisioning_profiles_wo` however carefully it was written.
// Configuration is the only place the value exists.
//
// version is used only to explain the failure: it is persisted, so its presence
// identifies a configuration being managed through `provisioning_profiles_wo`.
func resolveIOSSigningProfiles(
	ctx context.Context,
	config tfsdk.Config,
	profiles []iosSigningConfigProfileModel,
	version types.Int64,
	diagnostics *diag.Diagnostics,
) ([]circleci.CreateSigningProvisioningProfile, bool) {
	if len(profiles) > 0 {
		return iosSigningProfileRequests(profiles), true
	}

	var writeOnly []iosSigningConfigProfileModel

	diagnostics.Append(config.GetAttribute(ctx, path.Root("provisioning_profiles_wo"), &writeOnly)...)

	if diagnostics.HasError() {
		return nil, false
	}

	if len(writeOnly) == 0 {
		// Erroring out is the whole point of this branch.
		// iosSigningProfilesConfigValidator already rejects a configuration that sets
		// neither list, so reaching here means the value went missing between
		// validation and apply — and carrying on would POST a configuration with an
		// empty provisioning_profiles, which the API accepts as a configuration that
		// can sign nothing.
		detail := "Neither `provisioning_profiles` nor `provisioning_profiles_wo` has a value at " +
			"apply time, so there is nothing to send to CircleCI."
		if !version.IsNull() {
			detail = "`provisioning_profiles_wo_version` is set, so this signing configuration's " +
				"provisioning profiles are managed through `provisioning_profiles_wo`, but " +
				"`provisioning_profiles_wo` has no value at apply time. Terraform supplies a " +
				"write-only value only during apply and stores nothing, and CircleCI never returns " +
				"a profile's content, so there is no previous value to fall back on.\n\nNo request " +
				"was sent: a signing configuration with no provisioning profiles cannot sign a " +
				"build."
		}

		diagnostics.AddError("Missing iOS provisioning profiles", detail)

		return nil, false
	}

	return iosSigningProfileRequests(writeOnly), true
}

// iosSigningProfileRequests converts either spelling of the profile list into
// the API request shape. The two models are the same struct, which is what makes
// the two paths produce byte-identical requests.
func iosSigningProfileRequests(profiles []iosSigningConfigProfileModel) []circleci.CreateSigningProvisioningProfile {
	requests := make([]circleci.CreateSigningProvisioningProfile, len(profiles))
	for i, p := range profiles {
		requests[i] = circleci.CreateSigningProvisioningProfile{
			FileName: p.FileName.ValueString(),
			Blob:     p.Blob.ValueString(),
		}
	}

	return requests
}
