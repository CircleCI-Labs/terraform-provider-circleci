// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

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

// This file holds the write-only credentials for circleci_ios_signing_certificate:
// `certificate_blob_wo` + `certificate_password_wo`, and the single
// `certificate_wo_version` that governs both.
//
// It is the third of these, after environment_variable_write_only.go and
// webhook_write_only.go, and deliberately the same shape: same `_wo` naming, same
// "resolve from config, never from plan" helper, same refusals. The four decisions
// written up at the top of environment_variable_write_only.go apply here unchanged
// and are not repeated:
//
//   - no PreferWriteOnlyAttribute() plan modifier;
//   - no RequiresReplace on a write-only attribute itself (it is null in both plan
//     and state always, so stringplanmodifier's opening "if req.PlanValue.Equal(
//     req.StateValue) { return }" means the modifier never fires — the framework's
//     own documentation is wrong about this for the standard modifier);
//   - no Default on the version;
//   - a write-only attribute cannot be Computed.
//
// The helpers in the other two files are not called from here, for the reason
// given in webhook_write_only.go: their attribute paths and their prose are
// specific to their own attribute names, and generalizing them would mean editing
// every existing call site to gain nothing but a shorter file. What is shared is
// the structure, which is what has to stay the same for all three pairs to mean
// the same thing to a practitioner.
//
// WHY THIS IS THE HIGHEST-VALUE ONE
//
// `certificate_blob` is the private key half of an Apple code-signing identity.
// On the state-backed path it sits in Terraform state in cleartext, so anyone who
// can read the state file can sign iOS builds as the organization. That is a
// materially worse exposure than an environment variable or a webhook secret, and
// it is why the write-only path is documented as the preferred one on this
// resource specifically.
//
// WHY ONE VERSION ATTRIBUTE, NOT ONE PER WRITE-ONLY ATTRIBUTE
//
// AWS and Azure spell this pair as one `_wo_version` per `_wo` attribute, and
// following that habit here would give `certificate_blob_wo_version` and
// `certificate_password_wo_version`. That would be wrong.
//
// A `.p12` and its password are a single rotatable unit. The password decrypts
// that specific file, so re-exporting a certificate always produces both a new
// blob and a new password; there is no such thing as rotating one without the
// other. Two counters would make an invalid combination expressible: bump the
// blob's version and not the password's, and the provider would upload a new
// certificate with the old password, which cannot decrypt it. CircleCI would
// accept the upload and every signed build would fail later, somewhere else.
//
// The convention is really "one trigger per rotatable unit", and the counter-example
// proves it rather than contradicting it: the Kubernetes provider has separate
// `data_wo_revision` and `binary_data_wo_revision` because a Secret's string data
// and its binary data are independent secrets that genuinely rotate separately.
// These two are not.
//
// WHY REPLACEMENT IS THE RIGHT TRIGGER HERE
//
// Unlike the other two resources, this one is already immutable: there is no
// update route on the API, iosSigningCertificateResource.Update is a no-op, and
// every configurable attribute carries RequiresReplace. So `certificate_wo_version`
// carrying RequiresReplace introduces no new concept — replacement is already the
// only way anything about a certificate changes. It is also load-bearing: without
// it a version bump would plan an update, Terraform would call the no-op Update,
// and the apply would report success having uploaded nothing.
//
// ALL-OR-NOTHING, IN EVERY DIRECTION
//
// The three write-only attributes are all-or-nothing, and each one requires the
// other two explicitly. AlsoRequires is one-directional — it validates only when
// the attribute carrying it is set — so declaring the relationship once is not
// enough. A `certificate_blob_wo` with no `certificate_wo_version` would validate
// cleanly, upload once, and then be unrotatable forever: every later edit to the
// blob is a no-op that Terraform reports as no change at all. Refusing the
// configuration is the only way that surfaces.
//
// The same pairing is preserved on the state-backed path, where `certificate_blob`
// and `certificate_password` were both Required before either became Optional:
// see their Validators in ios_signing_certificate_resource.go.

