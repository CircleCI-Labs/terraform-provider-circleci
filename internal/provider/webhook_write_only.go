// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// This file holds the write-only pair `signing_secret_wo` +
// `signing_secret_wo_version` for circleci_webhook, and the tripwire on the
// masked secret a read returns.
//
// It is the sibling of environment_variable_write_only.go, and deliberately the
// same shape: same attribute-name convention, same validators in both
// directions, same "resolve from config, never from plan" helper, same refusals.
// The four decisions written up at the top of that file apply here unchanged and
// are not repeated:
//
//   - no PreferWriteOnlyAttribute() plan modifier;
//   - no RequiresReplace on the write-only attribute itself (it is null in both
//     plan and state, so stringplanmodifier's "if req.PlanValue.Equal(
//     req.StateValue) { return }" means the modifier never fires) — and here a
//     rotation is an in-place PUT anyway, so nothing needs replacing;
//   - no Default on the version;
//   - the write-only attribute cannot be Computed.
//
// The helpers there are not called from here because their attribute paths and
// their prose are `value`-specific; generalizing them would have meant editing
// both environment variable resources' call sites. What is shared is the
// structure, which is what has to stay the same for the two pairs to mean the
// same thing.
//
// WHAT IS DIFFERENT HERE, AND WHY IT MATTERS MORE
//
// The webhook update route is a full-replace PUT: UpdateWebhook sends a complete
// WebhookInput, so every field absent from the body is cleared server-side. That
// makes one otherwise-tempting optimization actively dangerous.
//
// The obvious-looking pattern — send the write-only secret only when
// `signing_secret_wo_version` has changed — is the shipped bug
// hashicorp/terraform-provider-vault#2900. There, an update triggered by an
// unrelated field omitted the write-only value, the endpoint was full-replace,
// and it silently wiped `token_reviewer_jwt`, breaking Kubernetes auth logins.
// The AWS provider gates on the same condition and gets away with it only
// because ModifyDBInstance is a *partial* update. Ours is not.
//
// So: the secret is sent on every write where the configuration has one, and
// nothing about the request body is conditional on the version. That is sound
// because a write-only value comes from *configuration*, which Terraform
// populates on every apply — not only on the apply that changed the version. An
// update triggered by `name` or `events` still has `signing_secret_wo` in hand.
//
// `signing_secret_wo_version` therefore has exactly one job: to make Terraform
// plan an update when nothing else changed. It never decides what goes into the
// request.
//
// And because it never decides that, the guard in resolveWebhookSigningSecret is
// mandatory rather than defensive: there is no read-modify-write fallback to
// recover with. GET /webhook/{id} returns the secret only as a mask (see
// circleci.WebhookSigningSecretMask), so a value missing at apply time cannot be
// re-sent from anywhere. Sending the request regardless would put "" in the
// body and delete a live signing secret.

// webhookSigningSecretWriteOnlyAttribute returns the `signing_secret_wo`
// attribute.
func webhookSigningSecretWriteOnlyAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: "The secret used to sign webhook payloads, as a write-only argument: " +
			"Terraform sends it to CircleCI but never records it in state or in a plan file. " +
			"Requires Terraform 1.11 or later.\n\n" +
			"Because nothing derived from the secret is stored, Terraform cannot see that it " +
			"changed. `signing_secret_wo_version` is required alongside it, and must be " +
			"incremented every time this value changes, or the new secret is never sent.\n\n" +
			"Set exactly one of `signing_secret` and `signing_secret_wo`.",
		Optional:  true,
		WriteOnly: true,
		Sensitive: true,
		Validators: []validator.String{
			// Both directions are required, not just version-implies-secret. A
			// `signing_secret_wo` with no version can be created but can never be
			// rotated: every later edit to it is a no-op that Terraform reports as no
			// change at all. Refusing the configuration is the only way that surfaces.
			stringvalidator.AlsoRequires(path.MatchRoot("signing_secret_wo_version")),
		},
	}
}

// webhookSigningSecretWriteOnlyVersionAttribute returns the
// `signing_secret_wo_version` attribute.
//
// The name is `_wo_version`, not `_wo_revision`: `_wo` is HashiCorp's documented
// naming convention, and the counter is spelled "version" by every provider that
// has shipped this pair except Kubernetes.
//
// It carries no RequiresReplace. The webhook API updates in place, and a
// rotation should not tear down and recreate a webhook — the URL's receiver
// would see a new webhook id for no reason.
func webhookSigningSecretWriteOnlyVersionAttribute() schema.Int64Attribute {
	return schema.Int64Attribute{
		MarkdownDescription: "Rotation counter for `signing_secret_wo`. Increment it whenever " +
			"`signing_secret_wo` changes: a write-only value leaves no trace in state, so this " +
			"is the only thing Terraform has to compare, and changing `signing_secret_wo` on its " +
			"own is not a change as far as Terraform is concerned.\n\n" +
			"Required when `signing_secret_wo` is set, and must be at least 1. Incrementing it " +
			"rewrites the signing secret in place.",
		Optional: true,
		Validators: []validator.Int64{
			int64validator.AtLeast(1),
			// A version with no secret is never useful, and there is no Default: 0 that
			// would make it optional-with-a-fallback.
			int64validator.AlsoRequires(path.MatchRoot("signing_secret_wo")),
		},
	}
}

