// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &insightsSummaryDataSource{}
	_ datasource.DataSourceWithConfigure = &insightsSummaryDataSource{}
)

// insightsSummaryDataSourceModel maps the data source schema.
type insightsSummaryDataSourceModel struct {
	OrganizationSlug types.String `tfsdk:"organization_slug"`
	ReportingWindow  types.String `tfsdk:"reporting_window"`
	ProjectNames     types.List   `tfsdk:"project_names"`

	Metrics     *insightsSummaryMetricsModel  `tfsdk:"metrics"`
	Trends      *insightsSummaryTrendsModel   `tfsdk:"trends"`
	Projects    []insightsSummaryProjectModel `tfsdk:"projects"`
	AllProjects types.List                    `tfsdk:"all_projects"`
}

// insightsSummaryMetricsModel maps one metrics block, for the organization or for
// a project.
type insightsSummaryMetricsModel struct {
	TotalRuns         types.Int64   `tfsdk:"total_runs"`
	TotalDurationSecs types.Int64   `tfsdk:"total_duration_secs"`
	TotalCreditsUsed  types.Int64   `tfsdk:"total_credits_used"`
	SuccessRate       types.Float64 `tfsdk:"success_rate"`
	Throughput        types.Float64 `tfsdk:"throughput"`
}

// insightsSummaryTrendsModel maps one trends block. It is a distinct type from the
// metrics model because every field is a ratio of change rather than a count, so
// the two are not interchangeable even though they share field names.
type insightsSummaryTrendsModel struct {
	TotalRuns         types.Float64 `tfsdk:"total_runs"`
	TotalDurationSecs types.Float64 `tfsdk:"total_duration_secs"`
	TotalCreditsUsed  types.Float64 `tfsdk:"total_credits_used"`
	SuccessRate       types.Float64 `tfsdk:"success_rate"`
	Throughput        types.Float64 `tfsdk:"throughput"`
}

// insightsSummaryProjectModel maps one project's summary.
type insightsSummaryProjectModel struct {
	ProjectName types.String                 `tfsdk:"project_name"`
	Metrics     *insightsSummaryMetricsModel `tfsdk:"metrics"`
	Trends      *insightsSummaryTrendsModel  `tfsdk:"trends"`
}

// NewInsightsSummaryDataSource is a helper function to simplify the provider
// implementation.
func NewInsightsSummaryDataSource() datasource.DataSource {
	return &insightsSummaryDataSource{}
}

// insightsSummaryDataSource reads organization-wide Insights metrics.
type insightsSummaryDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *insightsSummaryDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_insights_summary"
}

// Schema defines the schema for the data source.
func (d *insightsSummaryDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	metricsBlock := func(description string) schema.SingleNestedAttribute {
		return schema.SingleNestedAttribute{
			MarkdownDescription: description,
			Computed:            true,
			Attributes: map[string]schema.Attribute{
				"total_runs": schema.Int64Attribute{
					MarkdownDescription: "Total pipeline runs in the window.",
					Computed:            true,
				},
				"total_duration_secs": schema.Int64Attribute{
					MarkdownDescription: "Total compute duration in seconds across every run in the window.",
					Computed:            true,
				},
				"total_credits_used": schema.Int64Attribute{
					MarkdownDescription: "Credits consumed in the window. See the accuracy note above before " +
						"relying on this.",
					Computed: true,
				},
				"success_rate": schema.Float64Attribute{
					MarkdownDescription: "Proportion of runs that succeeded, as a ratio between 0 and 1 — not a " +
						"percentage.",
					Computed: true,
				},
				"throughput": schema.Float64Attribute{
					MarkdownDescription: "Average number of runs per day. Reported for the organization but " +
						"`null` for individual projects, which the API does not compute it for.",
					Computed: true,
				},
			},
		}
	}

	trendsBlock := func(description string) schema.SingleNestedAttribute {
		trend := func(metric string) schema.Float64Attribute {
			return schema.Float64Attribute{
				MarkdownDescription: "Change in " + metric + " against the preceding window of the same length, " +
					"as a ratio: `0.1` is a ten percent increase and `-0.1` a ten percent decrease.",
				Computed: true,
			}
		}

		return schema.SingleNestedAttribute{
			MarkdownDescription: description,
			Computed:            true,
			Attributes: map[string]schema.Attribute{
				"total_runs":          trend("total runs"),
				"total_duration_secs": trend("total duration"),
				"total_credits_used":  trend("credits consumed"),
				"success_rate":        trend("success rate"),
				"throughput":          trend("throughput"),
			},
		}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches organization-wide Insights metrics, with the change against the " +
			"preceding window, and optionally the same for named projects.\n\n" +
			"This is the only organization-level view the CircleCI API offers: total runs, total compute " +
			"duration, credits consumed and success rate, each with a trend. Use it for a rollup output or " +
			"to assert an organization's success rate has not regressed.\n\n" +
			"~> **`projects` is empty unless `project_names` is set.** The API only computes per-project " +
			"metrics for the names passed to it; it does not return every project by default. Read " +
			"`all_projects` first to discover the names, then pass the ones you want. That does mean two " +
			"applies to get per-project data for a project set you do not already know.\n\n" +
			insightsCaveat,
		Attributes: map[string]schema.Attribute{
			"organization_slug": schema.StringAttribute{
				MarkdownDescription: "Slug of the organization to report on, in `vcs-slug/org-name` form — for " +
					"example `gh/acme` — or `circleci/<org-uuid>` for a standalone organization.\n\n" +
					"Note this is two segments, unlike a project slug's three. The " +
					"`circleci_user_collaborations` data source reports the slug of every organization the " +
					"configured token can see.",
				Required: true,
			},
			"reporting_window": schema.StringAttribute{
				MarkdownDescription: "The aggregation window. One of `last-24-hours`, `last-7-days`, " +
					"`last-30-days`, `last-60-days` or `last-90-days`. Defaults to `last-90-days`, which is " +
					"also the longest window Insights retains.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.OneOf(circleci.InsightsReportingWindows...)},
			},
			"project_names": schema.ListAttribute{
				MarkdownDescription: "Names of the projects to report per-project metrics for. These are " +
					"project names, not slugs. Omit this to get organization totals only; the names available " +
					"are reported in `all_projects`.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"metrics": metricsBlock("Aggregated metrics for the whole organization over the window."),
			"trends": trendsBlock(
				"Change in each organization-wide metric against the preceding window of the same length.",
			),
			"projects": schema.ListNestedAttribute{
				MarkdownDescription: "Per-project metrics, one entry per name in `project_names` that the API " +
					"recognised. Empty when `project_names` is not set.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"project_name": schema.StringAttribute{
							MarkdownDescription: "Name of the project. The API identifies projects by name here " +
								"rather than by ID.",
							Computed: true,
						},
						"metrics": metricsBlock("Aggregated metrics for this project, across all branches."),
						"trends": trendsBlock(
							"Change in each of this project's metrics against the preceding window.",
						),
					},
				},
			},
			"all_projects": schema.ListAttribute{
				MarkdownDescription: "Names of every project in the organization the configured token can see. " +
					"This is what to read to discover the values to pass in `project_names`.",
				Computed:    true,
				ElementType: types.StringType,
			},
		},
	}
}

