// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	_ resource.Resource               = &notificationPreferencesResource{}
	_ resource.ResourceWithConfigure  = &notificationPreferencesResource{}
	_ resource.ResourceWithModifyPlan = &notificationPreferencesResource{}
)

// notificationPreferencesTypeName is the Terraform type name.
const notificationPreferencesTypeName = "circleci_notification_preferences"

// notificationPreferenceItemModel is one row of the computed preference
// matrix.
type notificationPreferenceItemModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	Channel          types.String `tfsdk:"channel"`
	EntityScope      types.String `tfsdk:"entity_scope"`
	DisplayOrder     types.Int64  `tfsdk:"display_order"`
	Experimental     types.Bool   `tfsdk:"experimental"`
	GroupID          types.String `tfsdk:"group_id"`
	GroupName        types.String `tfsdk:"group_name"`
	SectionID        types.String `tfsdk:"section_id"`
	SectionName      types.String `tfsdk:"section_name"`
	UserConfigurable types.Bool   `tfsdk:"user_configurable"`
	IsEnabled        types.Bool   `tfsdk:"is_enabled"`
}

// notificationPreferenceObjectType is the object type of a
// notificationPreferenceItemModel.
var notificationPreferenceObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"id":                types.StringType,
	"name":              types.StringType,
	"channel":           types.StringType,
	"entity_scope":      types.StringType,
	"display_order":     types.Int64Type,
	"experimental":      types.BoolType,
	"group_id":          types.StringType,
	"group_name":        types.StringType,
	"section_id":        types.StringType,
	"section_name":      types.StringType,
	"user_configurable": types.BoolType,
	"is_enabled":        types.BoolType,
}}

// notificationPreferencesResourceModel maps the resource schema.
//
// Updates is Optional and never Computed: see the Schema method for why. It
// manages a sparse set of rows by preference_id; every other row keeps
// whatever value CircleCI already has.
type notificationPreferencesResourceModel struct {
	Scope       types.String `tfsdk:"scope"`
	ProjectID   types.String `tfsdk:"project_id"`
	OrgID       types.String `tfsdk:"org_id"`
	Updates     types.Map    `tfsdk:"updates"`
	Preferences types.List   `tfsdk:"preferences"`
}

// NewNotificationPreferencesResource is a helper function to simplify the
// provider implementation.
func NewNotificationPreferencesResource() resource.Resource {
	return &notificationPreferencesResource{}
}

// notificationPreferencesResource manages the sparse set of notification
// preference toggles a configuration chooses to set, for the calling user or
// for a project.
//
// It is a settings resource, not a thing that gets created and destroyed: the
// preference matrix exists for as long as the user or project does, seeded
// with CircleCI's defaults, and every row always has a value. Create reads the
// matrix and writes the rows updates names; Delete makes no API call.
type notificationPreferencesResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *notificationPreferencesResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_preferences"
}