// webhookSigningSecretConfigValidator requires exactly one of `signing_secret`
// and `signing_secret_wo`.
//
// Exactly one rather than at least one: both set would be ambiguous if they
// disagreed, and neither leaves the provider with no secret to send to a
// full-replace route. This is what makes it safe for `signing_secret` to have
// become Optional, which it had to for either name to be usable on its own.
func webhookSigningSecretConfigValidator() resource.ConfigValidator {
	return resourcevalidator.ExactlyOneOf(
		path.MatchRoot("signing_secret"),
		path.MatchRoot("signing_secret_wo"),
	)
}

// resolveWebhookSigningSecret returns the signing secret to send to CircleCI,
// from whichever of the two attributes the configuration set.
//
// It is called unconditionally by both Create and Update, and its result always
// goes into the request body. See the top of this file: gating the body on the
// version is vault#2900, and this route is full-replace.
//
// config, not plan. A write-only attribute is null in the plan by design — the
// framework nullifies it there and in state — so req.Plan would hand back an
// empty string, and an empty string in a full-replace PUT deletes the live
// secret. Configuration is the only place the value exists, and Terraform
// supplies it on every apply, whatever triggered the update.
//
// version is used only to explain the failure: it is persisted, so its presence
// identifies a webhook already being managed through `signing_secret_wo`.
func resolveWebhookSigningSecret(
	ctx context.Context,
	config tfsdk.Config,
	secret types.String,
	version types.Int64,
	diagnostics *diag.Diagnostics,
) (string, bool) {
	if !secret.IsNull() && !secret.IsUnknown() {
		return secret.ValueString(), true
	}

	var writeOnly types.String

	diagnostics.Append(config.GetAttribute(ctx, path.Root("signing_secret_wo"), &writeOnly)...)

	if diagnostics.HasError() {
		return "", false
	}

	if writeOnly.IsNull() || writeOnly.IsUnknown() {
		// Erroring out is the whole point of this branch, and there is no
		// read-modify-write alternative to fall back on: the API returns the secret
		// only as a mask, so it cannot be read back and re-sent. Carrying on would
		// PUT an empty signing_secret, and the route is full-replace — the live
		// secret would be deleted, leaving a webhook whose deliveries can no longer
		// be authenticated by their receiver.
		detail := "Neither `signing_secret` nor `signing_secret_wo` has a value at apply time, " +
			"so there is nothing to send to CircleCI."
		if !version.IsNull() {
			detail = "`signing_secret_wo_version` is set, so this webhook's signing secret is " +
				"managed through `signing_secret_wo`, but `signing_secret_wo` has no value at " +
				"apply time. Terraform supplies a write-only value only during apply and stores " +
				"nothing, and CircleCI returns the secret only as a mask, so there is no previous " +
				"value to fall back on.\n\nNo request was sent: CircleCI's webhook update route " +
				"replaces every field, so a request with no signing secret would have deleted the " +
				"live one."
		}

		diagnostics.AddError("Missing webhook signing secret", detail)

		return "", false
	}

	return writeOnly.ValueString(), true
}

// webhookSecretLooksUnmasked reports whether a signing_secret CircleCI returned
// is something other than a mask.
//
// The masking is an undocumented invariant this resource depends on: Read leaves
// `signing_secret` alone precisely because the API never discloses it, and if
// that ever changed — a real secret in a GET response — the resource would carry
// on writing configuration values into state and nobody would find out from the
// outside. This turns that invariant into something observable: a warning in the
// log, once per read, with no secret material in it.
//
// The community provider kelvintaywl/terraform-provider-circleci has the same
// idea, comparing against strings.Repeat("*", len(secret)) — which an empty
// string satisfies, so a webhook with no secret at all trips it. Empty is
// checked first here. Any number of asterisks counts as masked, because "****"
// is the mask today but a mask as long as the secret would be a reasonable
// change and is not a disclosure.
func webhookSecretLooksUnmasked(secret string) bool {
	if secret == "" || secret == circleci.WebhookSigningSecretMask {
		return false
	}

	return strings.Trim(secret, "*") != ""
}
