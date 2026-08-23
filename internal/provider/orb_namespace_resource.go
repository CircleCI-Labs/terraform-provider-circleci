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

// orbNamespaceSupportTicketURL is CircleCI's own documentation of the process
// for a namespace rename or transfer. No equivalent self-service process is
// documented for deletion at all — see orbNamespaceNameImmutable and Delete
// below, both of which link here.
const orbNamespaceSupportTicketURL = "https://support.circleci.com/hc/en-us/articles/21518826780827"

// orbNamespaceNameImmutable blocks a plan that would change name on a
// namespace already in state.
//
// This is deliberately not stringplanmodifier.RequiresReplace. Replacing this
// resource means destroying the old namespace before creating the new one,
// and [NET, measured against a live organization-admin token] CircleCI's
// delete route answers 403 Forbidden unconditionally — see Namespace's doc
// comment in internal/circleci/namespace.go — so Delete below does not even
// attempt it any more. A RequiresReplace plan here would promise a
// destroy-then-create that can never finish cleanly: the destroy cannot
// actually remove the old namespace, so the create that follows would try to
// claim a name CircleCI still considers taken. Refusing the rename outright,
// before either half of that plan is attempted, is the only shape that cannot
// leave a namespace half-migrated or a practitioner staring at a plan that
// applies but does not do what it says.
type orbNamespaceNameImmutable struct{}

func (orbNamespaceNameImmutable) Description(_ context.Context) string {
	return "Namespace names cannot be changed once created; renaming requires a CircleCI support ticket."
}

func (m orbNamespaceNameImmutable) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (orbNamespaceNameImmutable) PlanModifyString(
	_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse,
) {
	// A null state value means this is Create: there is no prior name to
	// protect yet. An unknown plan value means it depends on something else
	// in this plan not yet resolved; Terraform re-plans before apply once it
	// is, and this modifier runs again then.
	if req.StateValue.IsNull() || req.PlanValue.IsUnknown() {
		return
	}

	if req.PlanValue.Equal(req.StateValue) {
		return
	}

	resp.Diagnostics.AddAttributeError(
		req.Path,
		"CircleCI orb namespace names cannot be changed",
		fmt.Sprintf(
			"A namespace name is a permanent, global, one-per-organization claim: %q cannot become "+
				"%q through Terraform. CircleCI's namespace rename route answers 403 Forbidden "+
				"unconditionally — measured on a namespace the calling organization had just created, "+
				"on one belonging to an unrelated organization, and even on a no-op rename of a "+
				"namespace to its own existing name — so this provider no longer attempts the call.\n\n"+
				"Renaming a namespace requires a CircleCI support ticket (%s). Revert `name` to %q "+
				"in this configuration; once support has actually renamed the namespace, update `name` "+
				"here to match and this attribute will plan as unchanged.",
			req.StateValue.ValueString(), req.PlanValue.ValueString(), orbNamespaceSupportTicketURL,
			req.StateValue.ValueString(),
		),
	)
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
			"!> **Creating a namespace is permanent.** A namespace name is a global, " +
			"one-per-organization claim with no self-service way to undo it: measured against a " +
			"live organization-admin token, CircleCI's rename and delete routes both answer " +
			"`403 Forbidden` unconditionally — including a no-op rename of a namespace to its own " +
			"existing name, and a namespace whose owning organization has since been deleted. This " +
			"resource does not call either route. Changing `name` fails at `terraform plan`, " +
			"before anything is attempted, with a diagnostic explaining why. `terraform destroy` " +
			"succeeds — the resource comes out of Terraform state — but the namespace itself, and " +
			"every orb in it, keeps existing in CircleCI; every destroy raises a WARNING naming the " +
			"namespace and saying that a CircleCI support ticket is the only way to actually rename " +
			"or remove it. That is a deliberate, announced version of `terraform destroy` reporting " +
			"success on an object that survives — the honest alternative to the one this resource " +
			"used to offer, a destroy that errors forever on every namespace ever created, with no " +
			"path forward at all.\n\n" +
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
				MarkdownDescription: "The namespace name, unique across all of CircleCI. This is " +
					"permanent once created: CircleCI's rename route answers 403 Forbidden " +
					"unconditionally (see the resource description), so changing this value fails at " +
					"`terraform plan`, with a diagnostic explaining why, rather than being attempted " +
					"against the API.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[^/\s]+$`),
						"must not contain a slash or whitespace; it is the prefix of `<namespace>/<orb>`",
					),
				},
				PlanModifiers: []planmodifier.String{
					orbNamespaceNameImmutable{},
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

// Update exists only to satisfy resource.Resource; nothing about a namespace
// can genuinely reach it any more. name is blocked from ever planning a
// change by orbNamespaceNameImmutable above, and organization_id forces
// replacement instead of an in-place Update (see org_id_deprecation.go). If
// either guard has a bug, refusing here outright is safer than silently
// calling an API this package no longer even exposes a client method for.
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

	if plan.Name.ValueString() != state.Name.ValueString() {
		resp.Diagnostics.AddError(
			"CircleCI orb namespace rename reached Update unexpectedly",
			"The name plan modifier should have blocked this change during terraform plan, before "+
				"apply ever called Update. Refusing to call an API route that answers 403 Forbidden on "+
				"every account this provider has been able to test; this is a bug in the provider, not "+
				"something your configuration can work around.",
		)

		return
	}

	plan.Id = state.Id
	setOrgIDs(&plan.OrganizationId, &plan.OrgId, effectiveOrgID(plan.OrganizationId, plan.OrgId))

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the namespace from Terraform state without calling the API.
//
// [NET, measured against a live organization-admin token]: CircleCI's delete
// route answers 403 Forbidden unconditionally — on a namespace this
// investigation had just created, on one belonging to an unrelated
// organization, and even on a namespace whose owning organization had since
// been deleted. No account permission or namespace state changed the answer,
// and no self-service alternative is documented anywhere. This resource used
// to call the route and surface that 403 as a failed `terraform destroy`; the
// result was an honest failure with no path forward, on every namespace ever
// created, forever. That is worse than the alternative implemented here,
// which follows circleci_project_group's pattern for its own unrouted revoke:
// succeed, and say plainly, in an unmissable warning, exactly what is left
// behind and where to actually act on it. A destroy that reports success
// while the object survives is the same shape as the silent lies this
// project has spent weeks hunting down elsewhere — what makes this one
// acceptable is that it is announced here rather than inferred by a caller
// who never gets told otherwise.
//
// Deliberately not gated on requireCloud: a namespace stranded in state by a
// later switch to `deployment = "server"` must stay removable from Terraform,
// the same reasoning storageRetentionResource.Delete and
// projectGroupResource.Delete both give for skipping that gate on their own
// no-API-call deletes.
func (r *orbNamespaceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state orbNamespaceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddWarning(
		"CircleCI orb namespace left in place",
		fmt.Sprintf(
			"Namespace %q (id %s) still exists in CircleCI, still owns every orb in it, and still "+
				"holds its name globally. Deleting a namespace is not self-service: CircleCI's delete "+
				"route answers 403 Forbidden unconditionally, so this has only been removed from "+
				"Terraform state, not from CircleCI.\n\n"+
				"Open a CircleCI support ticket (%s) to actually delete the namespace, or to free its "+
				"name for reuse elsewhere.",
			state.Name.ValueString(), state.Id.ValueString(), orbNamespaceSupportTicketURL,
		),
	)
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
