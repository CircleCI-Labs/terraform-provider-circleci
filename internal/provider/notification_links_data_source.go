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
	_ datasource.DataSource              = &notificationLinksDataSource{}
	_ datasource.DataSourceWithConfigure = &notificationLinksDataSource{}
)

// notificationLinksDataSourceModel maps the data source schema.
type notificationLinksDataSourceModel struct {
	UserId         types.String            `tfsdk:"user_id"`
	ConnectionType types.String            `tfsdk:"connection_type"`
	TeamId         types.String            `tfsdk:"team_id"`
	Links          []notificationLinkModel `tfsdk:"links"`
}

// notificationLinkModel maps one external identity link.
type notificationLinkModel struct {
	ConnectionType  types.String `tfsdk:"connection_type"`
	ExternalId      types.String `tfsdk:"external_id"`
	ExternalScopeId types.String `tfsdk:"external_scope_id"`
	DisplayName     types.String `tfsdk:"display_name"`
	UserId          types.String `tfsdk:"user_id"`
}

// NewNotificationLinksDataSource is a helper function to simplify the provider
// implementation.
func NewNotificationLinksDataSource() datasource.DataSource {
	return &notificationLinksDataSource{}
}

// notificationLinksDataSource lists the calling user's external identity links.
type notificationLinksDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *notificationLinksDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_links"
}

// Schema defines the schema for the data source.
func (d *notificationLinksDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the external identity links belonging to the **calling token's own " +
			"user** — today, the Slack accounts that user has connected so CircleCI can send them " +
			"personal notifications.\n\n" +
			"~> **This is a data source and there is deliberately no matching resource.** The API has no " +
			"create route: a link is established by the Slack user-linking OAuth flow, a browser consent " +
			"step Terraform cannot drive. A resource whose Create could never run would be worse than no " +
			"resource at all. Its DELETE also takes no ID — it unlinks by criteria — so even destroy-only " +
			"management would not map onto a Terraform resource cleanly.\n\n" +
			"~> **Experimental upstream.** The API's own documentation states that field " +
			"names, request and response shapes, and pagination semantics are not yet stable, and asks " +
			"clients not to depend on the endpoint in production. Treat this data source the same way.\n\n" +
			"-> **You cannot read another user's links.** `user_id` accepts only `me` or the calling " +
			"user's own UUID; anything else is refused with HTTP 403. This is a privilege boundary in the " +
			"API, not a provider limitation.\n\n" +
			"Requires CircleCI Cloud: the v3 API is not routed by a CircleCI Server installation.",
		Attributes: map[string]schema.Attribute{
			"user_id": schema.StringAttribute{
				MarkdownDescription: "Whose links to list. Only `me` (the default) or the calling user's " +
					"own UUID are accepted — see the note above.",
				Optional: true,
			},
			"connection_type": schema.StringAttribute{
				MarkdownDescription: "Restrict to one connection type. Today `slack` is the only one. " +
					"Omit to list every type.",
				Optional: true,
			},
			"team_id": schema.StringAttribute{
				MarkdownDescription: "Restrict to one external scope, for example a single Slack " +
					"workspace/team ID such as `T0123`. Omit to list every scope.",
				Optional: true,
			},
			"links": schema.ListNestedAttribute{
				MarkdownDescription: "The matching links. Empty when the user has connected no external " +
					"accounts.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"connection_type": schema.StringAttribute{
							MarkdownDescription: "The third-party connection the link is on, e.g. `slack`.",
							Computed:            true,
						},
						"external_id": schema.StringAttribute{
							MarkdownDescription: "The identity's ID on the external connection, for example " +
								"a Slack user ID such as `U0123`.",
							Computed: true,
						},
						"external_scope_id": schema.StringAttribute{
							MarkdownDescription: "The connection-specific scope the link lives in, for " +
								"example a Slack workspace/team ID such as `T0123`. Together with " +
								"`connection_type` and the owning user this is the link's identity — " +
								"**a link has no ID of its own**, which is why there is no singular data " +
								"source.",
							Computed: true,
						},
						"display_name": schema.StringAttribute{
							MarkdownDescription: "The external identity's display name, captured when the " +
								"link was created. It is not refreshed if the name later changes on the " +
								"external service.",
							Computed: true,
						},
						"user_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the CircleCI user the link " +
								"belongs to.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

// Read lists the links.
func (d *notificationLinksDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state notificationLinksDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !requireCloud(d.client, "circleci_notification_links", &resp.Diagnostics) {
		return
	}

	links, err := d.client.ListNotificationLinks(ctx, circleci.ListNotificationLinksOptions{
		UserID:         state.UserId.ValueString(),
		ConnectionType: state.ConnectionType.ValueString(),
		TeamID:         state.TeamId.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI notification links",
			circleci.Detail(err),
		)

		return
	}

	state.Links = make([]notificationLinkModel, 0, len(links))
	for _, link := range links {
		state.Links = append(state.Links, notificationLinkModel{
			ConnectionType:  types.StringValue(link.ConnectionType),
			ExternalId:      types.StringValue(link.ExternalID),
			ExternalScopeId: types.StringValue(link.ExternalScopeID),
			DisplayName:     optionalString(link.DisplayName),
			UserId:          types.StringValue(link.UserID),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *notificationLinksDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
