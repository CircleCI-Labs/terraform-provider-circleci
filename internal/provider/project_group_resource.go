// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
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
	_ resource.Resource                     = &projectGroupResource{}
	_ resource.ResourceWithConfigure        = &projectGroupResource{}
	_ resource.ResourceWithImportState      = &projectGroupResource{}
	_ resource.ResourceWithConfigValidators = &projectGroupResource{}
)

// projectGroupResourceModel maps the resource schema.
type projectGroupResourceModel struct {
	Id             types.String `tfsdk:"id"`
	OrganizationId types.String `tfsdk:"organization_id"`
	OrgId          types.String `tfsdk:"org_id"`
	ProjectId      types.String `tfsdk:"project_id"`
	GroupId        types.String `tfsdk:"group_id"`
	Role           types.String `tfsdk:"role"`
	Name           types.String `tfsdk:"name"`
}

// NewProjectGroupResource is a helper function to simplify the provider implementation.
func NewProjectGroupResource() resource.Resource {
	return &projectGroupResource{}
}

// projectGroupResource is the resource implementation.
type projectGroupResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *projectGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_group"
}

// Schema defines the schema for the resource.
//
// role is the only attribute that can change in place, because it is the only one
// the API can update: the /update-role action takes a role and nothing else. The
// three identifiers address the grant and so force replacement.
func (r *projectGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Grants a CircleCI group a role on a project, so that every member of the " +
			"group holds that role on the project. " +
			"Requires CircleCI Cloud.\n\n" +
			"!> **A group cannot be removed from a project by Terraform.** The API exposes no endpoint " +
			"for revoking a project group grant, so destroying this resource removes it from Terraform " +
			"state and leaves the grant in place. Remove the group from the project in the CircleCI web " +
			"UI to actually revoke the access.\n\n" +
			"~> **These endpoints are not part of the published CircleCI OpenAPI specification** and may " +
			"change without notice.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Identifier of this grant, in the form " +
					"`organization_id/project_id/group_id`.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// See org_id_deprecation.go for why these are Optional+Computed and why
			// replacement is conditional on being configured.
			"organization_id": deprecatedOrgIDAttribute("the project and group in this grant", true),
			"org_id":          orgIDAttribute("the project and group in this grant", true),
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the project the group is granted access " +
					"to. Changing this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"group_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the group being granted access. " +
					"Changing this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"role": schema.StringAttribute{
				MarkdownDescription: "Role the group holds on the project. One of `project-admin`, " +
					"`project-contributor` or `project-viewer`. This can be changed in place. " +
					"The organization-level roles (`org-admin`, `org-contributor`, `org-viewer`) are " +
					"not valid for a project grant.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(circleci.ProjectRoles()...),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Name of the group, as reported by the API.",
				Computed:            true,
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (r *projectGroupResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		orgIDConfigValidator(),
	}
}

// Create grants the group its role on the project.
//
// The assign call answers with an acknowledgement rather than the stored grant,
// so the grant is read back to pick up the group's name and to confirm it landed.
func (r *projectGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectGroupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(plan.OrganizationId, plan.OrgId)
	projectID := plan.ProjectId.ValueString()
	groupID := plan.GroupId.ValueString()

	err := r.client.ProjectGroups().Assign(ctx, organizationID, projectID, plan.Role.ValueString(),
		[]string{groupID})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to add CircleCI group "+groupID+" to project "+projectID,
			circleci.Detail(err),
		)

		return
	}

	group, err := r.client.ProjectGroups().Get(ctx, organizationID, projectID, groupID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read back the CircleCI project group grant for group "+groupID,
			circleci.Detail(err),
		)

		return
	}

	plan.Id = types.StringValue(projectGroupID(organizationID, projectID, groupID))
	plan.Role = types.StringValue(group.Role)
	plan.Name = types.StringValue(group.Name)
	setOrgIDs(&plan.OrganizationId, &plan.OrgId, organizationID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the grant's current role.
func (r *projectGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectGroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(state.OrganizationId, state.OrgId)
	projectID := state.ProjectId.ValueString()
	groupID := state.GroupId.ValueString()

	group, err := r.client.ProjectGroups().Get(ctx, organizationID, projectID, groupID)
	if err != nil {
		// A grant revoked in the web UI is not an error: drop it from state so the
		// next plan recreates it.
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read the CircleCI project group grant for group "+groupID,
			circleci.Detail(err),
		)

		return
	}

	state.Id = types.StringValue(projectGroupID(organizationID, projectID, groupID))
	state.Role = types.StringValue(group.Role)
	state.Name = types.StringValue(group.Name)
	setOrgIDs(&state.OrganizationId, &state.OrgId, organizationID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update changes the role the group holds on the project.
func (r *projectGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan projectGroupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(plan.OrganizationId, plan.OrgId)
	projectID := plan.ProjectId.ValueString()
	groupID := plan.GroupId.ValueString()

	err := r.client.ProjectGroups().UpdateRole(ctx, organizationID, projectID, groupID,
		plan.Role.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to update the role of CircleCI group "+groupID+" on project "+projectID,
			circleci.Detail(err),
		)

		return
	}

	group, err := r.client.ProjectGroups().Get(ctx, organizationID, projectID, groupID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read back the CircleCI project group grant for group "+groupID,
			circleci.Detail(err),
		)

		return
	}

	plan.Id = types.StringValue(projectGroupID(organizationID, projectID, groupID))
	plan.Role = types.StringValue(group.Role)
	plan.Name = types.StringValue(group.Name)
	setOrgIDs(&plan.OrganizationId, &plan.OrgId, organizationID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete drops the grant from Terraform state without revoking it.
//
// The public API routes a list, an assign and a role update for project groups,
// but no revoke: a delete appears in the API's own OpenAPI definition yet is not
// served. Rather than fail every destroy and leave configurations that can never
// be torn down, this succeeds with a warning that names what is left behind and
// where to remove it. Returning an error here would also block destroying the
// project or group the grant belongs to.
func (r *projectGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectGroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddWarning(
		"CircleCI project group grant left in place",
		fmt.Sprintf(
			"Group %s still has the %q role on project %s.\n\n"+
				"The CircleCI API has no endpoint for removing a group from a project, so this grant "+
				"has been removed from Terraform state but not revoked. Every member of the group keeps "+
				"their access to the project.\n\n"+
				"Remove the group from the project's settings in the CircleCI web UI to revoke it.",
			state.GroupId.ValueString(), state.Role.ValueString(), state.ProjectId.ValueString(),
		),
	)
}

// Configure adds the provider configured client to the resource.
func (r *projectGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing project group grant into Terraform state.
//
// All three ids are needed: the group and project ids are only meaningful within
// their organization, and the organization is part of the route.
func (r *projectGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID for circleci_project_group",
			fmt.Sprintf(
				"Expected an import ID in the format \"organization_id/project_id/group_id\", got: %q.\n\n"+
					"For example:\n  terraform import circleci_project_group.example "+
					"\"00000000-0000-0000-0000-000000000000/11111111-1111-1111-1111-111111111111"+
					"/22222222-2222-2222-2222-222222222222\"",
				req.ID,
			),
		)

		return
	}

	// Both organization attribute names are set, so a configuration written
	// against either one imports cleanly. See org_id_deprecation.go.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("org_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), parts[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"),
		projectGroupID(parts[0], parts[1], parts[2]))...)
}

// projectGroupID builds the synthetic id for a project group grant. The API has
// no id for a grant, so it is derived from the triple that addresses it.
func projectGroupID(organizationID, projectID, groupID string) string {
	return organizationID + "/" + projectID + "/" + groupID
}
