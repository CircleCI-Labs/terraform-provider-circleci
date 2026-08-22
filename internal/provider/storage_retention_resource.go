// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	_ resource.Resource                = &storageRetentionResource{}
	_ resource.ResourceWithConfigure   = &storageRetentionResource{}
	_ resource.ResourceWithImportState = &storageRetentionResource{}
	_ resource.ResourceWithModifyPlan  = &storageRetentionResource{}
)

// storageRetentionTypeName is the Terraform type name, used both for Metadata
// and for the CircleCI Server error message.
const storageRetentionTypeName = "circleci_storage_retention"

// storageRetentionResourceModel maps the resource schema.
//
// org_id has no deprecated organization_id sibling the way most resources in
// this provider do (see org_id_deprecation.go). That pairing exists only to
// keep configurations written before org_id was introduced working; this
// resource is new after that migration finished, so it has no such
// configurations to be compatible with — circleci_organization_contacts, added
// alongside this resource, made the same call for the same reason.
//
// Every *Days field is Required, not Optional: the PUT route this resource
// calls (circleci.Client.SetStorageRetention) always writes all three values
// together, so there is no "leave this one alone" the way OrganizationSettings
// has for its toggles. The *_min and *_max fields are Computed only — they are
// reported by CircleCI's plan configuration, never sent by this resource. See
// the package doc comment below for why they cannot be validated against at
// plan time.
type storageRetentionResourceModel struct {
	OrgID types.String `tfsdk:"org_id"`

	CacheRetentionDays    types.Int64 `tfsdk:"cache_retention_days"`
	CacheRetentionDaysMin types.Int64 `tfsdk:"cache_retention_days_min"`
	CacheRetentionDaysMax types.Int64 `tfsdk:"cache_retention_days_max"`

	WorkspaceRetentionDays    types.Int64 `tfsdk:"workspace_retention_days"`
	WorkspaceRetentionDaysMin types.Int64 `tfsdk:"workspace_retention_days_min"`
	WorkspaceRetentionDaysMax types.Int64 `tfsdk:"workspace_retention_days_max"`

	ArtifactRetentionDays    types.Int64 `tfsdk:"artifact_retention_days"`
	ArtifactRetentionDaysMin types.Int64 `tfsdk:"artifact_retention_days_min"`
	ArtifactRetentionDaysMax types.Int64 `tfsdk:"artifact_retention_days_max"`
}

// payload builds the full write body. All three fields go every time — see the
// model doc comment.
func (m storageRetentionResourceModel) payload() circleci.StorageRetentionControls {
	return circleci.StorageRetentionControls{
		CacheDays:     m.CacheRetentionDays.ValueInt64(),
		WorkspaceDays: m.WorkspaceRetentionDays.ValueInt64(),
		ArtifactDays:  m.ArtifactRetentionDays.ValueInt64(),
	}
}

// refresh overwrites every field — configured values and bounds alike — with
// what the API just reported.
//
// This is unconditional, unlike OrganizationSettings.refresh, which keeps an
// unmanaged toggle null. There is nothing unmanaged here: all three retention
// values are Required, so the practitioner's configuration is the only source
// of truth for what should be requested, and the API response is the only
// source of truth for what is actually stored. Adopting the response
// unconditionally is what makes a clamped value visible as drift on the next
// plan instead of being silently reported as a successful apply.
func (m *storageRetentionResourceModel) refresh(remote *circleci.StorageRetention) {
	m.CacheRetentionDays = types.Int64Value(remote.Controls.CacheDays)
	m.CacheRetentionDaysMin = types.Int64Value(remote.Limits.Cache.Min)
	m.CacheRetentionDaysMax = types.Int64Value(remote.Limits.Cache.Max)

	m.WorkspaceRetentionDays = types.Int64Value(remote.Controls.WorkspaceDays)
	m.WorkspaceRetentionDaysMin = types.Int64Value(remote.Limits.Workspace.Min)
	m.WorkspaceRetentionDaysMax = types.Int64Value(remote.Limits.Workspace.Max)

	m.ArtifactRetentionDays = types.Int64Value(remote.Controls.ArtifactDays)
	m.ArtifactRetentionDaysMin = types.Int64Value(remote.Limits.Artifact.Min)
	m.ArtifactRetentionDaysMax = types.Int64Value(remote.Limits.Artifact.Max)
}

