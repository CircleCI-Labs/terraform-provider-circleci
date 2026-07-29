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
	_ datasource.DataSource              = &pipelineWorkflowsDataSource{}
	_ datasource.DataSourceWithConfigure = &pipelineWorkflowsDataSource{}
)

// pipelineWorkflowsDataSourceModel maps the data source schema.
type pipelineWorkflowsDataSourceModel struct {
	RunId     types.String            `tfsdk:"run_id"`
	Workflows []pipelineWorkflowModel `tfsdk:"workflows"`
}

// pipelineWorkflowModel maps one workflow in a pipeline run.
type pipelineWorkflowModel struct {
	Id             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	Status         types.String `tfsdk:"status"`
	CreatedAt      types.String `tfsdk:"created_at"`
	StoppedAt      types.String `tfsdk:"stopped_at"`
	PipelineNumber types.Int64  `tfsdk:"pipeline_number"`
	ProjectSlug    types.String `tfsdk:"project_slug"`
	StartedBy      types.String `tfsdk:"started_by"`
	CanceledBy     types.String `tfsdk:"canceled_by"`
	ErroredBy      types.String `tfsdk:"errored_by"`
	Tag            types.String `tfsdk:"tag"`
}

// NewPipelineRunWorkflowsDataSource is a helper function to simplify the provider
// implementation.
func NewPipelineRunWorkflowsDataSource() datasource.DataSource {
	return &pipelineWorkflowsDataSource{}
}

// pipelineWorkflowsDataSource lists the workflows in a CircleCI pipeline run.
type pipelineWorkflowsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *pipelineWorkflowsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pipeline_run_workflows"
}

// Schema defines the schema for the data source.
func (d *pipelineWorkflowsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every workflow in a CircleCI pipeline run. Pagination is followed " +
			"internally, so the result covers every workflow rather than one page.\n\n" +
			"This completes the chain from a pipeline run down to individual jobs. Previously " +
			"[`circleci_workflow`](workflow) and [`circleci_workflow_jobs`](workflow_jobs) both required a " +
			"workflow ID that nothing in the provider could produce, so the only way in was to already " +
			"know it. Now: [`circleci_pipeline_run`](pipeline_run) → `circleci_pipeline_run_workflows` → " +
			"`circleci_workflow_jobs`.\n\n" +
			"~> **This is a point-in-time read of mutable runtime state.** " +
			"`status` changes as each workflow runs, and the set of workflows can grow while the pipeline " +
			"is in flight. Use it for inspection and in `check` blocks; using it to derive a resource " +
			"attribute will cause a perpetual diff.\n\n" +
			"Available on both CircleCI Cloud and CircleCI Server: this route is implemented directly in " +
			"the same API application on both.",
		Attributes: map[string]schema.Attribute{
			"run_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the pipeline run whose workflows to " +
					"read, as returned by [`circleci_pipeline_run`](pipeline_run).\n\n" +
					"This is a pipeline **run**, not a " +
					"[`circleci_pipeline_definition`](pipeline_definition).",
				Required: true,
			},
			"workflows": schema.ListNestedAttribute{
				MarkdownDescription: "The workflows in the pipeline run, in the order the API returns them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the workflow. Pass this to " +
								"[`circleci_workflow_jobs`](workflow_jobs) to read its job graph.",
							Computed: true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Name of the workflow, as declared in configuration.",
							Computed:            true,
						},
						"status": schema.StringAttribute{
							MarkdownDescription: "Current status of the workflow.",
							Computed:            true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "When the workflow was created, as an RFC 3339 timestamp.",
							Computed:            true,
						},
						"stopped_at": schema.StringAttribute{
							MarkdownDescription: "When the workflow stopped, as an RFC 3339 timestamp. Null " +
								"while it is still running.",
							Computed: true,
						},
						"pipeline_number": schema.Int64Attribute{
							MarkdownDescription: "Number of the pipeline this workflow belongs to.",
							Computed:            true,
						},
						"project_slug": schema.StringAttribute{
							MarkdownDescription: "Slug of the project this workflow belongs to.",
							Computed:            true,
						},
						"started_by": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the user who started the workflow.",
							Computed:            true,
						},
						"canceled_by": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the user who canceled the " +
								"workflow. Null unless it was canceled.",
							Computed: true,
						},
						"errored_by": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the user whose action errored " +
								"the workflow. Null unless it errored.",
							Computed: true,
						},
						"tag": schema.StringAttribute{
							MarkdownDescription: "Tag of the workflow, for example `setup` for a setup " +
								"workflow in a dynamic-config pipeline. Null for an ordinary workflow.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

// Read lists the pipeline run's workflows.
func (d *pipelineWorkflowsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state pipelineWorkflowsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	runID := state.RunId.ValueString()

	workflows, err := d.client.Workflows().ListByPipeline(ctx, runID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list workflows for CircleCI pipeline "+runID,
			circleci.Detail(err),
		)

		return
	}

	state.Workflows = make([]pipelineWorkflowModel, 0, len(workflows))
	for _, workflow := range workflows {
		model := pipelineWorkflowModel{
			Id:             types.StringValue(workflow.ID),
			Name:           types.StringValue(workflow.Name),
			Status:         types.StringValue(workflow.Status),
			CreatedAt:      optionalString(workflow.CreatedAt),
			StoppedAt:      optionalString(workflow.StoppedAt),
			PipelineNumber: types.Int64Value(int64(workflow.PipelineNumber)),
			ProjectSlug:    types.StringValue(workflow.ProjectSlug),
			StartedBy:      optionalString(workflow.StartedBy),
			CanceledBy:     optionalString(workflow.CanceledBy),
			ErroredBy:      optionalString(workflow.ErroredBy),
			Tag:            optionalString(workflow.Tag),
		}

		state.Workflows = append(state.Workflows, model)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *pipelineWorkflowsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
