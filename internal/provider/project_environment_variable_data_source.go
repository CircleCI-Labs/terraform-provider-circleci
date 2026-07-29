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
	_ datasource.DataSource              = &ProjectEnvironmentVariableDataSource{}
	_ datasource.DataSourceWithConfigure = &ProjectEnvironmentVariableDataSource{}
)

// projectEnvironmentVariableDataSourceModel maps the output schema.
type projectEnvironmentVariableDataSourceModel struct {
	Name        types.String `tfsdk:"name"`
	Value       types.String `tfsdk:"value"`
	ProjectSlug types.String `tfsdk:"project_slug"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// NewProjectEnvironmentVariableDataSource is a helper function to simplify the provider implementation.
func NewProjectEnvironmentVariableDataSource() datasource.DataSource {
	return &ProjectEnvironmentVariableDataSource{}
}

// ProjectEnvironmentVariableDataSource is the data source implementation.
type ProjectEnvironmentVariableDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *ProjectEnvironmentVariableDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_environment_variable"
}

// Schema defines the schema for the data source.
func (d *ProjectEnvironmentVariableDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches information about a CircleCI project environment variable.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the environment variable.",
				Required:            true,
			},
			"project_slug": schema.StringAttribute{
				MarkdownDescription: "The project slug in the format `vcs-type/org-name/repo-name`.",
				Required:            true,
			},
			"value": schema.StringAttribute{
				MarkdownDescription: "The masked value of the environment variable as returned by the API.",
				Computed:            true,
				Sensitive:           true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the environment variable was created.",
				Computed:            true,
			},
		},
	}
}

// Read refreshes the Terraform state with the latest data.
func (d *ProjectEnvironmentVariableDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state projectEnvironmentVariableDataSourceModel
	diags := req.Config.Get(ctx, &state)
	if diags != nil {
		resp.Diagnostics.Append(diags...)
		return
	}

	envVar, err := d.client.GetProjectEnvironmentVariable(ctx, state.ProjectSlug.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Read CircleCI project environment variable "+state.Name.ValueString(),
			circleci.Detail(err),
		)
		return
	}

	// Value is always the API's masked form (e.g. "xxxx1234"), never the literal
	// configured value: no route ever discloses that. See
	// ProjectEnvironmentVariable's doc comment in internal/circleci.
	state.Name = types.StringValue(envVar.Name)
	state.Value = types.StringValue(envVar.Value)
	state.CreatedAt = types.StringValue(envVar.CreatedAt)

	// Set state
	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Configure adds the provider configured client to the data source.
func (d *ProjectEnvironmentVariableDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
