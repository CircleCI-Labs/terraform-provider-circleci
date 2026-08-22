// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
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
	_ resource.Resource                     = &orbNamespaceResource{}
	_ resource.ResourceWithConfigure        = &orbNamespaceResource{}
	_ resource.ResourceWithImportState      = &orbNamespaceResource{}
	_ resource.ResourceWithConfigValidators = &orbNamespaceResource{}
)

// orbNamespaceTypeName is used in diagnostics, including the Cloud-only error.
const orbNamespaceTypeName = "circleci_orb_namespace"

// orbUUIDPattern recognises a UUID, so that composite import IDs can tell a
// namespace id from a namespace name. Namespace names are lowercase, may contain
// hyphens, and are never in this shape.
var orbUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// orbIsUUID reports whether s looks like a UUID.
func orbIsUUID(s string) bool { return orbUUIDPattern.MatchString(s) }

// namespaceForbiddenDetail annotates a namespace rename or delete failure with
// why retrying will not help, the same shape orb_version_resource.go's
// publishFailureDetail follows for a different immutable-by-design failure.
//
// [NET]: every rename and every delete this provider's author attempted
// against a live account answered 403 Forbidden — on a namespace the calling
// organization had just created, and on one belonging to an unrelated
// organization — and left the namespace unchanged. CircleCI's support
// documentation (https://support.circleci.com/hc/en-us/articles/21518826780827,
// "Transferring and Renaming Namespaces") says a rename or transfer requires a
// support ticket, and no equivalent self-service delete exists at all. This
// investigation found no account permission that changed the answer, so a 403
// here is treated as permanent rather than as a transient authorization gap.
func namespaceForbiddenDetail(err error) string {
	detail := circleci.Detail(err)
	if !circleci.HasStatus(err, http.StatusForbidden) {
		return detail
	}

	return detail + "\n\nCircleCI does not offer this as a self-service API call: every account this " +
		"provider has been able to test received the same 403 response. Renaming or deleting an orb " +
		"namespace requires a CircleCI support ticket; see " +
		"https://support.circleci.com/hc/en-us/articles/21518826780827. The namespace was not changed."
}

// orbNamespaceResourceModel maps the resource schema.
type orbNamespaceResourceModel struct {
	Id             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	OrganizationId types.String `tfsdk:"organization_id"`
	OrgId          types.String `tfsdk:"org_id"`
}

// NewOrbNamespaceResource is a helper function to simplify the provider implementation.
func NewOrbNamespaceResource() resource.Resource {
	return &orbNamespaceResource{}
}

// orbNamespaceResource is the resource implementation.
type orbNamespaceResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *orbNamespaceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_orb_namespace"
}

// Schema defines the schema for the resource.
func (r *orbNamespaceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI orb registry namespace.\n\n" +
			"A namespace is the globally unique prefix that owns a set of orbs, as in " +
			"`<namespace>/<orb>`. An organization may own exactly one namespace, and the name " +
			"is claimed across all of CircleCI, not just within the organization.\n\n" +
			"A namespace is also what a self-hosted runner resource class is named after: a " +
			"resource class is `<namespace>/<class>`, so this resource is how you create the " +
			"namespace that `circleci_runner_resource_class` needs.\n\n" +
			"!> **Renaming and deleting a namespace are not self-service.** Every account this " +
			"provider's author was able to test received a 403 Forbidden response when attempting " +
			"either through the API, on namespaces both inside and outside the calling account's " +
			"organization. CircleCI's support documentation describes renaming or transferring a " +
			"namespace as a support-ticket process, not an API call, and no equivalent process is " +
			"documented for deleting one at all. Practically: treat `name` as permanent once " +
			"applied, and expect `terraform destroy` on this resource to fail rather than remove " +
			"the namespace — it does not silently report a removal that did not happen.\n\n" +
			"~> **CircleCI Cloud only.** Namespaces are served by the CircleCI v3 API, which " +
			"CircleCI Server does not route. Using this resource against a provider configured " +
			"with `deployment = \"server\"` fails with an explicit error.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the namespace.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The namespace name, unique across all of CircleCI. " +
					"Changing this attempts to rename the namespace in place rather than replacing " +
					"it: if the API ever accepts the rename, the namespace keeps its id and its " +
					"orbs. In practice every account tested against received 403 Forbidden for this " +
					"call — see the resource description — so plan on this attribute being " +
					"effectively immutable once applied, not on the rename succeeding.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[^/\s]+$`),
						"must not contain a slash or whitespace; it is the prefix of `<namespace>/<orb>`",
					),
				},
			},
			// See org_id_deprecation.go for why these are Optional+Computed and why
			// replacement is conditional on being configured.
			"organization_id": deprecatedOrgIDAttribute("this namespace", true),
			"org_id":          orgIDAttribute("this namespace", true),
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *orbNamespaceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (r *orbNamespaceResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		orgIDConfigValidator(),
	}
}

