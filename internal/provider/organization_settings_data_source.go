// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource                     = &organizationSettingsDataSource{}
	_ datasource.DataSourceWithConfigure        = &organizationSettingsDataSource{}
	_ datasource.DataSourceWithConfigValidators = &organizationSettingsDataSource{}
)

// organizationSettingsDataSourceModel maps the data source schema. It mirrors the
// resource model, but here every toggle really is Computed: a read-only view has
// no "unmanaged" case to preserve, so reporting the API's current value for all
// fifteen toggles is exactly right.
type organizationSettingsDataSourceModel struct {
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

// NewOrganizationSettingsDataSource is a helper function to simplify the provider
// implementation.
func NewOrganizationSettingsDataSource() datasource.DataSource {
	return &organizationSettingsDataSource{}
}

// organizationSettingsDataSource reads an organization's settings.
type organizationSettingsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *organizationSettingsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_settings"
}

// Schema defines the schema for the data source.
func (d *organizationSettingsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	toggle := func(description string) schema.BoolAttribute {
		return schema.BoolAttribute{
			MarkdownDescription: description,
			Computed:            true,
		}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches the organization-wide settings for a CircleCI organization.\n\n" +
			"**CircleCI Cloud only.** These settings are served by the CircleCI v3 API, which " +
			"CircleCI Server does not route. Using this data source with `deployment = \"server\"` " +
			"reports an error.",
		Attributes: map[string]schema.Attribute{
			// See org_id_deprecation.go for why the organization is accepted under
			// two names.
			"organization_id":               deprecatedOrgIDDataSourceAttribute("settings"),
			"org_id":                        orgIDDataSourceAttribute("settings"),
			"enable_ai_agents":              toggle("Whether CircleCI AI agents may run for this organization."),
			"enable_ai_error_summarization": toggle("Whether CircleCI generates AI summaries of build and test failures."),
			"enable_certified_public_orbs":  toggle("Whether pipelines may use public orbs certified by CircleCI."),
			"enable_chunk_ip_ranges":        toggle("Whether the organization is opted into chunked IP address ranges."),
			"enable_image_brownouts":        toggle("Whether the organization is opted into scheduled brownouts of deprecated Docker images."),
			"enable_minor_ai_features":      toggle("Whether smaller AI-backed conveniences are enabled in the CircleCI web application."),
			"enable_private_orbs": toggle(
				"Whether the organization may publish and use private orbs. Private orbs cannot be created or referenced while this is `false`.",
			),
			"enable_resource_class_brownouts": toggle("Whether the organization is opted into scheduled brownouts of deprecated resource classes."),
			"enable_uncertified_public_orbs":  toggle("Whether pipelines may use public orbs that CircleCI has not certified."),
			"enable_unversioned_config":       toggle("Whether pipelines may run configuration that carries no `version` pin."),
			"is_bitbucket_workspace_member_org_member": toggle(
				"Whether every member of the linked Bitbucket workspace counts as a member of this CircleCI organization.",
			),
			"is_context_group_restriction_required": toggle(
				"Whether every context in this organization must carry at least one group restriction.",
			),
			"is_runner_terms_of_service_accepted": toggle(
				"Whether the CircleCI self-hosted runner terms of service have been accepted. Runner onboarding is gated on this: runner resource classes and tokens are unavailable while it is `false`.",
			),
			"is_running_disabled": toggle("Whether the organization is blocked from running pipelines."),
			"is_user_checkout_keys_disabled": toggle(
				"Whether user checkout keys are forbidden, leaving deploy keys as the only checkout credential.",
			),
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (d *organizationSettingsDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		orgIDDataSourceConfigValidator(),
	}
}

// Read fetches the organization's settings.
func (d *organizationSettingsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil || !requireCloud(d.client, organizationSettingsTypeName, &resp.Diagnostics) {
		return
	}

	var data organizationSettingsDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := effectiveOrgID(data.OrganizationID, data.OrgID)

	settings, err := d.client.GetOrganizationSettings(ctx, orgID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI organization settings for "+orgID,
			circleci.Detail(err),
		)

		return
	}

	data.EnableAIAgents = types.BoolPointerValue(settings.EnableAIAgents)
	data.EnableAIErrorSummarization = types.BoolPointerValue(settings.EnableAIErrorSummarization)
	data.EnableCertifiedPublicOrbs = types.BoolPointerValue(settings.EnableCertifiedPublicOrbs)
	data.EnableChunkIPRanges = types.BoolPointerValue(settings.EnableChunkIPRanges)
	data.EnableImageBrownouts = types.BoolPointerValue(settings.EnableImageBrownouts)
	data.EnableMinorAIFeatures = types.BoolPointerValue(settings.EnableMinorAIFeatures)
	data.EnablePrivateOrbs = types.BoolPointerValue(settings.EnablePrivateOrbs)
	data.EnableResourceClassBrownouts = types.BoolPointerValue(settings.EnableResourceClassBrownouts)
	data.EnableUncertifiedPublicOrbs = types.BoolPointerValue(settings.EnableUncertifiedPublicOrbs)
	data.EnableUnversionedConfig = types.BoolPointerValue(settings.EnableUnversionedConfig)
	data.IsBitbucketWorkspaceMemberOrgMember = types.BoolPointerValue(settings.IsBitbucketWorkspaceMemberOrgMember)
	data.IsContextGroupRestrictionRequired = types.BoolPointerValue(settings.IsContextGroupRestrictionRequired)
	data.IsRunnerTermsOfServiceAccepted = types.BoolPointerValue(settings.IsRunnerTermsOfServiceAccepted)
	data.IsRunningDisabled = types.BoolPointerValue(settings.IsRunningDisabled)
	data.IsUserCheckoutKeysDisabled = types.BoolPointerValue(settings.IsUserCheckoutKeysDisabled)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// Configure adds the provider configured client to the data source.
func (d *organizationSettingsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
