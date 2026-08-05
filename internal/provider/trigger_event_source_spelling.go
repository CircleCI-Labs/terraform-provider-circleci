// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

// This file holds everything to do with the trigger event source attributes that
// `circleci_triggers` and `circleci_trigger` (the data sources) spell differently
// from `circleci_trigger` (the resource) and from the API itself.
//
// One file on purpose, like org_id_deprecation.go: the pair appears on two data
// sources, and when the deprecation ends the removal should be deleting this file
// and following the compiler, not auditing two schemas by hand.
//
// WHY THE RESOURCE'S SPELLING WON
//
// The API nests these identically on all four trigger routes:
// `event_source.repo.full_name`, `event_source.repo.external_id` and
// `event_source.webhook.url`. Neither side's flattening is the API's own, so there
// is no source-of-truth basis for calling one side wrong — the defect is that the
// resource (`event_source_repo_full_name`, `event_source_repo_external_id`,
// `event_source_web_hook_url`) and the two data sources
// (`event_source_repository_name`, `event_source_repository_external_id`,
// `event_source_webhook_url`) disagree with each other. The resource's spelling was
// picked to converge on: it is the one practitioners write in a `resource` block,
// and it is referenced from more places in this provider (the resource's own Create
// and Read, and every fake-backed test that exercises it) than either data source's
// spelling is.
//
// WHY BOTH SPELLINGS ARE SIMPLY POPULATED, NOT GATED BY A CONFIG VALIDATOR
//
// Unlike pipeline_definition_id_deprecation.go and org_id_deprecation.go, these are
// Computed-only attributes on a data source: a practitioner reads them, never
// writes them, so there is nothing to validate "exactly one of" over and no replace
// hazard to guard against. Both names always carry the same value.
//
// DeprecationMessage is set on the old attribute anyway, even though it is
// Computed. webhook_data_source.go's `signing_secret` already establishes that a
// Computed attribute can carry one; the warning here tells a practitioner reading
// `event_source_repository_name` in a `terraform plan` that the newer,
// resource-matching name exists, the same way it would for a writable attribute.

const (
	eventSourceRepoFullNameDeprecationMessage = "Use event_source_repo_full_name instead, which matches " +
		"circleci_trigger's (the resource's) spelling. event_source_repository_name still works and will " +
		"keep working until the next major release; both report the same value."
	eventSourceRepoExternalIDDeprecationMessage = "Use event_source_repo_external_id instead, which matches " +
		"circleci_trigger's (the resource's) spelling. event_source_repository_external_id still works and " +
		"will keep working until the next major release; both report the same value."
	eventSourceWebhookURLDeprecationMessage = "Use event_source_web_hook_url instead, which matches " +
		"circleci_trigger's (the resource's) spelling. event_source_webhook_url still works and will keep " +
		"working until the next major release; both report the same value."
)

// eventSourceRepoFullNameAttributes returns the deprecated `event_source_repository_name`
// attribute and the current `event_source_repo_full_name` attribute, both Computed
// and always carrying the same value.
func eventSourceRepoFullNameAttributes() (deprecated, current schema.StringAttribute) {
	return schema.StringAttribute{
			MarkdownDescription: "Full name (`owner/repo`) of the repository the events come from. Empty " +
				"for webhook and schedule triggers.\n\n" +
				"~> **Deprecated in favour of `event_source_repo_full_name`**, which matches " +
				"`circleci_trigger`'s (the resource's) spelling. Both are reported; prefer " +
				"`event_source_repo_full_name` in new configurations.",
			DeprecationMessage: eventSourceRepoFullNameDeprecationMessage,
			Computed:           true,
		}, schema.StringAttribute{
			MarkdownDescription: "Full name (`owner/repo`) of the repository the events come from. Empty " +
				"for webhook and schedule triggers.\n\n" +
				"Same field as the deprecated `event_source_repository_name`, spelled the way " +
				"`circleci_trigger` (the resource) spells it.",
			Computed: true,
		}
}

// eventSourceRepoExternalIDAttributes returns the deprecated
// `event_source_repository_external_id` attribute and the current
// `event_source_repo_external_id` attribute, both Computed and always carrying the
// same value.
func eventSourceRepoExternalIDAttributes() (deprecated, current schema.StringAttribute) {
	return schema.StringAttribute{
			MarkdownDescription: "Provider-side identifier of the repository the events come from.\n\n" +
				"~> **Deprecated in favour of `event_source_repo_external_id`**, which matches " +
				"`circleci_trigger`'s (the resource's) spelling. Both are reported; prefer " +
				"`event_source_repo_external_id` in new configurations.",
			DeprecationMessage: eventSourceRepoExternalIDDeprecationMessage,
			Computed:           true,
		}, schema.StringAttribute{
			MarkdownDescription: "Provider-side identifier of the repository the events come from.\n\n" +
				"Same field as the deprecated `event_source_repository_external_id`, spelled the " +
				"way `circleci_trigger` (the resource) spells it.",
			Computed: true,
		}
}

// eventSourceWebhookURLAttributes returns the deprecated `event_source_webhook_url`
// attribute and the current `event_source_web_hook_url` attribute, both Computed,
// Sensitive, and always carrying the same value.
func eventSourceWebhookURLAttributes() (deprecated, current schema.StringAttribute) {
	return schema.StringAttribute{
			MarkdownDescription: "Inbound URL for a webhook trigger. Empty for other providers. The API " +
				"redacts the embedded secret unless the token is allowed to see it.\n\n" +
				"~> **Deprecated in favour of `event_source_web_hook_url`**, which matches " +
				"`circleci_trigger`'s (the resource's) spelling. Both are reported; prefer " +
				"`event_source_web_hook_url` in new configurations.",
			DeprecationMessage: eventSourceWebhookURLDeprecationMessage,
			Computed:           true,
			Sensitive:          true,
		}, schema.StringAttribute{
			MarkdownDescription: "Inbound URL for a webhook trigger. Empty for other providers. The API " +
				"redacts the embedded secret unless the token is allowed to see it.\n\n" +
				"Same field as the deprecated `event_source_webhook_url`, spelled the way " +
				"`circleci_trigger` (the resource) spells it.",
			Computed:  true,
			Sensitive: true,
		}
}
