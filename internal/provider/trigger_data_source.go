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
	_ datasource.DataSource              = &TriggerDataSource{}
	_ datasource.DataSourceWithConfigure = &TriggerDataSource{}
)

// triggerDataSourceModel maps the output schema.
type triggerDataSourceModel struct {
	Id                                  types.String `tfsdk:"id"`
	ProjectId                           types.String `tfsdk:"project_id"`
	CreatedAt                           types.String `tfsdk:"created_at"`
	CheckoutRef                         types.String `tfsdk:"checkout_ref"`
	EventName                           types.String `tfsdk:"event_name"`
	EventPreset                         types.String `tfsdk:"event_preset"`
	EventSourceProvider                 types.String `tfsdk:"event_source_provider"`
	EventSourceRepositoryName           types.String `tfsdk:"event_source_repository_name"`
	EventSourceRepositoryExternalId     types.String `tfsdk:"event_source_repository_external_id"`
	EventSourceWebHookUrl               types.String `tfsdk:"event_source_webhook_url"`
	EventSourceScheduleCronExpression   types.String `tfsdk:"event_source_schedule_cron_expression"`
	EventSourceScheduleAttributionActor types.String `tfsdk:"event_source_schedule_attribution_actor"`
	Disabled                            types.Bool   `tfsdk:"disabled"`
	Parameters                          types.Map    `tfsdk:"parameters"`
}

// NewTriggerDataSource is a helper function to simplify the provider implementation.
func NewTriggerDataSource() datasource.DataSource {
	return &TriggerDataSource{}
}

// TriggerDataSource is the data source implementation.
type TriggerDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *TriggerDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_trigger"
}

// Schema defines the schema for the data source.
func (d *TriggerDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches information about a CircleCI pipeline trigger.\n\n" +
			"!> **CircleCI Cloud only.** Triggers live under `/api/v2` but are served by the public API " +
			"service, which CircleCI Server does not route.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The ID of the trigger.",
				Required:            true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the project the trigger belongs to.",
				Required:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the trigger was created.",
				Computed:            true,
			},
			"checkout_ref": schema.StringAttribute{
				MarkdownDescription: "The ref to check out when running pipelines from this trigger.",
				Computed:            true,
			},
			"disabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the trigger is disabled.",
				Optional:            true,
			},
			"event_name": schema.StringAttribute{
				MarkdownDescription: "The event name for webhook or scheduled triggers.",
				Computed:            true,
			},
			"event_preset": schema.StringAttribute{
				MarkdownDescription: "The event preset for GitHub triggers.",
				Computed:            true,
			},
			"event_source_provider": schema.StringAttribute{
				MarkdownDescription: "The event source provider (e.g., `github_app`, `webhook`, `schedule`).",
				Computed:            true,
			},
			"event_source_repository_name": schema.StringAttribute{
				MarkdownDescription: "The full name of the event source repository.",
				Computed:            true,
			},
			"event_source_repository_external_id": schema.StringAttribute{
				MarkdownDescription: "The external ID of the event source repository.",
				Computed:            true,
			},
			"event_source_webhook_url": schema.StringAttribute{
				MarkdownDescription: "The webhook URL for webhook-based triggers.",
				Computed:            true,
			},
			"event_source_schedule_cron_expression": schema.StringAttribute{
				MarkdownDescription: "The cron expression for scheduled triggers.",
				Computed:            true,
			},
			"event_source_schedule_attribution_actor": schema.StringAttribute{
				MarkdownDescription: "The actor attributed to scheduled pipeline runs.",
				Computed:            true,
			},
			"parameters": schema.MapAttribute{
				MarkdownDescription: "The default pipeline parameters for this trigger.",
				ElementType:         types.StringType,
				Computed:            true,
			},
		},
	}
}

// Read refreshes the Terraform state with the latest data.
func (d *TriggerDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	// Gate before the request: on CircleCI Server the route is not present at
	// all and the HTTP 404 would read as "no such trigger".
	if !requireCloud(d.client, triggerTypeName, &resp.Diagnostics) {
		return
	}

	var triggerState triggerDataSourceModel
	diags := req.Config.Get(ctx, &triggerState)
	if diags != nil {
		resp.Diagnostics.Append(diags...)
		return
	}

	if triggerState.Id.IsNull() {
		resp.Diagnostics.AddError(
			"Missing trigger id",
			"Missing trigger id",
		)
		return
	}

	if triggerState.ProjectId.IsNull() {
		resp.Diagnostics.AddError(
			"Missing trigger project_id",
			"Missing trigger project_id",
		)
		return
	}

	retrievedTrigger, err := d.client.GetTrigger(ctx, triggerState.ProjectId.ValueString(), triggerState.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Read CircleCI Trigger with id "+triggerState.Id.ValueString(),
			circleci.Detail(err),
		)
		return
	}

	// Map parameters from API response
	paramAttrs := make(map[string]attr.Value, len(retrievedTrigger.ParameterStrings()))
	for k, v := range retrievedTrigger.ParameterStrings() {
		paramAttrs[k] = types.StringValue(v)
	}
	parameters, paramDiags := types.MapValue(types.StringType, paramAttrs)
	resp.Diagnostics.Append(paramDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Map response body to model
	triggerState = triggerDataSourceModel{
		Id:                                  types.StringValue(retrievedTrigger.ID),
		ProjectId:                           triggerState.ProjectId,
		CreatedAt:                           types.StringValue(retrievedTrigger.CreatedAt),
		CheckoutRef:                         types.StringValue(retrievedTrigger.CheckoutRef),
		Disabled:                            types.BoolValue(retrievedTrigger.IsDisabled()),
		EventName:                           types.StringValue(retrievedTrigger.EventName),
		EventPreset:                         types.StringValue(retrievedTrigger.EventPreset),
		EventSourceProvider:                 types.StringValue(retrievedTrigger.EventSource.Provider),
		EventSourceRepositoryName:           types.StringValue(retrievedTrigger.EventSource.Repo.FullName),
		EventSourceRepositoryExternalId:     types.StringValue(retrievedTrigger.EventSource.Repo.ExternalID),
		EventSourceWebHookUrl:               types.StringValue(retrievedTrigger.EventSource.Webhook.URL),
		EventSourceScheduleCronExpression:   types.StringValue(retrievedTrigger.EventSource.Schedule.CronExpression),
		EventSourceScheduleAttributionActor: types.StringValue(retrievedTrigger.EventSource.Schedule.AttributionActor.ID),
		Parameters:                          parameters,
	}

	// Set state
	diags = resp.State.Set(ctx, &triggerState)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Configure adds the provider configured client to the data source.
func (d *TriggerDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
