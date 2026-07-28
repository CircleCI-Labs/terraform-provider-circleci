// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &workflowJobsDataSource{}
	_ datasource.DataSourceWithConfigure = &workflowJobsDataSource{}
)

// workflowJobsDataSourceModel maps the data source schema.
type workflowJobsDataSourceModel struct {
	WorkflowId types.String       `tfsdk:"workflow_id"`
	Jobs       []workflowJobModel `tfsdk:"jobs"`
}

// workflowJobModel maps one job in a workflow's job graph.
type workflowJobModel struct {
	Id                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Type              types.String `tfsdk:"type"`
	Status            types.String `tfsdk:"status"`
	StartedAt         types.String `tfsdk:"started_at"`
	StoppedAt         types.String `tfsdk:"stopped_at"`
	Dependencies      types.List   `tfsdk:"dependencies"`
	ProjectSlug       types.String `tfsdk:"project_slug"`
	JobNumber         types.Int64  `tfsdk:"job_number"`
	ApprovalRequestId types.String `tfsdk:"approval_request_id"`
	ApprovedBy        types.String `tfsdk:"approved_by"`
	CanceledBy        types.String `tfsdk:"canceled_by"`
}

// NewWorkflowJobsDataSource is a helper function to simplify the provider
// implementation.
func NewWorkflowJobsDataSource() datasource.DataSource {
	return &workflowJobsDataSource{}
}

// workflowJobsDataSource lists the jobs in a CircleCI workflow's job graph.
type workflowJobsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *workflowJobsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workflow_jobs"
}

// Schema defines the schema for the data source.
func (d *workflowJobsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every job in a CircleCI workflow's job graph. Pagination is " +
			"followed internally, so the result covers every job rather than one page.\n\n" +
			"~> **`circleci_workflow_jobs` is a point-in-time read of mutable runtime state.** `status` " +
			"changes as each job runs, and the set of jobs itself can grow as approval jobs unblock " +
			"downstream jobs. Use this data source for inspection and in `check` blocks; using it to " +
			"derive a resource attribute will cause a perpetual diff.\n\n" +
			"Available on both CircleCI Cloud and CircleCI Server: this route is implemented directly in " +
			"the same API application on both.\n\n" +
			"Requires-not-satisfied dependency detail (per-job upstream status) is intentionally not " +
			"exposed here; `dependencies` (by job name) is enough to reconstruct the graph.",
		Attributes: map[string]schema.Attribute{
			"workflow_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the workflow, as returned by " +
					"[`circleci_workflow`](workflow).",
				Required: true,
			},
			"jobs": schema.ListNestedAttribute{
				MarkdownDescription: "The jobs in the workflow's job graph, in the order the API returns them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the job.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Name of the job, as declared in configuration.",
							Computed:            true,
						},
						"type": schema.StringAttribute{
							MarkdownDescription: "Type of the job, e.g. `build` or `approval`.",
							Computed:            true,
						},
						"status": schema.StringAttribute{
							MarkdownDescription: "Current status of the job.",
							Computed:            true,
						},
						"started_at": schema.StringAttribute{
							MarkdownDescription: "When the job started, as an RFC 3339 timestamp.",
							Computed:            true,
						},
						"stopped_at": schema.StringAttribute{
							MarkdownDescription: "When the job stopped, as an RFC 3339 timestamp. Null " +
								"while the job is still running.",
							Computed: true,
						},
						"dependencies": schema.ListAttribute{
							MarkdownDescription: "Names of the jobs this job depends on.",
							Computed:            true,
							ElementType:         types.StringType,
						},
						"project_slug": schema.StringAttribute{
							MarkdownDescription: "Slug of the project this job belongs to.",
							Computed:            true,
						},
						"job_number": schema.Int64Attribute{
							MarkdownDescription: "Number of the job. Null for a job that has not yet been " +
								"assigned one, such as a pending approval job.",
							Computed: true,
						},
						"approval_request_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the approval request. Set " +
								"only when `type` is `approval`.",
							Computed: true,
						},
						"approved_by": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the user who approved the " +
								"job. Null unless `type` is `approval` and it has been approved.",
							Computed: true,
						},
						"canceled_by": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the user who canceled the " +
								"job. Null unless the job was canceled.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

// Read lists the workflow's jobs.
func (d *workflowJobsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state workflowJobsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	workflowID := state.WorkflowId.ValueString()

	jobs, err := d.client.Workflows().ListJobs(ctx, workflowID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list jobs for CircleCI workflow "+workflowID,
			circleci.Detail(err),
		)

		return
	}

	state.Jobs = make([]workflowJobModel, 0, len(jobs))
	for _, job := range jobs {
		model, diags := workflowJobToModel(ctx, job)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}

		state.Jobs = append(state.Jobs, model)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *workflowJobsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// workflowJobToModel converts a circleci.WorkflowJob into its Terraform model.
func workflowJobToModel(ctx context.Context, job circleci.WorkflowJob) (workflowJobModel, diag.Diagnostics) {
	dependencies, diags := types.ListValueFrom(ctx, types.StringType, job.Dependencies)

	model := workflowJobModel{
		Id:                types.StringValue(job.ID),
		Name:              types.StringValue(job.Name),
		Type:              types.StringValue(job.Type),
		Status:            types.StringValue(job.Status),
		StartedAt:         optionalString(job.StartedAt),
		StoppedAt:         optionalString(job.StoppedAt),
		Dependencies:      dependencies,
		ProjectSlug:       types.StringValue(job.ProjectSlug),
		JobNumber:         types.Int64Null(),
		ApprovalRequestId: optionalString(job.ApprovalRequestID),
		ApprovedBy:        optionalString(job.ApprovedBy),
		CanceledBy:        optionalString(job.CanceledBy),
	}

	if job.JobNumber != 0 {
		model.JobNumber = types.Int64Value(job.JobNumber)
	}

	return model, diags
}
