// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
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

// This file holds the write-only pair `headers_wo` + `headers_wo_version` for
// circleci_otel_exporter, and the read-back rule that pair forces.
//
// It is the sibling of environment_variable_write_only.go and
// webhook_write_only.go, and deliberately the same shape: same attribute-name
// convention, same validators wired in both directions, same "resolve from
// config, never from plan" helper, same refusal to send a partial payload. The
// decisions written up at the top of environment_variable_write_only.go apply
// here unchanged and are not repeated:
//
//   - no PreferWriteOnlyAttribute() plan modifier;
//   - no RequiresReplace on the write-only attribute itself (it is null in both
//     plan and state, so mapplanmodifier's "if req.PlanValue.Equal(
//     req.StateValue) { return }" means the modifier never fires) — replacement
//     is driven from `headers_wo_version`, which is persisted;
//   - no Default on the version;
//   - the write-only attribute cannot be Computed.
//
// WriteOnly is permitted on a map attribute. Only *set* nested attributes and
// set blocks are prohibited, because a write-only element would change a set's
// identity; see fwschema.InvalidSetNestedAttributeWithWriteOnlyDiag. A
// MapAttribute of strings has no such problem.
//
// THREE THINGS ARE DIFFERENT HERE, AND EACH ONE MATTERS
//
// 1. AT MOST one of `headers` and `headers_wo`, not exactly one.
//
// `headers` has always been Optional and an exporter with no headers at all is
// a perfectly ordinary thing to want — two of the three exporters in this
// resource's own example have none. ExactlyOneOf would start rejecting those
// configurations, which would be a breaking change dressed up as a security
// improvement. resourcevalidator.Conflicting is the right shape: it refuses
// both together, which would be ambiguous, and permits neither, which means
// "send no headers".
//
// The environment variable and webhook resources use ExactlyOneOf because their
// state-backed attribute was Required to begin with: for those, "neither" was
// already invalid and the validator only moved where the refusal came from.
//
// 2. vault#2900 does not apply, and that is a fact about the API rather than a
// choice.
//
// CircleCI has no update route for an exporter: the routes served is
// POST/GET/DELETE only, otelExporterResource.Update is an empty method, and
// every configurable attribute carries RequiresReplace. So there is no
// "update triggered by an unrelated field" for a version-gated send to omit the
// secret from — the failure mode that wiped token_reviewer_jwt in
// hashicorp/terraform-provider-vault#2900 has nowhere to occur. Nothing here is
// gated on the version anyway, which is the same rule the other two resources
// follow, and it stays correct if CircleCI ever adds a PATCH.
//
// 3. The write-only path gives up the one kind of drift this resource could see.
//
// GET returns header *names* in full and every header *value* as the placeholder
// circleci.OTelRedactedHeaderValue. On the state-backed path otelRefreshHeaders
// exploits that: it compares the key sets, keeps the configured values when they
// match, and adopts the remote map when they do not — so a header added or
// removed outside Terraform is detected even though a changed value is not.
//
// That comparison needs a prior key set, and on the write-only path there isn't
// one. `headers_wo` is null in state by construction, and ReadRequest carries no
// configuration, so Read has nothing to compare the returned names against.
// Worse, adopting the remote map would write `{"x-api-key": "xxxx"}` into
// `headers` — a non-write-only attribute — and since `headers` forces
// replacement, every subsequent plan would want to recreate the exporter
// forever.
//
// So otelHeadersAfterRead leaves `headers` null whenever the exporter is on the
// write-only path. The consequence is documented rather than worked around: on
// the write-only path neither a changed value nor an added or removed header is
// detected. Storing the names in a computed attribute would restore the
// add/remove half, at the cost of a new persisted attribute and a second thing
// that can force replacement; it was considered and not done, because the
// resource is replacement-only and re-applying with a bumped version is already
// the remedy for any header drift.

// otelHeadersWriteOnlyAttribute returns the `headers_wo` attribute.
func otelHeadersWriteOnlyAttribute() schema.MapAttribute {
	return schema.MapAttribute{
		MarkdownDescription: "Extra headers sent with each export, typically the collector's " +
			"credentials, as a write-only argument: Terraform sends them to CircleCI but never " +
			"records them in state or in a plan file. Requires Terraform 1.11 or later.\n\n" +
			"Because nothing derived from the headers is stored, Terraform cannot see that they " +
			"changed. `headers_wo_version` is required alongside them, and must be incremented " +
			"every time any header changes, or the new headers are never sent — incrementing it " +
			"replaces the exporter, because CircleCI has no route that updates one in place.\n\n" +
			"Set at most one of `headers` and `headers_wo`. Setting neither sends no headers.\n\n" +
			"~> **On this path no header drift is detected at all.** `headers` at least notices a " +
			"header added or removed outside Terraform, because CircleCI returns header names in " +
			"full; `headers_wo` stores no names to compare against, so it notices neither that nor " +
			"a changed value.",
		ElementType: types.StringType,
		Optional:    true,
		WriteOnly:   true,
		Sensitive:   true,
		Validators: []validator.Map{
			mapvalidator.KeysAre(stringvalidator.LengthAtLeast(1)),
			// Both directions are required, not just version-implies-headers. A
			// `headers_wo` with no version can be created but can never be rotated:
			// every later edit to it is a no-op that Terraform reports as no change at
			// all. Refusing the configuration is the only way that surfaces.
			mapvalidator.AlsoRequires(path.MatchRoot("headers_wo_version")),
		},
	}
}

