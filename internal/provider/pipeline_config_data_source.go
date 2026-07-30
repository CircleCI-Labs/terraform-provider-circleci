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
	_ datasource.DataSource              = &pipelineConfigDataSource{}
	_ datasource.DataSourceWithConfigure = &pipelineConfigDataSource{}
)

// pipelineConfigDataSourceModel maps the data source schema.
type pipelineConfigDataSourceModel struct {
	RunID               types.String `tfsdk:"run_id"`
	Source              types.String `tfsdk:"source"`
	Compiled            types.String `tfsdk:"compiled"`
	SetupConfig         types.String `tfsdk:"setup_config"`
	CompiledSetupConfig types.String `tfsdk:"compiled_setup_config"`
}

// NewPipelineRunConfigDataSource is a helper function to simplify the provider
// implementation.
func NewPipelineRunConfigDataSource() datasource.DataSource {
	return &pipelineConfigDataSource{}
}

// pipelineConfigDataSource fetches the resolved and original configuration
// for a pipeline run.
type pipelineConfigDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *pipelineConfigDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pipeline_run_config"
}

// Schema defines the schema for the data source.
func (d *pipelineConfigDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches the source and compiled configuration for a CircleCI pipeline " +
			"run. Genuinely useful for asserting what a run actually compiled to — orb expansion included " +
			"— rather than just the checked-in YAML.\n\n" +
			"~> **Point-in-time read.** A pipeline run's configuration does not change once compiled, but " +
			"the run itself might not exist yet or might still be compiling when this is read (`source` " +
			"and `compiled` are then empty). Use this data source for inspection and in `check` blocks; " +
			"using it to derive a resource attribute will cause a diff for any run whose configuration " +
			"was not yet available at the time of the read.\n\n" +
			"Available on both CircleCI Cloud and CircleCI Server: this route is implemented directly in " +
			"the same API application on both.",
		Attributes: map[string]schema.Attribute{
			"run_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the pipeline run, as returned by " +
					"[`circleci_pipeline_run`](pipeline_run).",
				Required: true,
			},
			"source": schema.StringAttribute{
				MarkdownDescription: "The source configuration, before any config compilation. Empty " +
					"when the run has no configuration (yet).",
				Computed: true,
			},
			"compiled": schema.StringAttribute{
				MarkdownDescription: "The compiled configuration, after orb expansion. May be empty if " +
					"there were errors processing the configuration.",
				Computed: true,
			},
			"setup_config": schema.StringAttribute{
				MarkdownDescription: "The setup configuration used for setup workflows. Null when the " +
					"run did not use setup workflows.",
				Computed: true,
			},
			"compiled_setup_config": schema.StringAttribute{
				MarkdownDescription: "The compiled setup configuration, after orb expansion. Null when " +
					"the run did not use setup workflows; may be empty if there were errors processing the " +
					"setup configuration.",
				Computed: true,
			},
		},
	}
}

// Read fetches the pipeline run's configuration and sets the data source state.
func (d *pipelineConfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config pipelineConfigDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	runID := config.RunID.ValueString()

	result, err := d.client.PipelineRuns().GetConfig(ctx, runID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read configuration for CircleCI pipeline run "+runID,
			circleci.Detail(err),
		)

		return
	}

	state := pipelineConfigDataSourceModel{
		RunID:               config.RunID,
		Source:              types.StringValue(result.Source),
		Compiled:            types.StringValue(result.Compiled),
		SetupConfig:         optionalString(result.SetupConfig),
		CompiledSetupConfig: optionalString(result.CompiledSetupConfig),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *pipelineConfigDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
