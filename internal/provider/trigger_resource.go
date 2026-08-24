// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// triggerTypeName is the Terraform type name, used in the Cloud-only
// diagnostic (see cloud_only.go).
const triggerTypeName = "circleci_trigger"

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                     = &triggerResource{}
	_ resource.ResourceWithConfigure        = &triggerResource{}
	_ resource.ResourceWithImportState      = &triggerResource{}
	_ resource.ResourceWithConfigValidators = &triggerResource{}
	_ resource.ResourceWithValidateConfig   = &triggerResource{}
	_ resource.ResourceWithModifyPlan       = &triggerResource{}
)

// triggerResourceModel maps the output schema.
type triggerResourceModel struct {
	Id                                  types.String `tfsdk:"id"`
	ProjectId                           types.String `tfsdk:"project_id"`
	PipelineId                          types.String `tfsdk:"pipeline_id"`
	PipelineDefinitionId                types.String `tfsdk:"pipeline_definition_id"`
	CreatedAt                           types.String `tfsdk:"created_at"`
	CheckoutRef                         types.String `tfsdk:"checkout_ref"`
	ConfigRef                           types.String `tfsdk:"config_ref"`
	EventSourceProvider                 types.String `tfsdk:"event_source_provider"`
	EventSourceRepoFullName             types.String `tfsdk:"event_source_repo_full_name"`
	EventSourceRepoExternalId           types.String `tfsdk:"event_source_repo_external_id"`
	EventSourceWebHookUrl               types.String `tfsdk:"event_source_web_hook_url"`
	EventSourceWebHookSender            types.String `tfsdk:"event_source_web_hook_sender"`
	EventSourceScheduleCronExpression   types.String `tfsdk:"event_source_schedule_cron_expression"`
	EventSourceScheduleAttributionActor types.String `tfsdk:"event_source_schedule_attribution_actor"`
	EventPreset                         types.String `tfsdk:"event_preset"`
	EventName                           types.String `tfsdk:"event_name"`
	Disabled                            types.Bool   `tfsdk:"disabled"`
	Parameters                          types.Map    `tfsdk:"parameters"`
}

// NewTriggerResource is a helper function to simplify the provider implementation.
func NewTriggerResource() resource.Resource {
	return &triggerResource{}
}

// triggerResource is the resource implementation.
type triggerResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *triggerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_trigger"
}

