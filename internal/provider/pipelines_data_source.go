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
	_ datasource.DataSource              = &pipelinesDataSource{}
	_ datasource.DataSourceWithConfigure = &pipelinesDataSource{}
)

// pipelineDefinitionsTypeName is the Terraform type name, used in the Cloud-only
// diagnostic.
const pipelineDefinitionsTypeName = "circleci_pipeline_definitions"

// pipelinesDataSourceModel maps the data source schema.
type pipelinesDataSourceModel struct {
	ProjectID           types.String        `tfsdk:"project_id"`
	PipelineDefinitions []pipelineItemModel `tfsdk:"pipeline_definitions"`
}

// pipelineItemModel maps one pipeline definition in the list. The attribute names
// match `circleci_pipeline_definition`, including the flattened config and checkout
// sources, so a definition read here and one read there describe themselves the same
// way.
type pipelineItemModel struct {
	ID                           types.String `tfsdk:"id"`
	Name                         types.String `tfsdk:"name"`
	Description                  types.String `tfsdk:"description"`
	CreatedAt                    types.String `tfsdk:"created_at"`
	ConfigSourceProvider         types.String `tfsdk:"config_source_provider"`
	ConfigSourceFilePath         types.String `tfsdk:"config_source_file_path"`
	ConfigSourceRepoFullName     types.String `tfsdk:"config_source_repo_full_name"`
	ConfigSourceRepoExternalID   types.String `tfsdk:"config_source_repo_external_id"`
	CheckoutSourceProvider       types.String `tfsdk:"checkout_source_provider"`
	CheckoutSourceRepoFullName   types.String `tfsdk:"checkout_source_repo_full_name"`
	CheckoutSourceRepoExternalID types.String `tfsdk:"checkout_source_repo_external_id"`
}

// NewPipelineDefinitionsDataSource is a helper function to simplify the provider
// implementation.
func NewPipelineDefinitionsDataSource() datasource.DataSource {
	return &pipelinesDataSource{}
}

// pipelinesDataSource is the data source implementation.
type pipelinesDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *pipelinesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pipeline_definitions"
}

// Schema defines the schema for the data source.
func (d *pipelinesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every pipeline definition on a CircleCI project, including definitions " +
			"created outside Terraform.\n\n" +
			"!> **CircleCI Cloud only.** Pipeline definitions live under `/api/v2` but are served by the " +
			"public API service, which CircleCI Server does not route. A Server installation answers HTTP " +
			"404 — indistinguishable from a project that does not exist — so this data source rejects " +
			"`deployment = \"server\"` outright.\n\n" +
			"The endpoint returns every definition in one response, so there is no pagination to follow.",
		Attributes: map[string]schema.Attribute{
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the project whose pipeline definitions are listed.",
				Required:            true,
			},
			"pipeline_definitions": schema.ListNestedAttribute{
				MarkdownDescription: "The pipeline definitions on the project, in the order the API " +
					"returns them.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the pipeline definition.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Name of the pipeline definition.",
							Computed:            true,
						},
						"description": schema.StringAttribute{
							MarkdownDescription: "Description of the pipeline definition. Empty when none is set.",
							Computed:            true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "Timestamp the definition was created, as the API reported it. " +
								"Empty for definitions created before CircleCI recorded it.",
							Computed: true,
						},
						"config_source_provider": schema.StringAttribute{
							MarkdownDescription: "Provider serving the pipeline configuration, such as `github_app`.",
							Computed:            true,
						},
						"config_source_file_path": schema.StringAttribute{
							MarkdownDescription: "Path to the configuration file within the configuration repository.",
							Computed:            true,
						},
						"config_source_repo_full_name": schema.StringAttribute{
							MarkdownDescription: "Full name (`owner/repo`) of the repository holding the configuration.",
							Computed:            true,
						},
						"config_source_repo_external_id": schema.StringAttribute{
							MarkdownDescription: "Provider-side identifier of the repository holding the configuration.",
							Computed:            true,
						},
						"checkout_source_provider": schema.StringAttribute{
							MarkdownDescription: "Provider serving the checked-out code.",
							Computed:            true,
						},
						"checkout_source_repo_full_name": schema.StringAttribute{
							MarkdownDescription: "Full name (`owner/repo`) of the repository that is checked out.",
							Computed:            true,
						},
						"checkout_source_repo_external_id": schema.StringAttribute{
							MarkdownDescription: "Provider-side identifier of the repository that is checked out.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// Read lists the project's pipeline definitions.
func (d *pipelinesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	// Gate before the request: on CircleCI Server the route is not present at all
	// and the HTTP 404 would read as "no such project".
	if !requireCloud(d.client, pipelineDefinitionsTypeName, &resp.Diagnostics) {
		return
	}

	var state pipelinesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := state.ProjectID.ValueString()

	definitions, err := d.client.ListPipelineDefinitions(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI pipeline definitions for project "+projectID,
			circleci.Detail(err),
		)

		return
	}

	// An empty, non-null list keeps `for_each` and `length()` working against a
	// project that has no pipeline definitions yet.
	items := make([]pipelineItemModel, 0, len(definitions))
	for _, definition := range definitions {
		items = append(items, pipelineItemModel{
			ID:                           types.StringValue(definition.ID),
			Name:                         types.StringValue(definition.Name),
			Description:                  types.StringValue(definition.Description),
			CreatedAt:                    types.StringValue(definition.CreatedAt),
			ConfigSourceProvider:         types.StringValue(definition.ConfigSource.Provider),
			ConfigSourceFilePath:         types.StringValue(definition.ConfigSource.FilePath),
			ConfigSourceRepoFullName:     types.StringValue(definition.ConfigSource.Repo.FullName),
			ConfigSourceRepoExternalID:   types.StringValue(definition.ConfigSource.Repo.ExternalID),
			CheckoutSourceProvider:       types.StringValue(definition.CheckoutSource.Provider),
			CheckoutSourceRepoFullName:   types.StringValue(definition.CheckoutSource.Repo.FullName),
			CheckoutSourceRepoExternalID: types.StringValue(definition.CheckoutSource.Repo.ExternalID),
		})
	}

	state.PipelineDefinitions = items

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *pipelinesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
