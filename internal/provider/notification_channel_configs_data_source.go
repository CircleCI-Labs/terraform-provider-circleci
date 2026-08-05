// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource                   = &notificationChannelConfigsDataSource{}
	_ datasource.DataSourceWithConfigure      = &notificationChannelConfigsDataSource{}
	_ datasource.DataSourceWithValidateConfig = &notificationChannelConfigsDataSource{}
)

// notificationChannelConfigItemModel is one entry of the channel configs list.
type notificationChannelConfigItemModel struct {
	ID          types.String `tfsdk:"id"`
	Scope       types.String `tfsdk:"scope"`
	ChannelType types.String `tfsdk:"channel_type"`
	Target      types.String `tfsdk:"target"`
	ChannelName types.String `tfsdk:"channel_name"`
	IsEnabled   types.Bool   `tfsdk:"is_enabled"`
	ProjectID   types.String `tfsdk:"project_id"`
	OrgID       types.String `tfsdk:"org_id"`
	UserID      types.String `tfsdk:"user_id"`
}

// notificationChannelConfigsDataSourceModel maps the data source schema.
type notificationChannelConfigsDataSourceModel struct {
	Scope     types.String                         `tfsdk:"scope"`
	ProjectID types.String                         `tfsdk:"project_id"`
	OrgID     types.String                         `tfsdk:"org_id"`
	Configs   []notificationChannelConfigItemModel `tfsdk:"channel_configs"`
}

// NewNotificationChannelConfigsDataSource is a helper function to simplify the
// provider implementation.
func NewNotificationChannelConfigsDataSource() datasource.DataSource {
	return &notificationChannelConfigsDataSource{}
}

// notificationChannelConfigsDataSource is the data source implementation.
type notificationChannelConfigsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *notificationChannelConfigsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_channel_configs"
}

// Schema defines the schema for the data source.
func (d *notificationChannelConfigsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists CircleCI notification channel configs for the calling user, or for " +
			"one project.\n\n" +
			"~> **CircleCI Cloud only.** Channel configs are served by the CircleCI v3 API, which " +
			"CircleCI Server does not route.",
		Attributes: map[string]schema.Attribute{
			"scope": schema.StringAttribute{
				MarkdownDescription: "Whether to list the calling user's own configs (`user`) or a " +
					"project's configs (`project`). `project_id` and `org_id` are required when this is " +
					"`project`; `org_id` must be omitted when this is `user` (see `org_id`).",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(circleci.NotificationScopeUser, circleci.NotificationScopeProject),
				},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the project to list configs for. " +
					"Required when `scope = \"project\"`.",
				Optional: true,
			},
			"org_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization the project belongs " +
					"to. Required when `scope = \"project\"`.\n\n" +
					"~> **Must be omitted when `scope = \"user\"`.** CircleCI's user-scoped listing takes " +
					"no organization parameter at all, so a value here would not be honoured -- it would " +
					"be silently ignored, and the result would be the calling user's channel configs " +
					"across every organization rather than just this one. That combination is rejected " +
					"at plan time instead of being accepted and doing the wrong thing.",
				Optional: true,
			},
			"channel_configs": schema.ListNestedAttribute{
				MarkdownDescription: "The matching channel configs.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the channel config.",
							Computed:            true,
						},
						"scope": schema.StringAttribute{
							MarkdownDescription: "Whether this config belongs to a user or a project.",
							Computed:            true,
						},
						"channel_type": schema.StringAttribute{
							MarkdownDescription: "The delivery channel: `email` or `slack`.",
							Computed:            true,
						},
						"target": schema.StringAttribute{
							MarkdownDescription: "The delivery address.",
							Computed:            true,
						},
						"channel_name": schema.StringAttribute{
							MarkdownDescription: "The resolved Slack channel name, when applicable.",
							Computed:            true,
						},
						"is_enabled": schema.BoolAttribute{
							MarkdownDescription: "Whether this channel is active.",
							Computed:            true,
						},
						"project_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the project, for a " +
								"project-scoped config.",
							Computed: true,
						},
						"org_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the organization.",
							Computed:            true,
						},
						"user_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the user, for a user-scoped " +
								"config.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

// ValidateConfig refuses org_id together with scope = "user".
//
// ListNotificationChannelConfigs always sends filter[org_id] when OrgID is
// set, regardless of scope, but the service method behind
// filter[scope]=user takes no organization argument at all: filter[org_id]
// is neither honoured nor rejected by that path, so a practitioner asking
// for one org's user-scoped configs would silently receive the calling
// user's configs across every organization instead, with no error. This is
// the only place that can be caught -- the API answers 200, not 4xx -- so it
// is refused here, at plan time, rather than left to surprise someone
// reading the result.
func (d *notificationChannelConfigsDataSource) ValidateConfig(
	ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse,
) {
	var config notificationChannelConfigsDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.Scope.IsNull() || config.Scope.IsUnknown() {
		return
	}

	if config.Scope.ValueString() != circleci.NotificationScopeUser {
		return
	}

	if config.OrgID.IsNull() || config.OrgID.IsUnknown() || config.OrgID.ValueString() == "" {
		return
	}

	resp.Diagnostics.AddAttributeError(
		path.Root("org_id"),
		"org_id is not honored for scope = \"user\"",
		"CircleCI's user-scoped channel config listing takes no organization: filter[org_id] is "+
			"silently ignored rather than rejected, so setting org_id here would not scope the result "+
			"to this organization -- it would return the calling user's channel configs across every "+
			"organization, with no error to say so.\n\nOmit org_id when scope = \"user\", or set "+
			"scope = \"project\" (with both project_id and org_id) if you need one organization's "+
			"configs.",
	)
}

// Configure adds the provider configured client to the data source.
func (d *notificationChannelConfigsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// Read lists the channel configs and sets the data source state.
func (d *notificationChannelConfigsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, "circleci_notification_channel_configs", &resp.Diagnostics) {
		return
	}

	var config notificationChannelConfigsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	configs, err := d.client.ListNotificationChannelConfigs(ctx, circleci.ListNotificationChannelConfigsOptions{
		Scope:     config.Scope.ValueString(),
		ProjectID: config.ProjectID.ValueString(),
		OrgID:     config.OrgID.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI notification channel configs",
			circleci.Detail(err),
		)

		return
	}

	config.Configs = make([]notificationChannelConfigItemModel, 0, len(configs))
	for _, cc := range configs {
		item := notificationChannelConfigItemModel{
			ID:          types.StringValue(cc.ID),
			Scope:       types.StringValue(cc.Scope),
			ChannelType: types.StringValue(cc.ChannelType),
			Target:      types.StringValue(cc.Target),
			IsEnabled:   types.BoolValue(cc.IsEnabled),
			OrgID:       types.StringValue(cc.OrgID),
			ChannelName: types.StringNull(),
			ProjectID:   types.StringNull(),
			UserID:      types.StringNull(),
		}
		if cc.ChannelName != "" {
			item.ChannelName = types.StringValue(cc.ChannelName)
		}
		if cc.ProjectID != "" {
			item.ProjectID = types.StringValue(cc.ProjectID)
		}
		if cc.UserID != "" {
			item.UserID = types.StringValue(cc.UserID)
		}

		config.Configs = append(config.Configs, item)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
