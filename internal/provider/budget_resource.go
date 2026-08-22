// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	_ resource.Resource                     = &budgetResource{}
	_ resource.ResourceWithConfigure        = &budgetResource{}
	_ resource.ResourceWithImportState      = &budgetResource{}
	_ resource.ResourceWithModifyPlan       = &budgetResource{}
	_ resource.ResourceWithConfigValidators = &budgetResource{}
)

// budgetTypeName is the Terraform type name, used both for Metadata and for
// the Cloud-only error.
const budgetTypeName = "circleci_budget"

// budgetResourceModel maps the resource schema.
type budgetResourceModel struct {
	ID                types.String  `tfsdk:"id"`
	OrganizationID    types.String  `tfsdk:"organization_id"`
	OrgID             types.String  `tfsdk:"org_id"`
	ProjectID         types.String  `tfsdk:"project_id"`
	Credits           types.Int64   `tfsdk:"credits"`
	EnforcementType   types.String  `tfsdk:"enforcement_type"`
	Consumption       types.Int64   `tfsdk:"consumption"`
	Percentage        types.Float64 `tfsdk:"percentage"`
	ThresholdExceeded types.Bool    `tfsdk:"threshold_exceeded"`
}

// NewBudgetResource is a helper function to simplify the provider
// implementation.
func NewBudgetResource() resource.Resource {
	return &budgetResource{}
}

// budgetResource is the resource implementation.
//
// One resource covers both scopes the API models, rather than two: a spend
// budget is a single `Budget` type on the wire (internal/circleci/budget.go)
// distinguished only by whether `project_id` is null, the same route serves
// both (PUT /private/orgs/{orgUUID}/budgets, with `project_id` in the body),
// and the org-level budget is not a parent of the per-project ones — there is
// nothing a two-resource split would let one express that a single optional
// `project_id` attribute does not already.
type budgetResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *budgetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_budget"
}

