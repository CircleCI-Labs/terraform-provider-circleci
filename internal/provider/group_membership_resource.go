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
	_ resource.Resource                     = &groupMembershipResource{}
	_ resource.ResourceWithConfigure        = &groupMembershipResource{}
	_ resource.ResourceWithImportState      = &groupMembershipResource{}
	_ resource.ResourceWithConfigValidators = &groupMembershipResource{}
)

// groupMembershipResourceModel maps the resource schema.
type groupMembershipResourceModel struct {
	Id             types.String `tfsdk:"id"`
	OrganizationId types.String `tfsdk:"organization_id"`
	OrgId          types.String `tfsdk:"org_id"`
	GroupId        types.String `tfsdk:"group_id"`
	UserIds        types.Set    `tfsdk:"user_ids"`
}

// NewGroupMembershipResource is a helper function to simplify the provider implementation.
func NewGroupMembershipResource() resource.Resource {
	return &groupMembershipResource{}
}

// groupMembershipResource is the resource implementation.
type groupMembershipResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *groupMembershipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_membership"
}

// Schema defines the schema for the resource.
//
// The resource owns the whole membership of one group rather than a single
// user-in-group edge. That follows from the API: there is no endpoint that
// replaces a membership, only bulk add and bulk remove actions, so converging on
// a desired state requires knowing the full intended list. It is the same shape
// as aws_iam_group_membership, and carries the same caveat that two of these
// pointed at one group will fight.
func (r *groupMembershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the full set of users in a CircleCI group. " +
			"Available on CircleCI Cloud only. Groups require a `circleci` type (standalone) " +
			"organization: the API documents group creation as supported only for standalone " +
			"organizations, and a CircleCI Server installation is always a `github` type " +
			"organization.\n\n" +
			"~> **This resource takes exclusive ownership of the group's membership.** It manages the " +
			"complete member list, so any user added to the group outside Terraform is removed on the " +
			"next apply. Do not declare more than one `circleci_group_membership` for the same " +
			"`group_id`, and do not combine it with membership changes made in the CircleCI web UI.\n\n" +
			"~> **These endpoints are not part of the published CircleCI OpenAPI specification** and may " +
			"change without notice.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Identifier of this membership, in the form `organization_id/group_id`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// See org_id_deprecation.go for why these are Optional+Computed and why
			// replacement is conditional on being configured.
			"organization_id": deprecatedOrgIDAttribute("the group whose membership this manages", true),
			"org_id":          orgIDAttribute("the group whose membership this manages", true),
			"group_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the group whose membership is managed. " +
					"Changing this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"user_ids": schema.SetAttribute{
				MarkdownDescription: "Unique identifiers (UUIDs) of the users that make up the group. " +
					"This is the complete membership: users not listed here are removed from the group. " +
					"Users are addressed by UUID; a login or email address is not accepted. " +
					"An empty set empties the group.",
				Required:    true,
				ElementType: types.StringType,
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (r *groupMembershipResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		orgIDConfigValidator(),
	}
}

