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
	_ datasource.DataSource              = &triggersDataSource{}
	_ datasource.DataSourceWithConfigure = &triggersDataSource{}
)

// triggersTypeName is the Terraform type name, used in the Cloud-only
// diagnostic.
const triggersTypeName = "circleci_triggers"

// triggersDataSourceModel maps the data source schema.
type triggersDataSourceModel struct {
	ProjectID            types.String       `tfsdk:"project_id"`
	PipelineDefinitionID types.String       `tfsdk:"pipeline_definition_id"`
	Triggers             []triggerItemModel `tfsdk:"triggers"`
}

// triggerItemModel maps one trigger in the list. The attribute names match
// `circleci_trigger`, including the flattened event source, so a trigger read
// here and one read there describe themselves the same way.
type triggerItemModel struct {
	ID                                  types.String `tfsdk:"id"`
	Name                                types.String `tfsdk:"name"`
	CreatedAt                           types.String `tfsdk:"created_at"`
	CheckoutRef                         types.String `tfsdk:"checkout_ref"`
	ConfigRef                           types.String `tfsdk:"config_ref"`
	EventName                           types.String `tfsdk:"event_name"`
	EventPreset                         types.String `tfsdk:"event_preset"`
	EventSourceProvider                 types.String `tfsdk:"event_source_provider"`
	EventSourceRepositoryName           types.String `tfsdk:"event_source_repository_name"`
	EventSourceRepositoryExternalID     types.String `tfsdk:"event_source_repository_external_id"`
	EventSourceWebhookURL               types.String `tfsdk:"event_source_webhook_url"`
	EventSourceWebhookSender            types.String `tfsdk:"event_source_webhook_sender"`
	EventSourceScheduleCronExpression   types.String `tfsdk:"event_source_schedule_cron_expression"`
	EventSourceScheduleAttributionActor types.String `tfsdk:"event_source_schedule_attribution_actor"`
	Disabled                            types.Bool   `tfsdk:"disabled"`
	Parameters                          types.Map    `tfsdk:"parameters"`

	// The three attributes below carry the same values as
	// EventSourceRepositoryName, EventSourceRepositoryExternalID and
	// EventSourceWebhookURL, spelled the way `circleci_trigger` (the resource)
	// spells them. See trigger_event_source_spelling.go.
	EventSourceRepoFullName   types.String `tfsdk:"event_source_repo_full_name"`
	EventSourceRepoExternalID types.String `tfsdk:"event_source_repo_external_id"`
	EventSourceWebHookURL     types.String `tfsdk:"event_source_web_hook_url"`
}

// NewTriggersDataSource is a helper function to simplify the provider implementation.
func NewTriggersDataSource() datasource.DataSource {
	return &triggersDataSource{}
}

// triggersDataSource is the data source implementation.
type triggersDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *triggersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_triggers"
}