// Schema defines the schema for the resource.
func (r *notificationPreferencesResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a sparse set of CircleCI notification preference toggles, for the " +
			"calling user or for a project.\n\n" +
			"~> **CircleCI Cloud only.** Preferences are served by the CircleCI v3 API, which CircleCI " +
			"Server does not route.\n\n" +
			"This is a settings resource rather than a thing that gets created and destroyed. The " +
			"preference matrix is a fixed, CircleCI-defined catalog that exists for as long as the user " +
			"or project does, seeded with CircleCI's defaults; there is no route to add, remove or reset " +
			"a row. `updates` is written only for the preference ids the configuration names, so several " +
			"configurations may safely manage disjoint rows of the same matrix. `preferences` reports the " +
			"full catalog, including rows this configuration does not manage, so that the ids `updates` " +
			"needs can be discovered from `terraform plan`/`state show` after a first apply that sets no " +
			"toggles.",
		Attributes: map[string]schema.Attribute{
			"scope": schema.StringAttribute{
				MarkdownDescription: "Whether this manages the calling user's own preferences (`user`) " +
					"or a project's preferences (`project`). `project_id` and `org_id` are required when " +
					"this is `project`, and must be omitted when this is `user`. Changing this value " +
					"forces a new resource to be created.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(circleci.NotificationScopeUser, circleci.NotificationScopeProject),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the project whose preferences these " +
					"are. Required when `scope = \"project\"`. Changing this value forces a new resource " +
					"to be created.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"org_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization the project belongs " +
					"to. Required when `scope = \"project\"`. Changing this value forces a new resource " +
					"to be created.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"updates": schema.MapAttribute{
				MarkdownDescription: "The preference rows to manage, keyed by preference id (see " +
					"`preferences[*].id` after a first apply) and mapping to the desired `is_enabled` " +
					"value.\n\n" +
					"Leave a preference id out to let CircleCI manage it; the provider only writes rows " +
					"that appear in this map. Removing a key stops managing that row without reverting " +
					"it, since there is no route that restores a CircleCI default.",
				ElementType: types.BoolType,
				Optional:    true,
			},
			"preferences": schema.ListNestedAttribute{
				MarkdownDescription: "The full preference matrix CircleCI reports for this scope, " +
					"including rows `updates` does not manage.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the preference row. This is " +
								"the key `updates` uses.",
							Computed: true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Display name of the preference.",
							Computed:            true,
						},
						"channel": schema.StringAttribute{
							MarkdownDescription: "The delivery channel this row's `is_enabled` applies to, " +
								"for example `email`.",
							Computed: true,
						},
						"entity_scope": schema.StringAttribute{
							MarkdownDescription: "The preference's own scope attribute (for example " +
								"`actor`), distinct from this resource's `scope`.",
							Computed: true,
						},
						"display_order": schema.Int64Attribute{
							MarkdownDescription: "Display order of the preference within its group.",
							Computed:            true,
						},
						"experimental": schema.BoolAttribute{
							MarkdownDescription: "Whether CircleCI flags this preference as experimental.",
							Computed:            true,
						},
						"group_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the preference's group.",
							Computed:            true,
						},
						"group_name": schema.StringAttribute{
							MarkdownDescription: "Display name of the preference's group.",
							Computed:            true,
						},
						"section_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the preference's section, " +
								"when it belongs to one; null otherwise.",
							Computed: true,
						},
						"section_name": schema.StringAttribute{
							MarkdownDescription: "Display name of the preference's section, when it " +
								"belongs to one; null otherwise.",
							Computed: true,
						},
						"user_configurable": schema.BoolAttribute{
							MarkdownDescription: "Whether this row's `is_enabled` may be changed at all; " +
								"some rows are informational only and reject a write from `updates`.",
							Computed: true,
						},
						"is_enabled": schema.BoolAttribute{
							MarkdownDescription: "The current value CircleCI reports for this row.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// Create writes the configured preference updates.
//
// Nothing is created: the preference matrix already exists. The current
// matrix is read first, so that an invalid scope/project/org fails before
// anything is written.
func (r *notificationPreferencesResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil || !requireCloud(r.client, notificationPreferencesTypeName, &resp.Diagnostics) {
		return
	}

	var plan notificationPreferencesResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !validateNotificationPreferencesScope(plan, &resp.Diagnostics) {
		return
	}

	updates, diags := notificationPreferenceUpdatesFromMap(ctx, plan.Updates)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	scope, projectID, orgID := plan.Scope.ValueString(), plan.ProjectID.ValueString(), plan.OrgID.ValueString()

	var (
		prefs []circleci.NotificationPreference
		err   error
	)
	if len(updates) > 0 {
		prefs, err = r.client.UpdateNotificationPreferences(ctx, scope, projectID, orgID, updates)
	} else {
		prefs, err = r.client.ListNotificationPreferences(ctx, circleci.ListNotificationPreferencesOptions{
			Scope: scope, ProjectID: projectID, OrgID: orgID,
		})
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to write CircleCI notification preferences",
			circleci.Detail(err),
		)

		return
	}

	resp.Diagnostics.Append(applyNotificationPreferences(ctx, &plan, prefs)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the full matrix, and the managed rows of updates, from the
// API.
func (r *notificationPreferencesResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil || !requireCloud(r.client, notificationPreferencesTypeName, &resp.Diagnostics) {
		return
	}

	var state notificationPreferencesResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	prefs, err := r.client.ListNotificationPreferences(ctx, circleci.ListNotificationPreferencesOptions{
		Scope:     state.Scope.ValueString(),
		ProjectID: state.ProjectID.ValueString(),
		OrgID:     state.OrgID.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI notification preferences",
			circleci.Detail(err),
		)

		return
	}

	resp.Diagnostics.Append(applyNotificationPreferences(ctx, &state, prefs)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update writes the configured preference updates, warning about any
// preference id that was managed in state but is no longer in the
// configuration.
func (r *notificationPreferencesResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil || !requireCloud(r.client, notificationPreferencesTypeName, &resp.Diagnostics) {
		return
	}

	var plan, state notificationPreferencesResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !validateNotificationPreferencesScope(plan, &resp.Diagnostics) {
		return
	}

	warnAbandonedPreferences(ctx, state, plan, &resp.Diagnostics)

	updates, diags := notificationPreferenceUpdatesFromMap(ctx, plan.Updates)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	scope, projectID, orgID := plan.Scope.ValueString(), plan.ProjectID.ValueString(), plan.OrgID.ValueString()

	var (
		prefs []circleci.NotificationPreference
		err   error
	)
	if len(updates) > 0 {
		prefs, err = r.client.UpdateNotificationPreferences(ctx, scope, projectID, orgID, updates)
	} else {
		prefs, err = r.client.ListNotificationPreferences(ctx, circleci.ListNotificationPreferencesOptions{
			Scope: scope, ProjectID: projectID, OrgID: orgID,
		})
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to write CircleCI notification preferences",
			circleci.Detail(err),
		)

		return
	}

	resp.Diagnostics.Append(applyNotificationPreferences(ctx, &plan, prefs)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the resource from state without calling the API.
//
// CircleCI has no route that resets a preference row to its default, so every
// row managed by updates keeps the value Terraform last applied.
func (r *notificationPreferencesResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil || !requireCloud(r.client, notificationPreferencesTypeName, &resp.Diagnostics) {
		return
	}

	resp.Diagnostics.AddWarning(
		"CircleCI notification preferences left in place",
		fmt.Sprintf(
			"%s was removed from Terraform state, but CircleCI has no route to reset a preference row, "+
				"so every row named in `updates` keeps its current value.",
			notificationPreferencesTypeName,
		),
	)
}

// Configure adds the provider configured client to the resource.
func (r *notificationPreferencesResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ModifyPlan rejects CircleCI Server at plan time. Destroy is exempt.
func (r *notificationPreferencesResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, notificationPreferencesTypeName, &resp.Diagnostics)
}

// validateNotificationPreferencesScope checks that project_id and org_id are
// present for project scope and absent for user scope.
func validateNotificationPreferencesScope(m notificationPreferencesResourceModel, diags *diag.Diagnostics) bool {
	scope := m.Scope.ValueString()
	hasProject := m.ProjectID.ValueString() != ""
	hasOrg := m.OrgID.ValueString() != ""

	switch scope {
	case circleci.NotificationScopeProject:
		if !hasProject || !hasOrg {
			diags.AddError(
				"project_id and org_id are required",
				`project_id and org_id must both be set when scope = "project".`,
			)

			return false
		}
	case circleci.NotificationScopeUser:
		if hasProject || hasOrg {
			diags.AddError(
				"project_id and org_id must be omitted",
				`project_id and org_id must be omitted when scope = "user".`,
			)

			return false
		}
	}

	return true
}

// notificationPreferenceUpdatesFromMap converts the configured updates map
// into the client's update list. A null or unknown map means no row is
// managed.
func notificationPreferenceUpdatesFromMap(ctx context.Context, updates types.Map) ([]circleci.NotificationPreferenceUpdate, diag.Diagnostics) {
	if updates.IsNull() || updates.IsUnknown() {
		return nil, nil
	}

	var raw map[string]bool
	diags := updates.ElementsAs(ctx, &raw, false)
	if diags.HasError() {
		return nil, diags
	}

	// Sorted so requests, and therefore acceptance-test fixtures, are stable.
	ids := make([]string, 0, len(raw))
	for id := range raw {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]circleci.NotificationPreferenceUpdate, 0, len(ids))
	for _, id := range ids {
		out = append(out, circleci.NotificationPreferenceUpdate{PreferenceID: id, IsEnabled: raw[id]})
	}

	return out, diags
}

// warnAbandonedPreferences reports preference ids that state managed but the
// new plan no longer sets.
func warnAbandonedPreferences(ctx context.Context, state, plan notificationPreferencesResourceModel, diags *diag.Diagnostics) {
	var was, wanted map[string]bool
	diags.Append(state.Updates.ElementsAs(ctx, &was, false)...)
	diags.Append(plan.Updates.ElementsAs(ctx, &wanted, false)...)
	if diags.HasError() {
		return
	}

	abandoned := make([]string, 0, len(was))
	for id := range was {
		if _, stillManaged := wanted[id]; !stillManaged {
			abandoned = append(abandoned, id)
		}
	}
	if len(abandoned) == 0 {
		return
	}
	sort.Strings(abandoned)

	diags.AddWarning(
		"CircleCI notification preferences no longer managed",
		fmt.Sprintf(
			"These preference ids were removed from `updates`: %s.\n\n"+
				"CircleCI has no route that reverts a preference to its default, so each keeps the "+
				"value Terraform last applied. Set it explicitly in `updates` if you need a different "+
				"value.",
			strings.Join(abandoned, ", "),
		),
	)
}

// applyNotificationPreferences refreshes preferences (always) and the managed
// rows of updates (only the keys already present) from the API's matrix.
func applyNotificationPreferences(
	ctx context.Context, model *notificationPreferencesResourceModel, prefs []circleci.NotificationPreference,
) diag.Diagnostics {
	var diags diag.Diagnostics

	items := make([]notificationPreferenceItemModel, 0, len(prefs))
	byID := make(map[string]circleci.NotificationPreference, len(prefs))
	for _, p := range prefs {
		byID[p.ID] = p

		item := notificationPreferenceItemModel{
			ID:               types.StringValue(p.ID),
			Name:             types.StringValue(p.Name),
			Channel:          types.StringValue(p.Channel),
			EntityScope:      types.StringValue(p.EntityScope),
			DisplayOrder:     types.Int64Value(int64(p.DisplayOrder)),
			Experimental:     types.BoolValue(p.Experimental),
			GroupID:          types.StringValue(p.GroupID),
			GroupName:        types.StringValue(p.GroupName),
			SectionID:        types.StringNull(),
			SectionName:      types.StringNull(),
			UserConfigurable: types.BoolValue(p.UserConfigurable),
			IsEnabled:        types.BoolValue(p.IsEnabled),
		}
		if p.SectionID != nil {
			item.SectionID = types.StringValue(*p.SectionID)
		}
		if p.SectionName != nil {
			item.SectionName = types.StringValue(*p.SectionName)
		}

		items = append(items, item)
	}

	preferences, listDiags := types.ListValueFrom(ctx, notificationPreferenceObjectType, items)
	diags.Append(listDiags...)
	model.Preferences = preferences

	if model.Updates.IsNull() || model.Updates.IsUnknown() {
		return diags
	}

	managed := model.Updates.Elements()
	refreshed := make(map[string]attr.Value, len(managed))
	for id := range managed {
		if p, ok := byID[id]; ok {
			refreshed[id] = types.BoolValue(p.IsEnabled)
		}
		// A managed id that no longer appears in the matrix is dropped: there is
		// nothing left to manage, and keeping a stale value would misreport
		// what CircleCI actually holds.
	}

	updates, updatesDiags := types.MapValue(types.BoolType, refreshed)
	diags.Append(updatesDiags...)
	model.Updates = updates

	return diags
}