// Schema defines the schema for the resource.
func (r *triggerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI pipeline trigger. Triggers define when and how a pipeline runs — via GitHub events, webhooks, or a cron schedule.\n\n" +
			"!> **CircleCI Cloud only.** Triggers live under `/api/v2` but are served by the public API " +
			"service, which CircleCI Server does not route.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The unique identifier of the trigger.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					// This is the CRITICAL line. It suppresses the drift by telling TF
					// to ignore the 'unknown' value coming from the Read and use the prior state.
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the project this trigger belongs to. Changing this " +
					"value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					// A trigger does not exist outside the project it was created under —
					// both UpdateTrigger and DeleteTrigger address it as
					// /projects/{project_id}/triggers/{trigger_id} — so an in-place "update"
					// of project_id would send its PATCH to the new project carrying the old
					// (and, there, nonexistent) trigger id. Same defect and same fix as
					// circleci_pipeline's project_id; see that schema's comment and
					// DESIGN.md's characterization test notes.
					stringplanmodifier.RequiresReplace(),
				},
			},
			// See pipeline_definition_id_deprecation.go: this pair takes a pipeline
			// *definition* id under two names while `pipeline_id` is retired.
			"pipeline_id":            deprecatedTriggerPipelineIDAttribute(),
			"pipeline_definition_id": triggerPipelineDefinitionIDAttribute(),
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the trigger was created.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"checkout_ref": schema.StringAttribute{
				MarkdownDescription: "The ref to use when checking out code for pipeline runs created from this trigger. Always required when `event_source_provider` is `webhook` or `schedule`. When `event_source_provider` is `github_app` or `github_server`, only expected if the event source repository differs from the checkout source repository of the associated pipeline definition. Otherwise, must be omitted.",
				Optional:            true,
			},
			"config_ref": schema.StringAttribute{
				MarkdownDescription: "The ref to use when fetching configuration for pipeline runs created from this trigger. Always required when `event_source_provider` is `webhook` or `schedule`. When `event_source_provider` is `github_app` or `github_server`, only expected if the event source repository differs from the config source repository of the associated pipeline definition. Otherwise, must be omitted.",
				Optional:            true,
			},
			"event_source_provider": schema.StringAttribute{
				MarkdownDescription: "The event source provider: " +
					markdownValueList(circleci.TriggerEventSourceProviders()) + ".\n\n" +
					"~> The required attributes differ per provider, because this one endpoint covers " +
					"several contracts. `event_name` is required for `webhook` and `schedule` only. " +
					"`checkout_ref` and `config_ref` are required for `webhook` and `schedule`, because " +
					"neither carries an event ref to fall back to. " +
					"`event_source_repo_external_id` is required for `github_app`, `github_server` and " +
					"`github_oauth`, and must be the repository's numeric ID. " +
					"`event_preset` is required for `github_oauth` and accepts only `all-pushes` or " +
					"`only-build-prs` there, is optional for `github_app` and `github_server`, and must " +
					"be omitted for `webhook` and `schedule`. `disabled` is unsupported for " +
					"`github_oauth`, and `parameters` is supported only for `schedule`. Every one of " +
					"these is checked at plan time, so a wrong combination fails before anything is " +
					"created.\n\n" +
					"For `github_app` and `github_server` there is one rule this provider cannot check " +
					"before applying: `checkout_ref` and `config_ref` are required when the event source " +
					"repository differs from the pipeline definition's corresponding repository, and " +
					"**rejected** when it is the same one. Deciding that needs the definition's own " +
					"repositories, so it surfaces as an API error rather than a plan-time diagnostic.\n\n" +
					"GitLab pipelines cannot be given triggers through this API. Bitbucket Data Center " +
					"triggers can be read but not created here: `bitbucket_dc` is absent from the " +
					"documented set of accepted values on create.\n\n" +
					"Changing this value forces a new resource to be created: " +
					"UpdateTriggerEventSourceInput (see internal/circleci/trigger.go) carries no repo " +
					"field at all, matching the update API's own request shape exactly, so a " +
					"trigger's provider — and the repository-vs-webhook-vs-schedule shape that " +
					"comes with it — is immutable after creation, the same way " +
					"circleci_pipeline_definition's config_source_provider is.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(circleci.TriggerEventSourceProviders()...),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"event_source_repo_full_name": schema.StringAttribute{
				MarkdownDescription: "The full name of the event source repository.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"event_source_repo_external_id": schema.StringAttribute{
				MarkdownDescription: "The external ID of the event source repository. Required when " +
					"`event_source_provider` is `github_app` or `github_server`. This is the GitHub " +
					"repository numeric ID. Changing this value forces a new resource to be created: " +
					"UpdateTriggerEventSourceInput has no repo field, so a trigger's event source " +
					"repository is immutable after creation (see event_source_provider above).",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"event_source_web_hook_url": schema.StringAttribute{
				MarkdownDescription: "The webhook URL for webhook-based triggers, including the secret that " +
					"authenticates an inbound POST as a query parameter.\n\n" +
					"~> **Only the create response carries the real secret; every other read redacts it.** `GET " +
					"/projects/{project_id}/triggers/{trigger_id}` — the same route this resource's Read " +
					"uses on every refresh and on import — answers with the literal string `**REDACTED**` in " +
					"place of the secret, and so does the `PATCH` update route. Probed 2026-08-21 with the " +
					"very token that had just created the trigger: the create response carried the signed URL " +
					"and the immediately following read carried `**REDACTED**`, so this is not merely a " +
					"question of the calling token being insufficiently privileged.\n\n" +
					"Read and Update both recognize that placeholder and leave whatever is already in state " +
					"alone when they see it, rather than overwriting a working URL with an unusable one — so " +
					"the value captured at create time survives ordinary refreshes and updates. The one place " +
					"it is genuinely lost is **import**: a fresh import has no prior state to preserve, so an " +
					"imported webhook trigger's `event_source_web_hook_url` reads null and stays null. There " +
					"is no write-only counterpart to recover from this: unlike a practitioner-supplied secret, " +
					"this URL is minted by CircleCI, not configured, so there is nothing to re-supply — " +
					"replacing the trigger, which mints a new one, is the only way to populate it after an " +
					"import.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"event_source_web_hook_sender": schema.StringAttribute{
				MarkdownDescription: "The webhook sender identifier. Required when `event_source_provider` is `webhook`.",
				Optional:            true,
			},
			"event_source_schedule_cron_expression": schema.StringAttribute{
				MarkdownDescription: "Cron expression for the schedule event source. Required when event_source_provider is schedule.",
				Optional:            true,
				Validators:          []validator.String{CronExpressionValidator()},
			},
			"event_source_schedule_attribution_actor": schema.StringAttribute{
				MarkdownDescription: "Attribution actor for the schedule event source. Required when event_source_provider is schedule. Must be \"system\" or \"current\".\n\n" +
					"~> **Does not round-trip through import.** A create or update sends the bare alias " +
					"(`system` or `current`); a read reports the actor as an object carrying its resolved " +
					"id instead, with no alias anywhere in the response. Import therefore populates this " +
					"attribute with a literal actor id, not an alias — and `terraform plan " +
					"-generate-config-out` writes that literal id into the generated configuration. This " +
					"provider only accepts `system` or `current` here (matching what the API accepts on " +
					"write), so applying generated config verbatim fails plan-time validation with a clear " +
					"error rather than a confusing API rejection; replace the generated id with `system` or " +
					"`current` by hand. The first apply after that correction is a same-value PATCH — the " +
					"alias re-resolves to the same actor — after which the plan is clean.",
				Optional:   true,
				Computed:   true,
				Validators: []validator.String{stringvalidator.OneOf("system", "current")},
			},
			"event_preset": schema.StringAttribute{
				MarkdownDescription: "The event preset: which GitHub events fire the trigger.\n\n" +
					"Optional for `github_app` and `github_server`, **required** for `github_oauth` — " +
					"where only `all-pushes` and `only-build-prs` are accepted — and must be omitted " +
					"for `webhook` and `schedule`.\n\nValid values: " +
					markdownValueList(circleci.TriggerEventPresets()) + ".",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.OneOf(circleci.TriggerEventPresets()...),
				},
			},
			"event_name": schema.StringAttribute{
				MarkdownDescription: "The event name. Required when `event_source_provider` is `webhook` or `schedule`.",
				Optional:            true,
			},
			"disabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the trigger is disabled. Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"parameters": schema.MapAttribute{
				MarkdownDescription: "Pipeline parameters to pass when running pipelines from this trigger. Only supported when `event_source_provider` is `schedule`.",
				Optional:            true,
				ElementType:         types.StringType,
				PlanModifiers: []planmodifier.Map{
					triggerParametersRequiresReplaceIfCleared{},
				},
			},
		},
	}
}

