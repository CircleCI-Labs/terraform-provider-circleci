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
	_ datasource.DataSource              = &projectGroupsDataSource{}
	_ datasource.DataSourceWithConfigure = &projectGroupsDataSource{}
)

// projectGroupsDataSourceModel maps the data source schema.
type projectGroupsDataSourceModel struct {
	OrganizationId types.String            `tfsdk:"organization_id"`
	ProjectId      types.String            `tfsdk:"project_id"`
	Groups         []projectGroupItemModel `tfsdk:"groups"`
}

// projectGroupItemModel maps one project group grant in the list.
type projectGroupItemModel struct {
	Id   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
	Role types.String `tfsdk:"role"`
}

// NewProjectGroupsDataSource is a helper function to simplify the provider implementation.
func NewProjectGroupsDataSource() datasource.DataSource {
	return &projectGroupsDataSource{}
}

// projectGroupsDataSource is the data source implementation.
type projectGroupsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *projectGroupsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_groups"
}

// Schema defines the schema for the data source.
func (d *projectGroupsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches the groups granted a role on a CircleCI project. " +
			"Requires CircleCI Cloud.\n\n" +
			"Pagination is followed internally, so the result covers every group rather than one page.\n\n" +
			"~> **These endpoints are not part of the published CircleCI OpenAPI specification** and may " +
			"change without notice.",
		Attributes: map[string]schema.Attribute{
			"organization_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization the project belongs to.",
				Required:            true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the project whose groups are listed.",
				Required:            true,
			},
			"groups": schema.ListNestedAttribute{
				MarkdownDescription: "The groups granted a role on the project, in the order the API " +
					"returns them.",
				Computed: true,
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
						"role": schema.StringAttribute{
							MarkdownDescription: "Role the group holds on the project: one of " +
								"`project-admin`, `project-contributor` or `project-viewer`.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

// Read lists the groups granted a role on the project.
func (d *projectGroupsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state projectGroupsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID, projectID := state.OrganizationId.ValueString(), state.ProjectId.ValueString()

	groups, err := d.client.ProjectGroups().List(ctx, organizationID, projectID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list the groups on CircleCI project "+projectID,
			circleci.Detail(err),
		)

		return
	}

	// An empty, non-null list keeps `for_each` and `length()` working against a
	// project that has no groups assigned.
	state.Groups = make([]projectGroupItemModel, 0, len(groups))
	for _, group := range groups {
		state.Groups = append(state.Groups, projectGroupItemModel{
			Id:   types.StringValue(group.ID),
			Name: types.StringValue(group.Name),
			Role: types.StringValue(group.Role),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *projectGroupsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