// Schema defines the schema for the resource.
func (r *budgetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI spend budget: a credit limit for an organization, or " +
			"for one project within it, with an enforcement mode that warns or blocks new workflows " +
			"once spend crosses it.\n\n" +
			"~> **CircleCI Cloud only, and undocumented.** This is served by a private, unpublished " +
			"route with no OpenAPI specification. It is exercised in production " +
			"by CircleCI's own org-migration tooling with a plain personal API token, which is the " +
			"only evidence this provider has for its shape.\n\n" +
			"~> **`enforcement_type` cannot be set.** The write route accepts only `credits` and " +
			"`project_id`; there is no field for it anywhere on the request. It is reported here as " +
			"read-only so drift on it is visible, but it can only be changed in the CircleCI web UI.\n\n" +
			"Set `project_id` to manage a per-project budget; omit it to manage the organization-level " +
			"budget. An organization may have at most one budget per scope: applying this resource a " +
			"second time for the same `org_id` and `project_id` still leaves exactly one budget for " +
			"that scope, not two. Measured against a live organization, that write is not an update to " +
			"the existing budget record: CircleCI deletes it and creates a new one with a new `id`, " +
			"every time, even when `credits` is unchanged. `id` is therefore not a stable handle across " +
			"applies — see its own description below.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the budget, assigned by CircleCI. " +
					"There is no route to read a budget by this id — it exists so `terraform destroy` " +
					"has something to send to the delete route, which addresses a budget only by id.\n\n" +
					"**This id is not stable across writes.** Measured against a live organization: " +
					"every `PUT` to an existing scope — including one that resends the same `credits` " +
					"unchanged — deletes the underlying budget and creates a new one with a freshly " +
					"minted id. Do not rely on this value outside Terraform, and expect it to show " +
					"`(known after apply)` on every `terraform plan` that updates `credits`, not only on " +
					"create.",
				Computed: true,
				// Deliberately no UseStateForUnknown: that plan modifier tells Terraform
				// "this Computed value will not change unless the resource is replaced,"
				// which was true of nothing here. It shipped as if the migration CLI's
				// upsert preserved the underlying budget's id across an update, and it
				// does not — see the schema note above. Keeping the modifier would tell
				// Terraform to expect the old id back after an Update that actually
				// produces a new one, which is exactly the shape of a "Provider produced
				// inconsistent result after apply" error. Leaving `id` planned as unknown
				// on every update (the default for a bare Computed attribute) is what
				// this resource must do given the id churns; UseStateForUnknown would
				// still be correct on Create, where there is no prior value to carry
				// forward regardless of the modifier's presence, so removing it here
				// costs nothing there.
			},
			// See org_id_deprecation.go for why these are Optional+Computed. CircleCI
			// has no route that moves a budget between organizations, so changing
			// either replaces the resource.
			"organization_id": deprecatedOrgIDAttribute("this budget", true),
			"org_id":          orgIDAttribute("this budget", true),
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the project this budget limits. Omit " +
					"this to manage the organization-level budget instead.\n\n" +
					"Changing this value forces a new resource to be created: it addresses a different " +
					"budget entry on the API, not a rename of this one, so the old entry would " +
					"otherwise be orphaned rather than moved.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"credits": schema.Int64Attribute{
				MarkdownDescription: "The credit limit for this scope. Updatable in place. Measured " +
					"against a live organization: CircleCI rejects `0` (`400 \"Invalid budget " +
					"settings\"`), so the minimum accepted value is `1`, not `0`.",
				Required: true,
				Validators: []validator.Int64{
					int64validator.AtLeast(1),
				},
			},
			"enforcement_type": schema.StringAttribute{
				MarkdownDescription: "CircleCI's report of what happens once spend crosses this budget: " +
					"`" + circleci.BudgetEnforcementWarn + "` (surface overage without blocking) or " +
					"`" + circleci.BudgetEnforcementBlock + "` (stop new workflows). Read-only — see the " +
					"resource-level warning above.",
				Computed: true,
			},
			"consumption": schema.Int64Attribute{
				MarkdownDescription: "Credits consumed against this budget so far, as CircleCI last " +
					"computed it. This reflects real spend and can change between applies with no " +
					"configuration change.",
				Computed: true,
			},
			"percentage": schema.Float64Attribute{
				MarkdownDescription: "`consumption` as a percentage of `credits`, as CircleCI last " +
					"computed it. Like `consumption`, this can change between applies on its own.",
				Computed: true,
			},
			"threshold_exceeded": schema.BoolAttribute{
				MarkdownDescription: "Whether CircleCI reports this budget's enforcement threshold as " +
					"currently exceeded.",
				Computed: true,
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (r *budgetResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		orgIDConfigValidator(),
	}
}

// Create writes the budget via PUT, then reads the organization's budget list
// back to learn the id, enforcement type and current statistics — none of
// which the write response carries. See circleci.Client.SetBudget.
func (r *budgetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil || !requireCloud(r.client, budgetTypeName, &resp.Diagnostics) {
		return
	}

	var plan budgetResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := effectiveOrgID(plan.OrganizationID, plan.OrgID)
	projectID := budgetProjectIDPointer(plan.ProjectID)
	credits := int(plan.Credits.ValueInt64())

	if err := r.client.SetBudget(ctx, orgID, projectID, credits); err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI budget",
			fmt.Sprintf(
				"Could not create a spend budget for organization %s: %s",
				orgID, circleci.Detail(err),
			),
		)

		return
	}

	// The budget now exists at this scope, and SetBudget is an upsert: unlike
	// circleci_orb_version or circleci_orb, a retry after this point converges
	// rather than orphaning or colliding with anything, so this is the
	// self-healing case #37 also asked to be fixed for consistency. Even so, a
	// failure to read the write back below must not return before state
	// records what org_id and org/project scope were just written — see
	// trigger_resource.go's Create for the model.
	setOrgIDs(&plan.OrganizationID, &plan.OrgID, orgID)

	budget, ok, err := r.client.FindBudget(ctx, orgID, projectID)
	if !budgetFoundAfterWrite(&resp.Diagnostics, "creating", budget, ok, err) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

		return
	}

	applyBudget(&plan, budget)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read re-lists the organization's budgets and locates the entry for this