// iosSigningCertificateBlobWriteOnlyAttribute returns the `certificate_blob_wo`
// attribute.
func iosSigningCertificateBlobWriteOnlyAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: "The certificate's `.p12` file, base64-encoded (standard encoding), " +
			"as a write-only argument: Terraform sends it to CircleCI but never records it in " +
			"state or in a plan file. Requires Terraform 1.11 or later. This is the preferred " +
			"way to supply a signing certificate — see the resource-level \"Security\" section.\n\n" +
			"Because nothing derived from the certificate is stored, Terraform cannot see that it " +
			"changed. `certificate_password_wo` and `certificate_wo_version` are both required " +
			"alongside it, and the version must be incremented every time the certificate " +
			"changes, or the new certificate is never sent — incrementing it replaces the " +
			"resource, because CircleCI has no route that updates a signing certificate in " +
			"place.\n\n" +
			"Set exactly one of `certificate_blob` and `certificate_blob_wo`.",
		Optional:  true,
		WriteOnly: true,
		Sensitive: true,
		Validators: []validator.String{
			// Every direction, not just blob-implies-the-rest: see the note at the top
			// of this file. A blob with no version can be created but never rotated.
			stringvalidator.AlsoRequires(
				path.MatchRoot("certificate_password_wo"),
				path.MatchRoot("certificate_wo_version"),
			),
		},
	}
}

// iosSigningCertificatePasswordWriteOnlyAttribute returns the
// `certificate_password_wo` attribute.
func iosSigningCertificatePasswordWriteOnlyAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: "The password that unlocks `certificate_blob_wo`, as a write-only " +
			"argument: Terraform sends it to CircleCI but never records it in state or in a plan " +
			"file. Requires Terraform 1.11 or later.\n\n" +
			"Required alongside `certificate_blob_wo`, and covered by the same " +
			"`certificate_wo_version`: a `.p12` and the password that decrypts it are one " +
			"rotatable unit, so there is no counter of its own to bump.",
		Optional:  true,
		WriteOnly: true,
		Sensitive: true,
		Validators: []validator.String{
			stringvalidator.AlsoRequires(
				path.MatchRoot("certificate_blob_wo"),
				path.MatchRoot("certificate_wo_version"),
			),
		},
	}
}

// iosSigningCertificateWriteOnlyVersionAttribute returns the
// `certificate_wo_version` attribute.
//
// The name is `_wo_version`, not `_wo_revision`: `_wo` is HashiCorp's documented
// naming convention, and the counter is spelled "version" by every provider that
// has shipped this pair except Kubernetes. It is `certificate_wo_version` rather
// than `certificate_blob_wo_version` because it governs the blob and the password
// together — see the top of this file.
func iosSigningCertificateWriteOnlyVersionAttribute() schema.Int64Attribute {
	return schema.Int64Attribute{
		MarkdownDescription: "Rotation counter for `certificate_blob_wo` and " +
			"`certificate_password_wo`. Increment it whenever either changes: a write-only value " +
			"leaves no trace in state, so this is the only thing Terraform has to compare, and " +
			"changing `certificate_blob_wo` on its own is not a change as far as Terraform is " +
			"concerned.\n\n" +
			"One counter covers both values because a `.p12` and its password are a single " +
			"rotatable unit — the password decrypts that specific file, so re-exporting a " +
			"certificate always produces a new pair. Separate counters would let you send a new " +
			"certificate with the old password.\n\n" +
			"Required when `certificate_blob_wo` is set, and must be at least 1. Incrementing it " +
			"forces a new resource to be created, which uploads the replacement certificate and " +
			"deletes the old one; the API has no update route.",
		Optional: true,
		Validators: []validator.Int64{
			int64validator.AtLeast(1),
			// A version with no certificate is never useful, and there is no Default: 0
			// that would make it optional-with-a-fallback.
			int64validator.AlsoRequires(
				path.MatchRoot("certificate_blob_wo"),
				path.MatchRoot("certificate_password_wo"),
			),
		},
		PlanModifiers: []planmodifier.Int64{
			// Load-bearing, and the only place RequiresReplace can usefully live on
			// this path: the write-only attributes are null in both plan and state, so
			// a plan modifier on either of them never fires. Without this a version
			// bump plans an update, Update is a no-op on this resource, and the apply
			// succeeds having uploaded nothing.
			int64planmodifier.RequiresReplace(),
		},
	}
}

