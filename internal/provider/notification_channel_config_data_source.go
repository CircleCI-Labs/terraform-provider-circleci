// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &notificationChannelConfigDataSource{}
	_ datasource.DataSourceWithConfigure = &notificationChannelConfigDataSource{}
)

// notificationChannelConfigDataSourceModel maps the data source schema. It
// reuses the resource's model: every attribute here is Computed except id, so
// the shapes line up exactly.
type notificationChannelConfigDataSourceModel = notificationChannelConfigResourceModel

// NewNotificationChannelConfigDataSource is a helper function to simplify the
// provider implementation.
func NewNotificationChannelConfigDataSource() datasource.DataSource {
	return &notificationChannelConfigDataSource{}
}

// notificationChannelConfigDataSource is the data source implementation.
type notificationChannelConfigDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *notificationChannelConfigDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_channel_config"
}

// Schema defines the schema for the data source.
func (d *notificationChannelConfigDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a CircleCI notification channel config by its id.\n\n" +
			"~> **CircleCI Cloud only.** Channel configs are served by the CircleCI v3 API, which " +
			"CircleCI Server does not route.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the channel config.",
				Required:            true,
			},
			"scope": schema.StringAttribute{
				MarkdownDescription: "Whether this config belongs to a user (`user`) or a project (`project`).",
				Computed:            true,
			},
			"channel_type": schema.StringAttribute{
				MarkdownDescription: "The delivery channel: `email` or `slack`.",
				Computed:            true,
			},
			"target": schema.StringAttribute{
				MarkdownDescription: "The delivery address: an email address, or a Slack channel ID.",
				Computed:            true,
			},
			"channel_name": schema.StringAttribute{
				MarkdownDescription: "The Slack channel name CircleCI resolved for `target`. Populated " +
					"only for a project-scoped Slack config; null otherwise.",
				Computed: true,
			},
			"is_enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether this channel is active.",
				Computed:            true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the project this config belongs to. " +
					"Null for a user-scoped config.",
				Computed: true,
			},
			"org_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization this config is tied to.",
				Computed:            true,
			},
			"user_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the user this config belongs to. Null " +
					"for a project-scoped config.",
				Computed: true,
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *notificationChannelConfigDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// Read fetches the channel config and sets the data source state.
func (d *notificationChannelConfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, "circleci_notification_channel_config", &resp.Diagnostics) {
		return
	}

	var config notificationChannelConfigDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cc, err := d.client.GetNotificationChannelConfig(ctx, config.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI notification channel config "+config.ID.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	applyNotificationChannelConfig(&config, cc)
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
