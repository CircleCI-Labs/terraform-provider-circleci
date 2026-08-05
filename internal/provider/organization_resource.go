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
	_ resource.Resource                = &organizationResource{}
	_ resource.ResourceWithConfigure   = &organizationResource{}
	_ resource.ResourceWithImportState = &organizationResource{}
)

// organizationResourceModel maps the resource schema.
type organizationResourceModel struct {
	Id      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Slug    types.String `tfsdk:"slug"`
	VcsType types.String `tfsdk:"vcs_type"`
}

// NewOrganizationResource is a helper function to simplify the provider implementation.
func NewOrganizationResource() resource.Resource {
	return &organizationResource{}
}

// organizationResource is the resource implementation.
type organizationResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *organizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization"
}

// Schema defines the schema for the resource.
func (r *organizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI organization.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The unique ID of the CircleCI organization.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the CircleCI organization. Changing this value forces a new resource to be created.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "The slug of the CircleCI organization.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"vcs_type": schema.StringAttribute{
				MarkdownDescription: "The VCS type of the CircleCI organization: `github`, `bitbucket` or " +
					"`circleci`. Changing this value forces a new resource to be created.\n\n" +
					"~> **Only these three exact spellings are accepted.** The abbreviations that work in " +
					"an organization *slug* — `gh` and `bb` — are not valid here: the create route " +
					"validates this field against an enumeration and answers `400` for anything else. " +
					"This provider rejects it at plan time rather than letting the apply fail.",
				Required: true,
				Validators: []validator.String{
					// The enumeration is the API's, not this provider's: the create
					// route's accepted values are exactly the set
					// {"github" "bitbucket" "circleci"}, validated before anything else
					// runs, and rejected again by name if something else slips through.
					// Without this validator `vcs_type = "gh"` plans cleanly and fails
					// mid-apply — and for a resource whose central hazard is that create
					// and destroy behave differently per VCS type, a plan that looks fine
					// is the wrong place to learn the value was never valid.
					stringvalidator.OneOf(circleci.OrganizationVCSTypes()...),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *organizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan organizationResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	org, err := r.client.CreateOrganization(ctx, circleci.OrganizationInput{
		Name:    plan.Name.ValueString(),
		VCSType: plan.VcsType.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI organization",
			circleci.Detail(err),
		)
		return
	}

	// Map response body to schema and populate Computed attribute values
	plan.Id = types.StringValue(org.ID)
	plan.Slug = types.StringValue(org.Slug)

	// Set state to fully populated data
	diags = resp.State.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
}

// Read refreshes the Terraform state with the latest data.
func (r *organizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state organizationResourceModel
	diags := req.State.Get(ctx, &state)
	if diags != nil {
		resp.Diagnostics.Append(diags...)
		return
	}

	if state.Id.IsNull() {
		resp.Diagnostics.AddError(
			"Missing organization id",
			"Missing organization id",
		)
		return
	}

	org, err := r.client.GetOrganization(ctx, state.Id.ValueString())
	if circleci.IsNotFound(err) {
		resp.Diagnostics.AddWarning(
			"Organization not found during Read",
			fmt.Sprintf("Organization ID %s could not be retrieved from CircleCI. Removing from state.", state.Id.ValueString()),
		)
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Read CircleCI organization with id "+state.Id.ValueString(),
			circleci.Detail(err),
		)
		return
	}

	// Map response body to model
	state = organizationResourceModel{
		Id:      types.StringValue(org.ID),
		Name:    types.StringValue(org.Name),
		Slug:    types.StringValue(org.Slug),
		VcsType: types.StringValue(org.VCSType),
	}

	// Set state
	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *organizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Organizations cannot be updated - name and vcs_type changes require replacement
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *organizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state organizationResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Destroy must mirror what Create actually did, and that differs by vcs_type.
	//
	// For a VCS-backed organization, POST /api/v2/organization is a find-or-create:
	// it does not create anything on the VCS, it verifies the caller is an admin and
	// syncs CircleCI's record of an organization that already existed. Create
	// therefore only ever *adopted* the organization.
	//
	// DELETE /api/v2/organization/{id} has no such distinction. It tears down the
	// organization's VCS connections and deletes the organization, and the API spec
	// says that deletes "all projects including all build data".
	//
	// Calling it here would mean `terraform destroy` irreversibly destroying an
	// organization that Terraform never created — including projects and history
	// belonging to people who have never heard of this configuration. So an adopted
	// organization is released from state instead, which is the symmetric inverse
	// of adopting it.
	if !organizationIsStandalone(state.VcsType.ValueString()) {
		resp.Diagnostics.AddWarning(
			"CircleCI organization released from state, not deleted",
			fmt.Sprintf(
				"Organization %q (vcs_type = %q) was adopted rather than created: for a VCS-backed "+
					"organization the CircleCI API only verifies admin access and synchronizes its "+
					"record. It has been removed from Terraform state and left intact.\n\n"+
					"Deleting it would also delete every project in it and all of their build "+
					"history, which Terraform did not create and cannot restore. If you genuinely "+
					"intend to delete the organization, do it deliberately in the CircleCI web "+
					"application.",
				state.Name.ValueString(), state.VcsType.ValueString(),
			),
		)

		return
	}

	// A standalone organization really was created by Create, so destroying it here
	// is symmetric. An organization already gone is the desired end state, so
	// absence is not an error.
	err := r.client.DeleteOrganization(ctx, state.Id.ValueString())
	if err != nil && !circleci.IsNotFound(err) {
		resp.Diagnostics.AddError(
			"Error Deleting CircleCI Organization",
			circleci.Detail(err),
		)

		return
	}
}

// organizationIsStandalone reports whether vcsType denotes a CircleCI-native
// ("standalone") organization, whose slug looks like `circleci/<uuid>`.
//
// Only for these does POST /api/v2/organization genuinely create an
// organization; every other value resolves an organization that already exists
// on the VCS.
func organizationIsStandalone(vcsType string) bool {
	return strings.EqualFold(strings.TrimSpace(vcsType), "circleci")
}

// Configure adds the provider configured client to the resource.
func (r *organizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing resource into Terraform state.
func (r *organizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import using the organization ID
	resp.Diagnostics.Append(resp.State.SetAttribute(
		ctx, path.Root("id"), req.ID,
	)...)
}
