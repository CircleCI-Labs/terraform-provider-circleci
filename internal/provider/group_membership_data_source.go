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
	_ datasource.DataSource                     = &groupMembershipDataSource{}
	_ datasource.DataSourceWithConfigure        = &groupMembershipDataSource{}
	_ datasource.DataSourceWithConfigValidators = &groupMembershipDataSource{}
)

// groupMembershipDataSourceModel maps the data source schema.
//
// Members are exposed twice on purpose: user_ids is the shape the
// circleci_group_membership resource consumes, and members carries the display
// fields the API returns alongside each id.
type groupMembershipDataSourceModel struct {
	OrganizationId types.String           `tfsdk:"organization_id"`
	OrgId          types.String           `tfsdk:"org_id"`
	GroupId        types.String           `tfsdk:"group_id"`
	UserIds        types.Set              `tfsdk:"user_ids"`
	Members        []groupMemberItemModel `tfsdk:"members"`
}

// groupMemberItemModel maps one member in the list.
type groupMemberItemModel struct {
	UserId    types.String `tfsdk:"user_id"`
	Username  types.String `tfsdk:"username"`
	Email     types.String `tfsdk:"email"`
	AvatarUrl types.String `tfsdk:"avatar_url"`
}

// NewGroupMembershipDataSource is a helper function to simplify the provider implementation.
func NewGroupMembershipDataSource() datasource.DataSource {
	return &groupMembershipDataSource{}
}

// groupMembershipDataSource is the data source implementation.
type groupMembershipDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *groupMembershipDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_membership"
}

// Schema defines the schema for the data source.
func (d *groupMembershipDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches the users that are members of a CircleCI group. " +
			"Available on CircleCI Cloud only. Groups require a `circleci` type (standalone) " +
			"organization: the API documents group creation as supported only for standalone " +
			"organizations, and a CircleCI Server installation is always a `github` type " +
			"organization.\n\n" +
			"~> **Only the first page of members is returned.** The endpoint reports a next page token " +
			"but ignores one on the way in, so later pages cannot be requested.\n\n" +
			"~> **These endpoints are not part of the published CircleCI OpenAPI specification** and may " +
			"change without notice.",
		Attributes: map[string]schema.Attribute{
			// See org_id_deprecation.go for why the organization is accepted under
			// two names.
			"organization_id": deprecatedOrgIDDataSourceAttribute("group memberships"),
			"org_id":          orgIDDataSourceAttribute("group memberships"),
			"group_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the group whose members are listed.",
				Required:            true,
			},
			"user_ids": schema.SetAttribute{
				MarkdownDescription: "Unique identifiers (UUIDs) of the group's members, for feeding " +
					"straight into a `circleci_group_membership` resource.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"members": schema.ListNestedAttribute{
				MarkdownDescription: "The group's members, in the order the API returns them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"user_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the user.",
							Computed:            true,
						},
						"username": schema.StringAttribute{
							MarkdownDescription: "Username of the user.",
							Computed:            true,
						},
						"email": schema.StringAttribute{
							MarkdownDescription: "Primary email address of the user. " +
								"The API returns an empty string when it has none to report.",
							Computed: true,
						},
						"avatar_url": schema.StringAttribute{
							MarkdownDescription: "URL of the user's avatar image.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (d *groupMembershipDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		orgIDDataSourceConfigValidator(),
	}
}

// Read lists the group's members.
func (d *groupMembershipDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireStandaloneCapable(d.client, "circleci_group_membership", &resp.Diagnostics) {
		return
	}

	var state groupMembershipDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(state.OrganizationId, state.OrgId)
	groupID := state.GroupId.ValueString()

	members, err := d.client.GroupMembership().List(ctx, organizationID, groupID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list the members of CircleCI group "+groupID,
			circleci.Detail(err),
		)

		return
	}

	userIDs, diags := types.SetValueFrom(ctx, types.StringType, memberIDs(members))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.UserIds = userIDs

	// An empty, non-null list keeps `for_each` and `length()` working against a
	// group that has no members yet.
	state.Members = make([]groupMemberItemModel, 0, len(members))
	for _, member := range members {
		state.Members = append(state.Members, groupMemberItemModel{
			UserId:    types.StringValue(member.UserID),
			Username:  types.StringValue(member.Username),
			Email:     types.StringValue(member.Email),
			AvatarUrl: types.StringValue(member.AvatarURL),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *groupMembershipDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
