// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

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
	_ resource.Resource                = &notificationChannelConfigResource{}
	_ resource.ResourceWithConfigure   = &notificationChannelConfigResource{}
	_ resource.ResourceWithImportState = &notificationChannelConfigResource{}
	_ resource.ResourceWithModifyPlan  = &notificationChannelConfigResource{}
)

// notificationChannelConfigTypeName is the Terraform type name, used both for
// Metadata and for the Cloud-only error.
const notificationChannelConfigTypeName = "circleci_notification_channel_config"

// notificationChannelConfigResourceModel maps the resource schema.
type notificationChannelConfigResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Scope       types.String `tfsdk:"scope"`
	ChannelType types.String `tfsdk:"channel_type"`
	Target      types.String `tfsdk:"target"`
	ChannelName types.String `tfsdk:"channel_name"`
	IsEnabled   types.Bool   `tfsdk:"is_enabled"`
	ProjectID   types.String `tfsdk:"project_id"`
	OrgID       types.String `tfsdk:"org_id"`
	UserID      types.String `tfsdk:"user_id"`
}

// NewNotificationChannelConfigResource is a helper function to simplify the
// provider implementation.
func NewNotificationChannelConfigResource() resource.Resource {
	return &notificationChannelConfigResource{}
}

// notificationChannelConfigResource is the resource implementation.
type notificationChannelConfigResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *notificationChannelConfigResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_channel_config"
}

// Schema defines the schema for the resource.
func (r *notificationChannelConfigResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI notification channel config: where notifications for a " +
			"user or a project are delivered, by email or by Slack.\n\n" +
			"~> **CircleCI Cloud only.** Channel configs are served by the CircleCI v3 API, which " +
			"CircleCI Server does not route. Using this resource against a provider configured with " +
			"`deployment = \"server\"` fails with an explicit error.\n\n" +
			"~> **A user-scoped config always belongs to the API token's own user.** There is no way " +
			"to address another user's channel configs through this API; `scope = \"user\"` manages the " +
			"configuration of whoever the provider authenticates as.\n\n" +
			"A Slack channel config requires an active `circleci_notification_integrations` Slack " +
			"installation for the organization, and `target` must be a channel ID the installed app " +
			"can already post to.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the channel config.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"scope": schema.StringAttribute{
				MarkdownDescription: "Whether this config belongs to the calling user (`user`) or to a " +
					"project (`project`). `project_id` is required when this is `project`, and must be " +
					"omitted when this is `user`. Changing this value forces a new resource to be created.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(circleci.NotificationScopeUser, circleci.NotificationScopeProject),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"channel_type": schema.StringAttribute{
				MarkdownDescription: "The delivery channel: `email` or `slack`. Changing this value " +
					"forces a new resource to be created.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(circleci.NotificationChannelTypeEmail, circleci.NotificationChannelTypeSlack),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"target": schema.StringAttribute{
				MarkdownDescription: "The delivery address: an email address for `channel_type = \"email\"`, " +
					"or a Slack channel ID for `channel_type = \"slack\"`. Updatable in place.",
				Required: true,
			},
			"channel_name": schema.StringAttribute{
				MarkdownDescription: "The Slack channel name CircleCI resolved for `target`, without the " +
					"leading `#`. Populated only for a project-scoped Slack config; null otherwise.",
				Computed: true,
			},
			"is_enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether this channel is active. Updatable in place.",
				Required:            true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the project this config belongs to. " +
					"Required when `scope = \"project\"`, and must be omitted when `scope = \"user\"`. " +
					"Changing this value forces a new resource to be created.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"org_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization this config is tied to. " +
					"Required for both scopes: even a user-scoped config belongs to one organization. " +
					"Changing this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"user_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the user this config belongs to. Set " +
					"only when `scope = \"user\"`; null for a project-scoped config. This is always the " +
					"API token's own user, never a value the configuration can choose.",
				Computed: true,
			},
		},
	}
}

