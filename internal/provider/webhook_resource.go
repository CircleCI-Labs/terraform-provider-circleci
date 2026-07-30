// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                     = &webhookResource{}
	_ resource.ResourceWithConfigure        = &webhookResource{}
	_ resource.ResourceWithConfigValidators = &webhookResource{}
	_ resource.ResourceWithImportState      = &webhookResource{}
)

// webhookResourceModel maps the resource schema.
type webhookResourceModel struct {
	Id            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Url           types.String `tfsdk:"url"`
	VerifyTls     types.Bool   `tfsdk:"verify_tls"`
	SigningSecret types.String `tfsdk:"signing_secret"`
	// SigningSecretWO is always null here. The framework nullifies a write-only
	// attribute in plan and state, so the field exists only to satisfy the schema;
	// the value is read from configuration by resolveWebhookSigningSecret.
	SigningSecretWO        types.String `tfsdk:"signing_secret_wo"`
	SigningSecretWOVersion types.Int64  `tfsdk:"signing_secret_wo_version"`
	ScopeId                types.String `tfsdk:"scope_id"`
	ScopeType              types.String `tfsdk:"scope_type"`
	// Events is a Set rather than a List because the API does not preserve the
	// order the events were submitted in. See the schema for the whole story.
	Events    types.Set    `tfsdk:"events"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

// NewWebhookResource is a helper function to simplify the provider implementation.
func NewWebhookResource() resource.Resource {
	return &webhookResource{}
}

// webhookResource is the resource implementation.
type webhookResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *webhookResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webhook"
}

// Schema defines the schema for the resource.
func (r *webhookResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI webhook. Webhooks allow you to receive notifications when events occur in your CircleCI projects.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The unique identifier of the webhook.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the webhook.",
				Required:            true,
			},
			"url": schema.StringAttribute{
				MarkdownDescription: "The URL to which webhook payloads will be sent. Must be a valid HTTPS URL and cannot point to localhost or private IP addresses.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^https://[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*(:[0-9]{1,5})?(/.*)?$`),
						"URL must be a valid HTTPS URL with a proper hostname",
					),
					WebhookURLValidator(),
				},
			},
			"verify_tls": schema.BoolAttribute{
				MarkdownDescription: "Whether to verify TLS certificates when sending payloads. Defaults to true.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},
			// Optional, not Required as it once was, so that `signing_secret_wo` can
			// be used instead. Nothing about an existing configuration changes:
			// webhookSigningSecretConfigValidator requires exactly one of the two, so
			// a configuration that sets `signing_secret` is still valid and one that
			// sets neither is still refused — with a different diagnostic than
			// before, but at the same point in the run. `signing_secret` is not
			// deprecated.
			"signing_secret": schema.StringAttribute{
				MarkdownDescription: "The secret used to sign webhook payloads.\n\n" +
					"It is recorded in Terraform state in cleartext. Use `signing_secret_wo` " +
					"instead to keep it out of state, at the cost of having to bump " +
					"`signing_secret_wo_version` to rotate it. Set exactly one of the two.",
				Optional:  true,
				Sensitive: true,
			},
			"signing_secret_wo":         webhookSigningSecretWriteOnlyAttribute(),
			"signing_secret_wo_version": webhookSigningSecretWriteOnlyVersionAttribute(),
			"scope_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the scope (project) for which the webhook is configured. Changing this value forces a new resource to be created.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"scope_type": schema.StringAttribute{
				MarkdownDescription: "The type of the scope. Currently only 'project' is supported. Changing this value forces a new resource to be created.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			// A Set, not a List. The webhook API treats events as an unordered
			// collection and returns them in an order of its own choosing, which is
			// stable across reads but is not the order they were submitted in —
			// verified against the live API. Declared as a List, Terraform compared
			// the configured order against the returned order and planned a change on
			// every run, for ever, with nothing to apply.
			//
			// The same bug was reported against the community provider
			// kelvintaywl/terraform-provider-circleci as issue #45 ("events attribute
			// changed on terraform plan even though there is no changes") and fixed
			// the same way.
			//
			// No state upgrade accompanies this change: a list and a set of the same
			// element type share one JSON encoding, and the framework re-reads prior
			// raw state against the current schema type, so existing state decodes as
			// a set unchanged. TestListToSetNeedsNoStateUpgrade proves it rather than
			// assuming it.
			"events": schema.SetAttribute{
				MarkdownDescription: fmt.Sprintf(
					"The events that will trigger the webhook. Valid values are: %s. "+
						"Order is not significant: CircleCI returns the events in an order of its own, "+
						"so this is a set rather than a list.",
					strings.Join(circleci.WebhookEvents(), ", "),
				),
				Required:    true,
				ElementType: types.StringType,
				Validators: []validator.Set{
					setvalidator.ValueStringsAre(
						stringvalidator.OneOf(circleci.WebhookEvents()...),
					),
				},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the webhook was created.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the webhook was last updated.",
				Computed:            true,
			},
		},
	}
}

