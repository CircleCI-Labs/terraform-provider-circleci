// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &jobDataSource{}
	_ datasource.DataSourceWithConfigure = &jobDataSource{}
)

// jobDataSourceModel maps the data source schema.
type jobDataSourceModel struct {
	ProjectSlug      types.String `tfsdk:"project_slug"`
	JobNumber        types.Int64  `tfsdk:"job_number"`
	Status           types.String `tfsdk:"status"`
	CreatedAt        types.String `tfsdk:"created_at"`
	QueuedAt         types.String `tfsdk:"queued_at"`
	StartedAt        types.String `tfsdk:"started_at"`
	StoppedAt        types.String `tfsdk:"stopped_at"`
	DurationMs       types.Int64  `tfsdk:"duration_ms"`
	ResourceClass    types.String `tfsdk:"resource_class"`
	ExecutorType     types.String `tfsdk:"executor_type"`
	Name             types.String `tfsdk:"name"`
	WebUrl           types.String `tfsdk:"web_url"`
	Parallelism      types.Int64  `tfsdk:"parallelism"`
	PipelineRunId    types.String `tfsdk:"pipeline_run_id"`
	WorkflowId       types.String `tfsdk:"workflow_id"`
	WorkflowName     types.String `tfsdk:"workflow_name"`
	OrganizationName types.String `tfsdk:"organization_name"`
	Contexts         types.List   `tfsdk:"contexts"`
	ParallelRuns     types.List   `tfsdk:"parallel_runs"`
}

// jobParallelRunModel is one element of the parallel_runs list.
type jobParallelRunModel struct {
	Index  types.Int64  `tfsdk:"index"`
	Status types.String `tfsdk:"status"`
}

// jobParallelRunAttrTypes describes jobParallelRunModel to the framework when
// building the parallel_runs list value.
var jobParallelRunAttrTypes = map[string]attr.Type{
	"index":  types.Int64Type,
	"status": types.StringType,
}

// NewJobDataSource is a helper function to simplify the provider implementation.
func NewJobDataSource() datasource.DataSource {
	return &jobDataSource{}
}

// jobDataSource fetches one CircleCI job.
type jobDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *jobDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_job"
}

// Schema defines the schema for the data source.
func (d *jobDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches one CircleCI job by project slug and job number.\n\n" +
			"~> **`circleci_job` is a point-in-time read of mutable runtime state.** `status`, timestamps " +
			"and `parallel_runs` change while the job runs. Use this data source for inspection and in " +
			"`check` blocks; using it to derive a resource attribute will cause a perpetual diff.\n\n" +
			"-> **Addressed by number, not by the id the API reference documents.** The CircleCI v2 API " +
			"reference documents `GET /api/v2/jobs/{id}`, but that route is a pass-through to a backend " +
			"that only exists on CircleCI Cloud, and CircleCI Server has no equivalent route at all. " +
			"`GET /api/v2/project/{slug}/job/{job-number}` is used instead, which *is* available on " +
			"CircleCI Server. That makes `project_slug` and `job_number` the identifying inputs here, " +
			"not a job id.\n\n" +
			"Per-step detail (the equivalent of `circleci_job`'s `parallel_runs[].steps`) and artifacts/test " +
			"results are not exposed: they are unbounded, per-build detail that would make this read as a " +
			"partial build log rather than a job summary. Use the CircleCI web UI or the REST API directly " +
			"for those.",
		Attributes: map[string]schema.Attribute{
			"project_slug": schema.StringAttribute{
				MarkdownDescription: "The project's slug, e.g. `gh/CircleCI-Public/api-preview-docs`, or " +
					"`circleci/<org-id>/<project-id>` for a GitLab, GitHub App or GitHub Server project.",
				Required: true,
			},
			"job_number": schema.Int64Attribute{
				MarkdownDescription: "The job's number, unique within its project.",
				Required:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Current status of the job.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the job was created, as an RFC 3339 timestamp. Null for a job " +
					"whose creation time was not recorded.",
				Computed: true,
			},
			"queued_at": schema.StringAttribute{
				MarkdownDescription: "When the job was placed in a queue, as an RFC 3339 timestamp. Null " +
					"while the job is still queued or has not been queued yet.",
				Computed: true,
			},
			"started_at": schema.StringAttribute{
				MarkdownDescription: "When the job started running, as an RFC 3339 timestamp. Null while " +
					"the job has not started.",
				Computed: true,
			},
			"stopped_at": schema.StringAttribute{
				MarkdownDescription: "When the job stopped, as an RFC 3339 timestamp. Null while the job " +
					"is still running.",
				Computed: true,
			},
			"duration_ms": schema.Int64Attribute{
				MarkdownDescription: "Duration of the job in milliseconds. Null while the job is still running.",
				Computed:            true,
			},
			"resource_class": schema.StringAttribute{
				MarkdownDescription: "Resource class the job ran on. Null until the job has been dispatched.",
				Computed:            true,
			},
			"executor_type": schema.StringAttribute{
				MarkdownDescription: "Executor type the job ran on, e.g. `docker`. Null until the job has " +
					"been dispatched.",
				Computed: true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Name of the job, as declared in configuration.",
				Computed:            true,
			},
			"web_url": schema.StringAttribute{
				MarkdownDescription: "URL of the job in the CircleCI web UI.",
				Computed:            true,
			},
			"parallelism": schema.Int64Attribute{
				MarkdownDescription: "Number of parallel runs the job has.",
				Computed:            true,
			},
			"pipeline_run_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the pipeline run this job belongs to. " +
					"See [`circleci_pipeline_run`](pipeline_run).",
				Computed: true,
			},
			"workflow_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the workflow this job was most recently " +
					"part of. See [`circleci_workflow`](workflow).",
				Computed: true,
			},
			"workflow_name": schema.StringAttribute{
				MarkdownDescription: "Name of the workflow this job was most recently part of.",
				Computed:            true,
			},
			"organization_name": schema.StringAttribute{
				MarkdownDescription: "Name of the organization owning the job's project.",
				Computed:            true,
			},
			"contexts": schema.ListAttribute{
				MarkdownDescription: "Names of the contexts used by the job.",
				Computed:            true,
				ElementType:         types.StringType,
			},
			"parallel_runs": schema.ListNestedAttribute{
				MarkdownDescription: "Status of each parallel run ('task') of the job. Empty until the " +
					"job has been dispatched.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"index": schema.Int64Attribute{
							MarkdownDescription: "Index of the parallel run.",
							Computed:            true,
						},
						"status": schema.StringAttribute{
							MarkdownDescription: "Status of the parallel run.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// Read fetches the job and sets the data source state.
func (d *jobDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config jobDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectSlug := config.ProjectSlug.ValueString()
	jobNumber := config.JobNumber.ValueInt64()

	job, err := d.client.Jobs().Get(ctx, projectSlug, jobNumber)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI job",
			circleci.Detail(err),
		)

		return
	}

	state, diags := jobToModel(ctx, projectSlug, jobNumber, *job)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *jobDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// jobToModel converts a circleci.Job into its Terraform model. projectSlug and