// Schema defines the schema for the data source.
func (d *triggersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	// See trigger_event_source_spelling.go: each pair is the deprecated
	// data-source spelling and the current, resource-matching one, both Computed
	// and always carrying the same value.
	eventSourceRepoFullNameDeprecatedAttr, eventSourceRepoFullNameCurrentAttr := eventSourceRepoFullNameAttributes()
	eventSourceRepoExternalIDDeprecatedAttr, eventSourceRepoExternalIDCurrentAttr := eventSourceRepoExternalIDAttributes()
	eventSourceWebhookURLDeprecatedAttr, eventSourceWebhookURLCurrentAttr := eventSourceWebhookURLAttributes()

	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every trigger attached to a CircleCI pipeline definition, including " +
			"triggers created outside Terraform.\n\n" +
			"!> **CircleCI Cloud only.** Triggers live under `/api/v2` but are served by the public API " +
			"service, which CircleCI Server does not route. A Server installation answers HTTP 404 — " +
			"indistinguishable from a pipeline definition that does not exist — so this data source rejects " +
			"`deployment = \"server\"` outright.\n\n" +
			"The endpoint returns every trigger in one response, so there is no pagination to follow.",
		Attributes: map[string]schema.Attribute{
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the project owning the pipeline definition.",
				Required:            true,
			},
			"pipeline_definition_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the pipeline **definition** whose " +
					"triggers are listed — not of a pipeline run, which is what `pipeline_id` means " +
					"elsewhere in this provider. Use " +
					"[`circleci_pipeline_definitions`](pipeline_definitions) to discover the " +
					"definitions on a project.",
				Required: true,
			},
			"triggers": schema.ListNestedAttribute{
				MarkdownDescription: "The triggers on the pipeline definition, in the order the API returns them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the trigger.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Name of the trigger. For webhook triggers this is the " +
								"expected sender, also reported as `event_source_webhook_sender`.",
							Computed: true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "Timestamp the trigger was created, as the API reported it.",
							Computed:            true,
						},
						"checkout_ref": schema.StringAttribute{
							MarkdownDescription: "Ref checked out when this trigger runs the pipeline.",
							Computed:            true,
						},
						"config_ref": schema.StringAttribute{
							MarkdownDescription: "Ref the pipeline configuration is read from when this " +
								"trigger fires.",
							Computed: true,
						},
						"event_name": schema.StringAttribute{
							MarkdownDescription: "Human-readable name of the event that fires the trigger.",
							Computed:            true,
						},
						"event_preset": schema.StringAttribute{
							MarkdownDescription: "Event preset the trigger's rules share, such as " +
								"`github_app.push`. Empty when the rules cover more than one event.",
							Computed: true,
						},
						"event_source_provider": schema.StringAttribute{
							MarkdownDescription: "Event source provider: `github_app`, `github_server`, " +
								"`bitbucket_dc`, `webhook` or `schedule`.",
							Computed: true,
						},
						"event_source_repository_name": eventSourceRepoFullNameDeprecatedAttr,
						"event_source_repo_full_name":  eventSourceRepoFullNameCurrentAttr,

						"event_source_repository_external_id": eventSourceRepoExternalIDDeprecatedAttr,
						"event_source_repo_external_id":       eventSourceRepoExternalIDCurrentAttr,

						"event_source_webhook_url":  eventSourceWebhookURLDeprecatedAttr,
						"event_source_web_hook_url": eventSourceWebhookURLCurrentAttr,
						"event_source_webhook_sender": schema.StringAttribute{
							MarkdownDescription: "Expected sender of a webhook trigger. Empty for other providers.",
							Computed:            true,
						},
						"event_source_schedule_cron_expression": schema.StringAttribute{
							MarkdownDescription: "Cron expression of a scheduled trigger. Empty for other providers.",
							Computed:            true,
						},
						"event_source_schedule_attribution_actor": schema.StringAttribute{
							MarkdownDescription: "Unique identifier of the actor a scheduled trigger's pipelines " +
								"are attributed to. Empty for other providers.",
							Computed: true,
						},
						"disabled": schema.BoolAttribute{
							MarkdownDescription: "Whether the trigger is disabled. The API omits the field for " +
								"an enabled trigger, which is reported here as `false`.",
							Computed: true,
						},
						"parameters": schema.MapAttribute{
							MarkdownDescription: "Default pipeline parameters the trigger supplies, rendered as " +
								"strings. Null when the trigger sets none; non-string JSON values are rendered " +
								"as they were written, and composite values as JSON.",
							ElementType: types.StringType,
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

// Read lists the pipeline definition's triggers.
func (d *triggersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	// Gate before the request: on CircleCI Server the route is not present at all
	// and the HTTP 404 would read as "no such pipeline definition".
	if !requireCloud(d.client, triggersTypeName, &resp.Diagnostics) {
		return
	}

	var state triggersDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := state.ProjectID.ValueString()
	pipelineDefinitionID := state.PipelineDefinitionID.ValueString()

	triggers, err := d.client.ListTriggers(ctx, projectID, pipelineDefinitionID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI triggers for pipeline definition "+pipelineDefinitionID,
			circleci.Detail(err),
		)

		return
	}

	// An empty, non-null list keeps `for_each` and `length()` working against a
	// pipeline definition that has no triggers yet.
	state.Triggers = make([]triggerItemModel, 0, len(triggers))
	for _, trigger := range triggers {
		// A trigger with no parameters reports null rather than an empty map, so
		// that "sets none" and "sets an empty one" stay distinguishable.
		parameters := types.MapNull(types.StringType)
		if rendered := trigger.ParameterStrings(); rendered != nil {
			value, diags := types.MapValueFrom(ctx, types.StringType, rendered)
			resp.Diagnostics.Append(diags...)
			if resp.Diagnostics.HasError() {
				return
			}

			parameters = value
		}

		state.Triggers = append(state.Triggers, triggerItemModel{
			ID:                                  types.StringValue(trigger.ID),
			Name:                                types.StringValue(trigger.Name),
			CreatedAt:                           types.StringValue(trigger.CreatedAt),
			CheckoutRef:                         types.StringValue(trigger.CheckoutRef),
			ConfigRef:                           types.StringValue(trigger.ConfigRef),
			EventName:                           types.StringValue(trigger.EventName),
			EventPreset:                         types.StringValue(trigger.EventPreset),
			EventSourceProvider:                 types.StringValue(trigger.EventSource.Provider),
			EventSourceRepositoryName:           types.StringValue(trigger.EventSource.Repo.FullName),
			EventSourceRepositoryExternalID:     types.StringValue(trigger.EventSource.Repo.ExternalID),
			EventSourceWebhookURL:               types.StringValue(trigger.EventSource.Webhook.URL),
			EventSourceWebhookSender:            types.StringValue(trigger.EventSource.Webhook.Sender),
			EventSourceScheduleCronExpression:   types.StringValue(trigger.EventSource.Schedule.CronExpression),
			EventSourceScheduleAttributionActor: types.StringValue(trigger.EventSource.Schedule.AttributionActor.ID),
			// Same values as the three deprecated attributes above, spelled the way
			// `circleci_trigger` (the resource) spells them.
			EventSourceRepoFullName:   types.StringValue(trigger.EventSource.Repo.FullName),
			EventSourceRepoExternalID: types.StringValue(trigger.EventSource.Repo.ExternalID),
			EventSourceWebHookURL:     types.StringValue(trigger.EventSource.Webhook.URL),
			Disabled:                  types.BoolValue(trigger.IsDisabled()),
			Parameters:                parameters,
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *triggersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
