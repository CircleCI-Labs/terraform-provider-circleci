// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
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
	_ resource.Resource                = &organizationContactsResource{}
	_ resource.ResourceWithConfigure   = &organizationContactsResource{}
	_ resource.ResourceWithImportState = &organizationContactsResource{}
	_ resource.ResourceWithModifyPlan  = &organizationContactsResource{}
)

// organizationContactsTypeName is the Terraform type name, used both for
// Metadata and for the Cloud-only error.
const organizationContactsTypeName = "circleci_organization_contacts"

// organizationContactsMaxAddresses is the most addresses the service accepts
// in either list. See OrganizationContacts.
const organizationContactsMaxAddresses = 5

// organizationContactsResourceModel maps the resource schema.
//
// There is no separate id: this is a singleton per organization, so org_id is
// the whole identity. Both lists are Required rather than Optional+Computed:
// GET/PUT /api/private/organization/{orgID}/contacts is a full replacement of
// both lists together (see circleci.SetOrganizationContacts), with no route to
// update one list while leaving the other alone. Making either attribute
// sparse would need this resource to read the live value of the *other* list
// before every write just to avoid clobbering it, for a resource whose whole
// purpose is owning both. Full ownership, required attributes, is the simpler
// and more honest contract.
type organizationContactsResourceModel struct {
	OrgID    types.String `tfsdk:"org_id"`
	Primary  types.Set    `tfsdk:"primary_contacts"`
	Security types.Set    `tfsdk:"security_contacts"`
}

// NewOrganizationContactsResource is a helper function to simplify the
// provider implementation.
func NewOrganizationContactsResource() resource.Resource {
	return &organizationContactsResource{}
}

// organizationContactsResource manages an organization's technical (primary)
// and security contact email lists.
//
// It is a settings resource, not a thing that gets created and destroyed: the
// lists exist for as long as the organization does (defaulting to empty), and
// this resource only ever reads and overwrites them in full. See Delete.
type organizationContactsResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *organizationContactsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_contacts"
}

// Schema defines the schema for the resource.
func (r *organizationContactsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	contactList := func(kind string) schema.SetAttribute {
		return schema.SetAttribute{
			MarkdownDescription: fmt.Sprintf(
				"%s contact email addresses for the organization. At most %d; CircleCI rejects a "+
					"write that would exceed that.\n\n"+
					"This is a set, not a list: CircleCI does not preserve the order addresses were "+
					"submitted in, and reports them back in an order of its own choosing. Declaring "+
					"this as an ordered list would show a difference on every plan with nothing to "+
					"apply, the same bug fixed for `circleci_webhook`'s `events`.",
				kind, organizationContactsMaxAddresses,
			),
			Required:    true,
			ElementType: types.StringType,
			Validators: []validator.Set{
				setvalidator.SizeAtMost(organizationContactsMaxAddresses),
			},
		}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI organization's technical (primary) and security contact " +
			"email lists.\n\n" +
			"~> **CircleCI Cloud only, and served by an unofficial route.** " +
			"`GET`/`PUT /api/private/organization/{orgID}/contacts` carries no published specification " +
			"and is not part of the versioned public API; a CircleCI Server installation's gateway does " +
			"not route `/api/private` at all, only Cloud serves it, and CircleCI may change or remove it " +
			"without notice.\n\n" +
			"This is a settings resource rather than a thing that gets created and destroyed: both " +
			"lists exist for as long as the organization does, defaulting to empty. `terraform destroy` " +
			"only stops Terraform from tracking them; CircleCI keeps whichever lists were last applied. " +
			"See the two attributes below for why both are required rather than independently " +
			"optional.",
		Attributes: map[string]schema.Attribute{
			"org_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization these contact lists " +
					"belong to. Changing this value forces a new resource to be created, rather than " +
					"moving management to a different organization's existing lists.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"primary_contacts":  contactList("Technical (primary)"),
			"security_contacts": contactList("Security"),
		},
	}
}