// Create creates the channel config.
func (r *notificationChannelConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil || !requireCloud(r.client, notificationChannelConfigTypeName, &resp.Diagnostics) {
		return
	}

	var plan notificationChannelConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scope := plan.Scope.ValueString()
	projectID := plan.ProjectID.ValueString()

	if scope == circleci.NotificationScopeProject && projectID == "" {
		resp.Diagnostics.AddError(
			"project_id is required",
			`project_id must be set when scope = "project".`,
		)

		return
	}
	if scope == circleci.NotificationScopeUser && projectID != "" {
		resp.Diagnostics.AddError(
			"project_id must be omitted",
			`project_id must be omitted when scope = "user": a user-scoped config always belongs to the API token's own user.`,
		)

		return
	}

	cc, err := r.client.CreateNotificationChannelConfig(ctx, circleci.CreateNotificationChannelConfigRequest{
		Scope:       scope,
		ChannelType: plan.ChannelType.ValueString(),
		Target:      plan.Target.ValueString(),
		IsEnabled:   plan.IsEnabled.ValueBool(),
		ProjectID:   projectID,
		OrgID:       plan.OrgID.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to create CircleCI notification channel config",
			circleci.Detail(err),
		)

		return
	}

	applyNotificationChannelConfig(&plan, cc)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *notificationChannelConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil || !requireCloud(r.client, notificationChannelConfigTypeName, &resp.Diagnostics) {
		return
	}

	var state notificationChannelConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cc, err := r.client.GetNotificationChannelConfig(ctx, state.ID.ValueString())
	if err != nil {
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read CircleCI notification channel config "+state.ID.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	applyNotificationChannelConfig(&state, cc)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update applies target and is_enabled, the only attributes the API can change
// in place. Every other attribute is RequiresReplace.
func (r *notificationChannelConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil || !requireCloud(r.client, notificationChannelConfigTypeName, &resp.Diagnostics) {
		return
	}

	var plan, state notificationChannelConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cc, err := r.client.UpdateNotificationChannelConfig(
		ctx, state.ID.ValueString(), plan.Target.ValueString(), plan.IsEnabled.ValueBool(),
	)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to update CircleCI notification channel config "+state.ID.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	plan.ID = state.ID
	applyNotificationChannelConfig(&plan, cc)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the channel config.
func (r *notificationChannelConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil || !requireCloud(r.client, notificationChannelConfigTypeName, &resp.Diagnostics) {
		return
	}

	var state notificationChannelConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteNotificationChannelConfig(ctx, state.ID.ValueString())
	if err != nil && !circleci.IsNotFound(err) {
		resp.Diagnostics.AddError(
			"Unable to delete CircleCI notification channel config "+state.ID.ValueString(),
			circleci.Detail(err),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *notificationChannelConfigResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing channel config by its id.
func (r *notificationChannelConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// ModifyPlan rejects CircleCI Server at plan time rather than at apply time.
// Destroy is exempt so a resource stranded in state by a deployment change
// stays removable.
func (r *notificationChannelConfigResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, notificationChannelConfigTypeName, &resp.Diagnostics)
}

// applyNotificationChannelConfig copies an API channel config into the model.
func applyNotificationChannelConfig(model *notificationChannelConfigResourceModel, cc *circleci.NotificationChannelConfig) {
	model.ID = types.StringValue(cc.ID)
	model.Scope = types.StringValue(cc.Scope)
	model.ChannelType = types.StringValue(cc.ChannelType)
	model.Target = types.StringValue(cc.Target)
	model.IsEnabled = types.BoolValue(cc.IsEnabled)
	model.OrgID = types.StringValue(cc.OrgID)

	if cc.ChannelName == "" {
		model.ChannelName = types.StringNull()
	} else {
		model.ChannelName = types.StringValue(cc.ChannelName)
	}

	if cc.ProjectID == "" {
		model.ProjectID = types.StringNull()
	} else {
		model.ProjectID = types.StringValue(cc.ProjectID)
	}

	if cc.UserID == "" {
		model.UserID = types.StringNull()
	} else {
		model.UserID = types.StringValue(cc.UserID)
	}
}
