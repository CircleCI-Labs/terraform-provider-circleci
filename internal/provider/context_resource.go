// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                     = &contextResource{}
	_ resource.ResourceWithConfigure        = &contextResource{}
	_ resource.ResourceWithImportState      = &contextResource{}
	_ resource.ResourceWithConfigValidators = &contextResource{}
)

// contextResourceModel maps the output schema.
type contextResourceModel struct {
	OrganizationId types.String `tfsdk:"organization_id"`
	OrgId          types.String `tfsdk:"org_id"`
	Id             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

// NewContextResource is a helper function to simplify the provider implementation.
func NewContextResource() resource.Resource {
	return &contextResource{}
}

// contextResource is the resource implementation.
type contextResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *contextResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_context"
}

// Schema defines the schema for the resource.
func (r *contextResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI context. Contexts provide a mechanism for securing and sharing environment variables across projects.",
		Attributes: map[string]schema.Attribute{
			// There is no API route that moves a context between organizations, so a
			// genuine organization change must be a replacement — hence replaces:
			// true. See org_id_deprecation.go for why these are Optional+Computed
			// and why replacement is conditional on being configured: without the
			// conditional form, a practitioner who follows the deprecation notice
			// and drops organization_id would have the context destroyed and
			// recreated.
			"organization_id": deprecatedOrgIDAttribute("this context", true),
			"org_id":          orgIDAttribute("this context", true),
			"id": schema.StringAttribute{
				MarkdownDescription: "The unique identifier of the context.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the CircleCI context. Changing this value forces a new resource to be created.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the context was created.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (r *contextResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		orgIDConfigValidator(),
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *contextResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan contextResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(plan.OrganizationId, plan.OrgId)

	created, err := r.client.CreateContext(ctx, organizationID, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI context",
			circleci.Detail(err),
		)

		return
	}

	plan.Id = types.StringValue(created.ID)
	plan.Name = types.StringValue(created.Name)
	plan.CreatedAt = types.StringValue(created.CreatedAt)
	setOrgIDs(&plan.OrganizationId, &plan.OrgId, organizationID)

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *contextResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state contextResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	found, err := r.client.GetContext(ctx, state.Id.ValueString())
	if err != nil {
		// A context deleted outside Terraform is not an error: drop it from
		// state so the next plan recreates it. This is only reachable for the
		// rare case documented on GetContext (a lookup that resolves the id but
		// then fails to read it); see the 403 handling just below for the
		// common case.
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		// A context that no longer exists answers 403, not 404: every route
		// addressing a context by id maps a context that cannot be resolved —
		// deleted, in another organization, or simply inaccessible to this
		// token — to the same Forbidden response (see
		// internal/circleci/context.go's GetContext comment).
		//
		// Silently removing the resource on 403 would mean a token that merely
		// lost permission causes Terraform to recreate a live context on the
		// next apply. Erroring instead — and naming both possible causes — is
		// the safer default, matching circleci_group's Read.
		if circleci.IsUnauthorized(err) {
			resp.Diagnostics.AddError(
				"Unable to read CircleCI context "+state.Id.ValueString(),
				fmt.Sprintf(
					"The API denied access to this context. It has either been deleted outside "+
						"Terraform, or belongs to a different organization, or the configured token "+
						"lacks permission — the API returns the same response for all three and does "+
						"not distinguish them.\n\n"+
						"If the context was deleted, remove it from state with:\n"+
						"  terraform state rm %s\n\n%s",
					"circleci_context."+state.Name.ValueString(),
					circleci.Detail(err),
				),
			)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read CircleCI context "+state.Id.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	state.Id = types.StringValue(found.ID)
	state.Name = types.StringValue(found.Name)
	state.CreatedAt = types.StringValue(found.CreatedAt)
	// The organization comes from the API, not from state. The read route reports
	// org_id — see internal/circleci's GetContext for the measured shape — so this
	// is real drift detection rather than a value copied back over itself.
	//
	// This used to preserve whatever state held, on the stated grounds that "the
	// read route does not report which organization a context belongs to". That was
	// false. Preserving state's copy meant a wrong organization, however it got
	// into state, was never corrected by a refresh: it survived every plan and kept
	// forcing a replacement, because org_id replaces the resource when configured.
	//
	// Fall back to state only when the API reports no org_id at all, which Cloud
	// never does. That is a guard for a deployment this has not been measured
	// against, not a known case: overwriting a good state value with an empty
	// string would be worse than leaving it alone.
	organizationID := found.OrgID
	if organizationID == "" {
		organizationID = effectiveOrgID(state.OrganizationId, state.OrgId)
	}

	// Whichever name state holds is mirrored onto the other, so a configuration
	// written against either one is stable — see org_id_deprecation.go.
	setOrgIDs(&state.OrganizationId, &state.OrgId, organizationID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is unreachable for a real configuration change: every writable attribute
// forces replacement, so Terraform never calls this for anything but a
// refresh-driven re-apply of an unchanged plan. It persists the plan so that case
// is a no-op rather than leaving the prior state's zero-value fields behind, and
// mirrors the organization onto both attribute names so that the unconfigured one
// is never left unknown in state. See org_id_deprecation.go.
func (r *contextResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan contextResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	setOrgIDs(&plan.OrganizationId, &plan.OrgId, effectiveOrgID(plan.OrganizationId, plan.OrgId))

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *contextResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state contextResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteContext(ctx, state.Id.ValueString())
	if err != nil {
		// Already gone is the desired end state. 403 counts as gone here,
		// unlike in Read: DeleteContext documents that a missing context
		// answers 403 the same way GetContext does, so it is the response an
		// already-deleted context produces on delete.
		// A destroy is not expected to distinguish "gone" from "never had
		// permission" the way a refresh is, because there is no live resource
		// left to protect either way.
		if circleci.IsNotFound(err) || circleci.IsUnauthorized(err) {
			return
		}

		resp.Diagnostics.AddError(
			"Error deleting CircleCI context",
			circleci.Detail(err),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *contextResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports a context by id.
//
// Accepted forms:
//
//	CONTEXT_ID                  — preferred; the organization is read from the API
//	ORGANIZATION_ID/CONTEXT_ID  — the documented composite form, still supported
//
// THE ORGANIZATION ALWAYS COMES FROM THE API. It is never taken from the import
// id, because org_id forces replacement: this used to split the composite id and
// write the caller's first field into state with no validation at all, so a single
// mistyped digit produced a successful-looking import followed by a plan reading
// "1 to add, 1 to destroy" — destroying a live context and taking every
// environment variable and restriction on it along with it. Nothing in that
// sequence looked like an error to the practitioner; the plan looked like a
// legitimate replacement.
//
// When the composite form is used, the supplied organization is treated as an
// assertion to CHECK, not as a value to store. If it disagrees with the API the
// import fails and names both, because either one could be the typo: preferring
// the API silently would hide that the practitioner is importing a context they
// did not mean to, and preferring the caller's is the original bug.
func (r *contextResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	suppliedOrgID, contextID := splitContextImportID(req.ID)

	if contextID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID Format",
			fmt.Sprintf(
				"Expected a context id, or 'organization_id/context_id'. Got: %q.\n\n"+
					"A bare context id is enough — the organization is read from the API.",
				req.ID,
			),
		)

		return
	}

	// The read route is the authority on which organization owns this context.
	found, err := r.client.GetContext(ctx, contextID)
	if err != nil {
		if circleci.IsUnauthorized(err) {
			resp.Diagnostics.AddError(
				"Unable to import CircleCI context "+contextID,
				fmt.Sprintf(
					"The API denied access to this context. Either no context has that id, "+
						"or it belongs to a different organization, or the configured token "+
						"lacks permission — the API returns the same response for all three "+
						"and does not distinguish them.\n\n"+
						"Check that the id is a context id (not an organization id) and that "+
						"it was copied whole.\n\n%s",
					circleci.Detail(err),
				),
			)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to import CircleCI context "+contextID,
			circleci.Detail(err),
		)

		return
	}

	organizationID := found.OrgID

	switch {
	case organizationID == "" && suppliedOrgID == "":
		// Only reachable on a deployment whose read route omits org_id, which
		// Cloud does not. Ask for the composite form rather than writing an
		// empty organization into state.
		resp.Diagnostics.AddError(
			"Unable to determine the organization for CircleCI context "+contextID,
			"The API did not report an org_id for this context, so the organization "+
				"cannot be verified. Import using 'organization_id/context_id' instead.",
		)

		return

	case organizationID == "":
		// Nothing to check it against; trust the caller, as the old code always
		// did, but only in the one case where there is no alternative.
		organizationID = suppliedOrgID

	case suppliedOrgID != "" && suppliedOrgID != organizationID:
		resp.Diagnostics.AddError(
			"Import ID organization does not match the API",
			fmt.Sprintf(
				"The import id gives organization %q, but the API reports that context %s "+
					"is owned by organization %q.\n\n"+
					"One of the two is wrong, and importing either way would be unsafe: "+
					"org_id forces replacement, so storing the wrong organization would make "+
					"the next plan destroy and recreate this context, losing its environment "+
					"variables and restrictions.\n\n"+
					"Import using just the context id to take the organization from the API:\n"+
					"  terraform import circleci_context.example %s",
				suppliedOrgID, contextID, organizationID, contextID,
			),
		)

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), contextID)...)
	// Both organization attribute names are set, so a configuration written
	// against either one imports cleanly. See org_id_deprecation.go.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), organizationID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("org_id"), organizationID)...)
}

// splitContextImportID splits an import id into its optional organization and its
// context id.
//
// A bare id is a context id: that is the form worth optimising for, since the
// organization is read from the API either way. Anything with a slash is the
// documented composite form.
func splitContextImportID(importID string) (organizationID, contextID string) {
	organizationID, contextID, found := strings.Cut(importID, "/")
	if !found {
		return "", organizationID
	}

	return organizationID, contextID
}
