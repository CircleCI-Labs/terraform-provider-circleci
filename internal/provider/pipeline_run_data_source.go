// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource                     = &pipelineRunDataSource{}
	_ datasource.DataSourceWithConfigure        = &pipelineRunDataSource{}
	_ datasource.DataSourceWithConfigValidators = &pipelineRunDataSource{}
)

// pipelineRunDataSourceModel maps the data source schema.
//
// This is deliberately named circleci_pipeline_run, not circleci_pipeline:
// that name is already taken by a pipeline *definition* (see
// pipeline_data_source.go). The v3 API calls this concept a "run" for the
// same reason; see DESIGN.md.
type pipelineRunDataSourceModel struct {
	Id                types.String `tfsdk:"id"`
	ProjectSlug       types.String `tfsdk:"project_slug"`
	Number            types.Int64  `tfsdk:"number"`
	CreatedAt         types.String `tfsdk:"created_at"`
	UpdatedAt         types.String `tfsdk:"updated_at"`
	State             types.String `tfsdk:"state"`
	Errors            types.List   `tfsdk:"errors"`
	Warnings          types.List   `tfsdk:"warnings"`
	TriggerType       types.String `tfsdk:"trigger_type"`
	TriggerReceivedAt types.String `tfsdk:"trigger_received_at"`
	TriggerActorLogin types.String `tfsdk:"trigger_actor_login"`
	VcsProviderName   types.String `tfsdk:"vcs_provider_name"`
	VcsRevision       types.String `tfsdk:"vcs_revision"`
	VcsBranch         types.String `tfsdk:"vcs_branch"`
	VcsTag            types.String `tfsdk:"vcs_tag"`
}

// pipelineRunMessageModel is one element of the errors or warnings list.
type pipelineRunMessageModel struct {
	Type    types.String `tfsdk:"type"`
	Message types.String `tfsdk:"message"`
}

// pipelineRunMessageAttrTypes describes pipelineRunMessageModel to the
// framework when building the errors and warnings list values.
var pipelineRunMessageAttrTypes = map[string]attr.Type{
	"type":    types.StringType,
	"message": types.StringType,
}

// NewPipelineRunDataSource is a helper function to simplify the provider
// implementation.
func NewPipelineRunDataSource() datasource.DataSource {
	return &pipelineRunDataSource{}
}

// pipelineRunDataSource fetches one CircleCI pipeline run.
type pipelineRunDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *pipelineRunDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pipeline_run"
}

// Schema defines the schema for the data source.
func (d *pipelineRunDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	messageAttributes := map[string]schema.Attribute{
		"type": schema.StringAttribute{
			MarkdownDescription: "Category of the error or warning, e.g. `config` or `trigger-rule`.",
			Computed:            true,
		},
		"message": schema.StringAttribute{
			MarkdownDescription: "Human-readable message.",
			Computed:            true,
		},
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches one CircleCI pipeline run, by id or by project slug and pipeline " +
			"number.\n\n" +
			"~> **`circleci_pipeline_run` is a point-in-time read of mutable runtime state.** `state`, " +
			"`errors` and `warnings` change as the run is processed, so every `terraform plan` can see a " +
			"different value until the run reaches a terminal state. Use this data source for inspection " +
			"and in `check` blocks; using it to derive a resource attribute will cause a perpetual diff.\n\n" +
			"Available on both CircleCI Cloud and CircleCI Server: this route is implemented directly in " +
			"the same API application on both.\n\n" +
			"-> **Naming.** [`circleci_pipeline`](pipeline) is an unrelated data source: it reads a " +
			"pipeline *definition* (the declared pairing of a config source and a checkout source), not a " +
			"triggered run. This data source is named `_run` to keep the two apart, matching the v3 API's " +
			"own renaming of \"pipeline\" to \"run\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the pipeline run. Set either this, or " +
					"both `project_slug` and `number`.",
				Optional: true,
				Computed: true,
			},
			"project_slug": schema.StringAttribute{
				MarkdownDescription: "The project's slug, e.g. `gh/CircleCI-Public/api-preview-docs`, or " +
					"`circleci/<org-id>/<project-id>` for a GitLab, GitHub App or GitHub Server project. " +
					"Set together with `number`; leave both unset when `id` is set.",
				Optional: true,
				Computed: true,
			},
			"number": schema.Int64Attribute{
				MarkdownDescription: "The pipeline run's number, unique within its project. Set together " +
					"with `project_slug`.",
				Optional: true,
				Computed: true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the run was created, as an RFC 3339 timestamp.",
				Computed:            true,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "When the run was last updated, as an RFC 3339 timestamp. Null when " +
					"the run has not been updated since creation.",
				Computed: true,
			},
			"state": schema.StringAttribute{
				MarkdownDescription: "Current state of the run: one of `created`, `errored`, `setup-pending`, " +
					"`setup` or `pending`.",
				Computed: true,
			},
			"errors": schema.ListNestedAttribute{
				MarkdownDescription: "Errors encountered while processing the run.",
				Computed:            true,
				NestedObject:        schema.NestedAttributeObject{Attributes: messageAttributes},
			},
			"warnings": schema.ListNestedAttribute{
				MarkdownDescription: "Warnings encountered while processing the run.",
				Computed:            true,
				NestedObject:        schema.NestedAttributeObject{Attributes: messageAttributes},
			},
			"trigger_type": schema.StringAttribute{
				MarkdownDescription: "How the run was triggered: one of `api`, `explicit`, `webhook` or " +
					"`scheduled_pipeline`.",
				Computed: true,
			},
			"trigger_received_at": schema.StringAttribute{
				MarkdownDescription: "When the trigger was received, as an RFC 3339 timestamp.",
				Computed:            true,
			},
			"trigger_actor_login": schema.StringAttribute{
				MarkdownDescription: "VCS login of the user or integration that triggered the run.",
				Computed:            true,
			},
			"vcs_provider_name": schema.StringAttribute{
				MarkdownDescription: "Name of the VCS provider, e.g. `GitHub`. Null for a run triggered " +
					"directly through the API rather than by a VCS event.",
				Computed: true,
			},
			"vcs_revision": schema.StringAttribute{
				MarkdownDescription: "The VCS revision (commit SHA) the run used. Null when `vcs_provider_name` is null.",
				Computed:            true,
			},
			"vcs_branch": schema.StringAttribute{
				MarkdownDescription: "The VCS branch the run used. Null when the run used a tag instead, " +
					"or when `vcs_provider_name` is null.",
				Computed: true,
			},
			"vcs_tag": schema.StringAttribute{
				MarkdownDescription: "The VCS tag the run used. Null when the run used a branch instead, " +
					"or when `vcs_provider_name` is null.",
				Computed: true,
			},
		},
	}
}

