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
	_ datasource.DataSource              = &projectEnvironmentVariablesDataSource{}
	_ datasource.DataSourceWithConfigure = &projectEnvironmentVariablesDataSource{}
)

// projectEnvironmentVariablesDataSourceModel maps the data source schema.
type projectEnvironmentVariablesDataSourceModel struct {
	ProjectSlug          types.String                          `tfsdk:"project_slug"`
	EnvironmentVariables []projectEnvironmentVariableItemModel `tfsdk:"environment_variables"`
}

// projectEnvironmentVariableItemModel maps one environment variable in the list.
// The attribute names match `circleci_project_environment_variable`, so a
// variable read here and one read there describe themselves the same way.
type projectEnvironmentVariableItemModel struct {
	Name      types.String `tfsdk:"name"`
	Value     types.String `tfsdk:"value"`
	CreatedAt types.String `tfsdk:"created_at"`
}

// NewProjectEnvironmentVariablesDataSource is a helper function to simplify the provider implementation.
func NewProjectEnvironmentVariablesDataSource() datasource.DataSource {
	return &projectEnvironmentVariablesDataSource{}
}

// projectEnvironmentVariablesDataSource is the data source implementation.
type projectEnvironmentVariablesDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *projectEnvironmentVariablesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_environment_variables"
}

// Schema defines the schema for the data source.
func (d *projectEnvironmentVariablesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every environment variable set on a CircleCI project, including " +
			"variables created outside Terraform. Available on CircleCI Cloud and CircleCI Server.\n\n" +
			"Pagination is followed internally, so the result covers every variable rather than one page.\n\n" +
			"~> **Values are masked by the API.** CircleCI never discloses an environment variable's value: " +
			"it returns four `x` characters followed by the last four characters of the real value, matching " +
			"what the web UI displays. Use this data source to discover *which* variables are set, not what " +
			"they are set to — a masked value cannot be fed into another resource.",
		Attributes: map[string]schema.Attribute{
			"project_slug": schema.StringAttribute{
				MarkdownDescription: "Slug of the project whose environment variables are listed, in the " +
					"form `vcs-slug/org-name/repo-name`.",
				Required: true,
			},
			"environment_variables": schema.ListNestedAttribute{
				MarkdownDescription: "The environment variables on the project, in the order the API returns " +
					"them (sorted by name).",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							MarkdownDescription: "Name of the environment variable.",
							Computed:            true,
						},
						"value": schema.StringAttribute{
							MarkdownDescription: "Masked value of the environment variable, as returned by " +
								"the API. This is never the real value.",
							Computed:  true,
							Sensitive: true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "Timestamp the variable was created, as the API reported it. " +
								"Empty for variables created before CircleCI recorded it.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

// Read lists the project's environment variables.
func (d *projectEnvironmentVariablesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state projectEnvironmentVariablesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectSlug := state.ProjectSlug.ValueString()

	variables, err := d.client.ListProjectEnvironmentVariables(ctx, projectSlug)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI environment variables for project "+projectSlug,
			circleci.Detail(err),
		)

		return
	}

	// An empty, non-null list keeps `for_each` and `length()` working against a
	// project that has no environment variables yet.
	state.EnvironmentVariables = make([]projectEnvironmentVariableItemModel, 0, len(variables))
	for _, variable := range variables {
		state.EnvironmentVariables = append(state.EnvironmentVariables, projectEnvironmentVariableItemModel{
			Name:      types.StringValue(variable.Name),
			Value:     types.StringValue(variable.Value),
			CreatedAt: types.StringValue(variable.CreatedAt),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *projectEnvironmentVariablesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
