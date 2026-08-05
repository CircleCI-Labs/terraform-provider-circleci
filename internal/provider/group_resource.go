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

// groupNameAndDescriptionPattern is the character class the backend behind
// the public API's /organizations/{org_id}/groups routes actually enforces
// for both `name` and `description`, confirmed by reading its implementation:
// `^[a-zA-Z0-9-_ .,\s]*$`. Letters, digits, space, hyphen, underscore, period
// and comma are accepted; everything else, including a semicolon, is
// rejected with a 400.
//
// The API's own error message — "can contain only underscores, dashes and
// alphanumeric characters" — undersells what it actually accepts (it also allows
// spaces, periods and commas) but correctly identifies what it rejects, which is
// everything else. Do not derive this from the message text alone: it is
// identical for both attributes, but the length bound is not (100 vs. 200
// below), and reading the code rather than the message is what catches that.
var groupNameAndDescriptionPattern = regexp.MustCompile(`^[a-zA-Z0-9\-_ .,\s]*$`)

// groupNameAndDescriptionPatternDescription is shared between the two attribute
// validators below so the wording cannot drift between them.
const groupNameAndDescriptionPatternDescription = "must contain only letters, numbers, spaces, and the characters - _ . ,"

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                     = &groupResource{}
	_ resource.ResourceWithConfigure        = &groupResource{}
	_ resource.ResourceWithImportState      = &groupResource{}
	_ resource.ResourceWithConfigValidators = &groupResource{}
)

// groupResourceModel maps the resource schema.
type groupResourceModel struct {
	Id             types.String `tfsdk:"id"`
	OrganizationId types.String `tfsdk:"organization_id"`
	OrgId          types.String `tfsdk:"org_id"`
	Name           types.String `tfsdk:"name"`
	Description    types.String `tfsdk:"description"`
}

// NewGroupResource is a helper function to simplify the provider implementation.
func NewGroupResource() resource.Resource {
	return &groupResource{}
}

// groupResource is the resource implementation.
type groupResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *groupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

