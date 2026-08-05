// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"

	"terraform-provider-circleci/internal/circleci"
)

// Per-provider validation for `circleci_trigger`.
//
// WHY THIS IS NOT IN Create
//
// One endpoint covers five contracts, and which of a trigger's attributes are
// required — even which are permitted — depends on `event_source_provider`. None
// of that needs the API to decide: every rule here is a fact about the
// configuration alone.
//
// These rules used to live in Create (and, duplicated, in Update), so
// `terraform plan` succeeded and `terraform apply` failed. That is the worst
// possible ordering. The error arrives after the plan has been reviewed and
// approved, and for anyone who runs plan and apply as separate CI stages it
// arrives after the point of no return — with the run already part-applied if the
// configuration held more than one trigger. Validation that needs no API call
// belongs at plan time. Issue #30.
//
// Single-attribute rules (`event_source_provider` and `event_preset` being drawn
// from a fixed set) are schema validators instead, built from
// circleci.TriggerEventSourceProviders and circleci.TriggerEventPresets so the
// validator, the attribute description and the client cannot drift apart. What is
// left here is only what one attribute cannot know on its own.
//
// NULL VERSUS UNKNOWN, WHICH IS THE WHOLE DIFFICULTY
//
// This reads the *configuration*, not the plan, and the two differ in a way that
// matters:
//
//   - An Optional+Computed attribute left out of the configuration is **null**
//     here, where the plan would show it as unknown. Create had to test
//     IsNull() *and* IsUnknown() on `event_source_schedule_attribution_actor` for
//     exactly that reason, and got it wrong at first — a schedule trigger with no
//     attribution actor reached the API because only IsNull() was checked.
//   - An attribute set from something not yet resolved (a data source, another
//     resource's attribute) is **unknown**. It is *configured*, so a "required"
//     rule is satisfied and a "must be omitted" rule is violated, and neither may
//     look at the value. Treating unknown as absent would reject configurations
//     that are perfectly valid, which is worse than the bug this replaces.
//
// So: presence is `!IsNull()`, and any rule about a *value* runs only when the
// value is known.

// triggerInvalidConfigSummary is the summary every diagnostic here shares. The
// detail says which attribute and why; the summary says the configuration is wrong
// rather than the API, which is the distinction that was lost while these checks
// ran under "Error creating CircleCI trigger".
const triggerInvalidConfigSummary = "Invalid CircleCI trigger configuration"

// ValidateConfig applies the per-provider rules that the schema cannot express.
func (r *triggerResource) ValidateConfig(
	ctx context.Context,
	req resource.ValidateConfigRequest,
	resp *resource.ValidateConfigResponse,
) {
	// Ask before reading. Get-ing a null configuration into the model fails with a
	// value-conversion error naming the model type, which reads like a schema bug
	// rather than the missing guard it is. ModifyPlan carries the same guard, and
	// there it is demonstrably load-bearing: a destroy plans a null configuration —
	// see pipelineDefinitionIDPlanNeedsReconcile.
	//
	// Here it is defensive rather than demonstrated. Terraform validates the
	// configuration it has, so removing a resource's block means it is not validated
	// at all, and `terraform destroy` validates the block still written — which is
	// why TestTriggerResourceUnit_UpdateIsValidatedToo has to end on a configuration
	// that passes. Replacing these two lines with a panic and running the whole
	// trigger suite, destroy steps included, produced no null configuration. They
	// stay because the cost is two lines and the failure mode is an error message
	// that sends the reader to the wrong file.
	if req.Config.Raw.IsNull() {
		return
	}

	var config triggerResourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	validateTriggerConfig(config, &resp.Diagnostics)
}

// validateTriggerConfig holds the rules themselves, separately from reading the
// configuration, so they can be exercised directly as well as through a plan.
func validateTriggerConfig(config triggerResourceModel, diags *diag.Diagnostics) {
	// Nothing below can be decided without knowing the provider. When it is
	// unknown — set from a variable or another resource — Terraform re-plans once
	// it resolves, and this runs again with a value.
	if config.EventSourceProvider.IsNull() || config.EventSourceProvider.IsUnknown() {
		return
	}

	provider := config.EventSourceProvider.ValueString()

	// `parameters` is the one rule that is the same for every provider but one.
	if provider != circleci.TriggerProviderSchedule && !config.Parameters.IsNull() {
		diags.AddAttributeError(
			path.Root("parameters"),
			triggerInvalidConfigSummary,
			"CircleCI trigger with "+provider+" provider does not support parameters; "+
				"parameters is only valid for schedule triggers",
		)
	}

	validateTriggerEventPreset(config, provider, diags)

	// Every repository-backed event source needs the same repository id, in the same
	// form; only what *else* they require differs. github_oauth was previously
	// exempted from this check, which is how a github_oauth trigger came to be
	// created with no repository at all — see circleci.TriggerRepoEventSourceProviders.
	if circleci.TriggerProviderNeedsRepo(provider) {
		validateTriggerRepoExternalID(config, provider, diags)
	}

	switch provider {
	case circleci.TriggerProviderGitHubApp, circleci.TriggerProviderGitHubServer:
		validateGitHubRepoTrigger(config, provider, diags)
	case circleci.TriggerProviderGitHubOAuth:
		validateGitHubOAuthTrigger(config, provider, diags)
	case circleci.TriggerProviderWebhook:
		validateWebhookTrigger(config, diags)
	case circleci.TriggerProviderSchedule:
		validateScheduleTrigger(config, diags)
	}
	// No default: an unrecognized provider is already rejected by the OneOf
	// validator on the attribute, which names the accepted values. Repeating the
	// check here would produce a second diagnostic saying less.
}

