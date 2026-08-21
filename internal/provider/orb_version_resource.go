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
	_ resource.Resource                = &orbVersionResource{}
	_ resource.ResourceWithConfigure   = &orbVersionResource{}
	_ resource.ResourceWithImportState = &orbVersionResource{}
)

// orbVersionTypeName is used in diagnostics, including the Cloud-only error.
const orbVersionTypeName = "circleci_orb_version"

// orbDevVersionPrefix marks the mutable, expiring release channel. A dev version
// may be republished, which is what makes replacing one work where replacing a
// stable version cannot.
const orbDevVersionPrefix = "dev:"

// orbVersionResourceModel maps the resource schema.
type orbVersionResourceModel struct {
	Id        types.String `tfsdk:"id"`
	OrbId     types.String `tfsdk:"orb_id"`
	OrbName   types.String `tfsdk:"orb_name"`
	Version   types.String `tfsdk:"version"`
	Yaml      types.String `tfsdk:"yaml"`
	Source    types.String `tfsdk:"source"`
	CreatedAt types.String `tfsdk:"created_at"`
}

// NewOrbVersionResource is a helper function to simplify the provider implementation.
func NewOrbVersionResource() resource.Resource {
	return &orbVersionResource{}
}

// orbVersionResource is the resource implementation.
type orbVersionResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *orbVersionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_orb_version"
}