// ConfigValidators requires exactly one of the two pipeline definition id names.
func (r *triggerResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		pipelineDefinitionIDConfigValidator(),
	}
}

// ModifyPlan applies the Cloud-only gate and makes `pipeline_id` and
// `pipeline_definition_id` agree in the plan.
//
// The gate is the same one cloud_only.go applies to every other Cloud-only type; it is
// here because a resource has exactly one ModifyPlan and this one has a second job.
//
// Both id attributes are Optional+Computed, so the name the practitioner did not write
// is planned as its retained prior value. Changing the definition would otherwise plan
// one name as the new id and the other as the old one, and persist both — state naming
// two different definitions, which the next Read resolves to the stale one. See
// pipeline_definition_id_deprecation.go.
func (r *triggerResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client != nil && !req.Plan.Raw.IsNull() {
		requireCloud(r.client, triggerTypeName, &resp.Diagnostics)
	}

	// Ask before reading the configuration: a destroy plans a null config, and
	// Get-ing that into the model fails with a value-conversion error.
	if !pipelineDefinitionIDPlanNeedsReconcile(req) {
		return
	}

	var config triggerResourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	reconcilePipelineDefinitionIDPlan(ctx, resp, config.PipelineId, config.PipelineDefinitionId)
}

// Create creates the resource and sets the initial Terraform state.
func (r *triggerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireCloud(r.client, triggerTypeName, &resp.Diagnostics) {
		return
	}

	// Retrieve values from plan
	var circleCiTerrformTriggerResource triggerResourceModel
	diags := req.Plan.Get(ctx, &circleCiTerrformTriggerResource)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// There is no validation here. Every rule about which attributes a given
	// event_source_provider requires or forbids is knowable from the configuration
	// alone, so all of it runs at plan time in ValidateConfig — see
	// trigger_validation.go for why apply-time was the wrong place.
	provider := circleCiTerrformTriggerResource.EventSourceProvider.ValueString()

	newEventSource := circleci.TriggerEventSourceInput{Provider: provider}
	// github_oauth needs a repo as much as github_app and github_server do — the
	// API resolves all three through the same arm of the same switch, and rejects a
	// create with no event_source.repo. This used to name only the two GitHub App
	// providers, so every github_oauth trigger failed with an opaque HTTP 400 "bad
	// request". The set lives in the client so the create body and the plan-time
	// requirement in trigger_validation.go cannot disagree about it.
	switch {
	case circleci.TriggerProviderNeedsRepo(provider):
		newEventSource.Repo = &circleci.RepoInput{
			ExternalID: circleCiTerrformTriggerResource.EventSourceRepoExternalId.ValueString(),
		}
	case provider == circleci.TriggerProviderWebhook:
		newEventSource.Webhook = &circleci.TriggerWebhookInput{
			Sender: circleCiTerrformTriggerResource.EventSourceWebHookSender.ValueString(),
		}
	case provider == circleci.TriggerProviderSchedule:
		newEventSource.Schedule = &circleci.TriggerScheduleInput{
			CronExpression:   circleCiTerrformTriggerResource.EventSourceScheduleCronExpression.ValueString(),
			AttributionActor: circleCiTerrformTriggerResource.EventSourceScheduleAttributionActor.ValueString(),
		}
	}

	parameters, diags := triggerParametersToMap(ctx, circleCiTerrformTriggerResource.Parameters)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// New Trigger
	newTrigger := circleci.CreateTriggerInput{
		EventName:   circleCiTerrformTriggerResource.EventName.ValueString(),
		CheckoutRef: circleCiTerrformTriggerResource.CheckoutRef.ValueString(),
		ConfigRef:   circleCiTerrformTriggerResource.ConfigRef.ValueString(),
		EventSource: newEventSource,
		EventPreset: circleCiTerrformTriggerResource.EventPreset.ValueString(),
		Disabled:    triggerDisabledInput(provider, circleCiTerrformTriggerResource.Disabled),
		Parameters:  parameters,
	}

	// Whichever of the two names the configuration used; see
	// pipeline_definition_id_deprecation.go.
	pipelineDefinitionID := effectivePipelineDefinitionID(
		circleCiTerrformTriggerResource.PipelineId,
		circleCiTerrformTriggerResource.PipelineDefinitionId,
	)

	// Create new Trigger
	newReturnedTrigger, err := r.client.CreateTrigger(
		ctx,
		circleCiTerrformTriggerResource.ProjectId.ValueString(),
		pipelineDefinitionID,
		newTrigger,
	)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI trigger",
			circleci.Detail(err),
		)
		return
	}

	// Map response body to schema and populate Computed attribute values
	circleCiTerrformTriggerResource.Id = types.StringValue(newReturnedTrigger.ID)
	// Both names, so neither is left unknown in state and switching between them is
	// not a change. The API never echoes the definition back, so this is the value we
	// created against.
	setPipelineDefinitionIDs(
		&circleCiTerrformTriggerResource.PipelineId,
		&circleCiTerrformTriggerResource.PipelineDefinitionId,
		pipelineDefinitionID,
	)
	if newReturnedTrigger.CheckoutRef != "" {
		circleCiTerrformTriggerResource.CheckoutRef = types.StringValue(newReturnedTrigger.CheckoutRef)
	}
	if newReturnedTrigger.ConfigRef != "" {
		circleCiTerrformTriggerResource.ConfigRef = types.StringValue(newReturnedTrigger.ConfigRef)
	}
	circleCiTerrformTriggerResource.EventSourceProvider = types.StringValue(newReturnedTrigger.EventSource.Provider)
	if newReturnedTrigger.EventSource.Repo.FullName == "" {
		circleCiTerrformTriggerResource.EventSourceRepoFullName = types.StringNull()
	} else {
		circleCiTerrformTriggerResource.EventSourceRepoFullName = types.StringValue(newReturnedTrigger.EventSource.Repo.FullName)
	}

	if newReturnedTrigger.EventSource.Repo.ExternalID != "" {
		circleCiTerrformTriggerResource.EventSourceRepoExternalId = types.StringValue(newReturnedTrigger.EventSource.Repo.ExternalID)
	}
	circleCiTerrformTriggerResource.EventSourceWebHookUrl = types.StringValue(newReturnedTrigger.EventSource.Webhook.URL)
	if newReturnedTrigger.EventPreset != "" {
		circleCiTerrformTriggerResource.EventPreset = types.StringValue(newReturnedTrigger.EventPreset)
	}
	if circleCiTerrformTriggerResource.EventSourceProvider.ValueString() == circleci.TriggerProviderWebhook && circleCiTerrformTriggerResource.EventName.ValueString() != "" {
		circleCiTerrformTriggerResource.EventName = types.StringValue(newReturnedTrigger.EventName)
	}
	if newReturnedTrigger.EventSource.Schedule.CronExpression != "" {
		circleCiTerrformTriggerResource.EventSourceScheduleCronExpression = types.StringValue(newReturnedTrigger.EventSource.Schedule.CronExpression)
	} else {
		circleCiTerrformTriggerResource.EventSourceScheduleCronExpression = types.StringNull()
	}
	// For schedule triggers, preserve the user's input value. The API may transform aliases
	// like "system" to a UUID, which would cause perpetual drift if stored in state.
	if circleCiTerrformTriggerResource.EventSourceProvider.ValueString() != circleci.TriggerProviderSchedule {
		circleCiTerrformTriggerResource.EventSourceScheduleAttributionActor = types.StringNull()
	}

	// No early return on these diagnostics, and none on anything else below
	// either. Past this point the API has already created a trigger, so every
	// path has to reach the resp.State.Set at the end of this function: a
	// diagnostic that skipped it would leave a live trigger with no record in
	// Terraform state, and the next apply would create a second one. That is
	// issue #6, and it is why this tail has exactly one exit.
	parametersState, paramDiags := triggerParametersFromAPI(newReturnedTrigger.ParameterStrings())
	resp.Diagnostics.Append(paramDiags...)
	circleCiTerrformTriggerResource.Parameters = parametersState

	circleCiTerrformTriggerResource.Disabled = types.BoolValue(newReturnedTrigger.IsDisabled())

	// created_at has to be read back: the create response does not carry it.
	// Probed 2026-08-21 against circleci.com —
	//
	//	POST .../pipeline-definitions/{d}/triggers -> 200, body keys
	//	  id name event_name description checkout_ref config_ref event_source
	//	  disabled (+ parameters, when the request sent any). No created_at.
	//	GET  .../projects/{p}/triggers/{id}        -> same keys PLUS
	//	  "created_at":"2026-08-21T15:28:58.954521Z"
	//
	// — and recorded in CreateTrigger's doc comment. A comment here used to
	// claim the opposite ("the create response is a full Trigger, the same
	// shape GetTrigger returns, so CreatedAt is already populated") and dropped
	// this read on that basis. The claim came from the OpenAPI document, not
	// from the API: created_at stayed "" in state, which fails
	// ImportStateVerify and, worse for a practitioner, shows a permanent diff
	// on created_at at every refresh after apply.
	//
	// The read was dropped for a real reason, though — see the comment above on
	// the single exit. Both defects are real, so the read is back but it may
	// only ADD created_at; it may not gate writing state.
	//
	// Only CreatedAt is taken from it, deliberately. The read shape is not a
	// superset of the create shape: GET answers with event_source.webhook.url as
	// the literal "**REDACTED**" (same probe, using the very token that had just
	// created the trigger), so re-mapping the whole response — which is what the
	// old read-back did — would trade the real signed webhook URL for a
	// placeholder in the state this apply writes. Read clobbers it on the next
	// refresh regardless, which is a separate and pre-existing hazard documented
	// on the event_source_web_hook_url attribute above; that is a reason to fix
	// Read, not a reason for Create to throw the value away too.
	circleCiTerrformTriggerResource.CreatedAt = types.StringValue("")

	readBack, err := r.client.GetTrigger(
		ctx,
		circleCiTerrformTriggerResource.ProjectId.ValueString(),
		newReturnedTrigger.ID,
	)
	if err != nil {
		// A warning, not an error, and that choice is the whole point of the fix.
		// Terraform marks an object tainted when an apply reports an error yet
		// returns a non-null state, so the next apply would destroy and recreate
		// a trigger that is in fact perfectly good. Everything except created_at
		// is already correct in state, and created_at repopulates by itself on
		// the next refresh or plan (Read sets it from this same route) with no
		// practitioner action at all. An error here would trade a self-healing
		// problem for a destructive one.
		resp.Diagnostics.AddWarning(
			"Created CircleCI trigger, but could not read back created_at",
			"The trigger was created successfully and is recorded in state, so no "+
				"resource has been leaked. Reading it back to populate the computed "+
				"created_at attribute failed, because the create response does not "+
				"include it. created_at is empty in state for now and will be filled "+
				"in by the next refresh or plan; no action is required.\n\n"+
				circleci.Detail(err),
		)
	} else {
		circleCiTerrformTriggerResource.CreatedAt = types.StringValue(readBack.CreatedAt)
	}

	// Set state to fully populated data. Unconditional, and the only exit.
	resp.Diagnostics.Append(resp.State.Set(ctx, circleCiTerrformTriggerResource)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *triggerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireCloud(r.client, triggerTypeName, &resp.Diagnostics) {
		return
	}

	var triggerState triggerResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &triggerState)...)
	if resp.Diagnostics.HasError() {
		return // Stop immediately if error occurred during state retrieval
	}

	if triggerState.Id.IsNull() || triggerState.Id.IsUnknown() {
		// ID is lost, meaning the resource is unmanaged or deleted.
		resp.State.RemoveResource(ctx)
		return
	}

	if triggerState.ProjectId.IsNull() || triggerState.ProjectId.IsUnknown() {
		resp.State.RemoveResource(ctx)
		return
	}

	readTrigger, err := r.client.GetTrigger(ctx, triggerState.ProjectId.ValueString(), triggerState.Id.ValueString())
	// A trigger deleted outside Terraform must drop out of state so the next
	// plan recreates it, rather than becoming a permanent refresh error.
	// Absence is tested with circleci.IsNotFound rather than by
	// string-matching the error: matching "404" (or "not found") anywhere in
	// err.Error() also matches a 5xx whose body happens to mention it, which
	// silently removed live resources from state. This replaces the former
	// isApiNotFoundError helper, which did exactly that string match.
	if circleci.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error Reading Trigger", fmt.Sprintf("API error during read: %s", circleci.Detail(err)))
		return
	}

	// Map response body to model
	triggerState.Id = types.StringValue(readTrigger.ID)
	triggerState.CreatedAt = types.StringValue(readTrigger.CreatedAt)

	if readTrigger.CheckoutRef == "" {
		triggerState.CheckoutRef = types.StringNull()
	} else {
		triggerState.CheckoutRef = types.StringValue(readTrigger.CheckoutRef)
	}

	if readTrigger.ConfigRef == "" {
		triggerState.ConfigRef = types.StringNull()
	} else {
		triggerState.ConfigRef = types.StringValue(readTrigger.ConfigRef)
	}

	if readTrigger.EventSource.Provider == "" {
		triggerState.EventSourceProvider = types.StringNull()
	} else {
		triggerState.EventSourceProvider = types.StringValue(readTrigger.EventSource.Provider)
	}

	if readTrigger.EventSource.Repo.FullName == "" {
		triggerState.EventSourceRepoFullName = types.StringNull()
	} else {
		triggerState.EventSourceRepoFullName = types.StringValue(readTrigger.EventSource.Repo.FullName)
	}
	// The API redacts this on every read (see the attribute's own
	// MarkdownDescription and circleci.TriggerWebhookURLRedacted): GET answers
	// with a placeholder in place of the real signed URL, even to the token
	// that created it. Overwriting state with that placeholder unconditionally
	// would replace a working URL with an unusable one on the very first
	// refresh after every apply, so only a real value ever lands in state here;
	// seeing the placeholder leaves whatever is already in triggerState (the
	// prior state's value, or null on a fresh import, where there is nothing
	// prior to keep) untouched.
	if !readTrigger.EventSource.Webhook.URLIsRedacted() {
		triggerState.EventSourceWebHookUrl = types.StringValue(readTrigger.EventSource.Webhook.URL)
	}
	switch triggerState.EventSourceProvider.ValueString() {
	case circleci.TriggerProviderWebhook:
		triggerState.EventSourceWebHookSender = types.StringValue(readTrigger.EventSource.Webhook.Sender)
	case circleci.TriggerProviderGitHubApp, circleci.TriggerProviderGitHubServer, circleci.TriggerProviderSchedule:
	}

	if readTrigger.EventName == "" {
		triggerState.EventName = types.StringNull()
	} else {
		triggerState.EventName = types.StringValue(readTrigger.EventName)
	}

	if readTrigger.EventPreset == "" {
		triggerState.EventPreset = types.StringNull()
	} else {
		triggerState.EventPreset = types.StringValue(readTrigger.EventPreset)
	}

	if readTrigger.EventSource.Repo.ExternalID == "" {
		triggerState.EventSourceRepoExternalId = types.StringNull()
	} else {
		triggerState.EventSourceRepoExternalId = types.StringValue(readTrigger.EventSource.Repo.ExternalID)
	}

	if readTrigger.EventSource.Schedule.CronExpression == "" {
		triggerState.EventSourceScheduleCronExpression = types.StringNull()
	} else {
		triggerState.EventSourceScheduleCronExpression = types.StringValue(readTrigger.EventSource.Schedule.CronExpression)
	}

	// Preserve the prior state value for attribution_actor so aliases like "system" don't drift
	// to their resolved UUID. Only set from the API when the state has no value (e.g. import).
	if triggerState.EventSourceScheduleAttributionActor.IsNull() || triggerState.EventSourceScheduleAttributionActor.IsUnknown() {
		if readTrigger.EventSource.Schedule.AttributionActor.ID == "" {
			triggerState.EventSourceScheduleAttributionActor = types.StringNull()
		} else {
			triggerState.EventSourceScheduleAttributionActor = types.StringValue(readTrigger.EventSource.Schedule.AttributionActor.ID)
		}
	}

	triggerState.Disabled = types.BoolValue(readTrigger.IsDisabled())

	parametersState, paramDiags := triggerParametersFromAPI(readTrigger.ParameterStrings())
	resp.Diagnostics.Append(paramDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	triggerState.Parameters = parametersState

	// Set state
	resp.Diagnostics.Append(resp.State.Set(ctx, &triggerState)...)
	// Always check for errors after the final Set
	if resp.Diagnostics.HasError() {
		return
	}
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *triggerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !requireCloud(r.client, triggerTypeName, &resp.Diagnostics) {
		return
	}

	var state triggerResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// As in Create, there is no validation here: ValidateConfig runs on the
	// configuration of an update as well as a create, so an invalid change is
	// rejected at plan time. See trigger_validation.go.
	provider := state.EventSourceProvider.ValueString()

	parameters, diags := triggerParametersToMap(ctx, state.Parameters)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Prepare the new event source. Unlike create, there is no repo field at
	// all here: the update API's own request shape has none, so a trigger's
	// event source repository is immutable after creation regardless of
	// provider.
	newEventSource := circleci.UpdateTriggerEventSourceInput{Provider: provider}
	switch provider {
	case circleci.TriggerProviderWebhook:
		newEventSource.Webhook = &circleci.TriggerWebhookInput{
			Sender: state.EventSourceWebHookSender.ValueString(),
		}
	case circleci.TriggerProviderSchedule:
		newEventSource.Schedule = &circleci.TriggerScheduleInput{
			CronExpression:   state.EventSourceScheduleCronExpression.ValueString(),
			AttributionActor: state.EventSourceScheduleAttributionActor.ValueString(),
		}
	}

	// New Trigger
	updates := circleci.UpdateTriggerInput{
		EventName:   state.EventName.ValueString(),
		CheckoutRef: state.CheckoutRef.ValueString(),
		ConfigRef:   state.ConfigRef.ValueString(),
		EventSource: &newEventSource,
		EventPreset: state.EventPreset.ValueString(),
		Disabled:    triggerDisabledInput(provider, state.Disabled),
		Parameters:  parameters,
	}

	// update the trigger
	updatedTrigger, err := r.client.UpdateTrigger(ctx, state.ProjectId.ValueString(), state.Id.ValueString(), updates)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Update CircleCI trigger with id "+state.Id.ValueString()+" and project id "+state.ProjectId.ValueString(),
			circleci.Detail(err),
		)
		return
	}

	// update state
	state.Id = types.StringValue(updatedTrigger.ID)

	// Empty maps to null, the same way every other optional string in this function
	// is handled two blocks below. Without this, a trigger created without
	// checkout_ref/config_ref — the normal case for github_app and github_server,
	// where both are inherited — fails its next update with "Provider produced
	// inconsistent result after apply": the API answers "" and the plan expects null.
	if updatedTrigger.CheckoutRef == "" {
		state.CheckoutRef = types.StringNull()
	} else {
		state.CheckoutRef = types.StringValue(updatedTrigger.CheckoutRef)
	}

	if updatedTrigger.ConfigRef == "" {
		state.ConfigRef = types.StringNull()
	} else {
		state.ConfigRef = types.StringValue(updatedTrigger.ConfigRef)
	}
	state.EventSourceProvider = types.StringValue(updatedTrigger.EventSource.Provider)
	if updatedTrigger.EventSource.Repo.FullName == "" {
		state.EventSourceRepoFullName = types.StringNull()
	} else {
		state.EventSourceRepoFullName = types.StringValue(updatedTrigger.EventSource.Repo.FullName)
	}
	if updatedTrigger.EventSource.Repo.ExternalID == "" {
		state.EventSourceRepoExternalId = types.StringNull()
	} else {
		state.EventSourceRepoExternalId = types.StringValue(updatedTrigger.EventSource.Repo.ExternalID)
	}
	// PATCH redacts this the same way GET does — see Read's identical guard and
	// the attribute's own MarkdownDescription. `state` here already holds the
	// plan's value, which UseStateForUnknown carried forward from before this
	// update, so skipping the overwrite on a redacted response preserves the
	// real URL captured at create time rather than clobbering it with the
	// placeholder on every update.
	if !updatedTrigger.EventSource.Webhook.URLIsRedacted() {
		state.EventSourceWebHookUrl = types.StringValue(updatedTrigger.EventSource.Webhook.URL)
	}
	if updatedTrigger.EventSource.Schedule.CronExpression != "" {
		state.EventSourceScheduleCronExpression = types.StringValue(updatedTrigger.EventSource.Schedule.CronExpression)
	} else {
		state.EventSourceScheduleCronExpression = types.StringNull()
	}
	// Preserve plan value for schedule triggers; API may transform aliases like "system" → UUID.
	if state.EventSourceProvider.ValueString() != circleci.TriggerProviderSchedule {
		if updatedTrigger.EventSource.Schedule.AttributionActor.ID != "" {
			state.EventSourceScheduleAttributionActor = types.StringValue(updatedTrigger.EventSource.Schedule.AttributionActor.ID)
		} else {
			state.EventSourceScheduleAttributionActor = types.StringNull()
		}
	}
	if updatedTrigger.EventPreset == "" {
		state.EventPreset = types.StringNull()
	} else {
		state.EventPreset = types.StringValue(updatedTrigger.EventPreset)
	}
	if updatedTrigger.EventName == "" {
		state.EventName = types.StringNull()
	} else {
		state.EventName = types.StringValue(updatedTrigger.EventName)
	}
	state.CreatedAt = types.StringValue(updatedTrigger.CreatedAt)

	parametersState, paramDiags := triggerParametersFromAPI(updatedTrigger.ParameterStrings())
	resp.Diagnostics.Append(paramDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Parameters = parametersState

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *triggerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Retrieve values from state
	var state triggerResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A trigger already gone is the desired end state, so absence is not an
	// error.
	err := r.client.DeleteTrigger(ctx, state.ProjectId.ValueString(), state.Id.ValueString())
	if err != nil && !circleci.IsNotFound(err) {
		resp.Diagnostics.AddError(
			"Error Deleting CircleCI trigger",
			circleci.Detail(err),
		)
		return
	}
}