// Create creates the namespace and sets the initial Terraform state.
func (r *orbNamespaceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireCloud(r.client, orbNamespaceTypeName, &resp.Diagnostics) {
		return
	}

	var plan orbNamespaceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(plan.OrganizationId, plan.OrgId)

	ns, err := r.client.CreateNamespace(ctx, circleci.CreateNamespaceRequest{
		Name:           plan.Name.ValueString(),
		OrganizationID: organizationID,
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to create CircleCI orb namespace "+plan.Name.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	plan.Id = types.StringValue(ns.ID)
	plan.Name = types.StringValue(ns.Name)
	setOrgIDs(&plan.OrganizationId, &plan.OrgId, organizationID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
//
// It looks the namespace up by id when one is known, and by name otherwise,
// which is what makes importing by either form work.
func (r *orbNamespaceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireCloud(r.client, orbNamespaceTypeName, &resp.Diagnostics) {
		return
	}

	var state orbNamespaceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var (
		ns  *circleci.Namespace
		err error
	)
	if id := state.Id.ValueString(); id != "" {
		ns, err = r.client.GetNamespaceByID(ctx, id)
	} else {
		ns, err = r.client.GetNamespace(ctx, state.Name.ValueString())
	}

	if circleci.IsNotFound(err) {
		// Deleted outside Terraform: drop it so the next plan recreates it.
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI orb namespace",
			circleci.Detail(err),
		)

		return
	}

	state.Id = types.StringValue(ns.ID)
	state.Name = types.StringValue(ns.Name)
	// The organization is not part of the namespace representation, so it is
	// carried over from state rather than refreshed. Both attribute names are
	// written, so an import that supplied only organization_id still leaves
	// org_id populated. See org_id_deprecation.go.
	setOrgIDs(&state.OrganizationId, &state.OrgId, effectiveOrgID(state.OrganizationId, state.OrgId))

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update renames the namespace in place.
//
// The rename route's shape is a real update: a namespace it succeeds against
// keeps its id and its orbs, so name is not marked RequiresReplace — and must
// not be, because RequiresReplace would mean Terraform destroying the old
// namespace before creating the new one, which is the one outcome this
// resource must never risk. organization_id is the only other attribute and it
// does force replacement, so a rename is all that can reach here.
//
// [NET]: this investigation never observed the route accept a rename — every
// attempt against a live account answered 403. See namespaceForbiddenDetail.
// name staying non-replacing is what keeps that 403 a clean, no-op failure
// instead of a destroyed namespace.
func (r *orbNamespaceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !requireCloud(r.client, orbNamespaceTypeName, &resp.Diagnostics) {
		return
	}

	var plan, state orbNamespaceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.Id = state.Id
	setOrgIDs(&plan.OrganizationId, &plan.OrgId, effectiveOrgID(plan.OrganizationId, plan.OrgId))

	if plan.Name.ValueString() == state.Name.ValueString() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

		return
	}

	ns, err := r.client.RenameNamespace(ctx, state.Id.ValueString(), plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Unable to rename CircleCI orb namespace %s to %s",
				state.Name.ValueString(), plan.Name.ValueString()),
			namespaceForbiddenDetail(err),
		)

		return
	}

	resp.Diagnostics.AddWarning(
		"CircleCI orb namespace renamed",
		fmt.Sprintf(
			"The namespace %q is now %q. Orbs in it are reachable only under the new name, so "+
				"any configuration that still references %q or a runner resource class named "+
				"%q will no longer resolve.",
			state.Name.ValueString(), ns.Name,
			state.Name.ValueString()+"/<orb>",
			state.Name.ValueString()+"/<class>",
		),
	)

	plan.Id = types.StringValue(ns.ID)
	plan.Name = types.StringValue(ns.Name)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete asks the API to delete the namespace, and with it every orb it owns
// — if the API agrees, which [NET] this investigation never saw it do; see
// DeleteNamespace and namespaceForbiddenDetail. Terraform leaves a resource in
// state whenever Delete reports an error, so a 403 here correctly fails
// `terraform destroy` rather than reporting a removal that did not happen.
func (r *orbNamespaceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireCloud(r.client, orbNamespaceTypeName, &resp.Diagnostics) {
		return
	}

	var state orbNamespaceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteNamespace(ctx, state.Id.ValueString())
	if circleci.IsNotFound(err) {
		// Already gone; deleting is still a success.
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to delete CircleCI orb namespace "+state.Name.ValueString(),
			namespaceForbiddenDetail(err),
		)
	}
}

// ImportState imports an existing namespace.
//
// The import id is "<organization_id>/<namespace_name>" or
// "<organization_id>/<namespace_id>". The organization is part of the id because
// the API's namespace representation does not include it, so it cannot be
// discovered during the read that follows: importing without it would leave
// organization_id null and the next plan would then want to replace the
// namespace, destroying it.
func (r *orbNamespaceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	organizationID, ref, ok := strings.Cut(req.ID, "/")
	if !ok || organizationID == "" || ref == "" || strings.Contains(ref, "/") {
		resp.Diagnostics.AddError(
			"Invalid import ID for circleci_orb_namespace",
			fmt.Sprintf(
				"Expected \"<organization_id>/<namespace_name>\" or \"<organization_id>/<namespace_id>\", got %q.\n\n"+
					"The organization id is required because the CircleCI API does not report which "+
					"organization owns a namespace, so it cannot be filled in by the read that "+
					"follows the import.",
				req.ID,
			),
		)

		return
	}

	// Both organization attribute names are set, so a configuration written
	// against either one imports cleanly. See org_id_deprecation.go.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), organizationID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("org_id"), organizationID)...)

	// Read resolves whichever of the two is set.
	if orbIsUUID(ref) {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), ref)...)

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), ref)...)
}