// NewStorageRetentionResource is a helper function to simplify the provider
// implementation.
func NewStorageRetentionResource() resource.Resource {
	return &storageRetentionResource{}
}

// storageRetentionResource manages an organization's storage-retention
// controls: how many days CircleCI keeps cache, workspace and job-artifact
// data.
//
// It is a singleton settings resource, the same shape as
// organizationSettingsResource: the record always exists and always has a
// value for all three fields, so this resource only ever reads and overwrites
// it, never creates or destroys it.
//
// # Why there is no plan-time bound validation
//
// The read side of the route reports a plan-enforced [min, max] range for each
// of the three values, which is why this schema carries six Computed bound
// attributes. It is tempting to validate the three Required values against
// them at plan time. One thing makes that impossible, not just undesirable:
// the bounds are per-organization and come only from a GET against this same
// private route. There is no other way to learn them, and a resource cannot
// call the API from within schema validation (ValidateConfig runs before
// Configure, so no client exists yet) or even reliably from ModifyPlan on
// first apply, when there is no prior state to read bounds from at all. So an
// out-of-bounds value cannot be caught before apply time on a brand-new
// resource, however the service enforces it.
//
// [NET] measurement against gitlab-test (2026-08-21) settles what an
// earlier version of this comment only assumed: this route rejects an
// out-of-bounds value with 400 {"error":"Invalid value given"} and applies
// none of the three fields, rather than silently storing the nearest bound.
// See SetStorageRetention's doc comment for the measurement. That makes the
// resource's job simpler than the old clamp-and-detect design implied: a bad
// value surfaces as an ordinary apply-time error from CircleCI (Create/
// Update's own AddError below), and warnClampedStorageRetention is kept only
// as a defensive check for a value that is accepted but not stored
// byte-for-byte — a case this measurement never produced, but one this
// resource cannot rule out for every possible input without re-measuring
// every release.
type storageRetentionResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *storageRetentionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_storage_retention"
}

// Schema defines the schema for the resource.
func (r *storageRetentionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	bound := func(control, bound string) schema.Int64Attribute {
		return schema.Int64Attribute{
			MarkdownDescription: fmt.Sprintf(
				"Plan-enforced %s number of days CircleCI will accept for `%s_retention_days`, "+
					"as currently reported by CircleCI. This is not settable — CircleCI computes it from "+
					"the organization's plan — and it can change independently of this resource, for "+
					"example when the organization changes plans.",
				bound, control,
			),
			Computed: true,
		}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages how many days CircleCI retains an organization's build cache, " +
			"workspace data and job artifacts.\n\n" +
			"**CircleCI Cloud only.** This resource calls an internal, unpublished CircleCI route " +
			"rather than the public API. Using it with " +
			"`deployment = \"server\"` reports an error rather than the confusing failure the request " +
			"would otherwise produce.\n\n" +
			"This is a settings resource rather than a thing that gets created and destroyed: an " +
			"organization's storage-retention record always exists and always holds a value for all " +
			"three fields. `terraform destroy` only drops it from Terraform state; see the note on " +
			"`Delete` below.\n\n" +
			"~> **CircleCI rejects a value outside the reported bounds; it does not clamp it.** Each " +
			"retention value has a plan-enforced minimum and maximum, reported here as the matching " +
			"`..._min`/`..._max` attributes, which are only known once this resource has been read at " +
			"least once (after the first successful apply, or after import). Configuring a value " +
			"outside that range fails the apply, with an error CircleCI itself does not attribute to " +
			"any one field (`\"Invalid value given\"`) — this provider cannot improve on that message " +
			"because it cannot learn the bounds before the first read either. This provider also reads " +
			"the value back after every successful write and warns if what is now stored ever differs " +
			"from what was configured, as a defensive check; as of this provider's most recent " +
			"measurement that never happens — a write either applies exactly as configured or is " +
			"rejected outright, never partially.",
		Attributes: map[string]schema.Attribute{
			"org_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization these storage-retention " +
					"controls belong to. Changing this value forces a new resource to be created, rather " +
					"than moving management to a different organization's existing controls.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cache_retention_days": schema.Int64Attribute{
				MarkdownDescription: "Number of days CircleCI retains build cache for this organization.",
				Required:            true,
			},
			"cache_retention_days_min": bound("cache", "minimum"),
			"cache_retention_days_max": bound("cache", "maximum"),
			"workspace_retention_days": schema.Int64Attribute{
				MarkdownDescription: "Number of days CircleCI retains workspace data passed between jobs " +
					"for this organization.",
				Required: true,
			},
			"workspace_retention_days_min": bound("workspace", "minimum"),
			"workspace_retention_days_max": bound("workspace", "maximum"),
			"artifact_retention_days": schema.Int64Attribute{
				MarkdownDescription: "Number of days CircleCI retains job artifacts for this organization.",
				Required:            true,
			},
			"artifact_retention_days_min": bound("artifact", "minimum"),
			"artifact_retention_days_max": bound("artifact", "maximum"),
		},
	}
}