// ConfigValidators requires exactly one of `signing_secret` and
// `signing_secret_wo`.
func (r *webhookResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{webhookSigningSecretConfigValidator()}
}

// Create creates the resource and sets the initial Terraform state.
func (r *webhookResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan webhookResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Convert the event set to []string
	var events []string
	diags = plan.Events.ElementsAs(ctx, &events, false)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// From configuration, because `signing_secret_wo` is null in the plan. See
	// webhook_write_only.go.
	signingSecret, ok := resolveWebhookSigningSecret(
		ctx, req.Config, plan.SigningSecret, plan.SigningSecretWOVersion, &resp.Diagnostics,
	)
	if !ok {
		return
	}

	// Build the webhook request.
	//
	// This goes through the provider's own client rather than circleci-sdk-go
	// because the SDK tags verify_tls and signing_secret as "verify-tls" and
	// "signing-secret". The API ignores unrecognized keys, so every webhook
	// created through the SDK silently had NO signing secret and took the
	// server-side default for TLS verification, however they were configured.
	newWebhook := circleci.WebhookInput{
		Name:          plan.Name.ValueString(),
		URL:           plan.Url.ValueString(),
		VerifyTLS:     plan.VerifyTls.ValueBool(),
		SigningSecret: signingSecret,
		Scope: circleci.WebhookScope{
			ID:   plan.ScopeId.ValueString(),
			Type: plan.ScopeType.ValueString(),
		},
		Events: events,
	}

	createdWebhook, err := r.client.CreateWebhook(ctx, newWebhook)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI webhook",
			circleci.Detail(err),
		)

		return
	}

	// Map response to state
	plan.Id = types.StringValue(createdWebhook.ID)
	// Note: signing_secret is preserved from plan (user-provided value)
	plan.CreatedAt = types.StringValue(createdWebhook.CreatedAt)
	plan.UpdatedAt = types.StringValue(createdWebhook.UpdatedAt)

	// Set state
	diags = resp.State.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Read refreshes the Terraform state with the latest data.
