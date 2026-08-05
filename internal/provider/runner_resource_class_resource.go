// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Runner administration is served by a separate origin (https://runner.circleci.com
// on Cloud, the Server host on CircleCI Server) and is plumbed through the
// provider's runner_host attribute into circleci.Client's runnerHost.
//
// Canonical surface: every runner resource and data source in this provider
// targets the flat `{runner_host}/api/v3/runner/...` surface (`/runner/resource`,
// `/runner/token`, `/runner/tasks`, `/runner` — see internal/circleci/runner.go),
// which upstream calls the "legacy" one. The newer surface —
// `/api/v3/runner/resource-classes` (plural, with an `/update` action),
// `/runner/tokens`, `/runner/agents`, `/runner/metrics` — is designated canonical
// upstream and is the one CircleCI Cloud's public API proxies to. Both are
// served by the same backend behind the same ingress, so both are reachable
// at runner_host.
//
// Staying on the flat surface is still deliberate, for reasons stronger than "the
// new one is in flux":
//
//   - It is what CircleCI Server serves, and runner_host exists so one
//     configuration works on Cloud and Server alike.
//   - The newer agents route drops fields circleci_runners exposes today: it has
//     no hostname, no ip and no last_used, and replaces the status string with an
//     is_busy boolean. Migrating would be a breaking change to that data source
//     for less data.
//   - It requires a different request and response shape throughout — a
//     `{"data": {"attributes": …, "references": …}}` envelope, `filter[org_id]`
//     and `filter[resource_class]` query parameters, and an org supplied as
//     `references.org.id` rather than a body field — and it rejects unrecognised
//     request fields outright, unlike the flat surface, which ignores them.
//
// Revisit if the flat surface is announced for removal, not merely superseded.

// runnerOrgIDPattern recognises the UUID that the runner API expects for
// organization identifiers. The runner API takes a UUID only — it does not accept
// an organization slug — so catching the wrong shape in the plan gives a better
// error than the API's 400.
var runnerOrgIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// runnerResourceClassPattern recognises a "namespace/name" resource class.
//
// It mirrors the API's own resource-class validation rules, rather than
// merely checking for one slash. Those rules are:
//
//   - exactly one "/", splitting the value into a namespace and a class;
//   - no "." anywhere in the value — the API rejects the whole string on
//     sight of one, because a dot is how a fully qualified id separates the
//     executor ("runner.acme/linux") and callers must not supply that part;
//   - the namespace matches `^[a-z0-9_-]+$` — lower case only;
//   - the class matches `^[a-zA-Z0-9:_+.-]+$`, minus the dot the rule above
//     already forbids, so mixed case is fine here but not in the namespace.
//
// The looser `^[^/]+/[^/]+$` this replaced accepted two shapes the API then
// refused with an opaque HTTP 400 at apply time: an upper-case namespace
// ("MyOrg/linux") and any dotted name ("acme/ubuntu-22.04"). Catching the wrong
// shape while the plan is being made is the whole point of having the check, so
// the check has to know the same rules the service does.
var runnerResourceClassPattern = regexp.MustCompile(`^[a-z0-9_-]+/[a-zA-Z0-9:_+-]+$`)

// runnerResourceClassFormatMessage explains runnerResourceClassPattern. The
// namespace's case rule is called out because it is the half practitioners get
// wrong: an organization's display name is usually capitalised and its runner
// namespace is not.
const runnerResourceClassFormatMessage = "must be in the format 'namespace/name', " +
	"where the namespace is lower-case letters, digits, '_' or '-', and neither part contains a '.'"

// runnerNamespacePattern recognises a bare runner namespace, mirroring the
// API's own validation: it rejects any value containing "." or "/" outright
// and then applies the same lower-case namespace rule as
// runnerResourceClassPattern. Without it a namespace filter with the wrong shape
// reached the API and came back as HTTP 400 "invalid namespace".
var runnerNamespacePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// runnerNamespaceFormatMessage explains runnerNamespacePattern.
const runnerNamespaceFormatMessage = "must be a runner namespace: lower-case letters, digits, " +
	"'_' or '-', with no '.' or '/'"

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                     = &runnerResourceClassResource{}
	_ resource.ResourceWithConfigure        = &runnerResourceClassResource{}
	_ resource.ResourceWithImportState      = &runnerResourceClassResource{}
	_ resource.ResourceWithConfigValidators = &runnerResourceClassResource{}
	_ resource.ResourceWithModifyPlan       = &runnerResourceClassResource{}
)

