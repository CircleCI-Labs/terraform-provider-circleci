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
	_ datasource.DataSource              = &notificationIntegrationsDataSource{}
	_ datasource.DataSourceWithConfigure = &notificationIntegrationsDataSource{}
)

// notificationIntegrationItemModel is one entry of the integrations list.
type notificationIntegrationItemModel struct {
	ID            types.String `tfsdk:"id"`
	Type          types.String `tfsdk:"type"`
	WorkspaceName types.String `tfsdk:"workspace_name"`
	TeamID        types.String `tfsdk:"team_id"`
	Status        types.String `tfsdk:"status"`
	CreatedAt     types.String `tfsdk:"created_at"`
	UpdatedAt     types.String `tfsdk:"updated_at"`
	OrgID         types.String `tfsdk:"org_id"`
	OrgName       types.String `tfsdk:"org_name"`
}

// notificationIntegrationsDataSourceModel maps the data source schema.
type notificationIntegrationsDataSourceModel struct {
	OrgID        types.String                       `tfsdk:"org_id"`
	Type         types.String                       `tfsdk:"type"`
	Integrations []notificationIntegrationItemModel `tfsdk:"integrations"`
}

// NewNotificationIntegrationsDataSource is a helper function to simplify the
// provider implementation.
func NewNotificationIntegrationsDataSource() datasource.DataSource {
	return &notificationIntegrationsDataSource{}
}

// notificationIntegrationsDataSource is the data source implementation.
//
// There is deliberately no corresponding resource for creating an integration:
// a Slack workspace is installed through an OAuth flow in the CircleCI web UI,
// which this API does not expose. See circleci_notification_integration_status
// for the one integration attribute that is genuinely a piece of desired
// state.
type notificationIntegrationsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *notificationIntegrationsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_integrations"
}

// Schema defines the schema for the data source.
func (d *notificationIntegrationsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists CircleCI notification integrations (Slack workspace installations, " +
			"today). Omit `org_id` to list every integration for every organization the calling user " +
			"belongs to.\n\n" +
			"~> **CircleCI Cloud only.** Integrations are served by the CircleCI v3 API, which CircleCI " +
			"Server does not route.\n\n" +
			"There is no resource to create an integration: a Slack workspace is installed through an " +
			"OAuth flow in the CircleCI web UI, not through this API. Use " +
			"`circleci_notification_integration_status` to manage whether an installed integration is " +
			"active or disabled.",
		Attributes: map[string]schema.Attribute{
			"org_id": schema.StringAttribute{
				MarkdownDescription: "Only return integrations belonging to this organization (UUID). " +
					"Leave unset to list every organization the calling user belongs to.",
				Optional: true,
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "Only return integrations of this type. The only type today is " +
					"`" + circleci.NotificationIntegrationTypeSlack + "`.",
				Optional: true,
			},
			"integrations": schema.ListNestedAttribute{
				MarkdownDescription: "The matching integrations.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the integration.",
							Computed:            true,
						},
						"type": schema.StringAttribute{
							MarkdownDescription: "The integration type.",
							Computed:            true,
						},
						"workspace_name": schema.StringAttribute{
							MarkdownDescription: "The Slack workspace's display name.",
							Computed:            true,
						},
						"team_id": schema.StringAttribute{
							MarkdownDescription: "The Slack team id of the installed workspace.",
							Computed:            true,
						},
						"status": schema.StringAttribute{
							MarkdownDescription: "The integration's current status: `" +
								circleci.NotificationIntegrationStatusActive + "`, `" +
								circleci.NotificationIntegrationStatusDisabled + "` or `" +
								circleci.NotificationIntegrationStatusDisconnected + "`.",
							Computed: true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "When the integration was installed, as a UTC timestamp.",
							Computed:            true,
						},
						"updated_at": schema.StringAttribute{
							MarkdownDescription: "When the integration was last changed, as a UTC timestamp.",
							Computed:            true,
						},
						"org_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the owning organization.",
							Computed:            true,
						},
						"org_name": schema.StringAttribute{
							MarkdownDescription: "Display name of the owning organization.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *notificationIntegrationsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// Read lists the integrations and sets the data source state.
func (d *notificationIntegrationsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, "circleci_notification_integrations", &resp.Diagnostics) {
		return
	}

	var config notificationIntegrationsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	integrations, err := d.client.ListNotificationIntegrations(ctx, circleci.ListNotificationIntegrationsOptions{
		OrgID: config.OrgID.ValueString(),
		Type:  config.Type.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI notification integrations",
			circleci.Detail(err),
		)

		return
	}

	config.Integrations = make([]notificationIntegrationItemModel, 0, len(integrations))
	for _, integration := range integrations {
		config.Integrations = append(config.Integrations, notificationIntegrationItemModel{
			ID:            types.StringValue(integration.ID),
			Type:          types.StringValue(integration.Type),
			WorkspaceName: types.StringValue(integration.WorkspaceName),
			TeamID:        types.StringValue(integration.TeamID),
			Status:        types.StringValue(integration.Status),
			CreatedAt:     types.StringValue(integration.CreatedAt),
			UpdatedAt:     types.StringValue(integration.UpdatedAt),
			OrgID:         types.StringValue(integration.OrgID),
			OrgName:       types.StringValue(integration.OrgName),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
