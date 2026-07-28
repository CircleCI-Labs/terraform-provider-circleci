// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &notificationIntegrationStatusResource{}
	_ resource.ResourceWithConfigure   = &notificationIntegrationStatusResource{}
	_ resource.ResourceWithImportState = &notificationIntegrationStatusResource{}
	_ resource.ResourceWithModifyPlan  = &notificationIntegrationStatusResource{}
)

// notificationIntegrationStatusTypeName is the Terraform type name.
const notificationIntegrationStatusTypeName = "circleci_notification_integration_status"

// notificationIntegrationStatusResourceModel maps the resource schema.
type notificationIntegrationStatusResourceModel struct {
	ID            types.String `tfsdk:"id"`
	Status        types.String `tfsdk:"status"`
	Type          types.String `tfsdk:"type"`
	WorkspaceName types.String `tfsdk:"workspace_name"`
	TeamID        types.String `tfsdk:"team_id"`
	OrgID         types.String `tfsdk:"org_id"`
	OrgName       types.String `tfsdk:"org_name"`
	CreatedAt     types.String `tfsdk:"created_at"`
	UpdatedAt     types.String `tfsdk:"updated_at"`
}

// NewNotificationIntegrationStatusResource is a helper function to simplify
// the provider implementation.
func NewNotificationIntegrationStatusResource() resource.Resource {
	return &notificationIntegrationStatusResource{}
}

// notificationIntegrationStatusResource manages whether an already-installed
// notification integration is active or disabled.
//
// This resource adopts an existing integration; it does not create one. There
// is no API route to install a Slack workspace: that only happens through an
// OAuth flow in the CircleCI web UI (see circleci_notification_integrations to
// discover the id of an integration installed that way). What this resource
// manages is genuinely desired state, though: POST .../set-status is
// idempotent and reversible, and GET reports the current status, so a
// practitioner can declare "this integration should be active" the same way
// any other toggle is declared, and see drift if someone disables it from the
// CircleCI UI.
type notificationIntegrationStatusResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *notificationIntegrationStatusResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_integration_status"
}

// Schema defines the schema for the resource.
func (r *notificationIntegrationStatusResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages whether an existing CircleCI notification integration (a Slack " +
			"workspace installation, today) is active or disabled.\n\n" +
			"~> **CircleCI Cloud only.** Integrations are served by the CircleCI v3 API, which CircleCI " +
			"Server does not route.\n\n" +
			"!> **This resource adopts an integration; it does not create one.** There is no API route " +
			"to install a Slack workspace — that happens through an OAuth flow in the CircleCI web UI. " +
			"Use the `circleci_notification_integrations` data source to find the `id` of an integration " +
			"installed that way, then manage its status here.\n\n" +
			"`status` only accepts `" + circleci.NotificationIntegrationStatusActive + "` and `" +
			circleci.NotificationIntegrationStatusDisabled + "`. An integration may also report `" +
			circleci.NotificationIntegrationStatusDisconnected + "` (the workspace removed the CircleCI " +
			"app on Slack's side) after a read; reactivating it through this resource is not guaranteed " +
			"to succeed, since Slack, not CircleCI, revoked access.\n\n" +
			"!> **Destroying this resource does not revoke the integration.** There is no route that " +
			"resets a status to a default, and the real `DELETE` route revokes the whole Slack " +
			"connection — a much bigger action than this resource's scope. `terraform destroy` only " +
			"stops managing the status; the integration keeps whatever status it last had.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the existing notification integration " +
					"to manage. Changing this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "The desired status: `" + circleci.NotificationIntegrationStatusActive +
					"` or `" + circleci.NotificationIntegrationStatusDisabled + "`.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(
						circleci.NotificationIntegrationStatusActive,
						circleci.NotificationIntegrationStatusDisabled,
					),
				},
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "The integration type.",
				Computed:            true,
			},
			"workspace_name": schema.StringAttribute{
				MarkdownDescription: "The Slack workspace's display name.",
				Computed:            true,
			},
			"team_id": schema.StringAttribute{
				MarkdownDescription: "The Slack team id of the installed workspace.",
				Computed:            true,
			},
			"org_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the owning organization.",
				Computed:            true,
			},
			"org_name": schema.StringAttribute{
				MarkdownDescription: "Display name of the owning organization.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the integration was installed, as a UTC timestamp.",
				Computed:            true,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "When the integration was last changed, as a UTC timestamp.",
				Computed:            true,
			},
		},
	}
}