// otelHeadersWriteOnlyVersionAttribute returns the `headers_wo_version`
// attribute.
//
// The name is `_wo_version`, not `_wo_revision`: `_wo` is HashiCorp's documented
// naming convention, and the counter is spelled "version" by every provider that
// has shipped this pair except Kubernetes.
//
// It carries RequiresReplace, which is not a new behaviour being introduced on
// the write-only path but the existing one being preserved: `headers` already
// forces replacement, because there is no update route to rewrite headers with.
// The modifier belongs on the version rather than on `headers_wo` because a
// write-only attribute is null in both plan and state, so the standard modifier
// returns early and never fires.
func otelHeadersWriteOnlyVersionAttribute() schema.Int64Attribute {
	return schema.Int64Attribute{
		MarkdownDescription: "Rotation counter for `headers_wo`. Increment it whenever any header " +
			"in `headers_wo` changes: a write-only value leaves no trace in state, so this is the " +
			"only thing Terraform has to compare, and changing `headers_wo` on its own is not a " +
			"change as far as Terraform is concerned.\n\n" +
			"Required when `headers_wo` is set, and must be at least 1. Incrementing it forces a " +
			"new resource to be created, exactly as changing `headers` does: CircleCI has no route " +
			"that updates an exporter in place. The exporter gets a new `id` and traces are not " +
			"exported during the gap.",
		Optional: true,
		PlanModifiers: []planmodifier.Int64{
			int64planmodifier.RequiresReplace(),
		},
		Validators: []validator.Int64{
			int64validator.AtLeast(1),
			// A version with no headers is never useful, and there is no Default: 0 that
			// would make it optional-with-a-fallback.
			int64validator.AlsoRequires(path.MatchRoot("headers_wo")),
		},
	}
}

// otelHeadersConfigValidator refuses `headers` and `headers_wo` together.
//
// Conflicting rather than ExactlyOneOf: an exporter with no headers is valid and
// always has been. See the top of this file.
func otelHeadersConfigValidator() resource.ConfigValidator {
	return resourcevalidator.Conflicting(
		path.MatchRoot("headers"),
		path.MatchRoot("headers_wo"),
	)
}

// resolveOTelHeaders returns the headers to send to CircleCI, from whichever of
// the two attributes the configuration set.
//
// config, not plan. A write-only attribute is null in the plan by design — the
// framework nullifies it there and in state — so req.Plan would hand back a null
// map and the exporter would be created with no credentials, which fails at the
// collector rather than at apply. Configuration is the only place the value
// exists.
//
// version is used only to tell the two "no headers" cases apart: it is
// persisted, so its presence identifies an exporter being managed through
// `headers_wo`, for which an empty result is a failure rather than a choice.
func resolveOTelHeaders(
	ctx context.Context,
	config tfsdk.Config,
	headers types.Map,
	version types.Int64,
	diagnostics *diag.Diagnostics,
) (map[string]string, bool) {
	resolved := map[string]string{}

	if !headers.IsNull() && !headers.IsUnknown() {
		diagnostics.Append(headers.ElementsAs(ctx, &resolved, false)...)

		return resolved, !diagnostics.HasError()
	}

	var writeOnly types.Map

	diagnostics.Append(config.GetAttribute(ctx, path.Root("headers_wo"), &writeOnly)...)

	if diagnostics.HasError() {
		return nil, false
	}

	if writeOnly.IsNull() || writeOnly.IsUnknown() {
		if version.IsNull() {
			// Neither attribute is set and no version says otherwise: an exporter with
			// no headers, which is ordinary. otelHeadersConfigValidator permits it
			// deliberately.
			return resolved, true
		}

		// `headers_wo_version` is set, so headers were supposed to be sent. Creating
		// the exporter without them would succeed at the API and then fail at the
		// collector on every export, which is a much worse place to find out; and
		// there is nothing to fall back on, because a read answers with the
		// placeholder rather than the values.
		diagnostics.AddError(
			"Missing OTLP exporter headers",
			"`headers_wo_version` is set, so this exporter's headers are managed through "+
				"`headers_wo`, but `headers_wo` has no value at apply time. Terraform supplies a "+
				"write-only value only during apply and stores nothing, and CircleCI answers every "+
				"read with the placeholder `"+circleci.OTelRedactedHeaderValue+"` rather than the "+
				"header values, so there is no previous value to fall back on.\n\nNo request was "+
				"sent: an exporter created with no headers would be accepted by CircleCI and then "+
				"rejected by the collector on every export.",
		)

		return nil, false
	}

	diagnostics.Append(writeOnly.ElementsAs(ctx, &resolved, false)...)

	return resolved, !diagnostics.HasError()
}

// otelHeadersAfterRead returns the value `headers` should hold after a read.
//
// On the state-backed path this is otelRefreshHeaders, which compares key sets to
// surface a header added or removed outside Terraform. On the write-only path
// there is no prior key set to compare against — `headers_wo` is null in state
// and ReadRequest carries no configuration — and adopting the API's map would put
// the placeholder value into a non-write-only attribute that forces replacement,
// producing a plan that never converges. So `headers` stays null there. See the
// top of this file.
func otelHeadersAfterRead(
	ctx context.Context,
	prior types.Map,
	version types.Int64,
	remote map[string]string,
) (types.Map, diag.Diagnostics) {
	if !version.IsNull() {
		return types.MapNull(types.StringType), nil
	}

	return otelRefreshHeaders(ctx, prior, remote)
}
