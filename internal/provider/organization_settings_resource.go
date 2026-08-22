// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                     = &organizationSettingsResource{}
	_ resource.ResourceWithConfigure        = &organizationSettingsResource{}
	_ resource.ResourceWithImportState      = &organizationSettingsResource{}
	_ resource.ResourceWithConfigValidators = &organizationSettingsResource{}
)

// organizationSettingsTypeName is the Terraform type name, used both for
// Metadata and for the CircleCI Server error message.
const organizationSettingsTypeName = "circleci_organization_settings"

// organizationSettingsResourceModel maps the resource schema.
//
// Every toggle is types.Bool and Optional-only, never Computed. See the Schema
// method for why that matters.
type organizationSettingsResourceModel struct {
	OrganizationID                      types.String `tfsdk:"organization_id"`
	OrgID                               types.String `tfsdk:"org_id"`
	EnableAIAgents                      types.Bool   `tfsdk:"enable_ai_agents"`
	EnableAIErrorSummarization          types.Bool   `tfsdk:"enable_ai_error_summarization"`
	EnableCertifiedPublicOrbs           types.Bool   `tfsdk:"enable_certified_public_orbs"`
	EnableChunkIPRanges                 types.Bool   `tfsdk:"enable_chunk_ip_ranges"`
	EnableImageBrownouts                types.Bool   `tfsdk:"enable_image_brownouts"`
	EnableMinorAIFeatures               types.Bool   `tfsdk:"enable_minor_ai_features"`
	EnablePrivateOrbs                   types.Bool   `tfsdk:"enable_private_orbs"`
	EnableResourceClassBrownouts        types.Bool   `tfsdk:"enable_resource_class_brownouts"`
	EnableUncertifiedPublicOrbs         types.Bool   `tfsdk:"enable_uncertified_public_orbs"`
	EnableUnversionedConfig             types.Bool   `tfsdk:"enable_unversioned_config"`
	IsBitbucketWorkspaceMemberOrgMember types.Bool   `tfsdk:"is_bitbucket_workspace_member_org_member"`
	IsContextGroupRestrictionRequired   types.Bool   `tfsdk:"is_context_group_restriction_required"`
	IsRunnerTermsOfServiceAccepted      types.Bool   `tfsdk:"is_runner_terms_of_service_accepted"`
	IsRunningDisabled                   types.Bool   `tfsdk:"is_running_disabled"`
	IsUserCheckoutKeysDisabled          types.Bool   `tfsdk:"is_user_checkout_keys_disabled"`
}

// orgSettingRequest converts a schema value into an update payload field. A null
// or unknown value becomes nil, and a nil field is omitted from the request
// body, so a toggle the configuration says nothing about is left untouched.
func orgSettingRequest(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}

	return v.ValueBoolPointer()
}

// orgSettingRefresh folds an API value back into state.
//
// It deliberately keeps a null configured value null instead of adopting what
// the API reports. Adopting it would make the toggle indistinguishable from one
// the practitioner set to false, and the next apply would start writing it.
func orgSettingRefresh(configured types.Bool, remote *bool) types.Bool {
	if configured.IsNull() {
		return types.BoolNull()
	}

	return types.BoolPointerValue(remote)
}

// payload builds the partial update body for the toggles this configuration
// manages.
func (m organizationSettingsResourceModel) payload() circleci.OrganizationSettings {
	return circleci.OrganizationSettings{
		EnableAIAgents:                      orgSettingRequest(m.EnableAIAgents),
		EnableAIErrorSummarization:          orgSettingRequest(m.EnableAIErrorSummarization),
		EnableCertifiedPublicOrbs:           orgSettingRequest(m.EnableCertifiedPublicOrbs),
		EnableChunkIPRanges:                 orgSettingRequest(m.EnableChunkIPRanges),
		EnableImageBrownouts:                orgSettingRequest(m.EnableImageBrownouts),
		EnableMinorAIFeatures:               orgSettingRequest(m.EnableMinorAIFeatures),
		EnablePrivateOrbs:                   orgSettingRequest(m.EnablePrivateOrbs),
		EnableResourceClassBrownouts:        orgSettingRequest(m.EnableResourceClassBrownouts),
		EnableUncertifiedPublicOrbs:         orgSettingRequest(m.EnableUncertifiedPublicOrbs),
		EnableUnversionedConfig:             orgSettingRequest(m.EnableUnversionedConfig),
		IsBitbucketWorkspaceMemberOrgMember: orgSettingRequest(m.IsBitbucketWorkspaceMemberOrgMember),
		IsContextGroupRestrictionRequired:   orgSettingRequest(m.IsContextGroupRestrictionRequired),
		IsRunnerTermsOfServiceAccepted:      orgSettingRequest(m.IsRunnerTermsOfServiceAccepted),
		IsRunningDisabled:                   orgSettingRequest(m.IsRunningDisabled),
		IsUserCheckoutKeysDisabled:          orgSettingRequest(m.IsUserCheckoutKeysDisabled),
	}
}

