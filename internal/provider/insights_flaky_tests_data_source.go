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
	_ datasource.DataSource              = &insightsFlakyTestsDataSource{}
	_ datasource.DataSourceWithConfigure = &insightsFlakyTestsDataSource{}
)

// insightsFlakyTestsDataSourceModel maps the data source schema.
type insightsFlakyTestsDataSourceModel struct {
	ProjectSlug     types.String                 `tfsdk:"project_slug"`
	TotalFlakyTests types.Int64                  `tfsdk:"total_flaky_tests"`
	FlakyTests      []insightsFlakyTestItemModel `tfsdk:"flaky_tests"`
}

// insightsFlakyTestItemModel maps one recorded flake.
type insightsFlakyTestItemModel struct {
	TestName          types.String `tfsdk:"test_name"`
	Classname         types.String `tfsdk:"classname"`
	File              types.String `tfsdk:"file"`
	Source            types.String `tfsdk:"source"`
	TimesFlaked       types.Int64  `tfsdk:"times_flaked"`
	TimeWasted        types.Int64  `tfsdk:"time_wasted"`
	JobName           types.String `tfsdk:"job_name"`
	JobNumber         types.Int64  `tfsdk:"job_number"`
	PipelineNumber    types.Int64  `tfsdk:"pipeline_number"`
	WorkflowID        types.String `tfsdk:"workflow_id"`
	WorkflowName      types.String `tfsdk:"workflow_name"`
	WorkflowCreatedAt types.String `tfsdk:"workflow_created_at"`
}

// NewInsightsFlakyTestsDataSource is a helper function to simplify the provider
// implementation.
func NewInsightsFlakyTestsDataSource() datasource.DataSource {
	return &insightsFlakyTestsDataSource{}
}

// insightsFlakyTestsDataSource reads a project's recorded flakes.
type insightsFlakyTestsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *insightsFlakyTestsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_insights_flaky_tests"
}

// Schema defines the schema for the data source.
func (d *insightsFlakyTestsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches the flaky tests CircleCI has recorded for a project.\n\n" +
			"A flake is a test that both passed and failed at the same commit. This is the most directly " +
			"actionable thing Insights exposes: a plan can fail, or an output can be raised, when a " +
			"project's flake count crosses a threshold.\n\n" +
			"Flakes are branch agnostic and the window is fixed, so there is nothing to narrow this by. A " +
			"test stops being reported once two weeks pass without another flake, and each new flake resets " +
			"that timer.\n\n" +
			"~> **`total_flaky_tests` is not `length(flaky_tests)`.** The list holds one entry per flake " +
			"instance, while the total counts unique tests. A single test that flaked five times contributes " +
			"five entries but one to the total.",
		Attributes: map[string]schema.Attribute{
			"project_slug": schema.StringAttribute{
				MarkdownDescription: insightsProjectSlugDescription,
				Required:            true,
			},
			"total_flaky_tests": schema.Int64Attribute{
				MarkdownDescription: "Number of distinct tests that have flaked. This is the figure to gate " +
					"on; see the note above on why it differs from the length of `flaky_tests`.",
				Computed: true,
			},
			"flaky_tests": schema.ListNestedAttribute{
				MarkdownDescription: "One entry per recorded flake, in the order the API returns them. A test " +
					"that has flaked more than once appears more than once.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"test_name": schema.StringAttribute{
							MarkdownDescription: "Name of the flaking test.",
							Computed:            true,
						},
						"classname": schema.StringAttribute{
							MarkdownDescription: "The test's class or suite, as reported by the test runner.",
							Computed:            true,
						},
						"file": schema.StringAttribute{
							MarkdownDescription: "Source file the test lives in. Empty when the test runner did " +
								"not report one.",
							Computed: true,
						},
						"source": schema.StringAttribute{
							MarkdownDescription: "Which test result collection produced the record. Empty when " +
								"not reported.",
							Computed: true,
						},
						"times_flaked": schema.Int64Attribute{
							MarkdownDescription: "How many times this test has flaked.",
							Computed:            true,
						},
						"time_wasted": schema.Int64Attribute{
							MarkdownDescription: "Seconds spent on runs that flaked, or `null` when CircleCI has " +
								"not computed it.",
							Computed: true,
						},
						"job_name": schema.StringAttribute{
							MarkdownDescription: "Job the test ran in.",
							Computed:            true,
						},
						"job_number": schema.Int64Attribute{
							MarkdownDescription: "Number of the job the flake was last seen in.",
							Computed:            true,
						},
						"pipeline_number": schema.Int64Attribute{
							MarkdownDescription: "Number of the pipeline the flake was last seen in.",
							Computed:            true,
						},
						"workflow_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the workflow the flake was last " +
								"seen in.",
							Computed: true,
						},
						"workflow_name": schema.StringAttribute{
							MarkdownDescription: "Name of that workflow.",
							Computed:            true,
						},
						"workflow_created_at": schema.StringAttribute{
							MarkdownDescription: "When that workflow started (RFC 3339, with millisecond " +
								"precision).",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

// Read fetches the project's flaky tests.
func (d *insightsFlakyTestsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		return
	}

	var state insightsFlakyTestsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectSlug := state.ProjectSlug.ValueString()

	flaky, err := d.client.Insights().GetFlakyTests(ctx, projectSlug)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI Insights flaky tests for "+projectSlug,
			insightsDetail(err),
		)

		return
	}

	state.TotalFlakyTests = types.Int64Value(flaky.TotalFlakyTests)

	// An empty, non-null list keeps `for_each` and `length()` working for a project
	// with no flakes, which is the state every project should be in.
	state.FlakyTests = make([]insightsFlakyTestItemModel, 0, len(flaky.FlakyTests))
	for _, test := range flaky.FlakyTests {
		state.FlakyTests = append(state.FlakyTests, insightsFlakyTestItemModel{
			TestName:    types.StringValue(test.TestName),
			Classname:   types.StringValue(test.Classname),
			File:        types.StringValue(test.File),
			Source:      types.StringValue(test.Source),
			TimesFlaked: types.Int64Value(test.TimesFlaked),
			// Absent rather than zero: a flake CircleCI has not costed is not a flake
			// that cost nothing.
			TimeWasted:        types.Int64PointerValue(test.TimeWasted),
			JobName:           types.StringValue(test.JobName),
			JobNumber:         types.Int64Value(test.JobNumber),
			PipelineNumber:    types.Int64Value(test.PipelineNumber),
			WorkflowID:        types.StringValue(test.WorkflowID),
			WorkflowName:      types.StringValue(test.WorkflowName),
			WorkflowCreatedAt: types.StringValue(test.WorkflowCreatedAt),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *insightsFlakyTestsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
