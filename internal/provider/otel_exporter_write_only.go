// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"sort"

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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
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
// CircleCI has no update route for an exporter: the routes are
// POST/GET/DELETE only, otelExporterResource.Update is an empty method, and
// every configurable attribute carries RequiresReplace. So there is no
// "update triggered by an unrelated field" for a version-gated send to omit the
// secret from — the failure mode that wiped token_reviewer_jwt in
// hashicorp/terraform-provider-vault#2900 has nowhere to occur. Nothing here is
// gated on the version anyway, which is the same rule the other two resources
// follow, and it stays correct if CircleCI ever adds a PATCH.
//
// 3. The write-only path used to give up the one kind of drift this resource
// could see. `headers_wo_names` gets it back.
//
// GET returns header *names* in full and every header *value* as the placeholder
// circleci.OTelRedactedHeaderValue. On the state-backed path otelRefreshHeaders
// exploits that: it compares the key sets, keeps the configured values when they
// match, and adopts the remote map when they do not — so a header added or
// removed outside Terraform is detected even though a changed value is not.
//
// That comparison needs a prior key set, and on the write-only path there wasn't
// one: `headers_wo` is null in state by construction, and ReadRequest carries no
// configuration, so Read had nothing to compare the returned names against.
// Adopting the remote map directly into `headers` unconditionally would have
// written `{"x-api-key": "xxxx"}` into a non-write-only, replacement-forcing
// attribute on every single read, drift or not — and since the write-only path's
// configuration never sets `headers`, that mismatch would never clear and every
// later plan would want to recreate the exporter forever.
//
// `headers_wo_names` is the prior key set the write-only path was missing: a
// computed set of header names, populated by Create from the response and kept
// current by Read, that plays the same role state.Headers plays for
// otelRefreshHeaders. otelRefreshWriteOnlyHeaderNames compares it against the
// names the API just returned. When they match, nothing changed and both
// `headers_wo_names` and `headers` are left alone (`headers` stays null, as
// always on this path). When they differ — a header was added or removed
// outside Terraform — `headers_wo_names` is updated to the new set AND `headers`
// is populated with the API's redacted map, exactly what otelRefreshHeaders does
// on the state-backed path for the same reason: `headers` already forces
// replacement (mapplanmodifier.RequiresReplace on the attribute itself), while
// `headers_wo`'s own RequiresReplace can never fire (see point 1). Populating
// `headers` only on a genuine, detected difference — never unconditionally — is
// what keeps this a one-time replacement rather than the forever-loop above:
// the replacement's Create starts the new resource with accurate names and a
// null `headers`, matching reality again.
//
// Storing header names discloses nothing a read does not already reveal: the
// API returns them in full to anyone who can read the exporter. A changed
// header *value* is still undetectable on both paths — CircleCI never returns
// values, only names — and that limitation is unchanged and stated in both
// attributes' descriptions.

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
			"~> **A changed header value is never detected on either path.** CircleCI never " +
			"discloses a header's value, only its name. A header *added or removed* outside " +
			"Terraform is detected here too, through the computed `headers_wo_names` attribute, " +
			"and — because `headers` already forces replacement — detecting one recreates the " +
			"exporter, the same as it does on the `headers` path.",
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

// otelHeadersWriteOnlyNamesAttribute returns the `headers_wo_names` attribute:
// the computed set of header names CircleCI reports, populated only on the
// write-only path. See point 3 at the top of this file for why it exists and
// how it is used.
//
// It carries no RequiresReplace of its own. Detecting a header added or
// removed is done by otelRefreshWriteOnlyHeaderNames adopting CircleCI's map
// into `headers`, which already forces replacement; a second RequiresReplace
// here would be redundant, and — because a Computed attribute with no
// UseStateForUnknown is unknown on every plan regardless of whether anything
// changed — it would misfire as "changed" on every apply rather than only on
// genuine drift.
func otelHeadersWriteOnlyNamesAttribute() schema.SetAttribute {
	return schema.SetAttribute{
		MarkdownDescription: "The header names CircleCI reports for this exporter, populated only " +
			"when headers are managed through `headers_wo`. CircleCI discloses every header name in " +
			"full — only the values are masked — so recording the names here reveals nothing that " +
			"reading the exporter does not already reveal, and it is what lets a read detect a " +
			"header added or removed outside Terraform on this path, the way `headers` already does " +
			"on its own.\n\n" +
			"~> **An added or removed header recreates the exporter.** Detecting one adopts " +
			"CircleCI's header map into `headers`, and `headers` already forces replacement — the " +
			"same behavior the `headers` path has always had for this kind of drift, not something " +
			"new here. A changed header *value* is still undetectable on both paths: CircleCI never " +
			"discloses values, only names.",
		ElementType: types.StringType,
		Computed:    true,
		PlanModifiers: []planmodifier.Set{
			setplanmodifier.UseStateForUnknown(),
		},
	}
}