// resource's scope. There is no single-item GET, so the scope (org id plus an
// optional project id) is the only key this can look up by.
func (r *budgetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil || !requireCloud(r.client, budgetTypeName, &resp.Diagnostics) {
		return
	}

	var state budgetResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := effectiveOrgID(state.OrganizationID, state.OrgID)
	projectID := budgetProjectIDPointer(state.ProjectID)

	budget, ok, err := r.client.FindBudget(ctx, orgID, projectID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading CircleCI budget",
			fmt.Sprintf(
				"Could not list spend budgets for organization %s: %s",
				orgID, circleci.Detail(err),
			),
		)

		return
	}

	// No budget for this scope any more: removed outside Terraform. Drop it
	// from state so the next plan recreates it, rather than erroring — the
	// same convention circleci_context_restriction's Read follows for a
	// collection with no single-item read of its own.
	if !ok {
		resp.State.RemoveResource(ctx)

		return
	}

	setOrgIDs(&state.OrganizationID, &state.OrgID, orgID)
	applyBudget(&state, budget)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is reachable only for `credits`: `organization_id`, `org_id` and
// `project_id` all force replacement, and every other attribute is Computed.
// It writes via the same upsert PUT as Create and re-reads the result the
// same way.
func (r *budgetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil || !requireCloud(r.client, budgetTypeName, &resp.Diagnostics) {
		return
	}

	var plan budgetResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := effectiveOrgID(plan.OrganizationID, plan.OrgID)
	projectID := budgetProjectIDPointer(plan.ProjectID)
	credits := int(plan.Credits.ValueInt64())

	if err := r.client.SetBudget(ctx, orgID, projectID, credits); err != nil {
		resp.Diagnostics.AddError(
			"Error updating CircleCI budget",
			fmt.Sprintf(
				"Could not update the spend budget for organization %s: %s",
				orgID, circleci.Detail(err),
			),
		)

		return
	}

	budget, ok, err := r.client.FindBudget(ctx, orgID, projectID)
	if !budgetFoundAfterWrite(&resp.Diagnostics, "updating", budget, ok, err) {
		return
	}

	setOrgIDs(&plan.OrganizationID, &plan.OrgID, orgID)
	applyBudget(&plan, budget)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the budget by id.
//
// This IS gated on requireCloud, like Create/Read/Update above and like
// circleci_audit_log_config, circleci_orb_namespace and
// circleci_group_membership's own Delete methods — unlike
// circleci_storage_retention_controls and circleci_organization_contacts,
// whose Delete methods make no API call at all and so gate nothing worth
// gating. The distinction is not "does this call a private route" but "could
// this resource ever have been created under the deployment now in effect":
// a circleci_budget can only ever have been created while requireCloud
// passed, so `deployment = "server"` makes it entirely uncreatable, not
// merely unreachable — there is no scenario where a real budget exists on
// Cloud while this provider is configured for Server, the way there is for,
// say, circleci_audit_log_config's config record. Gating Delete the same way
// therefore does not strand anything new: it reports the same clear error
// here as everywhere else, and `terraform state rm` remains available exactly
// as it always was.
//
// On a DeleteBudget error, this does not trust circleci.IsNotFound the way an
// earlier version did. [NET] measurement (gh-app-cci-1, 2026-08-21) shows
// deleting an id that no longer exists — including the id of a budget this
// same sequence just deleted — answers 500 with a generic
// {"error":"There was an error deleting the budget"}, never 404. IsNotFound
// would therefore be false for exactly the "already gone" case Delete must
// treat as success, and true for nothing this route was ever observed to
// return. Corroborating by scope with FindBudget — the same lookup Read uses,
// since there is no single-budget GET either — is the only way available to
// tell "already gone" apart from a genuine failure: if nothing exists for this
// resource's scope any more, the delete has, by any definition that matters to
// Terraform, already happened.
func (r *budgetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil || !requireCloud(r.client, budgetTypeName, &resp.Diagnostics) {
		return
	}

	var state budgetResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := effectiveOrgID(state.OrganizationID, state.OrgID)
	projectID := budgetProjectIDPointer(state.ProjectID)

	err := r.client.DeleteBudget(ctx, orgID, state.ID.ValueString())
	if err == nil {
		return
	}

	if circleci.IsNotFound(err) {
		// Never actually observed for this route (see the doc comment above), but
		// if a future response ever does answer 404, honour it the same way as
		// the corroborated case below.
		return
	}

	if _, ok, findErr := r.client.FindBudget(ctx, orgID, projectID); findErr == nil && !ok {
		// Nothing exists for this scope any more: the delete already succeeded in
		// every sense Terraform cares about, whatever status DeleteBudget itself
		// answered with.
		return
	}

	resp.Diagnostics.AddError(
		"Error deleting CircleCI budget",
		fmt.Sprintf(
			"Could not delete budget %s for organization %s: %s",
			state.ID.ValueString(), orgID, circleci.Detail(err),
		),
	)
}

// Configure adds the provider configured client to the resource.
func (r *budgetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing budget from "org_id" (the organization-level
// budget) or "org_id/project_id" (a per-project budget).
//
// Only the scope is set here; `id` is left for Read to fill in, since — unlike
// circleci_context, which imports by an id it can GET directly — this resource
// has no single-item GET and locates everything, including its own id, by
// scope alone.
func (r *budgetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)

	organizationID := parts[0]
	if organizationID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID Format",
			fmt.Sprintf(
				`Expected import ID format: "org_id" for the organization-level budget, or `+
					`"org_id/project_id" for a per-project budget. Got: %s`, req.ID,
			),
		)

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), organizationID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("org_id"), organizationID)...)

	if len(parts) == 2 && parts[1] != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[1])...)
	}
}

