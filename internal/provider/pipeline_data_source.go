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
	_ datasource.DataSource              = &PipelineDataSource{}
	_ datasource.DataSourceWithConfigure = &PipelineDataSource{}
)

// pipelineDataSourceModel maps the output schema.
type pipelineDataSourceModel struct {
	Id                           types.String `tfsdk:"id"`
	ProjectId                    types.String `tfsdk:"project_id"`
	Name                         types.String `tfsdk:"name"`
	Description                  types.String `tfsdk:"description"`
	CreatedAt                    types.String `tfsdk:"created_at"`
	ConfigSourceProvider         types.String `tfsdk:"config_source_provider"`
	ConfigSourceFilePath         types.String `tfsdk:"config_source_file_path"`
	ConfigSourceRepoFullName     types.String `tfsdk:"config_source_repo_full_name"`
	ConfigSourceRepoExternalId   types.String `tfsdk:"config_source_repo_external_id"`
	CheckoutSourceProvider       types.String `tfsdk:"checkout_source_provider"`
	CheckoutSourceRepoFullName   types.String `tfsdk:"checkout_source_repo_full_name"`
	CheckoutSourceRepoExternalId types.String `tfsdk:"checkout_source_repo_external_id"`
}

// NewPipelineDefinitionDataSource is a helper function to simplify the provider
// implementation.
func NewPipelineDefinitionDataSource() datasource.DataSource {
	return &PipelineDataSource{}
}

// NewDeprecatedPipelineDataSource registers the same implementation under the old
// type name, circleci_pipeline. See pipeline_rename.go.
func NewDeprecatedPipelineDataSource() datasource.DataSource {
	return &PipelineDataSource{deprecated: true}
}

// PipelineDataSource is the data source implementation.
type PipelineDataSource struct {
	client *circleci.Client

	// deprecated marks the copy registered under the old type name.
	deprecated bool
}

// Metadata returns the data source type name.
func (d *PipelineDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName +
		renamedTypeName(d.deprecated, "_pipeline", "_pipeline_definition")
}

// Schema defines the schema for the data source.
func (d *PipelineDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches information about a CircleCI pipeline definition.\n\n" +
			"!> **CircleCI Cloud only.** Pipeline definitions live under `/api/v2` but are served by the " +
			"public API service, which CircleCI Server does not route.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The ID of the pipeline.",
				Required:            true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the project the pipeline belongs to.",
				Required:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the pipeline.",
				Computed:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "The description of the pipeline.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the pipeline was created.",
				Computed:            true,
			},
			"config_source_provider": schema.StringAttribute{
				MarkdownDescription: "The provider for the pipeline configuration source.",
				Computed:            true,
			},
			"config_source_file_path": schema.StringAttribute{
				MarkdownDescription: "The path to the pipeline configuration file within the repository.",
				Computed:            true,
			},
			"config_source_repo_full_name": schema.StringAttribute{
				MarkdownDescription: "The full name of the repository containing the pipeline configuration.",
				Computed:            true,
			},
			"config_source_repo_external_id": schema.StringAttribute{
				MarkdownDescription: "The external ID of the repository containing the pipeline configuration.",
				Computed:            true,
			},
			"checkout_source_provider": schema.StringAttribute{
				MarkdownDescription: "The provider for the code checkout source.",
				Computed:            true,
			},
			"checkout_source_repo_full_name": schema.StringAttribute{
				MarkdownDescription: "The full name of the repository used for code checkout.",
				Computed:            true,
			},
			"checkout_source_repo_external_id": schema.StringAttribute{
				MarkdownDescription: "The external ID of the repository used for code checkout.",
				Computed:            true,
			},
		},
	}

	if d.deprecated {
		deprecateRenamedDataSource(&resp.Schema, pipelineTypeName, pipelineDefinitionTypeName,
			"reads a pipeline definition, not a pipeline run")
	}
}

// Read refreshes the Terraform state with the latest data.
func (d *PipelineDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	// Gate before the request: on CircleCI Server the route is not present at
	// all and the HTTP 404 would read as "no such pipeline".
	typeName := renamedTypeName(d.deprecated, pipelineTypeName, pipelineDefinitionTypeName)
	if !requireCloud(d.client, typeName, &resp.Diagnostics) {
		return
	}

	var state pipelineDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	definition, err := d.client.GetPipelineDefinition(ctx, state.ProjectId.ValueString(), state.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Read CircleCI pipeline with id "+state.Id.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, pipelineResourceModelToDataSource(state.ProjectId, *definition))...)
}

// pipelineResourceModelToDataSource maps an API pipeline definition onto the
// data source model. It is a distinct function from
// pipelineResourceModelFromAPI (which returns pipelineResourceModel) because
// the resource and data source use separate model types, even though their
// fields are identical.
func pipelineResourceModelToDataSource(projectID types.String, definition circleci.PipelineDefinition) pipelineDataSourceModel {
	return pipelineDataSourceModel{
		Id:                           types.StringValue(definition.ID),
		ProjectId:                    projectID,
		Name:                         types.StringValue(definition.Name),
		Description:                  types.StringValue(definition.Description),
		CreatedAt:                    types.StringValue(definition.CreatedAt),
		ConfigSourceProvider:         types.StringValue(definition.ConfigSource.Provider),
		ConfigSourceFilePath:         types.StringValue(definition.ConfigSource.FilePath),
		ConfigSourceRepoFullName:     types.StringValue(definition.ConfigSource.Repo.FullName),
		ConfigSourceRepoExternalId:   types.StringValue(definition.ConfigSource.Repo.ExternalID),
		CheckoutSourceProvider:       types.StringValue(definition.CheckoutSource.Provider),
		CheckoutSourceRepoFullName:   types.StringValue(definition.CheckoutSource.Repo.FullName),
		CheckoutSourceRepoExternalId: types.StringValue(definition.CheckoutSource.Repo.ExternalID),
	}
}

// Configure adds the provider configured client to the data source.
func (d *PipelineDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
