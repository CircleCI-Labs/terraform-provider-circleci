// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

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
	_ resource.Resource                     = &runnerTokenResource{}
	_ resource.ResourceWithConfigure        = &runnerTokenResource{}
	_ resource.ResourceWithImportState      = &runnerTokenResource{}
	_ resource.ResourceWithConfigValidators = &runnerTokenResource{}
	_ resource.ResourceWithModifyPlan       = &runnerTokenResource{}
)

// runnerTokenResourceModel maps the resource schema.
type runnerTokenResourceModel struct {
	Id             types.String `tfsdk:"id"`
	OrganizationId types.String `tfsdk:"organization_id"`
	OrgId          types.String `tfsdk:"org_id"`
	ResourceClass  types.String `tfsdk:"resource_class"`
	Nickname       types.String `tfsdk:"nickname"`
	Token          types.String `tfsdk:"token"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

// NewRunnerTokenResource is a helper function to simplify the provider implementation.
func NewRunnerTokenResource() resource.Resource {
	return &runnerTokenResource{}
}

// runnerTokenResource is the resource implementation.
type runnerTokenResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *runnerTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_runner_token"
}

// Schema defines the schema for the resource.
func (r *runnerTokenResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI runner authentication token. The token value is only " +
			"available at creation time and cannot be retrieved afterwards.\n\n" +
			"~> **A resource class holds at most 10 tokens.** The eleventh create is refused with " +
			"HTTP 403 and a message naming the limit. The limit counts every token on the resource " +
			"class, including any created outside Terraform — list them with " +
			"`circleci_runner_tokens` if an apply hits it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the runner token.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// replaces is false here, unlike TokenInput's doc comment might suggest
			// at a glance -- and for exactly the reason circleci_runner_resource_class
			// gives for the same choice (see that resource's Schema method): the
			// runner API derives the owning organization from resource_class's
			// namespace and never reads org_id from the create body at all (see
			// TokenInput's doc comment), so a changed organization here is
			// bookkeeping, not something the service acts on. Forcing a replacement
			// on it would mean every import -- which cannot recover either
			// organization attribute, since the token representation carries no
			// organization field -- destroys and recreates the token, permanently
			// invalidating it, the moment a configuration supplies the organization
			// that ConfigValidators requires. See ModifyPlan and Update below, and
			// TestRunnerTokenImport.
			"organization_id": deprecatedOrgIDAttribute("this runner token", false),
			"org_id":          orgIDAttribute("this runner token", false),
			"resource_class": schema.StringAttribute{
				MarkdownDescription: "The resource class this token grants access to, in `namespace/name` format (e.g. `myorg/myrunner`). Changing this value forces a new resource to be created.",
				Required:            true,
				// Same shape check as every other runner attribute naming a resource
				// class. It was missing here, so a namespace the service rejects
				// (upper case, or containing a dot) only failed once Create was
				// already running. See runnerResourceClassPattern.
				Validators: []validator.String{
					stringvalidator.RegexMatches(runnerResourceClassPattern, runnerResourceClassFormatMessage),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"nickname": schema.StringAttribute{
				MarkdownDescription: "A human-readable label for the token. Changing this value forces a new resource to be created.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"token": schema.StringAttribute{
				MarkdownDescription: "The token value used to authenticate a runner agent. Only available at creation time — this value is not returned by the API on subsequent reads and is null after an import.",
				Computed:            true,
				Sensitive:           true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The time at which the token was created.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (r *runnerTokenResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		orgIDConfigValidator(),
	}
}

// ModifyPlan makes both organization attribute names agree in the plan.
//
// Needed for exactly the reason given on circleci_runner_resource_class's
// ModifyPlan, which this mirrors: every other attribute here still forces
// replacement, so the only way Update is ever called is a plan that changes
// nothing but the organization, and Terraform's dual Optional+Computed
// attributes retain a stale value across that plan unless it is reconciled
// here. See org_id_deprecation.go.
func (r *runnerTokenResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !orgIDPlanNeedsReconcile(req) {
		return
	}

	var config runnerTokenResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	reconcileOrgIDPlan(ctx, resp, config.OrganizationId, config.OrgId)
}

// Create creates the resource and sets the initial Terraform state.
func (r *runnerTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan runnerTokenResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(plan.OrganizationId, plan.OrgId)

	createReq := circleci.TokenInput{
		OrganizationID: organizationID,
		ResourceClass:  plan.ResourceClass.ValueString(),
		Nickname:       plan.Nickname.ValueString(),
	}

	t, err := r.client.CreateToken(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI runner token",
			circleci.Detail(err),
		)
		return
	}

	plan.Id = types.StringValue(t.ID)
	setOrgIDs(&plan.OrganizationId, &plan.OrgId, organizationID)
	plan.ResourceClass = types.StringValue(t.ResourceClass)
	plan.Nickname = types.StringValue(t.Nickname)
	plan.Token = types.StringValue(t.Token)
	plan.CreatedAt = types.StringValue(t.CreatedAt)

	diags = resp.State.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
}

// Read refreshes the Terraform state with the latest data.
func (r *runnerTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state runnerTokenResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tokens, err := r.client.ListTokens(ctx, state.ResourceClass.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading CircleCI runner tokens",
			"Could not list runner tokens for resource class "+state.ResourceClass.ValueString()+": "+circleci.Detail(err),
		)
		return
	}

	var found *circleci.Token
	for i := range tokens {
		if tokens[i].ID == state.Id.ValueString() {
			found = &tokens[i]
			break
		}
	}

	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.Id = types.StringValue(found.ID)
	state.ResourceClass = types.StringValue(found.ResourceClass)
	state.Nickname = types.StringValue(found.Nickname)
	state.CreatedAt = types.StringValue(found.CreatedAt)
	// Token is write-once and not returned by the API — preserve value from state.

	// The token representation carries no organization, so whichever attribute
	// name state holds is mirrored onto the other. The guard matters for an
	// imported token: the import ID is "resource_class/token_id", so neither name
	// is known, and writing "" over two null values would be a spurious change.
	// See org_id_deprecation.go.
	if organizationID := effectiveOrgID(state.OrganizationId, state.OrgId); organizationID != "" {
		setOrgIDs(&state.OrganizationId, &state.OrgId, organizationID)
	}

	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

// Update persists plan values (the organization) into state. Every other
// attribute forces replacement (see the Schema method), so the only plan that
// ever reaches Update is one that changes nothing but organization_id/org_id,
// and the runner API has no update route to call for that -- it is bookkeeping
// this provider carries, not something the service is told about. The plan is
// persisted verbatim, including both organization attribute names: ModifyPlan
// has already made them agree, and re-deriving them here could only disagree
// with the plan Terraform is holding. That also covers the update after an
// import, where neither name is in state and the unconfigured one would
// otherwise still be unknown.
func (r *runnerTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan runnerTokenResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *runnerTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state runnerTokenResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteToken(ctx, state.Id.ValueString())
	// A token already gone is the desired end state, so absence is not an error.
	if err != nil && !circleci.IsNotFound(err) {
		resp.Diagnostics.AddError(
			"Error deleting CircleCI runner token",
			"Could not delete runner token "+state.Id.ValueString()+": "+circleci.Detail(err),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *runnerTokenResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing runner token into Terraform state.
// The import ID format is "resource_class/token_id" (e.g. "myorg/myrunner/550e8400-...").
// Note: the token value cannot be recovered after import.
func (r *runnerTokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// resource_class is "namespace/name" (one slash), token_id is a UUID (no slashes).
	// Split on the last slash to separate them.
	lastSlash := strings.LastIndex(req.ID, "/")
	if lastSlash == -1 || lastSlash == 0 || lastSlash == len(req.ID)-1 {
		resp.Diagnostics.AddError(
			"Invalid import ID format",
			fmt.Sprintf("Expected format: resource_class/token_id (e.g. myorg/myrunner/550e8400-...). Got: %s", req.ID),
		)
		return
	}

	resourceClass := req.ID[:lastSlash]
	tokenID := req.ID[lastSlash+1:]

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_class"), resourceClass)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tokenID)...)
}
