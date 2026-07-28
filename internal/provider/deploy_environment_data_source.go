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
	_ datasource.DataSource              = &deployEnvironmentDataSource{}
	_ datasource.DataSourceWithConfigure = &deployEnvironmentDataSource{}
)

// deployEnvironmentTypeName is the data source's type name, used in diagnostics.
const deployEnvironmentTypeName = "circleci_deploy_environment"

// NewDeployEnvironmentDataSource is a helper function to simplify the
// provider implementation.
func NewDeployEnvironmentDataSource() datasource.DataSource {
	return &deployEnvironmentDataSource{}
}

// deployEnvironmentDataSource fetches one CircleCI deploy/release environment.
type deployEnvironmentDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *deployEnvironmentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deploy_environment"
}

// Schema defines the schema for the data source.
func (d *deployEnvironmentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches one CircleCI deploy/release environment by id. " +
			"Use [`circleci_deploy_environments`](deploy_environments) to list every environment in an " +
			"organization, e.g. to look one up by name.\n\n" +
			"~> **CircleCI Cloud only.** Deploy/release tracking is not part of CircleCI Server.",
		Attributes: deployEnvironmentAttributes(schema.StringAttribute{
			MarkdownDescription: "Unique identifier (UUID) of the environment.",
			Required:            true,
		}),
	}
}

// Read fetches the environment and sets the data source state.
func (d *deployEnvironmentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, deployEnvironmentTypeName, &resp.Diagnostics) {
		return
	}

	var config deployEnvironmentModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := config.Id.ValueString()

	env, err := d.client.DeployEnvironments().Get(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI deploy environment "+id,
			circleci.Detail(err),
		)

		return
	}

	state, diags := deployEnvironmentToModel(ctx, *env)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *deployEnvironmentDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