// ModifyPlan gates the resource on CircleCI Cloud at plan time.
//
// Without this, `terraform plan` against a Server installation would report a
// clean create, and the failure would only surface mid-apply as an
// indistinguishable-from-a-typo 404 from GetPrivate. See
// otel_exporter_resource.go's ModifyPlan for the same shape and the same caveat
// about requireCloud's own message: it says "backed by the CircleCI v3 API",
// which is not the reason this resource is gated — this route is not v3 at all,
// it is an unpublished private route that simply happens to exist only on
// Cloud. The approximation is accepted elsewhere in this codebase for the same
// reason: the alternative is a second diagnostic message to maintain for one
// resource.
//
// A destroy plan (req.Plan.Raw.IsNull()) is deliberately let through — see
// Delete for why that matters here specifically, not just by convention.
func (r *storageRetentionResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, storageRetentionTypeName, &resp.Diagnostics)
}

// Create writes the configured retention values to the organization's existing
// storage-retention record. Nothing is created: the record already exists for
// every organization.
func (r *storageRetentionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil || !requireCloud(r.client, storageRetentionTypeName, &resp.Diagnostics) {
		return
	}

	var plan storageRetentionResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := plan.OrgID.ValueString()

	updated, err := r.client.SetStorageRetention(ctx, orgID, plan.payload())
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to set CircleCI storage-retention controls for "+orgID,
			circleci.Detail(err),
		)

		return
	}

	warnClampedStorageRetention(plan, updated, orgID, &resp.Diagnostics)

	plan.refresh(updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the retention values and their bounds from the API.
func (r *storageRetentionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil || !requireCloud(r.client, storageRetentionTypeName, &resp.Diagnostics) {
		return
	}

	var state storageRetentionResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := state.OrgID.ValueString()

	retention, err := r.client.GetStorageRetention(ctx, orgID)
	if err != nil {
		// The organization itself is gone, so there is nothing left to manage.
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read CircleCI storage-retention controls for "+orgID,
			circleci.Detail(err),
		)

		return
	}

	state.refresh(retention)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update writes the newly configured retention values. Every field is
// rewritten every time, because the underlying route has no partial update.
func (r *storageRetentionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil || !requireCloud(r.client, storageRetentionTypeName, &resp.Diagnostics) {
		return
	}

	var plan storageRetentionResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := plan.OrgID.ValueString()

	updated, err := r.client.SetStorageRetention(ctx, orgID, plan.payload())
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to update CircleCI storage-retention controls for "+orgID,
			circleci.Detail(err),
		)

		return
	}

	warnClampedStorageRetention(plan, updated, orgID, &resp.Diagnostics)

	plan.refresh(updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the resource from Terraform state without calling the API.
//
// An organization's storage-retention record cannot be deleted: like
// OrganizationSettings, it exists for as long as the organization does, and
// every field has a live value at all times. There is also no "reset to
// default" route — a PUT always requires all three values, so there is nothing
// to send that means "stop managing this." The closest thing to destroying
// this resource is to stop tracking it, exactly as
// organizationSettingsResource.Delete and projectGroupResource.Delete do for
// their own unroutable cases.
//
// Unlike organizationSettingsResource.Delete, this method does *not* call
// requireCloud first. That is deliberate, not an oversight: this Delete makes
// no API call of any kind, so gating it buys nothing except a chance to reject
// a destroy that ModifyPlan already decided to let through. A record created
// while `deployment = "cloud"` and then stranded in state by a later switch to
// `deployment = "server"` must stay removable — see
// TestCloudOnlyModifyPlanAllowsDestroy and projectGroupResource's own Delete
// for the same reasoning applied to its unroutable revoke.
func (r *storageRetentionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state storageRetentionResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := state.OrgID.ValueString()

	// No API call. The framework drops the resource from state when Delete
	// returns without error.
	resp.Diagnostics.AddWarning(
		"CircleCI storage-retention controls left in place",
		fmt.Sprintf(
			"%s for organization %s was removed from Terraform state, but CircleCI has no route "+
				"to delete or reset storage-retention controls, so cache, workspace and artifact "+
				"retention keep their current values (%d, %d and %d day(s) respectively). Change "+
				"the values explicitly, in this provider or in the CircleCI web UI, if that is not "+
				"what you want.",
			storageRetentionTypeName, orgID,
			state.CacheRetentionDays.ValueInt64(), state.WorkspaceRetentionDays.ValueInt64(),
			state.ArtifactRetentionDays.ValueInt64(),
		),
	)
}

// Configure adds the provider configured client to the resource.
func (r *storageRetentionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports the storage-retention controls of an existing
// organization by its id.
func (r *storageRetentionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("org_id"), req.ID)...)
}

