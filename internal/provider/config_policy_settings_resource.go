// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &configPolicySettingsResource{}
	_ resource.ResourceWithConfigure   = &configPolicySettingsResource{}
	_ resource.ResourceWithImportState = &configPolicySettingsResource{}
)

// configPolicySettingsTypeName is the Terraform type name.
const configPolicySettingsTypeName = "circleci_config_policy_settings"

// configPolicySettingsResourceModel maps the resource schema.
type configPolicySettingsResourceModel struct {
	OwnerID       types.String `tfsdk:"owner_id"`
	PolicyContext types.String `tfsdk:"policy_context"`
	Enabled       types.Bool   `tfsdk:"enabled"`
}

// NewConfigPolicySettingsResource is a helper function to simplify the provider implementation.
func NewConfigPolicySettingsResource() resource.Resource {
	return &configPolicySettingsResource{}
}

// configPolicySettingsResource is the resource implementation.
type configPolicySettingsResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *configPolicySettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_config_policy_settings"
}

// Schema defines the schema for the resource.
func (r *configPolicySettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Controls whether CircleCI evaluates a policy context's config policies. " +
			"Available on CircleCI Cloud and CircleCI Server 4.2 or later.\n\n" +
			"Uploading policies with `circleci_config_policy_bundle` does not enforce them; this resource " +
			"is the switch that turns evaluation on. While it is enabled, every pipeline's configuration " +
			"is evaluated against the bundle and a configuration that produces a hard failure is blocked.\n\n" +
			"~> **This is a settings object, not a created entity.** Every policy context always has " +
			"decision settings, so there is nothing to create. Destroying this resource removes it from " +
			"state and leaves policy evaluation exactly as it is — it never silently switches enforcement " +
			"off. Set `enabled = false` and apply if that is what you want.\n\n" +
			"~> **Requires the Scale plan on CircleCI Cloud**, or CircleCI Server 4.2 or later.",
		Attributes: map[string]schema.Attribute{
			"owner_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization whose policy decisions " +
					"are configured. Changing this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"policy_context": schema.StringAttribute{
				MarkdownDescription: "Which policy context these settings apply to. Defaults to `" +
					circleci.PolicyContextConfig + "`. Changing this value forces a new resource to be " +
					"created.\n\n" +
					"~> A policy context is **not** a CircleCI context: it is a namespace for a bundle of " +
					"policies, unrelated to `circleci_context`. Its only valid values are `" +
					circleci.PolicyContextConfig + "`. CircleCI documents a `custom` context, but every " +
					"the API route rejects it with a 400, so it is not offered here.",
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(circleci.PolicyContextConfig),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf(
						circleci.PolicyContextConfig,
					),
				},
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether config policies are evaluated for this policy context. " +
					"Enable it only once the bundle holds the policies you intend to enforce: with an " +
					"empty bundle nothing is blocked, but with an unreviewed one pipelines can start " +
					"failing immediately.",
				Required: true,
			},
		},
	}
}

// Create writes the decision settings. The API has no create route: PATCH is the
// only write, and the settings record always exists.
func (r *configPolicySettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan configPolicySettingsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !r.write(ctx, plan, "creating", &resp.Diagnostics) {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *configPolicySettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state configPolicySettingsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	settings, err := r.client.GetPolicyDecisionSettings(ctx,
		state.OwnerID.ValueString(), state.PolicyContext.ValueString())
	if err != nil {
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Error reading CircleCI config policy settings",
			fmt.Sprintf(
				"Could not read the %q policy decision settings for organization %s: %s",
				state.PolicyContext.ValueString(), state.OwnerID.ValueString(), circleci.Detail(err),
			),
		)

		return
	}

	// The API omits enabled rather than reporting false when evaluation has never
	// been switched on, so an absent field means disabled.
	state.Enabled = types.BoolValue(settings.Enabled != nil && *settings.Enabled)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update writes the planned decision settings.
func (r *configPolicySettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan configPolicySettingsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !r.write(ctx, plan, "updating", &resp.Diagnostics) {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete makes no API call.
//
// The route could disable evaluation, but destroying a Terraform resource must
// not quietly turn a security control off: an organization that stops managing
// these settings should keep enforcing the policies it had. Warn instead, and
// leave the decision to the practitioner.
func (r *configPolicySettingsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state configPolicySettingsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddWarning(
		"CircleCI config policy settings left in place",
		fmt.Sprintf(
			"%s was removed from Terraform state, but policy evaluation for the %q policy context of "+
				"organization %s keeps its current value (enabled = %t). Turning enforcement off as a "+
				"side effect of a destroy would be a silent change to a security control.\n\n"+
				"Set enabled = false and apply before removing the resource if you want it disabled.",
			configPolicySettingsTypeName, state.PolicyContext.ValueString(),
			state.OwnerID.ValueString(), state.Enabled.ValueBool(),
		),
	)
}

// Configure adds the provider configured client to the resource.
func (r *configPolicySettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports existing policy decision settings into Terraform state.
// Expected import ID format: "owner_id" (which assumes the default policy
// context) or "owner_id/policy_context".
func (r *configPolicySettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ownerID, policyContext, ok := parsePolicyImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError(
			"Invalid Import ID Format",
			policyImportIDError(req.ID, configPolicySettingsTypeName),
		)

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owner_id"), ownerID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("policy_context"), policyContext)...)
}

// write applies the planned settings. It reports whether the call succeeded,
// having recorded a diagnostic if it did not.
//
// The API's answer is deliberately discarded rather than folded back into state.
// enabled is a Required attribute, so state has to match the plan after an
// apply; adopting a different value would make the framework reject the result
// as inconsistent. A server that disagreed surfaces at the next refresh instead.
func (r *configPolicySettingsResource) write(
	ctx context.Context, plan configPolicySettingsResourceModel, verb string, diags *diag.Diagnostics,
) bool {
	_, err := r.client.SetPolicyDecisionSettings(ctx,
		plan.OwnerID.ValueString(), plan.PolicyContext.ValueString(),
		circleci.PolicyDecisionSettings{Enabled: plan.Enabled.ValueBoolPointer()},
	)
	if err != nil {
		diags.AddError(
			fmt.Sprintf("Error %s CircleCI config policy settings", verb),
			fmt.Sprintf(
				"Could not write the %q policy decision settings for organization %s: %s\n\n"+
					"Config policies require the Scale plan on CircleCI Cloud, or CircleCI Server 4.2 "+
					"or later.",
				plan.PolicyContext.ValueString(), plan.OwnerID.ValueString(), circleci.Detail(err),
			),
		)

		return false
	}

	return true
}
