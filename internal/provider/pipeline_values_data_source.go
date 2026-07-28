// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &pipelineValuesDataSource{}
	_ datasource.DataSourceWithConfigure = &pipelineValuesDataSource{}
)

// pipelineValuesDataSourceModel maps the data source schema.
type pipelineValuesDataSourceModel struct {
	PipelineID types.String `tfsdk:"pipeline_id"`
	Values     types.Map    `tfsdk:"values"`
}

// NewPipelineValuesDataSource is a helper function to simplify the provider
// implementation.
func NewPipelineValuesDataSource() datasource.DataSource {
	return &pipelineValuesDataSource{}
}

// pipelineValuesDataSource reads the built-in pipeline.* values available to
// a pipeline run.
type pipelineValuesDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *pipelineValuesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pipeline_values"
}

// Schema defines the schema for the data source.
func (d *pipelineValuesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the built-in `pipeline.*` values available to a pipeline run: things " +
			"like `pipeline.id`, `pipeline.number`, `pipeline.project.git_url` and `pipeline.git.branch` — " +
			"the same values a config can interpolate as `<< pipeline.number >>`. See " +
			"[Pipeline Values](https://circleci.com/docs/reference/pipeline-variables/) for the full set.\n\n" +
			"This is deliberately narrow: it is bounded to one run's fixed set of built-in values, unlike " +
			"the insights run-reporting endpoints this provider omits, which return unbounded, " +
			"ever-growing row counts.\n\n" +
			"Available on both CircleCI Cloud and CircleCI Server: this route is implemented directly in " +
			"the same v2 API application on both, like `circleci_pipeline_run`.\n\n" +
			"-> **Naming.** `pipeline_id` refers to a pipeline *run* (what `circleci_pipeline_run` reads), " +
			"not a pipeline *definition* (what the unrelated `circleci_pipeline` data source reads). The " +
			"name matches the API route's own parameter, `/api/v2/pipeline/{pipeline_id}/values`.",
		Attributes: map[string]schema.Attribute{
			"pipeline_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the pipeline run, i.e. `circleci_pipeline_run`'s `id`.",
				Required:            true,
			},
			"values": schema.MapAttribute{
				MarkdownDescription: "The pipeline's built-in values, keyed by their dotted name (e.g. " +
					"`pipeline.number`). Every value is rendered as a string, since the API mixes strings " +
					"and numbers in the same response and a map attribute must have one element type; an " +
					"empty string here always means the run's underlying value was itself empty, not that " +
					"the key is absent.",
				ElementType: types.StringType,
				Computed:    true,
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *pipelineValuesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// Read fetches the pipeline values and sets the data source state.
func (d *pipelineValuesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config pipelineValuesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	pipelineID := config.PipelineID.ValueString()

	values, err := d.client.PipelineRuns().GetValues(ctx, pipelineID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI pipeline values for pipeline "+pipelineID,
			circleci.Detail(err),
		)

		return
	}

	valuesAttrs := make(map[string]attr.Value, len(values))
	for k, v := range values {
		valuesAttrs[k] = types.StringValue(v)
	}

	valuesMap, diags := types.MapValue(types.StringType, valuesAttrs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	config.Values = valuesMap

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