// otelHeaderNames returns remote's keys, sorted so the resulting attribute
// value is deterministic regardless of map iteration order.
func otelHeaderNames(remote map[string]string) []string {
	names := make([]string, 0, len(remote))
	for name := range remote {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// otelHeadersWriteOnlyNamesAfterCreate returns the value `headers_wo_names`
// should hold right after Create: the names CircleCI just echoed back, or null
// when the exporter is on the state-backed path (version identifies that, the
// same signal otelHeadersAfterRead uses). This is what gives Read a prior key
// set to compare against on every later refresh.
func otelHeadersWriteOnlyNamesAfterCreate(
	ctx context.Context,
	version types.Int64,
	remote map[string]string,
) (types.Set, diag.Diagnostics) {
	if version.IsNull() {
		return types.SetNull(types.StringType), nil
	}

	return types.SetValueFrom(ctx, types.StringType, otelHeaderNames(remote))
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

// otelHeadersAfterRead returns the values `headers` and `headers_wo_names`
// should hold after a read.
//
// On the state-backed path (version null) `headers` is otelRefreshHeaders'
// result, which compares key sets to surface a header added or removed outside
// Terraform, and `headers_wo_names` stays null — it belongs to the other path.
// On the write-only path it is the reverse: `headers_wo_names` is
// otelRefreshWriteOnlyHeaderNames' result, and `headers` stays null unless that
// comparison found a difference, in which case it briefly holds CircleCI's
// redacted map so that `headers`'s existing RequiresReplace fires. See point 3
// at the top of this file.
func otelHeadersAfterRead(
	ctx context.Context,
	priorHeaders types.Map,
	priorNames types.Set,
	version types.Int64,
	remote map[string]string,
) (types.Map, types.Set, diag.Diagnostics) {
	if version.IsNull() {
		headers, diags := otelRefreshHeaders(ctx, priorHeaders, remote)

		return headers, types.SetNull(types.StringType), diags
	}

	return otelRefreshWriteOnlyHeaderNames(ctx, priorNames, remote)
}

// otelRefreshWriteOnlyHeaderNames is otelRefreshHeaders' counterpart for the
// write-only path, comparing header *names* — the only thing CircleCI ever
// discloses in full — instead of a key-and-value map.
//
// When priorNames and remote's keys are the same set, nothing changed:
// `headers_wo_names` is left exactly as it was and `headers` stays null, same
// as always on this path. When they differ, `headers_wo_names` is updated to
// the new set and `headers` is populated with CircleCI's redacted map, which
// is what makes the drift visible: `headers` already forces replacement, and
// on this path it is otherwise always null in configuration, so populating it
// here is a mismatch that trips the same modifier. See point 3 at the top of
// this file for why that is a one-time replacement rather than a forever-loop.
func otelRefreshWriteOnlyHeaderNames(
	ctx context.Context,
	priorNames types.Set,
	remote map[string]string,
) (types.Map, types.Set, diag.Diagnostics) {
	var diags diag.Diagnostics

	priorElements := priorNames.Elements()

	if len(priorElements) == len(remote) {
		sameKeys := true

		for _, element := range priorElements {
			name, ok := element.(types.String)
			if !ok {
				sameKeys = false

				break
			}

			if _, ok := remote[name.ValueString()]; !ok {
				sameKeys = false

				break
			}
		}

		if sameKeys {
			return types.MapNull(types.StringType), priorNames, diags
		}
	}

	names, nameDiags := types.SetValueFrom(ctx, types.StringType, otelHeaderNames(remote))
	diags.Append(nameDiags...)

	headers, headerDiags := types.MapValueFrom(ctx, types.StringType, remote)
	diags.Append(headerDiags...)

	return headers, names, diags
}