// Configure adds the provider configured client to the resource.
func (r *triggerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports a trigger from a "project_id/pipeline_definition_id/trigger_id"
// address.
//
// The pipeline definition id has to be part of the import address because **the API
// never returns it.** A trigger is *created* under a definition
// (POST .../pipeline-definitions/{pipeline_definition_id}/triggers) but *read* under
// the project (GET /projects/{project_id}/triggers/{trigger_id}), and the response
// body carries no reference back to the definition — see circleci.Trigger.
//
// The import id used to be just "project_id/trigger_id", which meant the definition id
// stayed null in state after every import. The next plan then saw it missing and there
// was nothing the practitioner could do short of editing state by hand. Asking for the
// third segment is the only way to make import produce usable state.
func (r *triggerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	const wantSegments = 3

	parts := strings.Split(req.ID, "/")

	if len(parts) != wantSegments || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		detail := fmt.Sprintf(
			"Expected import ID format: 'project_id/pipeline_definition_id/trigger_id'. Got: %s",
			req.ID,
		)
		if len(parts) == 2 {
			// The old two-segment form. Say so explicitly: it used to be accepted, and
			// silently producing state with no definition id is what this replaced.
			detail += "\n\nEarlier provider versions accepted 'project_id/trigger_id', but that " +
				"left the definition id unset because the API does not return the pipeline definition " +
				"a trigger belongs to. Add the pipeline definition id as the middle segment."
		}

		resp.Diagnostics.AddError("Invalid Import ID Format", detail)

		return
	}

	projectID, pipelineDefinitionID, triggerID := parts[0], parts[1], parts[2]

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), triggerID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), projectID)...)
	// Both names: they are Computed, so an import that filled only one would leave the
	// other null in state, and the first plan afterwards would show a diff on an
	// attribute the practitioner cannot correct.
	resp.Diagnostics.Append(
		resp.State.SetAttribute(ctx, path.Root("pipeline_id"), pipelineDefinitionID)...,
	)
	resp.Diagnostics.Append(
		resp.State.SetAttribute(ctx, path.Root("pipeline_definition_id"), pipelineDefinitionID)...,
	)
}

