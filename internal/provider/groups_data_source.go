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
	_ datasource.DataSource              = &groupsDataSource{}
	_ datasource.DataSourceWithConfigure = &groupsDataSource{}
)

// groupsDataSourceModel maps the data source schema.
//
// The shape follows the provider's convention for plural data sources: the scope
// the collection is listed under is the input, and the collection itself is a
// single list-nested attribute named after the entity.
type groupsDataSourceModel struct {
	OrganizationId types.String     `tfsdk:"organization_id"`
	Groups         []groupItemModel `tfsdk:"groups"`
}

// groupItemModel maps one group in the list.
type groupItemModel struct {
	Id          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
}

// NewGroupsDataSource is a helper function to simplify the provider implementation.
func NewGroupsDataSource() datasource.DataSource {
	return &groupsDataSource{}
}

// groupsDataSource is the data source implementation.
type groupsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *groupsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_groups"
}

// Schema defines the schema for the data source.
func (d *groupsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every CircleCI group in an organization. " +
			"Available on CircleCI Cloud only. Groups require a `circleci` type (standalone) " +
			"organization: the API documents group creation as supported only for standalone " +
			"organizations, and a CircleCI Server installation is always a `github` type " +
			"organization.\n\n" +
			"Pagination is followed internally, so the result covers every group rather than one page.\n\n" +
			"~> **Group membership is not managed by Terraform.** Adding and removing group members is " +
			"only possible in the CircleCI web UI, so the users in a group are not exposed here.",
		Attributes: map[string]schema.Attribute{
			"organization_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization whose groups are listed.",
				Required:            true,
			},
			"groups": schema.ListNestedAttribute{
				MarkdownDescription: "The groups in the organization, in the order the API returns them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the group.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Name of the group.",
							Computed:            true,
						},
						"description": schema.StringAttribute{
							MarkdownDescription: "Description of the group.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// Read lists the organization's groups.
func (d *groupsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireStandaloneCapable(d.client, "circleci_groups", &resp.Diagnostics) {
		return
	}

	var state groupsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := state.OrganizationId.ValueString()

	groups, err := d.client.Groups().List(ctx, organizationID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI groups for organization "+organizationID,
			circleci.Detail(err),
		)

		return
	}

	// An empty, non-null list keeps `for_each` and `length()` working against
	// an organization that has no groups yet.
	state.Groups = make([]groupItemModel, 0, len(groups))
	for _, group := range groups {
		state.Groups = append(state.Groups, groupItemModel{
			Id:          types.StringValue(group.ID),
			Name:        types.StringValue(group.Name),
			Description: types.StringValue(group.Description),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *groupsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
