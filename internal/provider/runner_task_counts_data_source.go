// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/CircleCI-Public/circleci-sdk-go/runner"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The runner API serves the two task counts from two endpoints
// (/api/v3/runner/tasks and /api/v3/runner/tasks/running), but they are exposed
// here as one data source rather than two. The counts are only meaningful
// together — an autoscaling module compares unclaimed against running work to
// decide whether to scale — and a single data source reads both in one refresh,
// so a configuration cannot end up with the two halves fetched at different
// times or filtered by different resource classes.

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &runnerTaskCountsDataSource{}
	_ datasource.DataSourceWithConfigure = &runnerTaskCountsDataSource{}
)

// runnerTaskCountsDataSourceModel maps the data source schema.
type runnerTaskCountsDataSourceModel struct {
	ResourceClass      types.String `tfsdk:"resource_class"`
	UnclaimedTaskCount types.Int64  `tfsdk:"unclaimed_task_count"`
	RunningTaskCount   types.Int64  `tfsdk:"running_task_count"`
}

// NewRunnerTaskCountsDataSource is a helper function to simplify the provider implementation.
func NewRunnerTaskCountsDataSource() datasource.DataSource {
	return &runnerTaskCountsDataSource{}
}

// runnerTaskCountsDataSource is the data source implementation.
type runnerTaskCountsDataSource struct {
	client *runner.Service
}

// Metadata returns the data source type name.
func (d *runnerTaskCountsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_runner_task_counts"
}

// Schema defines the schema for the data source.
func (d *runnerTaskCountsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the queued and in-flight task counts for a self-hosted runner resource " +
			"class. Intended for autoscaling modules, which compare the two to decide how many runner " +
			"agents to keep.\n\n" +
			"Available on CircleCI Cloud and CircleCI Server. On Server the runner API is served by " +
			"your own installation, so the provider's `runner_host` attribute must be set to your " +
			"Server hostname.\n\n" +
			"~> **These counts change constantly.** They are read at refresh time and will differ on " +
			"the next run, so a plan that depends on them is never stable. Feed them to something " +
			"that acts on the current value, not to attributes that would churn the plan.",
		Attributes: map[string]schema.Attribute{
			"resource_class": schema.StringAttribute{
				MarkdownDescription: "The resource class to count tasks for, in `namespace/name` " +
					"format (e.g. `myorg/myrunner`).",
				Required: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(runnerResourceClassPattern, "must be in the format 'namespace/name'"),
				},
			},
			"unclaimed_task_count": schema.Int64Attribute{
				MarkdownDescription: "Number of tasks queued for this resource class that no runner has " +
					"claimed yet. A sustained non-zero value means there is not enough runner capacity.",
				Computed: true,
			},
			"running_task_count": schema.Int64Attribute{
				MarkdownDescription: "Number of tasks currently running on runners in this resource class.",
				Computed:            true,
			},
		},
	}
}

// Read fetches both task counts from the API.
func (d *runnerTaskCountsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config runnerTaskCountsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resourceClass := config.ResourceClass.ValueString()

	// The SDK returns untyped errors, so a 404 for an unknown resource class
	// cannot be told apart from any other failure on either call.
	unclaimed, err := d.client.GetUnclaimedTaskCount(ctx, resourceClass)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading CircleCI runner unclaimed task count",
			fmt.Sprintf("Could not read the unclaimed task count for resource class %s: %s", resourceClass, err.Error()),
		)

		return
	}

	running, err := d.client.GetRunningTaskCount(ctx, resourceClass)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading CircleCI runner running task count",
			fmt.Sprintf("Could not read the running task count for resource class %s: %s", resourceClass, err.Error()),
		)

		return
	}

	config.UnclaimedTaskCount = types.Int64Value(int64(unclaimed.UnclaimedTaskCount))
	config.RunningTaskCount = types.Int64Value(int64(running.RunningRunnerTasks))

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// Configure adds the provider configured client to the data source.
func (d *runnerTaskCountsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*CircleCiClientWrapper)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *CircleCiClientWrapper, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)

		return
	}

	d.client = client.RunnerService
}
