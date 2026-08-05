// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Routes for organization settings.
//
// v3 has no PATCH: partial updates are POSTed to a sibling verb route, so the
// read and the write are two different paths rather than two methods on one.
const (
	organizationSettingsRoute       = "/orgs/%s/settings"
	organizationUpdateSettingsRoute = "/orgs/%s/update-settings"
)

// OrganizationSettings holds the organization-wide toggles served by
// GET /api/v3/orgs/{id}/settings and accepted by
// POST /api/v3/orgs/{id}/update-settings.
//
// Every field is a *bool, and every field is omitempty, for one reason: the
// update route is a partial update over a shared settings record, so a field
// present in the request body is written and a field absent from it is left
// alone. If these were plain bools, marshalling a struct that carried only one
// intended change would also send false for the other fourteen toggles and
// silently switch off whatever the organization had enabled. A nil pointer is
// the only way to say "not managed here".
//
// The API declares these nullable on the request, but the provider never sends
// an explicit null: there is no route that reverts a toggle to a default, so a
// null would be indistinguishable from omitting the field.
type OrganizationSettings struct {
	// EnableAIAgents allows CircleCI AI agents to run for the organization.
	EnableAIAgents *bool `json:"enable_ai_agents,omitempty"`
	// EnableAIErrorSummarization allows AI-generated summaries of build errors.
	EnableAIErrorSummarization *bool `json:"enable_ai_error_summarization,omitempty"`
	// EnableCertifiedPublicOrbs allows use of CircleCI-certified public orbs.
	EnableCertifiedPublicOrbs *bool `json:"enable_certified_public_orbs,omitempty"`
	// EnableChunkIPRanges opts the organization into chunked IP ranges.
	EnableChunkIPRanges *bool `json:"enable_chunk_ip_ranges,omitempty"`
	// EnableImageBrownouts opts the organization into scheduled brownouts of
	// deprecated Docker images.
	EnableImageBrownouts *bool `json:"enable_image_brownouts,omitempty"`
	// EnableMinorAIFeatures allows smaller AI-backed conveniences in the UI.
	EnableMinorAIFeatures *bool `json:"enable_minor_ai_features,omitempty"`
	// EnablePrivateOrbs allows the organization to publish and use private orbs.
	EnablePrivateOrbs *bool `json:"enable_private_orbs,omitempty"`
	// EnableResourceClassBrownouts opts the organization into scheduled brownouts
	// of deprecated resource classes.
	EnableResourceClassBrownouts *bool `json:"enable_resource_class_brownouts,omitempty"`
	// EnableUncertifiedPublicOrbs allows use of public orbs CircleCI has not
	// certified.
	EnableUncertifiedPublicOrbs *bool `json:"enable_uncertified_public_orbs,omitempty"`
	// EnableUnversionedConfig allows a pipeline to be triggered through the API
	// with configuration supplied in the request, rather than only the
	// configuration committed to the repository.
	//
	// The v3 name is misleading and the underlying name is the accurate one: this
	// key is translated to allow_api_trigger_with_config on the way to the service
	// that stores it. It is not about unpinned config versions.
	EnableUnversionedConfig *bool `json:"enable_unversioned_config,omitempty"`
	// IsBitbucketWorkspaceMemberOrgMember treats every member of the linked
	// Bitbucket workspace as a member of the CircleCI organization.
	IsBitbucketWorkspaceMemberOrgMember *bool `json:"is_bitbucket_workspace_member_org_member,omitempty"`
	// IsContextGroupRestrictionRequired requires every context to carry at least
	// one group restriction.
	IsContextGroupRestrictionRequired *bool `json:"is_context_group_restriction_required,omitempty"`
	// IsRunnerTermsOfServiceAccepted records acceptance of the self-hosted runner
	// terms of service, which gates runner onboarding for the organization.
	IsRunnerTermsOfServiceAccepted *bool `json:"is_runner_terms_of_service_accepted,omitempty"`
	// IsRunningDisabled stops the organization from running any pipeline.
	IsRunningDisabled *bool `json:"is_running_disabled,omitempty"`
	// IsUserCheckoutKeysDisabled forbids user checkout keys, leaving deploy keys
	// as the only checkout credential.
	IsUserCheckoutKeysDisabled *bool `json:"is_user_checkout_keys_disabled,omitempty"`
}

// IsEmpty reports whether no toggle is set, meaning an update would carry an
// empty body and change nothing. Callers skip the request in that case rather
// than POST a no-op.
func (s OrganizationSettings) IsEmpty() bool {
	for _, field := range []*bool{
		s.EnableAIAgents,
		s.EnableAIErrorSummarization,
		s.EnableCertifiedPublicOrbs,
		s.EnableChunkIPRanges,
		s.EnableImageBrownouts,
		s.EnableMinorAIFeatures,
		s.EnablePrivateOrbs,
		s.EnableResourceClassBrownouts,
		s.EnableUncertifiedPublicOrbs,
		s.EnableUnversionedConfig,
		s.IsBitbucketWorkspaceMemberOrgMember,
		s.IsContextGroupRestrictionRequired,
		s.IsRunnerTermsOfServiceAccepted,
		s.IsRunningDisabled,
		s.IsUserCheckoutKeysDisabled,
	} {
		if field != nil {
			return false
		}
	}

	return true
}

// organizationSettingsBody is the entity inside the v3 envelope. Settings are a
// facet of the organization rather than an entity of their own, so the envelope
// carries attributes but no id and no references: the response is
// {"data": {"attributes": {...}}}.
type organizationSettingsBody struct {
	Attributes OrganizationSettings `json:"attributes"`
}

// GetOrganizationSettings reads the settings for an organization.
//
// v3 only: CircleCI Server does not route /api/v3, so callers must gate this on
// Client.IsCloud rather than let it fail as a 404.
func (c *Client) GetOrganizationSettings(ctx context.Context, orgID string) (*OrganizationSettings, error) {
	var envelope Entity[organizationSettingsBody]

	err := c.GetV3(ctx, organizationSettingsRoute, &envelope, RouteParams(orgID))
	if err != nil {
		return nil, err
	}

	settings := envelope.Data.Attributes

	return &settings, nil
}

// UpdateOrganizationSettings applies a partial update to an organization's
// settings and returns the full settings as the API reports them afterwards.
//
// Only the non-nil fields of settings are sent. The route is a POST to
// /update-settings rather than a PATCH, which is how v3 spells partial updates.
func (c *Client) UpdateOrganizationSettings(ctx context.Context, orgID string, settings OrganizationSettings) (*OrganizationSettings, error) {
	var envelope Entity[organizationSettingsBody]

	// The request body is the bare settings object, not the data/attributes
	// envelope; only the response is enveloped.
	err := c.PostV3(ctx, organizationUpdateSettingsRoute, settings, &envelope, RouteParams(orgID))
	if err != nil {
		return nil, err
	}

	updated := envelope.Data.Attributes

	return &updated, nil
}