// refresh overwrites the managed toggles with the values the API reports,
// leaving unmanaged ones null.
func (m *organizationSettingsResourceModel) refresh(remote *circleci.OrganizationSettings) {
	m.EnableAIAgents = orgSettingRefresh(m.EnableAIAgents, remote.EnableAIAgents)
	m.EnableAIErrorSummarization = orgSettingRefresh(m.EnableAIErrorSummarization, remote.EnableAIErrorSummarization)
	m.EnableCertifiedPublicOrbs = orgSettingRefresh(m.EnableCertifiedPublicOrbs, remote.EnableCertifiedPublicOrbs)
	m.EnableChunkIPRanges = orgSettingRefresh(m.EnableChunkIPRanges, remote.EnableChunkIPRanges)
	m.EnableImageBrownouts = orgSettingRefresh(m.EnableImageBrownouts, remote.EnableImageBrownouts)
	m.EnableMinorAIFeatures = orgSettingRefresh(m.EnableMinorAIFeatures, remote.EnableMinorAIFeatures)
	m.EnablePrivateOrbs = orgSettingRefresh(m.EnablePrivateOrbs, remote.EnablePrivateOrbs)
	m.EnableResourceClassBrownouts = orgSettingRefresh(m.EnableResourceClassBrownouts, remote.EnableResourceClassBrownouts)
	m.EnableUncertifiedPublicOrbs = orgSettingRefresh(m.EnableUncertifiedPublicOrbs, remote.EnableUncertifiedPublicOrbs)
	m.EnableUnversionedConfig = orgSettingRefresh(m.EnableUnversionedConfig, remote.EnableUnversionedConfig)
	m.IsBitbucketWorkspaceMemberOrgMember = orgSettingRefresh(m.IsBitbucketWorkspaceMemberOrgMember, remote.IsBitbucketWorkspaceMemberOrgMember)
	m.IsContextGroupRestrictionRequired = orgSettingRefresh(m.IsContextGroupRestrictionRequired, remote.IsContextGroupRestrictionRequired)
	m.IsRunnerTermsOfServiceAccepted = orgSettingRefresh(m.IsRunnerTermsOfServiceAccepted, remote.IsRunnerTermsOfServiceAccepted)
	m.IsRunningDisabled = orgSettingRefresh(m.IsRunningDisabled, remote.IsRunningDisabled)
	m.IsUserCheckoutKeysDisabled = orgSettingRefresh(m.IsUserCheckoutKeysDisabled, remote.IsUserCheckoutKeysDisabled)
}

// NewOrganizationSettingsResource is a helper function to simplify the provider
// implementation.
func NewOrganizationSettingsResource() resource.Resource {
	return &organizationSettingsResource{}
}

// organizationSettingsResource manages the organization-wide settings record.
//
// It is a singleton settings resource: it creates nothing and destroys nothing.
// The settings record exists for as long as the organization does, so this
// resource only ever reads it and writes over parts of it.
type organizationSettingsResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *organizationSettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_settings"
}