// iosSigningCertificateBlobConfigValidator requires exactly one of
// `certificate_blob` and `certificate_blob_wo`.
//
// Exactly one rather than at least one: both set would be ambiguous if they
// disagreed, and neither leaves the provider with no certificate to upload. This
// is what makes it safe for `certificate_blob` to have become Optional, which it
// had to for either name to be usable on its own.
//
// There is no matching validator for the two password attributes, and none is
// needed: each password is bound to its own blob (`certificate_password` by the
// validators in ios_signing_certificate_resource.go, `certificate_password_wo` by
// the ones above), so exactly one blob implies exactly one password. A crossed
// pair — `certificate_blob_wo` with `certificate_password` — is rejected because
// `certificate_password` requires `certificate_blob`, which this validator has
// already excluded.
func iosSigningCertificateBlobConfigValidator() resource.ConfigValidator {
	return resourcevalidator.ExactlyOneOf(
		path.MatchRoot("certificate_blob"),
		path.MatchRoot("certificate_blob_wo"),
	)
}

// resolveIOSSigningCertificateCredentials returns the certificate content and
// password to upload, from whichever pair of attributes the configuration set.
//
// config, not plan. A write-only attribute is null in the plan by design — the
// framework nullifies it there and in state — so req.Plan would hand back empty
// strings and this would upload an empty certificate. The configuration is the
// only place the values exist.
//
// version is used only to explain the failure: it is persisted, so its presence
// identifies a certificate already being managed through the write-only pair.
func resolveIOSSigningCertificateCredentials(
	ctx context.Context,
	config tfsdk.Config,
	blob types.String,
	password types.String,
	version types.Int64,
	diagnostics *diag.Diagnostics,
) (certBlob string, certPassword string, ok bool) {
	if !blob.IsNull() && !blob.IsUnknown() {
		if password.IsNull() || password.IsUnknown() {
			// The validators bind the two together, so this is the gap between
			// validation and apply. A certificate uploaded with the wrong password is
			// accepted by CircleCI and fails much later, in a build.
			diagnostics.AddError(
				"Missing iOS signing certificate password",
				"`certificate_blob` is set but `certificate_password` has no value at apply "+
					"time, so there is no password to upload with the certificate.",
			)

			return "", "", false
		}

		return blob.ValueString(), password.ValueString(), true
	}

	var writeOnlyBlob, writeOnlyPassword types.String

	diagnostics.Append(config.GetAttribute(ctx, path.Root("certificate_blob_wo"), &writeOnlyBlob)...)
	diagnostics.Append(config.GetAttribute(ctx, path.Root("certificate_password_wo"), &writeOnlyPassword)...)

	if diagnostics.HasError() {
		return "", "", false
	}

	if writeOnlyBlob.IsNull() || writeOnlyBlob.IsUnknown() {
		// Erroring out is the whole point of this branch, and there is nothing to fall
		// back on: CircleCI has no route that returns a certificate's content, so a
		// value missing at apply time cannot be recovered from anywhere. Carrying on
		// would POST an empty cert_blob, creating a certificate that signs nothing and
		// that a practitioner would have to notice from a failing build.
		detail := "Neither `certificate_blob` nor `certificate_blob_wo` has a value at apply " +
			"time, so there is nothing to upload to CircleCI."
		if !version.IsNull() {
			detail = "`certificate_wo_version` is set, so this certificate is managed through " +
				"`certificate_blob_wo`, but `certificate_blob_wo` has no value at apply time. " +
				"Terraform supplies a write-only value only during apply and stores nothing, and " +
				"CircleCI never returns a certificate's content, so there is no previous value to " +
				"fall back on.\n\nNothing was uploaded: a request with no certificate in it would " +
				"have created a signing certificate that cannot sign anything."
		}

		diagnostics.AddError("Missing iOS signing certificate", detail)

		return "", "", false
	}

	if writeOnlyPassword.IsNull() || writeOnlyPassword.IsUnknown() {
		diagnostics.AddError(
			"Missing iOS signing certificate password",
			"`certificate_blob_wo` has a value at apply time but `certificate_password_wo` does "+
				"not, so there is no password to upload with the certificate. Terraform supplies a "+
				"write-only value only during apply and stores nothing, so there is no previous "+
				"password to fall back on.",
		)

		return "", "", false
	}

	return writeOnlyBlob.ValueString(), writeOnlyPassword.ValueString(), true
}