// runnerResourceClassResourceModel maps the resource schema.
type runnerResourceClassResourceModel struct {
	Id             types.String `tfsdk:"id"`
	OrganizationId types.String `tfsdk:"organization_id"`
	OrgId          types.String `tfsdk:"org_id"`
	ResourceClass  types.String `tfsdk:"resource_class"`
	Description    types.String `tfsdk:"description"`
	ForceDelete    types.Bool   `tfsdk:"force_delete"`
}

// NewRunnerResourceClassResource is a helper function to simplify the provider implementation.
func NewRunnerResourceClassResource() resource.Resource {
	return &runnerResourceClassResource{}
}

// runnerResourceClassResource is the resource implementation.
type runnerResourceClassResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *runnerResourceClassResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_runner_resource_class"
}

// Schema defines the schema for the resource.
func (r *runnerResourceClassResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// replaces is false here, unlike every other resource carrying this attribute
	// pair: organization_id has never forced replacement on a runner resource
	// class, and the deprecation is not the place to start. The owning
	// organization is derived from resource_class's namespace by the service
	// anyway (see Create), so a changed organization_id is bookkeeping that the
	// existing no-op Update already absorbs, and turning it into a destroy would
	// be a gratuitous breaking change. See org_id_deprecation.go.
	//
	// The builders take no validators — no other resource needs one — so the
	// runner API's UUID-only shape check is attached here. Catching the wrong
	// shape at plan time gives a better error than the API's opaque 400, and both
	// spellings must enforce it or the check would be trivially bypassed by using
	// the new name.
	orgIDValidators := []validator.String{
		stringvalidator.RegexMatches(runnerOrgIDPattern, "must be an organization UUID"),
	}

	deprecatedOrganizationID := deprecatedOrgIDAttribute("this runner resource class", false)
	deprecatedOrganizationID.Validators = orgIDValidators

	orgID := orgIDAttribute("this runner resource class", false)
	orgID.Validators = orgIDValidators

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI runner resource class.\n\n" +
			"~> **A namespace holds at most 650 resource classes.** Creating one past the limit is " +
			"refused with HTTP 403 and a message naming it. The limit counts the whole namespace, " +
			"including resource classes created outside Terraform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the runner resource class.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"organization_id": deprecatedOrganizationID,
			"org_id":          orgID,
			"resource_class": schema.StringAttribute{
				MarkdownDescription: "The resource class name in `namespace/name` format (e.g. `myorg/myrunner`). Changing this value forces a new resource to be created.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(runnerResourceClassPattern, runnerResourceClassFormatMessage),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description of the runner resource class. Changing this value " +
					"forces a new resource to be created: the runner API has no update route for a " +
					"resource class at all (see CreateResourceClass and DeleteResourceClass in " +
					"internal/circleci/runner.go — there is no method between them), so without this " +
					"modifier an edited description planned an in-place update, Update() had nothing to " +
					"call and silently persisted the plan into state, and the next Read then overwrote it " +
					"back to the API's unchanged value — reporting apply success while changing nothing.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					// UseStateForUnknown must run first: it resolves an unconfigured
					// description to its prior state value before RequiresReplaceIfConfigured
					// ever compares anything, which is what keeps a merely-unknown value (for
					// example while organization_id is still being computed) from planning a
					// spurious replacement. RequiresReplaceIfConfigured rather than plain
					// RequiresReplace for the same reason org_id_deprecation.go gives: this
					// attribute is Optional+Computed, so a plain RequiresReplace would also fire
					// on the value UseStateForUnknown itself retains, if modifier order ever
					// changed. Conditioning on the configuration, not on any diff, is what makes
					// this safe independent of ordering.
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"force_delete": schema.BoolAttribute{
				MarkdownDescription: "If true, deletes the resource class even if it has associated tokens.",
				Optional:            true,
				Computed:            false,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (r *runnerResourceClassResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		orgIDConfigValidator(),
	}
}

// ModifyPlan makes both organization attribute names agree in the plan.
//
// This is needed here and nowhere else, because this is the only resource that
// passes replaces: false, and the two halves of org_id_deprecation.go's design do
// not compose without it:
//
//   - Both attributes are Optional+Computed, so when one leaves the configuration
//     Terraform plans the value it already had rather than null. That is what makes
//     switching between the two names a no-op.
//   - On the resources that replace, a genuine organization change is a destroy and
//     create, so Create sees a fresh plan and the retained value never matters.
//
// Without replacement, a genuine organization change is an in-place update — and
// then the retained value does matter, because it is stale. Changing
// organization_id from A to B plans organization_id = B (from configuration) and
// org_id = A (retained from state), and no Update can fix that: writing B to both
// contradicts the plan's org_id, writing A to both contradicts the plan's
// organization_id, and persisting the plan verbatim leaves state claiming two
// different organizations, which the next Read then resolves to the stale one.
// Terraform rejects the first two outright with "Provider produced inconsistent
// result after apply".
//
// The reconciliation therefore has to happen while the plan is still being made,
// which is the one place the retained value can be corrected. The configuration is
// the authority on which name is in use, so it is read from there rather than from
// the plan, where the two are indistinguishable.
func (r *runnerResourceClassResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Ask before reading the configuration: a destroy plans a null config, and
	// Get-ing that into the model fails with a value-conversion error.
	if !orgIDPlanNeedsReconcile(req) {
		return
	}

	var config runnerResourceClassResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Shared, because any resource built with `replaces: false` needs exactly this
	// and the reason is not obvious from the schema. See org_id_deprecation.go.
	reconcileOrgIDPlan(ctx, resp, config.OrganizationId, config.OrgId)
}

// Create creates the resource and sets the initial Terraform state.
func (r *runnerResourceClassResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan runnerResourceClassResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(plan.OrganizationId, plan.OrgId)

	// The schema requires an organization, so one is always sent here even though
	// the create route derives the owning org from resource_class's namespace and
	// never reads org_id from the body — see circleci.ResourceClassInput's doc
	// comment.
	createReq := circleci.ResourceClassInput{
		OrganizationID: organizationID,
		ResourceClass:  plan.ResourceClass.ValueString(),
		Description:    plan.Description.ValueString(),
	}

	rc, err := r.client.CreateResourceClass(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI runner resource class",
			circleci.Detail(err),
		)
		return
	}

	plan.Id = types.StringValue(rc.ID)
	plan.ResourceClass = types.StringValue(rc.ResourceClass)
	plan.Description = types.StringValue(rc.Description)
	setOrgIDs(&plan.OrganizationId, &plan.OrgId, organizationID)

	diags = resp.State.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
}

// Read refreshes the Terraform state with the latest data.
func (r *runnerResourceClassResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state runnerResourceClassResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	rcName := state.ResourceClass.ValueString()
	slashIdx := strings.Index(rcName, "/")
	if slashIdx == -1 {
		resp.Diagnostics.AddError(
			"Invalid resource_class format",
			fmt.Sprintf("Expected namespace/name format, got: %s", rcName),
		)
		return
	}
	namespace := rcName[:slashIdx]

	// Scope the list by organization as well as namespace. Neither organization
	// attribute is set immediately after an import (the organization is not part
	// of the import ID), in which case the namespace filter alone is used.
	organizationID := effectiveOrgID(state.OrganizationId, state.OrgId)

	classes, err := r.client.ListResourceClasses(ctx, namespace, organizationID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading CircleCI runner resource classes",
			"Could not list runner resource classes for namespace "+namespace+": "+circleci.Detail(err),
		)
		return
	}

	var found *circleci.ResourceClass
	for i := range classes {
		if classes[i].ResourceClass == rcName {
			found = &classes[i]
			break
		}
	}

	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.Id = types.StringValue(found.ID)
	state.ResourceClass = types.StringValue(found.ResourceClass)
	state.Description = types.StringValue(found.Description)
	// ForceDelete is not returned by the API — preserve value from state.

	// The resource class representation carries no organization, so whichever
	// attribute name state holds is mirrored onto the other. The guard matters
	// after an import, where neither name is known: writing "" over two null
	// values would be a spurious change. See org_id_deprecation.go.
	if organizationID != "" {
		setOrgIDs(&state.OrganizationId, &state.OrgId, organizationID)
	}

	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

// Update persists plan values (the organization and force_delete) into state. The
// runner API has no update endpoint, so no API call is made.
func (r *runnerResourceClassResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan runnerResourceClassResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The plan is persisted verbatim, including both organization attribute names:
	// ModifyPlan has already made them agree, and re-deriving them here could only
	// disagree with the plan Terraform is holding. That also covers the update
	// after an import, where neither name is in state and the unconfigured one
	// would otherwise still be unknown.
	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *runnerResourceClassResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state runnerResourceClassResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteResourceClass(ctx, state.Id.ValueString(), state.ForceDelete.ValueBool())
	// A resource class already gone is the desired end state, so absence is not
	// an error.
	if err != nil && !circleci.IsNotFound(err) {
		resp.Diagnostics.AddError(
			"Error deleting CircleCI runner resource class",
			"Could not delete runner resource class "+state.Id.ValueString()+": "+circleci.Detail(err),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *runnerResourceClassResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing resource class into Terraform state.
// The import ID is the resource_class string (e.g. "myorg/myrunner").
// After import, Read is called to populate the full state.
func (r *runnerResourceClassResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_class"), req.ID)...)
}
