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
	_ datasource.DataSource              = &deploySettingsDataSource{}
	_ datasource.DataSourceWithConfigure = &deploySettingsDataSource{}
)

// deploySettingsTypeName is the data source's type name, used in diagnostics.
const deploySettingsTypeName = "circleci_deploy_settings"

// deploySettingsDataSourceModel maps the data source schema.
type deploySettingsDataSourceModel struct {
	ProjectId                    types.String `tfsdk:"project_id"`
	RollbackPipelineDefinitionId types.String `tfsdk:"rollback_pipeline_definition_id"`
	DeployPipelineDefinitionId   types.String `tfsdk:"deploy_pipeline_definition_id"`
}

// NewDeploySettingsDataSource is a helper function to simplify the provider
// implementation.
func NewDeploySettingsDataSource() datasource.DataSource {
	return &deploySettingsDataSource{}
}

// deploySettingsDataSource fetches a project's deploy/release settings.
type deploySettingsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *deploySettingsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deploy_settings"
}

// Schema defines the schema for the data source.
func (d *deploySettingsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches a CircleCI project's deploy/release settings: the pipeline " +
			"definitions used for automatic deploys and rollbacks.\n\n" +
			"-> **This route also accepts writes** (`PATCH .../settings`), but this " +
			"provider exposes it read-only: both settings are pipeline-definition ids, and pipeline " +
			"definitions are a separate, not-yet-built area of this provider's scope. A future resource " +
			"can add the write side without changing this data source.\n\n" +
			"~> **CircleCI Cloud only.** Deploy/release tracking is not part of CircleCI Server.",
		Attributes: map[string]schema.Attribute{
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the CircleCI project.",
				Required:            true,
			},
			"rollback_pipeline_definition_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the pipeline definition used to roll " +
					"this project back. Null when rollback is not configured.",
				Computed: true,
			},
			"deploy_pipeline_definition_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the pipeline definition used to deploy " +
					"this project. Null when deploy is not configured.",
				Computed: true,
			},
		},
	}
}

// Read fetches the project's deploy settings and sets the data source state.
func (d *deploySettingsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, deploySettingsTypeName, &resp.Diagnostics) {
		return
	}

	var config deploySettingsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := config.ProjectId.ValueString()

	settings, err := d.client.DeployProjectSettings().Get(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI deploy settings for project "+projectID,
			circleci.Detail(err),
		)

		return
	}

	state := deploySettingsDataSourceModel{
		ProjectId:                    config.ProjectId,
		RollbackPipelineDefinitionId: types.StringPointerValue(settings.RollbackPipelineDefinitionID),
		DeployPipelineDefinitionId:   types.StringPointerValue(settings.DeployPipelineDefinitionID),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *deploySettingsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