// Schema defines the schema for the resource.
//
// Every writable attribute forces replacement: the groups API has no update
// endpoint, so a changed name or description can only be applied by creating a
// new group.
func (r *groupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI group: a named collection of organization members that " +
			"project and context permissions can be granted to. " +
			"Available on CircleCI Cloud only. Groups require a `circleci` type (standalone) " +
			"organization: the API documents group creation as supported only for standalone " +
			"organizations, and a CircleCI Server installation is always a `github` type " +
			"organization.\n\n" +
			"~> **Group membership is not managed by Terraform.** Adding and removing group members is " +
			"only possible in the CircleCI web UI. This resource manages the group itself, not the " +
			"users in it, and Terraform will not touch the membership of a group it manages.\n\n" +
			"~> **Groups cannot be updated in place.** The API has no update endpoint, so changing any " +
			"attribute destroys and recreates the group.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the group.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// See org_id_deprecation.go for why these are Optional+Computed and why
			// replacement is conditional on being configured.
			"organization_id": deprecatedOrgIDAttribute("this group", true),
			"org_id":          orgIDAttribute("this group", true),
			"name": schema.StringAttribute{
				MarkdownDescription: "Name of the group. Changing this value forces a new resource to be created.\n\n" +
					"CircleCI's group service rejects a name that is empty, is 100 characters or longer, or " +
					groupNameAndDescriptionPatternDescription + ".",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					// The service's own check is `len(s) >= 100` (rejected), which
					// allows at most 99 characters even though its error message
					// advertises "1-100" — read from source, not the message; see
					// groupNameAndDescriptionPattern.
					stringvalidator.LengthBetween(1, 99),
					stringvalidator.RegexMatches(groupNameAndDescriptionPattern, groupNameAndDescriptionPatternDescription),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description of the group. Changing this value forces a new resource to be created.\n\n" +
					"CircleCI's group service rejects a description that is 200 characters or longer, or " +
					groupNameAndDescriptionPatternDescription + " (an empty description is accepted).",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					// RequiresReplaceIfConfigured rather than RequiresReplace,
					// because this attribute is also computed: dropping it from
					// the configuration keeps the value the server already has
					// instead of destroying the group.
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.String{
					// Same off-by-one as name: the service rejects `len(s) >= 200`,
					// so 199 is the longest description it actually accepts.
					stringvalidator.LengthAtMost(199),
					stringvalidator.RegexMatches(groupNameAndDescriptionPattern, groupNameAndDescriptionPatternDescription),
				},
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (r *groupResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		orgIDConfigValidator(),
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *groupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireStandaloneCapable(r.client, "circleci_group", &resp.Diagnostics) {
		return
	}

	var plan groupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(plan.OrganizationId, plan.OrgId)

	group, err := r.client.Groups().Create(ctx, organizationID, circleci.CreateGroupRequest{
		Name:        plan.Name.ValueString(),
		Description: plan.Description.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to create CircleCI group",
			circleci.Detail(err),
		)

		return
	}

	state := groupResourceModel{
		Id:          types.StringValue(group.ID),
		Name:        types.StringValue(group.Name),
		Description: types.StringValue(group.Description),
	}
	setOrgIDs(&state.OrganizationId, &state.OrgId, organizationID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *groupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireStandaloneCapable(r.client, "circleci_group", &resp.Diagnostics) {
		return
	}

	var state groupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(state.OrganizationId, state.OrgId)

	group, err := r.client.Groups().Get(ctx, organizationID, state.Id.ValueString())
	if err != nil {
		// A group deleted outside Terraform is not an error: drop it from state
		// so the next plan recreates it.
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		// 403 "Permission denied." is a *different* condition from the 404 above,
		// and both have to be handled.
		//
		// Re-checked against source: absence really is 404. The service behind this
		// route resolves the group and maps "no such group" — including a group
		// belonging to a different organization — to a not-found error, which the
		// public API relays as 404. The 403 comes from earlier: the route sits
		// behind an organization-level permission check that runs before the
		// handler, so a token that cannot view the organization's access
		// configuration, or an organization it cannot see at all, is refused
		// without the group ever being looked up.
		//
		// So a 403 says nothing about whether the group still exists, which is
		// exactly why it must not drop state: a token that loses permission would
		// otherwise silently cause Terraform to recreate live groups. Erroring is
		// the safer default, and the diagnostic names every cause rather than
		// guessing between them.
		if circleci.IsUnauthorized(err) {
			resp.Diagnostics.AddError(
				"Unable to read CircleCI group "+state.Id.ValueString(),
				fmt.Sprintf(
					"The API denied access to this group. It has either been deleted outside "+
						"Terraform, or belongs to a different organization, or the configured token "+
						"lacks permission — the API returns the same response for all three and does "+
						"not distinguish them.\n\n"+
						"If the group was deleted, remove it from state with:\n"+
						"  terraform state rm %s\n\n%s",
					"circleci_group."+state.Name.ValueString(),
					circleci.Detail(err),
				),
			)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read CircleCI group "+state.Id.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	state.Id = types.StringValue(group.ID)
	state.Name = types.StringValue(group.Name)
	state.Description = types.StringValue(group.Description)
	setOrgIDs(&state.OrganizationId, &state.OrgId, organizationID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is a no-op beyond persisting the plan.
//
// The groups API has no update endpoint, so every writable attribute is marked
// RequiresReplace and Terraform never reaches this method for a real change. It
// exists to satisfy the resource interface. See internal/circleci/group.go.
func (r *groupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !requireStandaloneCapable(r.client, "circleci_group", &resp.Diagnostics) {
		return
	}

	var plan groupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	setOrgIDs(&plan.OrganizationId, &plan.OrgId, effectiveOrgID(plan.OrganizationId, plan.OrgId))

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *groupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireStandaloneCapable(r.client, "circleci_group", &resp.Diagnostics) {
		return
	}

	var state groupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.Groups().Delete(ctx, effectiveOrgID(state.OrganizationId, state.OrgId), state.Id.ValueString())
	if err != nil {
		// Already gone is the desired end state, so a 404 is a success.
		//
		// 403 is tolerated too, but for a weaker reason than Read's comment used to
		// claim: a deleted group answers 404, and the 403 comes from the
		// organization-level permission check in front of the route. Treating it as
		// success here is still right — the alternative is a destroy that can never
		// complete, stranding the resource in state — and unlike Read this direction
		// is safe, because failing to delete something that may still exist is
		// visible the next time anything reads it, whereas silently dropping state
		// on Read would quietly recreate a live group.
		if circleci.IsNotFound(err) || circleci.IsUnauthorized(err) {
			return
		}

		resp.Diagnostics.AddError(
			"Unable to delete CircleCI group "+state.Id.ValueString(),
			circleci.Detail(err),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *groupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing group into Terraform state.
//
// A group id is only unique within its organization, and the organization is
// part of every route, so the import id carries both.
func (r *groupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	organizationID, groupID, ok := strings.Cut(req.ID, "/")
	if !ok || organizationID == "" || groupID == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID for circleci_group",
			fmt.Sprintf(
				"Expected an import ID in the format \"organization_id/group_id\", got: %q.\n\n"+
					"For example:\n  terraform import circleci_group.example "+
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
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), groupID)...)
}