// Schema defines the schema for the resource.
func (r *orbVersionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Publishes a version of a CircleCI orb.\n\n" +
			"!> **Publishing is permanent and cannot be undone.** A published orb version is " +
			"immutable: its source cannot be edited, it cannot be republished with different " +
			"source, and there is no API route to delete it. Anyone who has referenced " +
			"`<namespace>/<orb>@<version>` keeps working forever, which is the point.\n\n" +
			"Because of that, `terraform destroy` on this resource does **not** delete anything. " +
			"It removes the resource from Terraform state and emits a warning saying the version " +
			"is still published. Changing `version` publishes an additional version and leaves the " +
			"previous one in place. Changing `yaml` for a version that is already published is " +
			"rejected by the API: publish a new version instead.\n\n" +
			"Dev versions are the exception. A version named `dev:<label>` may be republished and " +
			"expires on its own, so changing `yaml` on a dev version works: Terraform replaces the " +
			"resource and the API accepts the republish.\n\n" +
			"~> **CircleCI Cloud only.** Orb versions are served by the CircleCI v3 API, which " +
			"CircleCI Server does not route. Using this resource against a provider configured " +
			"with `deployment = \"server\"` fails with an explicit error.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the orb version.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"orb_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the orb to publish against, for " +
					"example `circleci_orb.example.id`. Changing this publishes against a " +
					"different orb, so it forces a new resource.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"version": schema.StringAttribute{
				MarkdownDescription: "The version to publish: a semantic version such as `1.2.3`, " +
					"or `dev:<label>` for a mutable dev release. Changing this publishes a new " +
					"version; the previous version stays published.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^(\d+\.\d+\.\d+|dev:\S+)$`),
						"must be a semantic version such as `1.2.3`, or `dev:<label>`",
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"yaml": schema.StringAttribute{
				MarkdownDescription: "The orb source YAML to publish, for example " +
					"`file(\"${path.module}/orb.yml\")`. This is write-only input: it is never " +
					"refreshed from the API, because CircleCI normalizes what it stores and the " +
					"normalized form would otherwise look like permanent drift. Read `source` to " +
					"see what CircleCI actually stored.\n\n" +
					"Changing this forces a new resource, which the API accepts only for a " +
					"`dev:<label>` version. For a stable version, bump `version` as well.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"source": schema.StringAttribute{
				MarkdownDescription: "The YAML source CircleCI stored for this version, as returned " +
					"by the API. It may differ from `yaml` by normalization.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"orb_name": schema.StringAttribute{
				MarkdownDescription: "Fully qualified name of the orb this version belongs to, " +
					"`<namespace>/<orb>`.\n\n" +
					"No orb version route reports this, so it is resolved with a second request " +
					"against the orb itself. That lookup is best effort: if it fails the version is " +
					"still recorded, with this attribute empty, rather than a just-published " +
					"version being lost.",
				Computed: true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the version was published, as an RFC 3339 timestamp.",
				Computed:            true,
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *orbVersionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// Create publishes the version and sets the initial Terraform state.
func (r *orbVersionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireCloud(r.client, orbVersionTypeName, &resp.Diagnostics) {
		return
	}

	var plan orbVersionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	version := plan.Version.ValueString()

	published, err := r.client.PublishOrbVersion(ctx, circleci.PublishOrbVersionRequest{
		OrbID:   plan.OrbId.ValueString(),
		Version: version,
		YAML:    plan.Yaml.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to publish CircleCI orb version "+version,
			r.publishFailureDetail(err, version),
		)

		return
	}

	// The version is published from here on, and publishing cannot be undone or
	// retried: a stable version is immutable, so a later apply cannot republish
	// it if this Create returns with no record of it. Every field the publish
	// response carries is copied in now, before the one call left that can still
	// fail, so that a failure below writes state instead of orphaning a
	// permanent orb version. See trigger_resource.go's Create for the model
	// this follows and why: it is the same shape issue #6 fixed there.
	plan.Id = types.StringValue(published.ID)
	plan.OrbId = types.StringValue(published.OrbID)
	plan.OrbName = types.StringValue(published.OrbName)
	plan.Version = types.StringValue(published.Version)
	plan.CreatedAt = types.StringValue(published.CreatedAt)

	// This call cannot be dropped: attributes.source is never present on a
	// publish response in practice (see orbVersionWire's comment and the
	// orbVersionEntity fixture in orb_test.go, which models exactly what
	// production sends), so this always runs, not merely as a fallback. The same
	// is true of trigger_resource.go's read-back for created_at — an earlier
	// version of this comment said otherwise, on the strength of a claim about
	// the trigger create response that turned out to be false; the lesson issue
	// #6 actually teaches is about where state is written, not about removing
	// the second call.
	source := published.Source
	if source == "" {
		source, err = r.client.GetOrbSource(ctx, published.ID)
		if err != nil {
			// source is Computed and cannot be left unknown in state. Recording it
			// empty rather than dropping the whole resource is fine: the next Read —
			// for example the one `terraform untaint` triggers — fetches it exactly
			// the same way.
			plan.Source = types.StringValue("")
			resp.Diagnostics.AddError(
				"Unable to read the source of the published CircleCI orb version "+version,
				circleci.Detail(err),
			)
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

			return
		}
	}
	plan.Source = types.StringValue(source)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
//
// yaml is deliberately not refreshed, except when it is null because the resource
// has just been imported: CircleCI normalizes stored source, and overwriting the
// configured YAML with the normalized form would look like permanent drift.
func (r *orbVersionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireCloud(r.client, orbVersionTypeName, &resp.Diagnostics) {
		return
	}

	var state orbVersionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	version, err := r.client.GetOrbVersion(ctx, state.Id.ValueString())
	if circleci.IsNotFound(err) {
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI orb version "+state.Id.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	source := version.Source
	if source == "" {
		source, err = r.client.GetOrbSource(ctx, version.ID)
		if err != nil {
			resp.Diagnostics.AddError(
				"Unable to read the source of CircleCI orb version "+version.Version,
				circleci.Detail(err),
			)

			return
		}
	}

	state.Id = types.StringValue(version.ID)
	state.OrbId = types.StringValue(version.OrbID)
	state.OrbName = types.StringValue(version.OrbName)
	state.Version = types.StringValue(version.Version)
	state.CreatedAt = types.StringValue(version.CreatedAt)
	state.Source = types.StringValue(source)

	if state.Yaml.IsNull() {
		// Freshly imported: seed the required attribute from what CircleCI stored,
		// so the resource has a complete state.
		state.Yaml = types.StringValue(source)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update cannot happen: every configurable attribute forces a replacement,
// because a published orb version is immutable. It exists only to satisfy the
// resource interface, and copies the plan into state if it is ever reached.
func (r *orbVersionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !requireCloud(r.client, orbVersionTypeName, &resp.Diagnostics) {
		return
	}

	var plan orbVersionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the version from Terraform state without calling the API.
//
// A published orb version cannot be deleted: the API has no route for it, and
// that is intentional, since any pipeline that pinned the version must keep
// resolving. Failing the destroy would leave the practitioner permanently unable
// to remove the resource from their configuration, so the state entry is dropped
// and a warning explains exactly what remains.
func (r *orbVersionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireCloud(r.client, orbVersionTypeName, &resp.Diagnostics) {
		return
	}

	var state orbVersionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	reference := state.Version.ValueString()
	if name := state.OrbName.ValueString(); name != "" {
		reference = name + "@" + reference
	}

	resp.Diagnostics.AddWarning(
		"Published CircleCI orb version cannot be deleted",
		fmt.Sprintf(
			"The orb version %s has been removed from Terraform state, but it is still published "+
				"and still resolvable by any project that references it. Publishing an orb version "+
				"is permanent: the CircleCI API has no route to delete or unpublish one.\n\n"+
				"Recreating this resource with the same version will fail, because the version "+
				"already exists. Publish a new version instead.",
			reference,
		),
	)
}

// ImportState imports an already published version by its UUID.
//
// The yaml attribute is required but cannot be recovered exactly, so the read
// that follows seeds it from the source CircleCI stored. If the configured YAML
// then differs by normalization, Terraform will plan a replacement that the API
// rejects; use `lifecycle { ignore_changes = [yaml] }` in that case.
func (r *orbVersionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !orbIsUUID(req.ID) {
		resp.Diagnostics.AddError(
			"Invalid import ID for circleci_orb_version",
			fmt.Sprintf(
				"Expected the orb version UUID, got %q.\n\n"+
					"Use the circleci_orb_version data source to look the UUID up from an orb id "+
					"and a version string.",
				req.ID,
			),
		)

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// publishFailureDetail annotates a publish failure with the reason a retry with
// the same version will not help, which is the single most common surprise here.
func (r *orbVersionResource) publishFailureDetail(err error, version string) string {
	detail := circleci.Detail(err)

	if strings.HasPrefix(version, orbDevVersionPrefix) {
		return detail
	}
	if !circleci.IsConflict(err) && !circleci.HasStatus(err, http.StatusBadRequest) {
		return detail
	}

	return detail + fmt.Sprintf(
		"\n\nIf %s is already published, this cannot succeed: a published orb version is "+
			"immutable and cannot be replaced or deleted. Publish a new version instead, or use a "+
			"dev:<label> version while iterating.",
		version,
	)
}