// ModifyPlan rejects CircleCI Server at plan time rather than at apply time.
// Destroy is exempt, matching every other resource in this file: a null plan
// means Terraform is destroying the resource, and that must reach Delete —
// which reports its own requireCloud error, consistently, if it still applies
// — rather than being rejected one hook earlier with no diagnostic at all.
func (r *budgetResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, budgetTypeName, &resp.Diagnostics)
}

// budgetProjectIDPointer converts the schema's project_id into the pointer
// FindBudget/SetBudget key on: nil for the organization-level budget, a
// pointer to the value otherwise.
func budgetProjectIDPointer(v types.String) *string {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil
	}

	s := v.ValueString()

	return &s
}

// budgetFoundAfterWrite centralizes the error handling Create and Update
// share once SetBudget has succeeded and FindBudget has been called to read
// the result back. Returns false (having already recorded a diagnostic) when
// the caller must stop; budget is only valid when it returns true.
func budgetFoundAfterWrite(diags *diag.Diagnostics, verb string, budget *circleci.Budget, ok bool, err error) bool {
	if err != nil {
		diags.AddError(
			"Error reading back CircleCI budget after "+verb+" it",
			fmt.Sprintf("The write succeeded, but the budget could not be read back: %s", circleci.Detail(err)),
		)

		return false
	}

	if !ok {
		diags.AddError(
			"CircleCI budget missing immediately after "+verb+" it",
			"The write succeeded, but a subsequent list of the organization's budgets does not "+
				"include an entry for this scope. This is unexpected; please report it.",
		)

		return false
	}

	return budget != nil
}

// applyBudget copies an API budget into the model, keeping every field the
// route reports.
func applyBudget(model *budgetResourceModel, budget *circleci.Budget) {
	model.ID = types.StringValue(budget.BudgetID)
	model.Credits = types.Int64Value(int64(budget.Credits))
	model.EnforcementType = types.StringValue(budget.EnforcementType)
	model.Consumption = types.Int64Value(int64(budget.Consumption))
	model.Percentage = types.Float64Value(budget.Percentage)
	model.ThresholdExceeded = types.BoolValue(budget.ThresholdExceeded)

	if budget.ProjectID == nil {
		model.ProjectID = types.StringNull()
	} else {
		model.ProjectID = types.StringValue(*budget.ProjectID)
	}
}