// jobNumber are echoed back from the configuration rather than read from the
// API response, because the job's own "number" field can be an untyped
// pass-through rather than a normalized value, and the slug is not returned
// at all.
func jobToModel(ctx context.Context, projectSlug string, jobNumber int64, job circleci.Job) (jobDataSourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	model := jobDataSourceModel{
		ProjectSlug:      types.StringValue(projectSlug),
		JobNumber:        types.Int64Value(jobNumber),
		Status:           types.StringValue(job.Status),
		CreatedAt:        optionalString(job.CreatedAt),
		QueuedAt:         optionalString(job.QueuedAt),
		StartedAt:        optionalString(job.StartedAt),
		StoppedAt:        optionalString(job.StoppedAt),
		DurationMs:       types.Int64Null(),
		ResourceClass:    types.StringNull(),
		ExecutorType:     types.StringNull(),
		Name:             optionalString(job.Name),
		WebUrl:           optionalString(job.WebURL),
		Parallelism:      types.Int64Null(),
		PipelineRunId:    types.StringNull(),
		WorkflowId:       types.StringNull(),
		WorkflowName:     types.StringNull(),
		OrganizationName: types.StringNull(),
	}

	if job.Duration != nil {
		model.DurationMs = types.Int64Value(*job.Duration)
	}
	if job.Executor != nil {
		if job.Executor.ResourceClass != nil {
			model.ResourceClass = types.StringValue(*job.Executor.ResourceClass)
		}
		if job.Executor.Type != nil {
			model.ExecutorType = types.StringValue(*job.Executor.Type)
		}
	}
	if job.Parallelism != 0 {
		model.Parallelism = types.Int64Value(int64(job.Parallelism))
	}
	if job.Pipeline != nil {
		model.PipelineRunId = types.StringValue(job.Pipeline.ID)
	}
	if job.LatestWorkflow != nil {
		model.WorkflowId = types.StringValue(job.LatestWorkflow.ID)
		model.WorkflowName = types.StringValue(job.LatestWorkflow.Name)
	}
	if job.Organization != nil {
		model.OrganizationName = types.StringValue(job.Organization.Name)
	}

	contextNames := make([]string, 0, len(job.Contexts))
	for _, c := range job.Contexts {
		contextNames = append(contextNames, c.Name)
	}
	contexts, contextDiags := types.ListValueFrom(ctx, types.StringType, contextNames)
	diags.Append(contextDiags...)
	model.Contexts = contexts

	parallelRuns := make([]jobParallelRunModel, 0, len(job.ParallelRuns))
	for _, run := range job.ParallelRuns {
		parallelRuns = append(parallelRuns, jobParallelRunModel{
			Index:  types.Int64Value(int64(run.Index)),
			Status: types.StringValue(run.Status),
		})
	}
	runs, runDiags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: jobParallelRunAttrTypes}, parallelRuns)
	diags.Append(runDiags...)
	model.ParallelRuns = runs

	return model, diags
}
