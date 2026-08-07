// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &orbResource{}
	_ resource.ResourceWithConfigure   = &orbResource{}
	_ resource.ResourceWithImportState = &orbResource{}
)

// orbTypeName is used in diagnostics, including the Cloud-only error.
const orbTypeName = "circleci_orb"

// orbCategoryModel is one entry of an orb's computed categories list.
type orbCategoryModel struct {
	Id   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
}

// orbCategoryObjectType is the object type of an orbCategoryModel.
var orbCategoryObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"id":   types.StringType,
	"name": types.StringType,
}}

// orbResourceModel maps the resource schema.
type orbResourceModel struct {
	Id            types.String `tfsdk:"id"`
	NamespaceId   types.String `tfsdk:"namespace_id"`
	Namespace     types.String `tfsdk:"namespace"`
	Name          types.String `tfsdk:"name"`
	FullName      types.String `tfsdk:"full_name"`
	IsPrivate     types.Bool   `tfsdk:"is_private"`
	IsListed      types.Bool   `tfsdk:"is_listed"`
	CategoryIds   types.Set    `tfsdk:"category_ids"`
	Categories    types.List   `tfsdk:"categories"`
	CreatedAt     types.String `tfsdk:"created_at"`
	HomeUrl       types.String `tfsdk:"home_url"`
	LatestVersion types.String `tfsdk:"latest_version"`
}

// NewOrbResource is a helper function to simplify the provider implementation.
func NewOrbResource() resource.Resource {
	return &orbResource{}
}

// orbResource is the resource implementation.
type orbResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *orbResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_orb"
}

