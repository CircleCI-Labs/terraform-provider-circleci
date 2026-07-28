// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &insightsWorkflowsDataSource{}
	_ datasource.DataSourceWithConfigure = &insightsWorkflowsDataSource{}
)

// insightsCaveat is the accuracy warning every Insights data source repeats.
//
// Both halves matter. The daily refresh means a plan run right after a pipeline
// will not see it, which looks like a bug unless it is stated. And CircleCI
// documents Insights credit figures as not sharing a source of truth with
// billing, so a configuration that gates on them is gating on approximations.
const insightsCaveat = "~> **Insights metrics are approximate and lag reality.** They are recomputed daily, so " +
	"the last 24 hours of activity may be missing, and no window longer than 90 days is retained. Credit " +
	"figures in particular do not share a source of truth with billing: CircleCI documents Insights as " +
	"unsuitable for credit reporting, and the Plan Overview page in the web application as the only accurate " +
	"source. Treat these values as indicators, not as inputs to anything that must be exact."

// insightsProjectSlugDescription documents the project slug argument. It is
// shared because getting this wrong is the most likely way to reach a confusing
// 404: a GitLab, GitHub App or GitHub Server project has no VCS-side org and repo
// name, so its slug is not the vcs/org/repo shape people expect.
const insightsProjectSlugDescription = "Slug of the project to report on, in `vcs-slug/org-name/repo-name` " +
	"form — for example `gh/acme/api`.\n\n" +
	"GitLab, GitHub App and GitHub Server projects do not have that form. They have no VCS-side " +
	"organization and repository name to build a slug from, so their slug is " +
	"`circleci/<org-uuid>/<project-uuid>` instead. Both forms are accepted; the `slug` attribute of the " +
	"`circleci_project` data source reports whichever one applies."

// insightsWorkflowsDataSourceModel maps the data source schema.
type insightsWorkflowsDataSourceModel struct {
	ProjectSlug     types.String                `tfsdk:"project_slug"`
	ReportingWindow types.String                `tfsdk:"reporting_window"`
	Branch          types.String                `tfsdk:"branch"`
	AllBranches     types.Bool                  `tfsdk:"all_branches"`
	Workflows       []insightsWorkflowItemModel `tfsdk:"workflows"`
}

// insightsWorkflowItemModel maps one workflow's metrics.
//
// The API's nested metrics and duration_metrics objects are flattened into this
// one level, with the duration fields prefixed. Two nested objects deep is
// painful to reference from a configuration — `w.metrics.duration_metrics.p95` —
// and there is nothing else at those levels to keep them apart.
type insightsWorkflowItemModel struct {
	Name        types.String `tfsdk:"name"`
	ProjectID   types.String `tfsdk:"project_id"`
	WindowStart types.String `tfsdk:"window_start"`
	WindowEnd   types.String `tfsdk:"window_end"`

	TotalRuns        types.Int64   `tfsdk:"total_runs"`
	SuccessfulRuns   types.Int64   `tfsdk:"successful_runs"`
	FailedRuns       types.Int64   `tfsdk:"failed_runs"`
	SuccessRate      types.Float64 `tfsdk:"success_rate"`
	Throughput       types.Float64 `tfsdk:"throughput"`
	MTTR             types.Int64   `tfsdk:"mttr"`
	TotalCreditsUsed types.Int64   `tfsdk:"total_credits_used"`
	TotalRecoveries  types.Int64   `tfsdk:"total_recoveries"`

	DurationMin               types.Int64   `tfsdk:"duration_min"`
	DurationMean              types.Int64   `tfsdk:"duration_mean"`
	DurationMedian            types.Int64   `tfsdk:"duration_median"`
	DurationP95               types.Int64   `tfsdk:"duration_p95"`
	DurationMax               types.Int64   `tfsdk:"duration_max"`
	DurationStandardDeviation types.Float64 `tfsdk:"duration_standard_deviation"`
}