// Schema defines the schema for the resource.
func (r *organizationSettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// Every toggle below is Optional and never Computed, which is the central
	// design decision of this resource.
	//
	// Optional+Computed would read better in the docs, but it would make
	// Terraform adopt whatever the API currently reports into state for a toggle
	// the configuration never mentions. From then on "not managed" and "managed
	// as false" look identical in state, and the provider would start writing
	// settings nobody asked it to manage. Optional-only keeps an unmentioned
	// toggle null, and only non-null toggles are ever sent.
	toggle := func(description string) schema.BoolAttribute {
		return schema.BoolAttribute{
			MarkdownDescription: description +
				"\n\nLeave this unset to let CircleCI manage it; the provider only writes toggles that appear in the configuration.",
			Optional: true,
		}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages organization-wide CircleCI settings.\n\n" +
			"**CircleCI Cloud only.** These settings are served by the CircleCI v3 API, which " +
			"CircleCI Server does not route. Using this resource with `deployment = \"server\"` " +
			"reports an error.\n\n" +
			"This is a settings resource rather than a thing that gets created and destroyed. " +
			"The settings record lives with the organization, so `terraform destroy` only drops it " +
			"from state and leaves every value as it stands. Each toggle is written only when the " +
			"configuration sets it, so several configurations may safely manage disjoint toggles " +
			"on the same organization.",
		Attributes: map[string]schema.Attribute{
			// See org_id_deprecation.go for why these are Optional+Computed and why
			// replacement is conditional on being configured.
			"organization_id":  deprecatedOrgIDAttribute("these settings", true),
			"org_id":           orgIDAttribute("these settings", true),
			"enable_ai_agents": toggle("Allow CircleCI AI agents to run for this organization."),
			"enable_ai_error_summarization": toggle(
				"Allow CircleCI to generate AI summaries of build and test failures.",
			),
			"enable_certified_public_orbs": toggle(
				"Allow pipelines in this organization to use public orbs certified by CircleCI.",
			),
			"enable_chunk_ip_ranges": toggle(
				"Opt into chunked IP address ranges for jobs that use the IP ranges feature.",
			),
			"enable_image_brownouts": toggle(
				"Opt into scheduled brownouts of deprecated Docker images, so pipelines fail during the brownout window rather than at final removal.",
			),
			"enable_minor_ai_features": toggle(
				"Allow smaller AI-backed conveniences in the CircleCI web application.",
			),
			"enable_private_orbs": toggle(
				"Allow this organization to publish and use private orbs. Private orbs cannot be created or referenced while this is disabled, so publishing pipelines depend on it.",
			),
			"enable_resource_class_brownouts": toggle(
				"Opt into scheduled brownouts of deprecated resource classes.",
			),
			"enable_uncertified_public_orbs": toggle(
				"Allow pipelines in this organization to use public orbs that CircleCI has not certified.",
			),
			// The attribute name is CircleCI's; the description is what the setting
			// does. The API translates this key to `allow_api_trigger_with_config`
			// before storing it, and that is the accurate name: it governs whether a
			// pipeline may be triggered through the API with configuration supplied in
			// the request instead of taken from the repository. It has nothing to do
			// with a `version` pin.
			"enable_unversioned_config": toggle(
				"Allow a pipeline to be triggered through the API with configuration supplied in the " +
					"request, instead of only the configuration committed to the repository.\n\n" +
					"~> Despite the name, this is not about a `version` pin. CircleCI stores it as " +
					"`allow_api_trigger_with_config`.",
			),
			"is_bitbucket_workspace_member_org_member": toggle(
				"Treat every member of the linked Bitbucket workspace as a member of this CircleCI organization.",
			),
			"is_context_group_restriction_required": toggle(
				"Require every context in this organization to carry at least one group restriction. A context starts with a permissive default `group` restriction (\"All members\"), so this mainly forecloses removing every `group` restriction — which, per CircleCI's documentation, otherwise locks the context down to organization administrators only.",
			),
			"is_runner_terms_of_service_accepted": toggle(
				"Record acceptance of the CircleCI self-hosted runner terms of service. Runner onboarding is gated on this: `circleci_runner_resource_class` and `circleci_runner_token` cannot be used for this organization until it is `true`.",
			),
			"is_running_disabled": toggle(
				"Stop this organization from running any pipeline. Setting this to `true` halts all builds.",
			),
			"is_user_checkout_keys_disabled": toggle(
				"Forbid user checkout keys for this organization, leaving deploy keys as the only checkout credential. Projects that rely on a user key lose the ability to check out submodules and other private repositories.",
			),
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (r *organizationSettingsResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		orgIDConfigValidator(),
	}
}

// Create applies the configured toggles to the organization's existing settings.
//
// Nothing is created: the settings record already exists. The current settings
// are read first so that a wrong organization id fails before anything is
// written, and so the resulting state reflects what the API actually holds.
func (r *organizationSettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil || !requireCloud(r.client, organizationSettingsTypeName, &resp.Diagnostics) {
		return
	}

	var plan organizationSettingsResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := effectiveOrgID(plan.OrganizationID, plan.OrgID)
	setOrgIDs(&plan.OrganizationID, &plan.OrgID, orgID)

	current, err := r.client.GetOrganizationSettings(ctx, orgID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI organization settings for "+orgID,
			circleci.Detail(err),
		)

		return
	}

	settings := plan.payload()
	if settings.IsEmpty() {
		// No toggle is configured, so there is nothing to write. The resource
		// still tracks the organization, which is a legitimate way to adopt it
		// before deciding which toggles to manage.
		resp.Diagnostics.AddWarning(
			"No CircleCI organization settings configured",
			fmt.Sprintf(
				"%s for organization %s sets no toggles, so nothing was written. "+
					"Set at least one toggle for this resource to have an effect.",
				organizationSettingsTypeName, orgID,
			),
		)

		plan.refresh(current)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

		return
	}

	updated, err := r.client.UpdateOrganizationSettings(ctx, orgID, settings)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to update CircleCI organization settings for "+orgID,
			circleci.Detail(err),
		)

		return
	}

	plan.refresh(updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the managed toggles from the API.
func (r *organizationSettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil || !requireCloud(r.client, organizationSettingsTypeName, &resp.Diagnostics) {
		return
	}

	var state organizationSettingsResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := effectiveOrgID(state.OrganizationID, state.OrgID)
	setOrgIDs(&state.OrganizationID, &state.OrgID, orgID)

	settings, err := r.client.GetOrganizationSettings(ctx, orgID)
	if err != nil {
		// A 404 here names its own ambiguity: reproduced against the live API on
		// 2026-08-21, GET .../settings for an organization id that does not exist
		// answers 404 with {"error": {"type": "404", "title": "Org not found."}}
		// — the same "Org not found." wording GetOrganization documents as
		// anti-enumeration (internal/circleci/organization.go), and the same
		// general pattern IsUnauthorized's doc comment names for v3: a permission
		// problem surfaces as a 404, not a 403. There is nothing left for this
		// resource to manage either way, so it still comes out of state — but
		// dropping it with no diagnostic at all would leave a practitioner
		// staring at a vanished resource with no explanation, so this warns
		// exactly like circleci_organization's own Read does for the identical
		// ambiguity.
		if circleci.IsNotFound(err) {
			resp.Diagnostics.AddWarning(
				"CircleCI organization settings not found during Read",
				fmt.Sprintf(
					"Organization %s could not be retrieved from CircleCI, so its settings could not "+
						"either. This means either the organization was deleted, or the configured token "+
						"can no longer view it — the API answers the same way for both. Removing "+
						"%s from state.",
					orgID, organizationSettingsTypeName,
				),
			)
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read CircleCI organization settings for "+orgID,
			circleci.Detail(err),
		)

		return
	}

	state.refresh(settings)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update writes the configured toggles. Every setting is updatable, so unlike
// most resources here this is a real implementation rather than a no-op.
func (r *organizationSettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil || !requireCloud(r.client, organizationSettingsTypeName, &resp.Diagnostics) {
		return
	}

	var plan, state organizationSettingsResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := effectiveOrgID(plan.OrganizationID, plan.OrgID)
	setOrgIDs(&plan.OrganizationID, &plan.OrgID, orgID)

	// Removing a toggle from the configuration stops managing it; it does not
	// revert it. There is no route that restores a CircleCI default, so say so
	// rather than leave the practitioner to discover it.
	warnAbandonedOrgSettings(state, plan, orgID, &resp.Diagnostics)

	settings := plan.payload()
	if settings.IsEmpty() {
		current, err := r.client.GetOrganizationSettings(ctx, orgID)
		if err != nil {
			resp.Diagnostics.AddError(
				"Unable to read CircleCI organization settings for "+orgID,
				circleci.Detail(err),
			)

			return
		}

		plan.refresh(current)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

		return
	}

	updated, err := r.client.UpdateOrganizationSettings(ctx, orgID, settings)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to update CircleCI organization settings for "+orgID,
			circleci.Detail(err),
		)

		return
	}

	plan.refresh(updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the resource from state without calling the API.
//
// This is deliberate and is not a missing implementation. An organization's
// settings record cannot be deleted: it exists as long as the organization does,
// and every toggle has a live value at all times. There is no "unset" route
// either, so the closest thing to destroying this resource is to stop tracking
// it. The organization keeps whatever values were last applied.
func (r *organizationSettingsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// requireCloud is still called so that the failure mode is consistent: a
	// Server-configured provider reports the same clear error here as elsewhere,
	// rather than appearing to succeed at something it never supported.
	if r.client == nil || !requireCloud(r.client, organizationSettingsTypeName, &resp.Diagnostics) {
		return
	}

	// No API call. The framework drops the resource from state when Delete
	// returns without error.
	resp.Diagnostics.AddWarning(
		"CircleCI organization settings left in place",
		fmt.Sprintf(
			"%s was removed from Terraform state, but CircleCI has no way to delete or reset an "+
				"organization's settings, so every toggle keeps its current value. Change the values "+
				"explicitly if that is not what you want.",
			organizationSettingsTypeName,
		),
	)
}

// Configure adds the provider configured client to the resource.
func (r *organizationSettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports the settings of an existing organization by its id.
//
// Only the organization is set: the toggles are left null on purpose, so the
// first plan after an import shows exactly the toggles the configuration asks
// to manage instead of every setting the organization happens to have.
func (r *organizationSettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Both organization attribute names are set, so a configuration written
	// against either one imports cleanly. See org_id_deprecation.go.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("org_id"), req.ID)...)
}

// warnAbandonedOrgSettings reports toggles that state managed but the new plan
// no longer sets.
func warnAbandonedOrgSettings(state, plan organizationSettingsResourceModel, orgID string, diags *diag.Diagnostics) {
	// Ordered so the warning text is stable between runs.
	managed := []struct {
		name        string
		was, wanted types.Bool
	}{
		{"enable_ai_agents", state.EnableAIAgents, plan.EnableAIAgents},
		{"enable_ai_error_summarization", state.EnableAIErrorSummarization, plan.EnableAIErrorSummarization},
		{"enable_certified_public_orbs", state.EnableCertifiedPublicOrbs, plan.EnableCertifiedPublicOrbs},
		{"enable_chunk_ip_ranges", state.EnableChunkIPRanges, plan.EnableChunkIPRanges},
		{"enable_image_brownouts", state.EnableImageBrownouts, plan.EnableImageBrownouts},
		{"enable_minor_ai_features", state.EnableMinorAIFeatures, plan.EnableMinorAIFeatures},
		{"enable_private_orbs", state.EnablePrivateOrbs, plan.EnablePrivateOrbs},
		{"enable_resource_class_brownouts", state.EnableResourceClassBrownouts, plan.EnableResourceClassBrownouts},
		{"enable_uncertified_public_orbs", state.EnableUncertifiedPublicOrbs, plan.EnableUncertifiedPublicOrbs},
		{"enable_unversioned_config", state.EnableUnversionedConfig, plan.EnableUnversionedConfig},
		{"is_bitbucket_workspace_member_org_member", state.IsBitbucketWorkspaceMemberOrgMember, plan.IsBitbucketWorkspaceMemberOrgMember},
		{"is_context_group_restriction_required", state.IsContextGroupRestrictionRequired, plan.IsContextGroupRestrictionRequired},
		{"is_runner_terms_of_service_accepted", state.IsRunnerTermsOfServiceAccepted, plan.IsRunnerTermsOfServiceAccepted},
		{"is_running_disabled", state.IsRunningDisabled, plan.IsRunningDisabled},
		{"is_user_checkout_keys_disabled", state.IsUserCheckoutKeysDisabled, plan.IsUserCheckoutKeysDisabled},
	}

	abandoned := make([]string, 0, len(managed))
	for _, toggle := range managed {
		if !toggle.was.IsNull() && toggle.wanted.IsNull() {
			abandoned = append(abandoned, toggle.name)
		}
	}

	if len(abandoned) == 0 {
		return
	}

	diags.AddWarning(
		"CircleCI organization settings no longer managed",
		fmt.Sprintf(
			"These toggles were removed from the configuration for organization %s: %s.\n\n"+
				"CircleCI has no route that reverts a setting to its default, so each keeps the value "+
				"Terraform last applied. Set it explicitly if you need a different value.",
			orgID, strings.Join(abandoned, ", "),
		),
	)
}