func (r *webhookResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state webhookResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.Id.IsNull() {
		resp.Diagnostics.AddError(
			"Missing webhook id",
			"Missing webhook id",
		)
		return
	}

	webhookData, err := r.client.GetWebhook(ctx, state.Id.ValueString())
	// A webhook deleted outside Terraform must drop out of state so the next plan
	// recreates it. Absence is tested with circleci.IsNotFound rather than by
	// string-matching the error: matching "404" also matches a 5xx whose body
	// happens to mention it, which silently removed live resources from state.
	if circleci.IsNotFound(err) {
		resp.Diagnostics.AddWarning(
			"Webhook not found during Read",
			fmt.Sprintf("Webhook ID %s no longer exists in CircleCI. Removing it from state.", state.Id.ValueString()),
		)
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Read CircleCI webhook with id "+state.Id.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	// A tripwire, not a behavior: signing_secret is left alone below because the
	// API only ever returns a mask, and this is what notices if that stops being
	// true. Nothing about the secret is logged beyond its length.
	if webhookSecretLooksUnmasked(webhookData.SigningSecret) {
		tflog.Warn(ctx, "CircleCI returned a webhook signing_secret that is not masked", map[string]any{
			"webhook_id": webhookData.ID,
			"length":     len(webhookData.SigningSecret),
			"note": "this resource assumes the API never discloses the signing secret, and does " +
				"not refresh signing_secret from a read; please report this",
		})
	}

	// Convert events to types.Set. The order the API reports is deliberately not
	// preserved anywhere: a set has none, which is the whole point of the type.
	events, diags := types.SetValueFrom(ctx, types.StringType, webhookData.Events)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Map response to state
	state.Id = types.StringValue(webhookData.ID)
	state.Name = types.StringValue(webhookData.Name)
	state.Url = types.StringValue(webhookData.URL)
	state.VerifyTls = types.BoolValue(webhookData.VerifyTLS)
	state.ScopeId = types.StringValue(webhookData.Scope.ID)
	state.ScopeType = types.StringValue(webhookData.Scope.Type)
	state.Events = events
	// Note: created_at, updated_at, and signing_secret may not be returned by Get, preserve from state
	if webhookData.CreatedAt != "" {
		state.CreatedAt = types.StringValue(webhookData.CreatedAt)
	}
	if webhookData.UpdatedAt != "" {
		state.UpdatedAt = types.StringValue(webhookData.UpdatedAt)
	}

	// Set state
	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *webhookResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan webhookResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state webhookResourceModel
	diags = req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Convert the event set to []string
	var events []string
	diags = plan.Events.ElementsAs(ctx, &events, false)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The signing secret is resolved and sent on EVERY update, whatever triggered
	// it, and nothing here is conditional on `signing_secret_wo_version` having
	// changed. That is not an oversight — see webhook_write_only.go. UpdateWebhook
	// is a full-replace PUT, so a body with no signing_secret deletes the live
	// secret; gating the send on the version is
	// hashicorp/terraform-provider-vault#2900, where exactly that silently wiped a
	// credential whenever an unrelated field changed.
	signingSecret, ok := resolveWebhookSigningSecret(
		ctx, req.Config, plan.SigningSecret, plan.SigningSecretWOVersion, &resp.Diagnostics,
	)
	if !ok {
		return
	}

	// Build the webhook update request
	// Note: Scope cannot be updated
	updateWebhook := circleci.WebhookInput{
		Name:          plan.Name.ValueString(),
		URL:           plan.Url.ValueString(),
		VerifyTLS:     plan.VerifyTls.ValueBool(),
		SigningSecret: signingSecret,
		Events:        events,
	}

	updatedWebhook, err := r.client.UpdateWebhook(ctx, state.Id.ValueString(), updateWebhook)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error updating CircleCI webhook",
			circleci.Detail(err),
		)

		return
	}

	// Map response to state
	plan.Id = state.Id
	// Note: signing_secret is preserved from plan (user-provided value)
	plan.CreatedAt = state.CreatedAt
	plan.UpdatedAt = types.StringValue(updatedWebhook.UpdatedAt)

	// Set state
	diags = resp.State.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *webhookResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state webhookResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A webhook already gone is the desired end state, so absence is not an error.
	err := r.client.DeleteWebhook(ctx, state.Id.ValueString())
	if err != nil && !circleci.IsNotFound(err) {
		resp.Diagnostics.AddError(
			"Error Deleting CircleCI Webhook",
			circleci.Detail(err),
		)

		return
	}
}

// Configure adds the provider configured client to the resource.
func (r *webhookResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*CircleCiClientWrapper)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *CircleCiClientWrapper, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = client.Client
}

// ImportState imports the resource state.
func (r *webhookResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Expected format: "SCOPE_ID/WEBHOOK_ID"
	parts := strings.SplitN(req.ID, "/", 2)

	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid Import ID Format",
			fmt.Sprintf("Expected import ID format: 'scope_id/webhook_id'. Got: %s", req.ID),
		)
		return
	}

	scopeID := parts[0]
	webhookID := parts[1]

	// Set the primary key 'id'
	resp.Diagnostics.Append(resp.State.SetAttribute(
		ctx, path.Root("id"), webhookID,
	)...)

	// Set the scope_id
	resp.Diagnostics.Append(resp.State.SetAttribute(
		ctx, path.Root("scope_id"), scopeID,
	)...)

	// Set the scope_type to "project" as default (currently only supported type)
	resp.Diagnostics.Append(resp.State.SetAttribute(
		ctx, path.Root("scope_type"), "project",
	)...)

	if resp.Diagnostics.HasError() {
		return
	}
}