// Schema defines the schema for the resource.
func (r *orbResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI orb: the named container in a namespace that owns " +
			"a series of published orb versions.\n\n" +
			"Creating an orb does not publish any source. Use `circleci_orb_version` to publish " +
			"versions against it.\n\n" +
			"~> **CircleCI Cloud only.** Orbs are served by the CircleCI v3 API, which CircleCI " +
			"Server does not route. Using this resource against a provider configured with " +
			"`deployment = \"server\"` fails with an explicit error.\n\n" +
			"!> **Orbs cannot be deleted.** The CircleCI API has no delete route for an orb. " +
			"Destroying this resource removes it from Terraform state and leaves the orb in the " +
			"registry; a warning says so. Set `is_listed = false` to hide a public orb, or delete " +
			"the whole `circleci_orb_namespace` to remove its orbs.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the orb.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"namespace_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the namespace that owns the orb, " +
					"for example `circleci_orb_namespace.example.id`. An orb cannot be moved " +
					"between namespaces, so changing this forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The orb name, without the namespace prefix (for example " +
					"`node`, not `acme/node`). An orb cannot be renamed, so changing this forces " +
					"a new resource to be created.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[^/\s@]+$`),
						"must be the orb name alone, with no namespace prefix, `@version` suffix or whitespace",
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"namespace": schema.StringAttribute{
				MarkdownDescription: "Name of the namespace that owns the orb, resolved from `namespace_id`.",
				Computed:            true,
			},
			"full_name": schema.StringAttribute{
				MarkdownDescription: "The orb's fully qualified name, `<namespace>/<name>`. This is " +
					"what a `.circleci/config.yml` refers to, as `<full_name>@<version>`.",
				Computed: true,
			},
			"is_private": schema.BoolAttribute{
				MarkdownDescription: "Whether the orb is private to the owning organization. " +
					"CircleCI defaults this to `false`. Visibility cannot be changed after creation and there " +
					"is no delete route for an orb, so changing this plans a replacement that the " +
					"API will reject because the orb already exists.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					// RequiresReplaceIfConfigured rather than plain RequiresReplace, because
					// this attribute is Optional+Computed: plain RequiresReplace fires whenever
					// the planned value differs from state, and it does not special-case an
					// unknown planned value (for example while is_private references another
					// resource's not-yet-known attribute) as equal to the retained one, so it
					// would plan a spurious replacement in that case.
					boolplanmodifier.RequiresReplaceIfConfigured(),

					// UseStateForUnknown, and deliberately NO Default. Pairing a Default with
					// RequiresReplaceIfConfigured broke apply, in the same way it did for
					// circleci_otel_exporter's insecure: the framework applies a Default
					// whenever the *config* value is null, without consulting prior state, so
					// an is_private that had been set and was then removed from the
					// configuration planned as false. RequiresReplaceIfConfigured then
					// declined to fire — a null config value is precisely its bail-out — so
					// an in-place update was planned, and applyOrb wrote the orb's real,
					// unchanged visibility back to state, contradicting the plan.
					//
					// It is reachable simply by importing an existing private orb and then
					// changing is_listed, which sits directly below and has always used this
					// pattern. Without the Default, an omitted is_private plans as unknown
					// and resolves to what is already in state.
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"is_listed": schema.BoolAttribute{
				MarkdownDescription: "Whether the orb appears in the public orb registry listing. " +
					"This is an in-place update. Left unset, the value CircleCI chose at creation " +
					"is kept.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"category_ids": schema.SetAttribute{
				MarkdownDescription: "Unique identifiers (UUIDs) of the registry categories the orb " +
					"is listed under, applied in place. Use the `circleci_orb_categories` data " +
					"source to resolve category names to ids.\n\n" +
					"Omit this attribute to leave categories unmanaged, so that categories set " +
					"outside Terraform are left alone. Set it to `[]` to manage them and remove " +
					"all of them.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"categories": schema.ListNestedAttribute{
				MarkdownDescription: "The registry categories the orb is currently listed under, " +
					"sorted by name so that the order the API returns them in — which is not part " +
					"of its contract — cannot produce a diff.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the category.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Name of the category.",
							Computed:            true,
						},
					},
				},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the orb was created, as an RFC 3339 timestamp.",
				Computed:            true,
			},
			"home_url": schema.StringAttribute{
				MarkdownDescription: "The orb's home page, when one is set.",
				Computed:            true,
			},
			"latest_version": schema.StringAttribute{
				MarkdownDescription: "The orb's most recently published version, or an empty string " +
					"when no version has been published yet.",
				Computed: true,
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *orbResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// Create creates the orb and sets the initial Terraform state.
func (r *orbResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireCloud(r.client, orbTypeName, &resp.Diagnostics) {
		return
	}

	var plan orbResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The API stores an orb under its fully qualified "<namespace>/<orb>" name and
	// wants that qualified form on create, even though the namespace is also given
	// by reference. Resolving the namespace first also turns a bad namespace_id
	// into a clear error instead of a confusing orb-create failure.
	ns, err := r.client.GetNamespaceByID(ctx, plan.NamespaceId.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read the CircleCI orb namespace "+plan.NamespaceId.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	fullName := ns.Name + "/" + plan.Name.ValueString()

	pkg, err := r.client.CreateOrbPackage(ctx, circleci.CreateOrbPackageRequest{
		Name:        fullName,
		NamespaceID: plan.NamespaceId.ValueString(),
		IsPrivate:   plan.IsPrivate.ValueBool(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to create CircleCI orb "+fullName,
			circleci.Detail(err),
		)

		return
	}

	// wantListed is what the configuration asked for, captured before applyOrb
	// overwrites plan.IsListed with whatever CreateOrbPackage actually returned
	// (which is CircleCI's own default, not the plan's).
	wantListed := plan.IsListed

	// The orb exists in CircleCI from here on, and its name cannot be reused by
	// a retry: the API has no delete route for an orb (see Delete below), so a
	// failure past this point must write state with what create already
	// returned instead of returning early and orphaning it. See
	// trigger_resource.go's Create for the model this follows — the same shape
	// issue #6 fixed there.
	r.applyOrb(ctx, &plan, pkg, false, &resp.Diagnostics)

	// is_listed and the categories are separate routes, so they are applied after
	// the orb exists.
	if !wantListed.IsNull() && !wantListed.IsUnknown() && wantListed.ValueBool() != pkg.IsListed {
		pkg, err = r.client.SetOrbListed(ctx, pkg.ID, wantListed.ValueBool())
		if err != nil {
			resp.Diagnostics.AddError(
				"Unable to set the listed status of CircleCI orb "+fullName,
				circleci.Detail(err),
			)
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

			return
		}
		r.applyOrb(ctx, &plan, pkg, false, &resp.Diagnostics)
	}

	pkg = r.reconcileCategories(ctx, pkg, plan.CategoryIds, &resp.Diagnostics)
	r.applyOrb(ctx, &plan, pkg, false, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *orbResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireCloud(r.client, orbTypeName, &resp.Diagnostics) {
		return
	}

	var state orbResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var (
		pkg *circleci.OrbPackage
		err error
	)
	if id := state.Id.ValueString(); id != "" {
		pkg, err = r.client.GetOrbPackage(ctx, id)
	} else {
		pkg, err = r.client.GetOrbPackageByName(ctx, state.FullName.ValueString())
	}

	if circleci.IsNotFound(err) {
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI orb",
			circleci.Detail(err),
		)

		return
	}

	r.applyOrb(ctx, &state, pkg, true, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update applies the two attributes the API can change in place: the listed
// status, and category membership. Everything else forces a replacement.
func (r *orbResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !requireCloud(r.client, orbTypeName, &resp.Diagnostics) {
		return
	}

	var plan, state orbResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.Id.ValueString()
	plan.Id = state.Id

	pkg, err := r.client.GetOrbPackage(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI orb "+id,
			circleci.Detail(err),
		)

		return
	}

	if !plan.IsListed.IsNull() && !plan.IsListed.IsUnknown() && plan.IsListed.ValueBool() != pkg.IsListed {
		pkg, err = r.client.SetOrbListed(ctx, id, plan.IsListed.ValueBool())
		if err != nil {
			resp.Diagnostics.AddError(
				"Unable to set the listed status of CircleCI orb "+id,
				circleci.Detail(err),
			)

			return
		}
	}

	pkg = r.reconcileCategories(ctx, pkg, plan.CategoryIds, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	r.applyOrb(ctx, &plan, pkg, false, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the orb from Terraform state without calling the API.
//
// The CircleCI API has no delete route for an orb: an orb, like the versions
// published against it, is permanent. Failing the destroy would leave the
// practitioner unable to remove the resource from their configuration at all, so
// the state entry is dropped and a warning explains what is left behind.
func (r *orbResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireCloud(r.client, orbTypeName, &resp.Diagnostics) {
		return
	}

	var state orbResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddWarning(
		"CircleCI orb cannot be deleted",
		fmt.Sprintf(
			"The orb %q has been removed from Terraform state, but it still exists in the CircleCI "+
				"orb registry: the API has no route to delete an orb.\n\n"+
				"To hide a public orb from the registry listing, set is_listed = false instead of "+
				"destroying it. To remove an orb entirely, delete the namespace that owns it with "+
				"circleci_orb_namespace, which deletes its orbs as well.",
			state.FullName.ValueString(),
		),
	)
}

// ImportState imports an existing orb by its UUID or by its fully qualified
// "<namespace>/<orb>" name.
func (r *orbResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if orbIsUUID(req.ID) {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)

		return
	}

	namespace, name, ok := strings.Cut(req.ID, "/")
	if !ok || namespace == "" || name == "" || strings.Contains(name, "/") {
		resp.Diagnostics.AddError(
			"Invalid import ID for circleci_orb",
			fmt.Sprintf("Expected an orb UUID or a fully qualified \"<namespace>/<orb>\" name, got %q.", req.ID),
		)

		return
	}

	// Read resolves the rest, including the orb's UUID, from the qualified name.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("full_name"), req.ID)...)
}

// reconcileCategories brings an orb's category membership in line with wanted.
//
// A null or unknown set means categories are unmanaged and are left as they are,
// which is what lets a configuration omit the attribute without Terraform
// removing categories somebody set in the CircleCI UI.
func (r *orbResource) reconcileCategories(
	ctx context.Context,
	pkg *circleci.OrbPackage,
	wanted types.Set,
	diags *diag.Diagnostics,
) *circleci.OrbPackage {
	if wanted.IsNull() || wanted.IsUnknown() {
		return pkg
	}

	var want []string
	diags.Append(wanted.ElementsAs(ctx, &want, false)...)
	if diags.HasError() {
		return pkg
	}

	have := make(map[string]bool, len(pkg.Categories))
	for _, category := range pkg.Categories {
		have[category.ID] = true
	}

	keep := make(map[string]bool, len(want))
	for _, id := range want {
		keep[id] = true
	}

	updated := pkg
	for _, id := range want {
		if have[id] {
			continue
		}

		next, err := r.client.AddOrbCategory(ctx, pkg.ID, id)
		if err != nil {
			diags.AddError(
				"Unable to add CircleCI orb "+pkg.Name+" to category "+id,
				circleci.Detail(err),
			)

			return updated
		}
		updated = next
	}

	for _, category := range pkg.Categories {
		if keep[category.ID] {
			continue
		}

		next, err := r.client.RemoveOrbCategory(ctx, pkg.ID, category.ID)
		if err != nil {
			diags.AddError(
				"Unable to remove CircleCI orb "+pkg.Name+" from category "+category.ID,
				circleci.Detail(err),
			)

			return updated
		}
		updated = next
	}

	return updated
}

// applyOrb copies an API orb into the model.
//
// refreshCategoryIds controls whether category_ids is refreshed from the API.
// Read wants that, so that a category added outside Terraform shows up as drift.
// Create and Update must not: category_ids is Optional and not Computed, so the
// value they write has to equal the planned value exactly or Terraform reports an
// inconsistent result after apply.
func (r *orbResource) applyOrb(
	ctx context.Context,
	model *orbResourceModel,
	pkg *circleci.OrbPackage,
	refreshCategoryIds bool,
	diags *diag.Diagnostics,
) {
	model.Id = types.StringValue(pkg.ID)
	model.NamespaceId = types.StringValue(pkg.NamespaceID)
	model.Namespace = types.StringValue(pkg.Namespace)
	model.FullName = types.StringValue(pkg.Name)
	model.Name = types.StringValue(orbBareName(pkg.Name, pkg.Namespace))
	model.IsPrivate = types.BoolValue(pkg.IsPrivate)
	model.IsListed = types.BoolValue(pkg.IsListed)
	model.CreatedAt = types.StringValue(pkg.CreatedAt)
	model.HomeUrl = types.StringValue(pkg.HomeURL)
	model.LatestVersion = types.StringValue(pkg.LatestVersion)

	categories, categoryDiags := orbCategoriesToList(ctx, pkg.Categories)
	diags.Append(categoryDiags...)
	model.Categories = categories

	if !refreshCategoryIds {
		return
	}

	if model.CategoryIds.IsNull() || model.CategoryIds.IsUnknown() {
		// Unmanaged: keep it null rather than reporting what the registry has, so
		// that omitting the attribute does not produce a permanent diff.
		model.CategoryIds = types.SetNull(types.StringType)

		return
	}

	ids := make([]attr.Value, 0, len(pkg.Categories))
	for _, category := range pkg.Categories {
		ids = append(ids, types.StringValue(category.ID))
	}

	set, setDiags := types.SetValue(types.StringType, ids)
	diags.Append(setDiags...)
	model.CategoryIds = set
}

// orbBareName strips the namespace prefix from a fully qualified orb name.
//
// The bare name is what the resource stores, so that renaming the namespace does
// not look like a change to the orb's own name.
func orbBareName(fullName, namespace string) string {
	if namespace != "" {
		if bare, ok := strings.CutPrefix(fullName, namespace+"/"); ok {
			return bare
		}
	}

	if _, after, ok := strings.Cut(fullName, "/"); ok {
		return after
	}

	return fullName
}

// orbCategoriesToList converts API categories into the computed categories list.
//
// The API reports an orb's categories in an order of its own choosing rather
// than a stable one, and nothing about the category-membership routes promises
// the order survives from one response to the next. `categories` is Computed
// with no plan modifier, so the framework's default plan carries the value in
// state forward whenever some OTHER attribute is what triggers an update. If
// Update then rebuilds this list from a fresh, differently-ordered API
// response, the apply's actual result no longer matches what was planned, and
// Terraform reports "Provider produced inconsistent result after apply" —
// worse than an ordinary diff, since every later plan keeps failing the same
// way until something touches category membership directly.
//
// Sorting here, by name and then by id to break a tie, makes the attribute's
// order a property of this provider rather than of the API, so a reordering
// response can never disagree with what was last written. It is a copy: the
// input slice is also used by reconcileCategories, whose category membership
// check does not care about order, but mutating a caller's slice in place is
// worth avoiding regardless.
func orbCategoriesToList(ctx context.Context, categories []circleci.OrbCategory) (types.List, diag.Diagnostics) {
	sorted := make([]circleci.OrbCategory, len(categories))
	copy(sorted, categories)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}

		return sorted[i].ID < sorted[j].ID
	})

	models := make([]orbCategoryModel, 0, len(sorted))
	for _, category := range sorted {
		models = append(models, orbCategoryModel{
			Id:   types.StringValue(category.ID),
			Name: types.StringValue(category.Name),
		})
	}

	return types.ListValueFrom(ctx, orbCategoryObjectType, models)
}