// Create adopts an existing integration and sets its status if it differs
// from the plan.
func (r *notificationIntegrationStatusResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil || !requireCloud(r.client, notificationIntegrationStatusTypeName, &resp.Diagnostics) {
		return
	}

	var plan notificationIntegrationStatusResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := plan.ID.ValueString()

	current, err := r.client.GetNotificationIntegration(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI notification integration "+id,
			circleci.Detail(err),
		)

		return
	}

	if current.Status != plan.Status.ValueString() {
		current, err = r.client.SetNotificationIntegrationStatus(ctx, id, plan.Status.ValueString())
		if err != nil {
			resp.Diagnostics.AddError(
				"Unable to set the status of CircleCI notification integration "+id,
				circleci.Detail(err),
			)

			return
		}
	}

	applyNotificationIntegrationStatus(&plan, current)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *notificationIntegrationStatusResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil || !requireCloud(r.client, notificationIntegrationStatusTypeName, &resp.Diagnostics) {
		return
	}

	var state notificationIntegrationStatusResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	integration, err := r.client.GetNotificationIntegration(ctx, state.ID.ValueString())
	if err != nil {
		// A revoked integration, or one deleted outside Terraform, both surface
		// as 404: neither is distinguishable, and both mean there is nothing left
		// to manage.
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read CircleCI notification integration "+state.ID.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	applyNotificationIntegrationStatus(&state, integration)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update sets the new status.
func (r *notificationIntegrationStatusResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil || !requireCloud(r.client, notificationIntegrationStatusTypeName, &resp.Diagnostics) {
		return
	}

	var plan, state notificationIntegrationStatusResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	integration, err := r.client.SetNotificationIntegrationStatus(ctx, state.ID.ValueString(), plan.Status.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to set the status of CircleCI notification integration "+state.ID.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	plan.ID = state.ID
	applyNotificationIntegrationStatus(&plan, integration)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the resource from state without calling the API.
//
// Create never installed the integration, so Destroy must not revoke it: the
// real DELETE route tears down the whole Slack connection, which is a much
// bigger action than "stop managing the status". There is also no route that
// resets a status to a default, so the integration keeps whatever status it
// last had.
func (r *notificationIntegrationStatusResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil || !requireCloud(r.client, notificationIntegrationStatusTypeName, &resp.Diagnostics) {
		return
	}

	var state notificationIntegrationStatusResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddWarning(
		"CircleCI notification integration left in place",
		fmt.Sprintf(
			"%s was removed from Terraform state, but the underlying integration %s was never created "+
				"by this resource and is not revoked by destroying it. It keeps whatever status it last "+
				"had. Delete the Slack connection from the CircleCI web UI if you want to remove it.",
			notificationIntegrationStatusTypeName, state.ID.ValueString(),
		),
	)
}

// Configure adds the provider configured client to the resource.
func (r *notificationIntegrationStatusResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing integration by its id. status is Required
// rather than Computed, but the subsequent Read fills it in from the API along
// with every other attribute, so only id needs to be set here.
func (r *notificationIntegrationStatusResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ModifyPlan rejects CircleCI Server at plan time. Destroy is exempt.
func (r *notificationIntegrationStatusResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, notificationIntegrationStatusTypeName, &resp.Diagnostics)
}

// applyNotificationIntegrationStatus copies an API integration into the model.
func applyNotificationIntegrationStatus(model *notificationIntegrationStatusResourceModel, integration *circleci.NotificationIntegration) {
	model.ID = types.StringValue(integration.ID)
	model.Status = types.StringValue(integration.Status)
	model.Type = types.StringValue(integration.Type)
	model.WorkspaceName = types.StringValue(integration.WorkspaceName)
	model.TeamID = types.StringValue(integration.TeamID)
	model.OrgID = types.StringValue(integration.OrgID)
	model.OrgName = types.StringValue(integration.OrgName)
	model.CreatedAt = types.StringValue(integration.CreatedAt)
	model.UpdatedAt = types.StringValue(integration.UpdatedAt)
}