// triggerDisabledInput renders the `disabled` field of a create or update body.
//
// nil for github_oauth, which does not support the field at all — the attribute has
// a default, so the plan always carries a value for it whether or not the
// practitioner asked for one, and sending `"disabled": false` to an endpoint that
// rejects the key would make a github_oauth trigger impossible to create. Configuring
// it is refused at plan time (see trigger_validation.go); this is the other half of
// that, for the value the schema supplies by itself.
func triggerDisabledInput(provider string, disabled types.Bool) *bool {
	if provider == circleci.TriggerProviderGitHubOAuth {
		return nil
	}

	value := disabled.ValueBool()

	return &value
}

// markdownValueList renders values as a prose list of code spans: "`a`, `b` or
// `c`".
//
// It exists so that an attribute description and the validator beside it can be
// built from the same slice. A description that lists valid values by hand goes
// stale the first time the set changes, and the stale half is the one
// practitioners read.
func markdownValueList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "`" + value + "`"
	}

	return joinWithOr(quoted)
}

// joinWithOr renders values as "a", "a or b", or "a, b or c". Diagnostics use it
// unquoted; markdownValueList quotes first.
func joinWithOr(values []string) string {
	switch len(values) {
	case 0:
		return ""
	case 1:
		return values[0]
	default:
		return strings.Join(values[:len(values)-1], ", ") + " or " + values[len(values)-1]
	}
}