// storageRetentionCheck is one field's requested-vs-actual comparison, used by
// warnClampedStorageRetention.
type storageRetentionCheck struct {
	attribute          string
	requested, applied int64
	bound              circleci.StorageRetentionBound
}

// warnClampedStorageRetention compares what was requested against what
// SetStorageRetention actually read back, and warns if they differ for one or
// more fields.
//
// This function exists for a scenario [NET] measurement (2026-08-21,
// gitlab-test) did not observe: it once documented "CircleCI clamps an
// out-of-bounds value instead of rejecting it" as settled fact, but measured
// behaviour is the opposite — a write outside the reported [min, max] bounds
// is rejected with 400 and applies nothing (see SetStorageRetention's own
// comment), so a call reaching this function has already, by definition,
// succeeded, and on every case actually measured requested and applied were
// therefore always equal. It is kept anyway as a defensive backstop: if
// CircleCI ever does start accepting-but-adjusting a value on this route, the
// alternative to this check is a silent, permanent diff on every subsequent
// `terraform plan` with no clue why. A warning firing here after this comment
// was written should be treated as news — it means the measured behaviour
// above has changed, not that this function was wrong to add.
func warnClampedStorageRetention(
	requested storageRetentionResourceModel, applied *circleci.StorageRetention, orgID string, diags *diag.Diagnostics,
) {
	checks := []storageRetentionCheck{
		{
			"cache_retention_days",
			requested.CacheRetentionDays.ValueInt64(), applied.Controls.CacheDays, applied.Limits.Cache,
		},
		{
			"workspace_retention_days",
			requested.WorkspaceRetentionDays.ValueInt64(), applied.Controls.WorkspaceDays, applied.Limits.Workspace,
		},
		{
			"artifact_retention_days",
			requested.ArtifactRetentionDays.ValueInt64(), applied.Controls.ArtifactDays, applied.Limits.Artifact,
		},
	}

	var clamped []string

	for _, check := range checks {
		if check.requested != check.applied {
			clamped = append(clamped, fmt.Sprintf(
				"%s: requested %d, CircleCI stored %d (plan allows %d-%d)",
				check.attribute, check.requested, check.applied, check.bound.Min, check.bound.Max,
			))
		}
	}

	if len(clamped) == 0 {
		return
	}

	diags.AddWarning(
		"CircleCI clamped storage-retention values to the organization's plan limits",
		fmt.Sprintf(
			"For organization %s:\n\n  %s\n\n"+
				"CircleCI accepted the request but did not store the configured value(s) verbatim. "+
				"Terraform state now holds what CircleCI actually stored, so the next plan will show "+
				"a difference from your configuration until it is changed to fit within the reported "+
				"bounds.",
			orgID, strings.Join(clamped, "\n  "),
		),
	)
}