// Create adds the configured users to the group.
//
// Creating a membership does not create anything server-side beyond the grants
// themselves, so a group that already has members keeps any that are also listed
// here. Members that are present but not configured are removed, so that taking
// over an existing group converges on the configuration rather than merging with
// it.
func (r *groupMembershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireStandaloneCapable(r.client, "circleci_group_membership", &resp.Diagnostics) {
		return
	}

	var plan groupMembershipResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	desired, ok := r.userIDs(ctx, plan.UserIds, &resp.Diagnostics)
	if !ok {
		return
	}

	organizationID, groupID := effectiveOrgID(plan.OrganizationId, plan.OrgId), plan.GroupId.ValueString()

	actual, err := r.client.GroupMembership().List(ctx, organizationID, groupID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read the current membership of CircleCI group "+groupID,
			circleci.Detail(err),
		)

		return
	}

	if !r.converge(ctx, organizationID, groupID, desired, memberIDs(actual), &resp.Diagnostics) {
		return
	}

	plan.Id = types.StringValue(membershipID(organizationID, groupID))
	setOrgIDs(&plan.OrganizationId, &plan.OrgId, organizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the group's actual membership.
func (r *groupMembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireStandaloneCapable(r.client, "circleci_group_membership", &resp.Diagnostics) {
		return
	}

	var state groupMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID, groupID := effectiveOrgID(state.OrganizationId, state.OrgId), state.GroupId.ValueString()

	members, err := r.client.GroupMembership().List(ctx, organizationID, groupID)
	if err != nil {
		// A group deleted outside Terraform takes its membership with it: drop the
		// resource from state so the next plan recreates it.
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read the membership of CircleCI group "+groupID,
			circleci.Detail(err),
		)

		return
	}

	userIDs, diags := types.SetValueFrom(ctx, types.StringType, memberIDs(members))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Id = types.StringValue(membershipID(organizationID, groupID))
	state.UserIds = userIDs
	setOrgIDs(&state.OrganizationId, &state.OrgId, organizationID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update converges the group's membership on the configured set.
//
// The delta is computed against the membership the API currently reports rather
// than against prior state, so a member added or removed outside Terraform is
// corrected in the same apply instead of causing a redundant or missing call.
func (r *groupMembershipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !requireStandaloneCapable(r.client, "circleci_group_membership", &resp.Diagnostics) {
		return
	}

	var plan groupMembershipResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	desired, ok := r.userIDs(ctx, plan.UserIds, &resp.Diagnostics)
	if !ok {
		return
	}

	organizationID, groupID := effectiveOrgID(plan.OrganizationId, plan.OrgId), plan.GroupId.ValueString()

	actual, err := r.client.GroupMembership().List(ctx, organizationID, groupID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read the current membership of CircleCI group "+groupID,
			circleci.Detail(err),
		)

		return
	}

	if !r.converge(ctx, organizationID, groupID, desired, memberIDs(actual), &resp.Diagnostics) {
		return
	}

	plan.Id = types.StringValue(membershipID(organizationID, groupID))
	setOrgIDs(&plan.OrganizationId, &plan.OrgId, organizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the managed users from the group.
//
// The group itself is left alone: this resource owns the membership, not the
// group, so destroying it empties out the users it put there. Only the users
// recorded in state are removed, so a member added outside Terraform after the
// last apply survives.
func (r *groupMembershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireStandaloneCapable(r.client, "circleci_group_membership", &resp.Diagnostics) {
		return
	}

	var state groupMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	userIDs, ok := r.userIDs(ctx, state.UserIds, &resp.Diagnostics)
	if !ok {
		return
	}

	groupID := state.GroupId.ValueString()

	err := r.client.GroupMembership().Remove(ctx, effectiveOrgID(state.OrganizationId, state.OrgId), groupID, userIDs)
	if err != nil {
		// A group that is already gone has no membership to clear, which is the
		// desired end state.
		if circleci.IsNotFound(err) {
			return
		}

		resp.Diagnostics.AddError(
			"Unable to remove users from CircleCI group "+groupID,
			circleci.Detail(err),
		)
	}
}

// converge issues the add and remove calls that take the group from actual to
// desired. It reports whether both succeeded.
//
// Removals are issued before additions so that a membership at a size limit can
// still be rewritten in one apply.
func (r *groupMembershipResource) converge(
	ctx context.Context,
	organizationID, groupID string,
	desired, actual []string,
	diags *diag.Diagnostics,
) bool {
	toAdd, toRemove := circleci.MemberDelta(desired, actual)

	if err := r.client.GroupMembership().Remove(ctx, organizationID, groupID, toRemove); err != nil {
		diags.AddError(
			"Unable to remove users from CircleCI group "+groupID,
			circleci.Detail(err),
		)

		return false
	}

	if err := r.client.GroupMembership().Add(ctx, organizationID, groupID, toAdd); err != nil {
		diags.AddError(
			"Unable to add users to CircleCI group "+groupID,
			circleci.Detail(err),
		)

		return false
	}

	return true
}

// userIDs converts a set of user ids into a slice, reporting false when the set
// cannot be read. A null or unknown set is treated as empty.
func (r *groupMembershipResource) userIDs(ctx context.Context, set types.Set, diags *diag.Diagnostics) ([]string, bool) {
	if set.IsNull() || set.IsUnknown() {
		return nil, true
	}

	var ids []string
	diags.Append(set.ElementsAs(ctx, &ids, false)...)

	return ids, !diags.HasError()
}

// Configure adds the provider configured client to the resource.
func (r *groupMembershipResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing group's membership into Terraform state.
//
// A group id is only unique within its organization, and the organization is
// part of every route, so the import id carries both.
func (r *groupMembershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	organizationID, groupID, ok := strings.Cut(req.ID, "/")
	if !ok || organizationID == "" || groupID == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID for circleci_group_membership",
			fmt.Sprintf(
				"Expected an import ID in the format \"organization_id/group_id\", got: %q.\n\n"+
					"For example:\n  terraform import circleci_group_membership.example "+
					"\"00000000-0000-0000-0000-000000000000/11111111-1111-1111-1111-111111111111\"",
				req.ID,
			),
		)

		return
	}

	// Both organization attribute names are set, so a configuration written
	// against either one imports cleanly. See org_id_deprecation.go.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), organizationID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("org_id"), organizationID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), groupID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), membershipID(organizationID, groupID))...)
}

// membershipID builds the synthetic id for a group membership. The API has no id
// for a membership, so it is derived from the pair that addresses it.
func membershipID(organizationID, groupID string) string {
	return organizationID + "/" + groupID
}

// memberIDs projects group members onto their user ids.
func memberIDs(members []circleci.GroupMember) []string {
	ids := make([]string, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.UserID)
	}

	return ids
}