// validateTriggerEventPreset applies the per-provider narrowing of `event_preset`.
//
// The set of valid presets is a schema validator; what it cannot express is that
// two providers reject the attribute outright and a third accepts only two of the
// fourteen.
func validateTriggerEventPreset(config triggerResourceModel, provider string, diags *diag.Diagnostics) {
	configured := !config.EventPreset.IsNull()

	switch provider {
	case circleci.TriggerProviderWebhook, circleci.TriggerProviderSchedule:
		if configured {
			diags.AddAttributeError(
				path.Root("event_preset"),
				triggerInvalidConfigSummary,
				"CircleCI trigger with "+provider+" provider does not support event_preset; "+
					"it applies to GitHub event sources only",
			)
		}
	case circleci.TriggerProviderGitHubOAuth:
		accepted := joinWithOr(circleci.GitHubOAuthTriggerEventPresets())

		if !configured {
			diags.AddAttributeError(
				path.Root("event_preset"),
				triggerInvalidConfigSummary,
				"CircleCI trigger with github_oauth provider requires event_preset, and accepts "+
					"only "+accepted+" there",
			)

			return
		}

		// Unknown is configured but unreadable, so the narrowing cannot be applied
		// yet; the schema validator does not run on unknown values either.
		if config.EventPreset.IsUnknown() {
			return
		}

		if !slices.Contains(circleci.GitHubOAuthTriggerEventPresets(), config.EventPreset.ValueString()) {
			diags.AddAttributeError(
				path.Root("event_preset"),
				triggerInvalidConfigSummary,
				"CircleCI trigger with github_oauth provider has an unexpected event_preset: "+
					config.EventPreset.ValueString()+" is a valid preset for a GitHub App event "+
					"source, but github_oauth accepts only "+accepted,
			)
		}
	}
	// github_app and github_server take any of the presets, or none.
}

// validateTriggerRepoExternalID applies the two rules every repository-backed
// event source shares: the id has to be there, and it has to be a number.
//
// The second rule is not cosmetic. The API parses the external id as a 64-bit
// integer and, when that fails, answers HTTP 400 with the body "bad request" and
// no field reference at all — the parse error is discarded before the response is
// built. A practitioner who writes a repository *name* there gets that, after
// apply, with nothing to point them at the attribute.
func validateTriggerRepoExternalID(config triggerResourceModel, provider string, diags *diag.Diagnostics) {
	// Empty counts as absent as well as null: an empty external id would be sent
	// as a repo with no id and rejected by the API, so this is the same mistake.
	if config.EventSourceRepoExternalId.IsNull() ||
		(!config.EventSourceRepoExternalId.IsUnknown() && config.EventSourceRepoExternalId.ValueString() == "") {
		diags.AddAttributeError(
			path.Root("event_source_repo_external_id"),
			triggerInvalidConfigSummary,
			"CircleCI trigger with "+provider+" provider requires event_source_repo_external_id "+
				"(the GitHub repository ID)",
		)

		return
	}

	// Unknown is configured but unreadable, so the form of the value cannot be
	// checked yet; Terraform re-plans once it resolves.
	if config.EventSourceRepoExternalId.IsUnknown() {
		return
	}

	if !circleci.TriggerRepoExternalIDIsValid(config.EventSourceRepoExternalId.ValueString()) {
		diags.AddAttributeError(
			path.Root("event_source_repo_external_id"),
			triggerInvalidConfigSummary,
			"CircleCI trigger with "+provider+" provider requires event_source_repo_external_id to be "+
				"the repository's numeric ID, not its name: "+
				config.EventSourceRepoExternalId.ValueString()+" is not a number. The API rejects a "+
				"non-numeric repository ID with an HTTP 400 that names no attribute",
		)
	}
}