func triggerParametersToMap(ctx context.Context, parameters types.Map) (map[string]string, diag.Diagnostics) {
	if parameters.IsNull() || parameters.IsUnknown() {
		return nil, nil
	}
	out := make(map[string]string, len(parameters.Elements()))
	diags := parameters.ElementsAs(ctx, &out, false)
	return out, diags
}

// Forces replacement when parameters are cleared; PATCH can't unset them (the API's settings blob is only ever replaced wholesale when non-nil, so an empty map cannot clear a previously-set one).
type triggerParametersRequiresReplaceIfCleared struct{}

func (m triggerParametersRequiresReplaceIfCleared) Description(_ context.Context) string {
	return "Forces resource replacement when parameters transition from non-empty to null/empty."
}

func (m triggerParametersRequiresReplaceIfCleared) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m triggerParametersRequiresReplaceIfCleared) PlanModifyMap(_ context.Context, req planmodifier.MapRequest, resp *planmodifier.MapResponse) {
	if req.StateValue.IsNull() || req.PlanValue.IsUnknown() {
		return
	}
	hadValue := len(req.StateValue.Elements()) > 0
	willBeEmpty := req.PlanValue.IsNull() || len(req.PlanValue.Elements()) == 0
	if hadValue && willBeEmpty {
		resp.RequiresReplace = true
	}
}

func triggerParametersFromAPI(parameters map[string]string) (types.Map, diag.Diagnostics) {
	if len(parameters) == 0 {
		return types.MapNull(types.StringType), nil
	}
	elements := make(map[string]attr.Value, len(parameters))
	for k, v := range parameters {
		elements[k] = types.StringValue(v)
	}
	return types.MapValue(types.StringType, elements)
}