// Create writes the configured contact lists.
//
// Nothing is created: the lists already exist, defaulting to empty. This is a
// PUT of both lists in full, exactly what the configuration says, since this
// resource owns both lists entirely.
func (r *organizationContactsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil || !requireCloud(r.client, organizationContactsTypeName, &resp.Diagnostics) {
		return
	}

	var plan organizationContactsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := plan.OrgID.ValueString()

	contacts, diags := organizationContactsFromModel(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	updated, err := r.client.SetOrganizationContacts(ctx, orgID, contacts)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error setting CircleCI organization contacts",
			fmt.Sprintf(
				"Could not set contact lists for organization %s: %s\n\nEach list may hold at most %d "+
					"addresses.",
				orgID, circleci.Detail(err), organizationContactsMaxAddresses,
			),
		)

		return
	}

	resp.Diagnostics.Append(applyOrganizationContacts(ctx, &plan, updated)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *organizationContactsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil || !requireCloud(r.client, organizationContactsTypeName, &resp.Diagnostics) {
		return
	}

	var state organizationContactsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := state.OrgID.ValueString()

	contacts, err := r.client.GetOrganizationContacts(ctx, orgID)
	if err != nil {
		// The organization itself is gone, so there is nothing left to manage.
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Error reading CircleCI organization contacts",
			fmt.Sprintf("Could not read contact lists for organization %s: %s", orgID, circleci.Detail(err)),
		)

		return
	}

	resp.Diagnostics.Append(applyOrganizationContacts(ctx, &state, contacts)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update writes the configured contact lists. Every attribute is updatable in
// place; org_id carries RequiresReplace so Update never sees it change.
func (r *organizationContactsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil || !requireCloud(r.client, organizationContactsTypeName, &resp.Diagnostics) {
		return
	}

	var plan organizationContactsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := plan.OrgID.ValueString()

	contacts, diags := organizationContactsFromModel(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	updated, err := r.client.SetOrganizationContacts(ctx, orgID, contacts)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error setting CircleCI organization contacts",
			fmt.Sprintf(
				"Could not set contact lists for organization %s: %s\n\nEach list may hold at most %d "+
					"addresses.",
				orgID, circleci.Detail(err), organizationContactsMaxAddresses,
			),
		)

		return
	}

	resp.Diagnostics.Append(applyOrganizationContacts(ctx, &plan, updated)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the resource from Terraform state without calling the API.
//
// This does not gate on requireCloud, unlike Create/Read/Update/ModifyPlan.
// Nothing here calls the client at all, so there is no failure mode to make
// "consistent" by rejecting it, and rejecting it would actively hurt: a
// contacts resource is exactly the kind of thing left stranded in state by an
// organization migrating from Cloud to Server (the scenario this whole
// capability exists for — see the org-migration CLI referenced from
// org_contacts.go), and it must stay removable rather than becoming
// permanently unmanageable because the provider's `deployment` was changed to
// match.
//
// CircleCI does have a route that would clear both lists (PUT with two empty
// arrays), but Delete deliberately does not call it: unlike an object with its
// own lifecycle, these lists are a facet of the organization that exists
// whether or not this resource ever did, and `terraform destroy` should mean
// "stop tracking this", not "wipe the organization's security contacts",
// matching circleci_organization_settings and circleci_notification_preferences.
func (r *organizationContactsResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(
		"CircleCI organization contacts left in place",
		fmt.Sprintf(
			"%s was removed from Terraform state, but CircleCI has no route to reset a contact list, "+
				"so both lists keep the values Terraform last applied. Set them explicitly (as empty "+
				"lists, if desired) if that is not what you want.",
			organizationContactsTypeName,
		),
	)
}

// Configure adds the provider configured client to the resource.
func (r *organizationContactsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports the contact lists of an existing organization by its id.
func (r *organizationContactsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("org_id"), req.ID)...)
}

// ModifyPlan rejects CircleCI Server at plan time rather than at apply time.
// Destroy is exempt so a resource stranded in state by a deployment change
// stays removable (Delete does not call ModifyPlan at all, but keeping the
// same destroy exemption here avoids blocking that plan before Delete is ever
// reached).
func (r *organizationContactsResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, organizationContactsTypeName, &resp.Diagnostics)
}

// organizationContactsFromModel converts the configured sets into the client's
// request shape.
func organizationContactsFromModel(ctx context.Context, model organizationContactsResourceModel) (circleci.OrganizationContacts, diag.Diagnostics) {
	var diags diag.Diagnostics

	var primary, security []string
	diags.Append(model.Primary.ElementsAs(ctx, &primary, false)...)
	diags.Append(model.Security.ElementsAs(ctx, &security, false)...)

	return circleci.OrganizationContacts{Primary: primary, Security: security}, diags
}

// applyOrganizationContacts copies an API response into the model.
func applyOrganizationContacts(
	ctx context.Context, model *organizationContactsResourceModel, contacts *circleci.OrganizationContacts,
) diag.Diagnostics {
	var diags diag.Diagnostics

	primary, primaryDiags := types.SetValueFrom(ctx, types.StringType, contacts.Primary)
	diags.Append(primaryDiags...)

	security, securityDiags := types.SetValueFrom(ctx, types.StringType, contacts.Security)
	diags.Append(securityDiags...)

	if diags.HasError() {
		return diags
	}

	model.Primary = primary
	model.Security = security

	return diags
}