// Read fetches the organization's summary metrics.
func (d *insightsSummaryDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		return
	}

	var state insightsSummaryDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var projectNames []string
	if !state.ProjectNames.IsNull() && !state.ProjectNames.IsUnknown() {
		resp.Diagnostics.Append(state.ProjectNames.ElementsAs(ctx, &projectNames, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	organizationSlug := state.OrganizationSlug.ValueString()

	summary, err := d.client.Insights().GetSummary(
		ctx, organizationSlug, state.ReportingWindow.ValueString(), projectNames,
	)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI Insights summary for "+organizationSlug,
			insightsDetail(err),
		)

		return
	}

	state.Metrics = newInsightsSummaryMetricsModel(summary.OrganizationData.Metrics)
	state.Trends = newInsightsSummaryTrendsModel(summary.OrganizationData.Trends)

	// An empty, non-null list keeps `for_each` and `length()` working when no
	// project names were requested, which is the common case.
	state.Projects = make([]insightsSummaryProjectModel, 0, len(summary.OrganizationProjectData))
	for _, project := range summary.OrganizationProjectData {
		state.Projects = append(state.Projects, insightsSummaryProjectModel{
			ProjectName: types.StringValue(project.ProjectName),
			Metrics:     newInsightsSummaryMetricsModel(project.Metrics),
			Trends:      newInsightsSummaryTrendsModel(project.Trends),
		})
	}

	// all_projects is nullable on the wire. Normalising nil to an empty slice keeps
	// the attribute a usable list rather than null.
	allProjects := summary.AllProjects
	if allProjects == nil {
		allProjects = []string{}
	}

	names, diags := types.ListValueFrom(ctx, types.StringType, allProjects)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.AllProjects = names

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// newInsightsSummaryMetricsModel converts a metrics block for Terraform.
func newInsightsSummaryMetricsModel(metrics circleci.InsightsSummaryMetrics) *insightsSummaryMetricsModel {
	return &insightsSummaryMetricsModel{
		TotalRuns:         types.Int64Value(metrics.TotalRuns),
		TotalDurationSecs: types.Int64Value(metrics.TotalDurationSecs),
		TotalCreditsUsed:  types.Int64Value(metrics.TotalCreditsUsed),
		SuccessRate:       types.Float64Value(metrics.SuccessRate),
		// Null rather than zero for a project: the API does not compute throughput
		// per project, and reporting 0 runs per day would be a lie.
		Throughput: types.Float64PointerValue(metrics.Throughput),
	}
}

// newInsightsSummaryTrendsModel converts a trends block for Terraform.
func newInsightsSummaryTrendsModel(trends circleci.InsightsSummaryTrends) *insightsSummaryTrendsModel {
	return &insightsSummaryTrendsModel{
		TotalRuns:         types.Float64Value(trends.TotalRuns),
		TotalDurationSecs: types.Float64Value(trends.TotalDurationSecs),
		TotalCreditsUsed:  types.Float64Value(trends.TotalCreditsUsed),
		SuccessRate:       types.Float64Value(trends.SuccessRate),
		Throughput:        types.Float64PointerValue(trends.Throughput),
	}
}

// Configure adds the provider configured client to the data source.
func (d *insightsSummaryDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
