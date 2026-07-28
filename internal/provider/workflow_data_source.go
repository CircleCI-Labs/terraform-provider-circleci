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
	_ datasource.DataSource              = &workflowDataSource{}
	_ datasource.DataSourceWithConfigure = &workflowDataSource{}
)

// workflowDataSourceModel maps the data source schema.
type workflowDataSourceModel struct {
	Id              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Status          types.String `tfsdk:"status"`
	CreatedAt       types.String `tfsdk:"created_at"`
	StoppedAt       types.String `tfsdk:"stopped_at"`
	PipelineRunId   types.String `tfsdk:"pipeline_run_id"`
	PipelineNumber  types.Int64  `tfsdk:"pipeline_number"`
	ProjectSlug     types.String `tfsdk:"project_slug"`
	StartedBy       types.String `tfsdk:"started_by"`
	CanceledBy      types.String `tfsdk:"canceled_by"`
	ErroredBy       types.String `tfsdk:"errored_by"`
	Tag             types.String `tfsdk:"tag"`
	MaxAutoReruns   types.Int64  `tfsdk:"max_auto_reruns"`
	AutoRerunNumber types.Int64  `tfsdk:"auto_rerun_number"`
}

// NewWorkflowDataSource is a helper function to simplify the provider
// implementation.
func NewWorkflowDataSource() datasource.DataSource {
	return &workflowDataSource{}
}

// workflowDataSource fetches one CircleCI workflow.
type workflowDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *workflowDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workflow"
}

// Schema defines the schema for the data source.
func (d *workflowDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches one CircleCI workflow by id.\n\n" +
			"~> **`circleci_workflow` is a point-in-time read of mutable runtime state.** `status` and " +
			"`stopped_at` change as the workflow runs. Use this data source for inspection and in `check` " +
			"blocks; using it to derive a resource attribute will cause a perpetual diff.\n\n" +
			"Available on both CircleCI Cloud and CircleCI Server: this route is implemented directly in " +
			"the same API application on both.\n\n" +
			"See also [`circleci_workflow_jobs`](workflow_jobs) to list the jobs within a workflow.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the workflow.",
				Required:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Name of the workflow, as declared in configuration.",
				Computed:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Current status of the workflow: one of `unauthorized`, `error`, " +
					"`not_run`, `running`, `failing`, `on_hold`, `canceled`, `failed` or `success`.",
				Computed: true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the workflow was created, as an RFC 3339 timestamp.",
				Computed:            true,
			},
			"stopped_at": schema.StringAttribute{
				MarkdownDescription: "When the workflow stopped, as an RFC 3339 timestamp. Null while the " +
					"workflow is still running.",
				Computed: true,
			},
			"pipeline_run_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the pipeline run this workflow belongs " +
					"to. See [`circleci_pipeline_run`](pipeline_run).",
				Computed: true,
			},
			"pipeline_number": schema.Int64Attribute{
				MarkdownDescription: "Number of the pipeline run this workflow belongs to.",
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
				MarkdownDescription: "Unique identifier (UUID) of the user who canceled the workflow. " +
					"Null unless the workflow was canceled.",
				Computed: true,
			},
			"errored_by": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the user attributed to the workflow " +
					"erroring. Null unless the workflow errored.",
				Computed: true,
			},
			"tag": schema.StringAttribute{
				MarkdownDescription: "`setup` when this is a setup workflow. Null otherwise.",
				Computed:            true,
			},
			"max_auto_reruns": schema.Int64Attribute{
				MarkdownDescription: "The maximum number of auto-reruns configured for the workflow. " +
					"Null when auto-rerun is not configured.",
				Computed: true,
			},
			"auto_rerun_number": schema.Int64Attribute{
				MarkdownDescription: "Present when this workflow was itself an auto-rerun of a previous " +
					"workflow: the Nth auto-rerun has `auto_rerun_number` N. Null otherwise.",
				Computed: true,
			},
		},
	}
}

// Read fetches the workflow and sets the data source state.
func (d *workflowDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config workflowDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := config.Id.ValueString()

	workflow, err := d.client.Workflows().Get(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI workflow "+id,
			circleci.Detail(err),
		)

		return
	}

	state := workflowToModel(*workflow)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *workflowDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// workflowToModel converts a circleci.Workflow into its Terraform model.
func workflowToModel(workflow circleci.Workflow) workflowDataSourceModel {
	model := workflowDataSourceModel{
		Id:              types.StringValue(workflow.ID),
		Name:            types.StringValue(workflow.Name),
		Status:          types.StringValue(workflow.Status),
		CreatedAt:       types.StringValue(workflow.CreatedAt),
		StoppedAt:       optionalString(workflow.StoppedAt),
		PipelineRunId:   types.StringValue(workflow.PipelineID),
		PipelineNumber:  types.Int64Value(int64(workflow.PipelineNumber)),
		ProjectSlug:     types.StringValue(workflow.ProjectSlug),
		StartedBy:       types.StringValue(workflow.StartedBy),
		CanceledBy:      optionalString(workflow.CanceledBy),
		ErroredBy:       optionalString(workflow.ErroredBy),
		Tag:             optionalString(workflow.Tag),
		MaxAutoReruns:   types.Int64Null(),
		AutoRerunNumber: types.Int64Null(),
	}

	if workflow.MaxAutoReruns != 0 {
		model.MaxAutoReruns = types.Int64Value(int64(workflow.MaxAutoReruns))
	}
	if workflow.AutoRerunNumber != 0 {
		model.AutoRerunNumber = types.Int64Value(int64(workflow.AutoRerunNumber))
	}

	return model
}