// NewInsightsWorkflowsDataSource is a helper function to simplify the provider
// implementation.
func NewInsightsWorkflowsDataSource() datasource.DataSource {
	return &insightsWorkflowsDataSource{}
}

// insightsWorkflowsDataSource reads per-workflow Insights metrics.
type insightsWorkflowsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *insightsWorkflowsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_insights_workflows"
}

// Schema defines the schema for the data source.
func (d *insightsWorkflowsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches per-workflow summary metrics for a project: run counts, success rate, " +
			"duration percentiles and credits consumed.\n\n" +
			"The practical use is a health check a plan can act on — asserting a workflow's success rate has " +
			"not dropped below a threshold, or surfacing the slowest workflows as outputs for a dashboard.\n\n" +
			"Pagination is followed internally, so the result covers every workflow rather than one page.\n\n" +
			insightsCaveat,
		Attributes: map[string]schema.Attribute{
			"project_slug": schema.StringAttribute{
				MarkdownDescription: insightsProjectSlugDescription,
				Required:            true,
			},
			"reporting_window": schema.StringAttribute{
				MarkdownDescription: "The aggregation window. One of `last-24-hours`, `last-7-days`, " +
					"`last-30-days`, `last-60-days` or `last-90-days`. Defaults to `last-90-days`, which is " +
					"also the longest window Insights retains.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.OneOf(circleci.InsightsReportingWindows...)},
			},
			"branch": schema.StringAttribute{
				MarkdownDescription: "Scope the metrics to a single branch. Omitting this does not report all " +
					"branches — the API falls back to the project's default branch. Set `all_branches` to " +
					"combine every branch instead.",
				Optional: true,
				Validators: []validator.String{
					// The API reads all-branches first and silently ignores branch when
					// both are set, which would quietly report something other than what
					// was asked for. Rejecting the combination up front is clearer.
					stringvalidator.ConflictsWith(path.MatchRoot("all_branches")),
				},
			},
			"all_branches": schema.BoolAttribute{
				MarkdownDescription: "Combine every branch into one set of metrics instead of scoping to a " +
					"single branch. Conflicts with `branch`.",
				Optional: true,
			},
			"workflows": schema.ListNestedAttribute{
				MarkdownDescription: "One entry per workflow, in the order the API returns them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							MarkdownDescription: "Name of the workflow, as written in `.circleci/config.yml`.",
							Computed:            true,
						},
						"project_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the project the workflow belongs to.",
							Computed:            true,
						},
						"window_start": schema.StringAttribute{
							MarkdownDescription: "Timestamp of the first run inside the reporting window " +
								"(RFC 3339).",
							Computed: true,
						},
						"window_end": schema.StringAttribute{
							MarkdownDescription: "Timestamp of the last run inside the reporting window " +
								"(RFC 3339).",
							Computed: true,
						},
						"total_runs": schema.Int64Attribute{
							MarkdownDescription: "Total runs in the window, including runs still on hold or " +
								"running. This is therefore not always `successful_runs + failed_runs`.",
							Computed: true,
						},
						"successful_runs": schema.Int64Attribute{
							MarkdownDescription: "Runs that succeeded.",
							Computed:            true,
						},
						"failed_runs": schema.Int64Attribute{
							MarkdownDescription: "Runs that failed.",
							Computed:            true,
						},
						"success_rate": schema.Float64Attribute{
							MarkdownDescription: "Proportion of runs that succeeded, as a ratio between 0 and " +
								"1 — not a percentage. Compare against `0.95`, not `95`.",
							Computed: true,
						},
						"throughput": schema.Float64Attribute{
							MarkdownDescription: "Average number of runs per day over the window.",
							Computed:            true,
						},
						"mttr": schema.Int64Attribute{
							MarkdownDescription: "Mean time to recovery in seconds: the mean gap between a " +
								"failure and the next success. `null` for a workflow that never failed in the " +
								"window, which is not the same as a recovery time of zero.",
							Computed: true,
						},
						"total_credits_used": schema.Int64Attribute{
							MarkdownDescription: "Credits consumed by the workflow in the window, or `null` " +
								"when not reported. See the accuracy note above before relying on this.",
							Computed: true,
						},
						"total_recoveries": schema.Int64Attribute{
							MarkdownDescription: "Number of runs that recovered a previously failing workflow. " +
								"`null` when the workflow never failed.",
							Computed: true,
						},
						"duration_min": schema.Int64Attribute{
							MarkdownDescription: "Shortest run duration in seconds, or `null` when nothing ran.",
							Computed:            true,
						},
						"duration_mean": schema.Int64Attribute{
							MarkdownDescription: "Mean run duration in seconds, or `null` when nothing ran.",
							Computed:            true,
						},
						"duration_median": schema.Int64Attribute{
							MarkdownDescription: "Median run duration in seconds, or `null` when nothing ran.",
							Computed:            true,
						},
						"duration_p95": schema.Int64Attribute{
							MarkdownDescription: "95th percentile run duration in seconds, or `null` when " +
								"nothing ran. This is usually the more useful latency figure than the mean.",
							Computed: true,
						},
						"duration_max": schema.Int64Attribute{
							MarkdownDescription: "Longest run duration in seconds, or `null` when nothing ran.",
							Computed:            true,
						},
						"duration_standard_deviation": schema.Float64Attribute{
							MarkdownDescription: "Standard deviation of run duration in seconds, or `null` when " +
								"nothing ran.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

// Read fetches the project's workflow metrics.
func (d *insightsWorkflowsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		return
	}

	var state insightsWorkflowsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectSlug := state.ProjectSlug.ValueString()

	workflows, err := d.client.Insights().ListWorkflows(ctx, projectSlug, circleci.InsightsWorkflowsOptions{
		ReportingWindow: state.ReportingWindow.ValueString(),
		Branch:          state.Branch.ValueString(),
		AllBranches:     state.AllBranches.ValueBool(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI Insights workflow metrics for "+projectSlug,
			insightsDetail(err),
		)

		return
	}

	// An empty, non-null list keeps `for_each` and `length()` working for a project
	// that has not run anything inside the window.
	state.Workflows = make([]insightsWorkflowItemModel, 0, len(workflows))
	for _, workflow := range workflows {
		metrics := workflow.Metrics
		duration := metrics.DurationMetrics

		state.Workflows = append(state.Workflows, insightsWorkflowItemModel{
			Name:        types.StringValue(workflow.Name),
			ProjectID:   types.StringValue(workflow.ProjectID),
			WindowStart: types.StringValue(workflow.WindowStart),
			WindowEnd:   types.StringValue(workflow.WindowEnd),

			TotalRuns:      types.Int64Value(metrics.TotalRuns),
			SuccessfulRuns: types.Int64Value(metrics.SuccessfulRuns),
			FailedRuns:     types.Int64Value(metrics.FailedRuns),
			SuccessRate:    types.Float64Value(metrics.SuccessRate),
			Throughput:     types.Float64Value(metrics.Throughput),
			// The pointer-valued metrics reach Terraform as null rather than zero, so
			// that "never failed" stays distinguishable from "recovered instantly".
			MTTR:             types.Int64PointerValue(metrics.MTTR),
			TotalCreditsUsed: types.Int64PointerValue(metrics.TotalCreditsUsed),
			TotalRecoveries:  types.Int64PointerValue(metrics.TotalRecoveries),

			DurationMin:               types.Int64PointerValue(duration.Min),
			DurationMean:              types.Int64PointerValue(duration.Mean),
			DurationMedian:            types.Int64PointerValue(duration.Median),
			DurationP95:               types.Int64PointerValue(duration.P95),
			DurationMax:               types.Int64PointerValue(duration.Max),
			DurationStandardDeviation: types.Float64PointerValue(duration.StandardDeviation),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *insightsWorkflowsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