// ConfigValidators requires exactly one lookup key, and project_slug/number
// together.
func (d *pipelineRunDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("id"),
			path.MatchRoot("project_slug"),
		),
		datasourcevalidator.RequiredTogether(
			path.MatchRoot("project_slug"),
			path.MatchRoot("number"),
		),
	}
}

// Configure adds the provider configured client to the data source.
func (d *pipelineRunDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// Read fetches the pipeline run and sets the data source state.
func (d *pipelineRunDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config pipelineRunDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var (
		run *circleci.PipelineRun
		err error
	)
	if id := config.Id.ValueString(); id != "" {
		run, err = d.client.PipelineRuns().Get(ctx, id)
	} else {
		run, err = d.client.PipelineRuns().GetByNumber(ctx, config.ProjectSlug.ValueString(), int(config.Number.ValueInt64()))
	}

	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI pipeline run",
			circleci.Detail(err),
		)

		return
	}

	state, diags := pipelineRunToModel(ctx, *run)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// pipelineRunToModel converts a circleci.PipelineRun into its Terraform model.
func pipelineRunToModel(ctx context.Context, run circleci.PipelineRun) (pipelineRunDataSourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	errs, errDiags := pipelineRunMessagesToList(ctx, run.Errors)
	diags.Append(errDiags...)
	warnings, warnDiags := pipelineRunWarningsToList(ctx, run.Warnings)
	diags.Append(warnDiags...)

	model := pipelineRunDataSourceModel{
		Id:                types.StringValue(run.ID),
		ProjectSlug:       types.StringValue(run.ProjectSlug),
		Number:            types.Int64Value(int64(run.Number)),
		CreatedAt:         types.StringValue(run.CreatedAt),
		UpdatedAt:         optionalString(run.UpdatedAt),
		State:             types.StringValue(run.State),
		Errors:            errs,
		Warnings:          warnings,
		TriggerType:       types.StringValue(run.Trigger.Type),
		TriggerReceivedAt: types.StringValue(run.Trigger.ReceivedAt),
		TriggerActorLogin: types.StringValue(run.Trigger.Actor.Login),
		VcsProviderName:   types.StringNull(),
		VcsRevision:       types.StringNull(),
		VcsBranch:         types.StringNull(),
		VcsTag:            types.StringNull(),
	}

	if run.VCS != nil {
		model.VcsProviderName = types.StringValue(run.VCS.ProviderName)
		model.VcsRevision = types.StringValue(run.VCS.Revision)
		model.VcsBranch = optionalString(run.VCS.Branch)
		model.VcsTag = optionalString(run.VCS.Tag)
	}

	return model, diags
}

// optionalString converts a Go string that the API omits when empty into a
// null Terraform value rather than an empty-string one, so a configuration
// can distinguish "not set" from "set to empty".
func optionalString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}

	return types.StringValue(s)
}

func pipelineRunMessagesToList(ctx context.Context, errs []circleci.PipelineRunError) (types.List, diag.Diagnostics) {
	values := make([]pipelineRunMessageModel, 0, len(errs))
	for _, e := range errs {
		values = append(values, pipelineRunMessageModel{Type: types.StringValue(e.Type), Message: types.StringValue(e.Message)})
	}

	return types.ListValueFrom(ctx, types.ObjectType{AttrTypes: pipelineRunMessageAttrTypes}, values)
}

func pipelineRunWarningsToList(ctx context.Context, warnings []circleci.PipelineRunWarning) (types.List, diag.Diagnostics) {
	values := make([]pipelineRunMessageModel, 0, len(warnings))
	for _, w := range warnings {
		values = append(values, pipelineRunMessageModel{Type: types.StringValue(w.Type), Message: types.StringValue(w.Message)})
	}

	return types.ListValueFrom(ctx, types.ObjectType{AttrTypes: pipelineRunMessageAttrTypes}, values)
}
