// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Cross-attribute validation for `circleci_pipeline_definition`, run at plan time
// rather than left to surface as an apply-time API error — the same reasoning as
// trigger_validation.go, and for a schema that has the same shape of problem:
// config_source_provider selects a contract (each accepted value needs a
// repository) that no single attribute's own validator can express as a
// requirement on another attribute. It also carries the one value the schema's
// own OneOf validator cannot explain: "circleci", removed from
// circleci.PipelineConfigSourceProviders because the create endpoint has never
// accepted it for a customer-plausible file path.
//
// checkout_source has no such split: every checkout_source_provider needs a
// repository (see circleci.PipelineCheckoutSourceProviders), so
// checkout_source_repo_external_id stays plain Required in the schema and the only
// rule left to check here is its numeric form.

// pipelineInvalidConfigSummary is the summary every diagnostic here shares, for the
// same reason triggerInvalidConfigSummary exists: it says the configuration is
// wrong, not the API.
const pipelineInvalidConfigSummary = "Invalid CircleCI pipeline definition configuration"

// ValidateConfig applies the rules the schema alone cannot express.
func (r *pipelineResource) ValidateConfig(
	ctx context.Context,
	req resource.ValidateConfigRequest,
	resp *resource.ValidateConfigResponse,
) {
	// Ask before reading: a destroy plans a null configuration, and Get-ing that
	// into the model fails with a value-conversion error naming the model type
	// rather than the missing guard it is. See trigger_validation.go's
	// ValidateConfig for the same guard, demonstrated there.
	if req.Config.Raw.IsNull() {
		return
	}

	var config pipelineResourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	validatePipelineResourceConfig(config, &resp.Diagnostics)
}

// validatePipelineResourceConfig holds the rules themselves, separately from
// reading the configuration.
func validatePipelineResourceConfig(config pipelineResourceModel, diags *diag.Diagnostics) {
	validatePipelineConfigSource(config, diags)

	// checkout_source_repo_external_id is Required in the schema, so presence is
	// already enforced there; only its numeric form is checked here.
	validateRepoExternalIDIsNumeric(
		path.Root("checkout_source_repo_external_id"),
		config.CheckoutSourceRepoExternalId,
		"checkout_source_repo_external_id",
		diags,
	)
}

// validatePipelineConfigSource applies config_source_provider's split: a
// github_app or github_server config source requires config_source_repo_external_id
// (and it must be numeric); "circleci" is refused outright, with an explanatory
// message, because circleci.PipelineConfigSourceProviders no longer offers it.
func validatePipelineConfigSource(config pipelineResourceModel, diags *diag.Diagnostics) {
	// Nothing below can be decided without knowing the provider. When it is
	// unknown — set from a variable or another resource — Terraform re-plans once
	// it resolves, and this runs again with a value.
	if config.ConfigSourceProvider.IsNull() || config.ConfigSourceProvider.IsUnknown() {
		return
	}

	provider := config.ConfigSourceProvider.ValueString()

	if provider == circleci.PipelineConfigSourceProviderCircleCI {
		// The schema's stringvalidator.OneOf already rejects this value with its
		// own generic "must be one of" diagnostic (circleci.PipelineConfigSourceProviders
		// no longer lists it) — this adds the explanation that message cannot carry,
		// for the practitioner who already has config_source_provider = "circleci" in
		// a configuration written before this restriction existed: it names a
		// CircleCI-internal, repo-less configuration source that the create endpoint
		// has never accepted for a customer-plausible file path (see
		// circleci.PipelineConfigSourceProviderCircleCI), not a capability this
		// release took away.
		diags.AddAttributeError(
			path.Root("config_source_provider"),
			pipelineInvalidConfigSummary,
			"CircleCI pipeline definition with config_source_provider \"circleci\" is not accepted: "+
				"it names a CircleCI-internal, repo-less configuration source that the create endpoint "+
				"rejects for any customer-plausible config_source_file_path (HTTP 400 \"Invalid config "+
				"file path.\"). Use \"github_app\" or \"github_server\" instead, both of which require "+
				"config_source_repo_external_id.",
		)

		return
	}

	// github_app or github_server — the only two remaining values — need the same
	// repository-id rule event_source_repo_external_id enforces for a trigger.
	if config.ConfigSourceRepoExternalId.IsNull() ||
		(!config.ConfigSourceRepoExternalId.IsUnknown() && config.ConfigSourceRepoExternalId.ValueString() == "") {
		diags.AddAttributeError(
			path.Root("config_source_repo_external_id"),
			pipelineInvalidConfigSummary,
			"CircleCI pipeline definition with config_source_provider "+provider+" requires "+
				"config_source_repo_external_id (the VCS provider's numeric repository ID)",
		)

		return
	}

	validateRepoExternalIDIsNumeric(
		path.Root("config_source_repo_external_id"),
		config.ConfigSourceRepoExternalId,
		"config_source_repo_external_id",
		diags,
	)
}

// validateRepoExternalIDIsNumeric applies the same rule
// trigger_validation.go's validateTriggerRepoExternalID does: the API parses a
// repository external id with strconv.ParseInt and answers a bare "bad request"
// with no field reference when it does not parse, so a practitioner who writes a
// repository *name* gets that, after apply, with nothing to point them at the
// attribute. Reuses circleci.TriggerRepoExternalIDIsValid rather than a second
// numeric check, because the rule — and the API behavior behind it — is the same
// one for both circleci_trigger and circleci_pipeline_definition.
func validateRepoExternalIDIsNumeric(
	attributePath path.Path, value types.String, attributeName string, diags *diag.Diagnostics,
) {
	if value.IsNull() || value.IsUnknown() {
		// Null is a presence question handled by the caller; unknown is configured
		// but unreadable, so the form of the value cannot be checked yet — Terraform
		// re-plans once it resolves.
		return
	}

	if value.ValueString() == "" {
		// An empty string is a presence question too, and the caller already
		// rejects it where presence is required; checkout_source_repo_external_id
		// is Required in the schema, so Terraform itself refuses an empty
		// configuration value before this ever runs.
		return
	}

	if !circleci.TriggerRepoExternalIDIsValid(value.ValueString()) {
		diags.AddAttributeError(
			attributePath,
			pipelineInvalidConfigSummary,
			"CircleCI pipeline definition requires "+attributeName+" to be the repository's numeric "+
				"ID, not its name: "+value.ValueString()+" is not a number. The API rejects a "+
				"non-numeric repository ID with an HTTP 400 that names no attribute",
		)
	}
}