// validateGitHubRepoTrigger covers github_app and github_server, which share one
// contract: the event source is a repository, named by GitHub's numeric id (checked
// in validateTriggerRepoExternalID), and there is no event name to give.
func validateGitHubRepoTrigger(config triggerResourceModel, provider string, diags *diag.Diagnostics) {
	if !config.EventName.IsNull() {
		diags.AddAttributeError(
			path.Root("event_name"),
			triggerInvalidConfigSummary,
			"CircleCI trigger with "+provider+" provider does not support event_name; "+
				"the event is selected by event_preset instead",
		)
	}
}

// validateGitHubOAuthTrigger covers github_oauth, whose contract is narrower than
// the GitHub App one: `event_preset` is mandatory (checked in
// validateTriggerEventPreset) and `disabled` is not supported at all. The
// repository id is required here too, and is checked in
// validateTriggerRepoExternalID with the other repository-backed providers.
func validateGitHubOAuthTrigger(config triggerResourceModel, provider string, diags *diag.Diagnostics) {
	// `disabled` has a default, so it is always set in the *plan*; only the
	// configuration can say whether the practitioner asked for it.
	if !config.Disabled.IsNull() {
		diags.AddAttributeError(
			path.Root("disabled"),
			triggerInvalidConfigSummary,
			"CircleCI trigger with "+provider+" provider does not support disabled; "+
				"remove the attribute, or delete the trigger to stop it firing",
		)
	}
}

// validateWebhookTrigger covers the inbound-URL contract.
//
// Create also tested `event_source_web_hook_url` for null here, which could never
// fire and could not be moved: the attribute is Computed, so it is unknown in the
// plan (never null) and null in every configuration (never set). Ported verbatim
// it would have rejected every webhook trigger ever written. It is dropped.
// It also required both refs only for schedule triggers, although a webhook
// trigger needs them for exactly the same reason and the API enforces
// it identically: an inbound POST carries no ref, so there is nothing for
// checkout_ref and config_ref to fall back to, and a create omitting either is
// rejected. The documentation said "required" for both providers all along; only
// the check was missing, so a webhook trigger with no refs planned cleanly and
// failed on apply.
func validateWebhookTrigger(config triggerResourceModel, diags *diag.Diagnostics) {
	if config.EventName.IsNull() {
		diags.AddAttributeError(
			path.Root("event_name"),
			triggerInvalidConfigSummary,
			"CircleCI trigger with webhook provider requires an event_name",
		)
	}

	if config.EventSourceWebHookSender.IsNull() {
		diags.AddAttributeError(
			path.Root("event_source_web_hook_sender"),
			triggerInvalidConfigSummary,
			"CircleCI trigger with webhook provider requires a Webhook Sender",
		)
	}

	validateTriggerRequiredRefs(config, circleci.TriggerProviderWebhook, diags)
}

// validateTriggerRequiredRefs requires checkout_ref and config_ref for the
// providers that have no event ref to inherit. Shared by webhook and schedule so
// the two cannot drift; see circleci.TriggerProviderRequiresRefs.
func validateTriggerRequiredRefs(config triggerResourceModel, provider string, diags *diag.Diagnostics) {
	refs := []struct {
		attribute string
		value     interface{ IsNull() bool }
	}{
		{"checkout_ref", config.CheckoutRef},
		{"config_ref", config.ConfigRef},
	}

	for _, ref := range refs {
		if ref.value.IsNull() {
			diags.AddAttributeError(
				path.Root(ref.attribute),
				triggerInvalidConfigSummary,
				"CircleCI trigger with "+provider+" provider requires "+ref.attribute+
					"; an event from this source carries no ref to fall back to",
			)
		}
	}
}

// validateScheduleTrigger covers the cron contract, which needs the most: there
// is no VCS event to take a ref from, so both refs are explicit, and the schedule
// itself has to be described.
func validateScheduleTrigger(config triggerResourceModel, diags *diag.Diagnostics) {
	validateTriggerRequiredRefs(config, circleci.TriggerProviderSchedule, diags)

	required := []struct {
		attribute string
		value     interface{ IsNull() bool }
	}{
		{"event_name", config.EventName},
		{"event_source_schedule_cron_expression", config.EventSourceScheduleCronExpression},
		// Optional+Computed, so this is null when omitted from the configuration
		// even though the plan would show it as unknown.
		{"event_source_schedule_attribution_actor", config.EventSourceScheduleAttributionActor},
	}

	for _, attribute := range required {
		if attribute.value.IsNull() {
			diags.AddAttributeError(
				path.Root(attribute.attribute),
				triggerInvalidConfigSummary,
				"CircleCI trigger with schedule provider requires "+attribute.attribute,
			)
		}
	}
}
