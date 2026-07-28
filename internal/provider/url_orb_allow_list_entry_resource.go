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
	_ resource.Resource                = &urlOrbAllowListEntryResource{}
	_ resource.ResourceWithConfigure   = &urlOrbAllowListEntryResource{}
	_ resource.ResourceWithImportState = &urlOrbAllowListEntryResource{}
)

// urlOrbAllowListOrganizationDescription documents the organization identifier,
// which the v2 route accepts as either a UUID or a slug. It is shared with the
// plural data source so the two cannot describe it differently.
const urlOrbAllowListOrganizationDescription = "The organization the allow list belongs to, as either an organization UUID or a slug in " +
	"`vcs-slug/org-name` form such as `gh/CircleCI-Public`. For GitLab and GitHub App " +
	"organizations, use `circleci/<organization-id>`."

// urlOrbAllowListEntryResourceModel maps the resource schema.
type urlOrbAllowListEntryResourceModel struct {
	Id           types.String `tfsdk:"id"`
	Organization types.String `tfsdk:"organization"`
	Name         types.String `tfsdk:"name"`
	Prefix       types.String `tfsdk:"prefix"`
	Auth         types.String `tfsdk:"auth"`
}

// NewURLOrbAllowListEntryResource is a helper function to simplify the provider
// implementation.
func NewURLOrbAllowListEntryResource() resource.Resource {
	return &urlOrbAllowListEntryResource{}
}

// urlOrbAllowListEntryResource manages one entry in an organization's URL orb
// allow list.
//
// This is a v2 API, so unlike the organization settings it works on CircleCI
// Server as well as Cloud and must not be gated on requireCloud.
type urlOrbAllowListEntryResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *urlOrbAllowListEntryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_url_orb_allow_list_entry"
}

// Schema defines the schema for the resource.
func (r *urlOrbAllowListEntryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages one entry in a CircleCI organization's URL orb allow list. " +
			"Each entry permits pipelines in the organization to reference URL orbs whose source " +
			"URL starts with the entry's prefix.\n\n" +
			"**Available on CircleCI Cloud and CircleCI Server.** This resource uses the v2 API, " +
			"which both serve.\n\n" +
			"The API has no update route for an entry, so changing any attribute replaces the entry.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The UUID of the allow list entry.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"organization": schema.StringAttribute{
				MarkdownDescription: urlOrbAllowListOrganizationDescription +
					" Changing this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "A human-readable name for the entry. Changing this value forces a new resource to be created, " +
					"because the API has no route that updates an entry in place.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"prefix": schema.StringAttribute{
				MarkdownDescription: "The URL prefix to allow, for example " +
					"`https://raw.githubusercontent.com/CircleCI-Public/orbs/refs/heads/main/`. " +
					"A URL orb reference is permitted when it starts with this prefix, so keep the " +
					"prefix as narrow as possible. Changing this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"auth": schema.StringAttribute{
				MarkdownDescription: "The authentication method used when fetching a URL that matches `prefix`. " +
					"One of `bitbucket-oauth`, `github-app`, `github-oauth` or `none`. Use `none` for a " +
					"publicly readable URL. Changing this value forces a new resource to be created.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(circleci.URLOrbAllowListAuthValues...),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

// Create adds the entry to the organization's allow list.
func (r *urlOrbAllowListEntryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError(
			"Provider Not Configured",
			"The CircleCI API client is unset. Please report this issue to the provider developers.",
		)

		return
	}

	var plan urlOrbAllowListEntryResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := plan.Organization.ValueString()

	entry, err := r.client.CreateURLOrbAllowListEntry(ctx, org, circleci.CreateURLOrbAllowListEntryRequest{
		Name:   plan.Name.ValueString(),
		Prefix: plan.Prefix.ValueString(),
		Auth:   plan.Auth.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to create CircleCI URL orb allow list entry in organization "+org,
			circleci.Detail(err),
		)

		return
	}

	plan.Id = types.StringValue(entry.ID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the entry from the organization's allow list.
func (r *urlOrbAllowListEntryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError(
			"Provider Not Configured",
			"The CircleCI API client is unset. Please report this issue to the provider developers.",
		)

		return
	}

	var state urlOrbAllowListEntryResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := state.Organization.ValueString()

	entry, err := r.client.GetURLOrbAllowListEntry(ctx, org, state.Id.ValueString())
	if err != nil {
		// IsNotFound covers both a 404 on the organization and an entry that is
		// simply absent from the listing, which is how a deleted entry shows up.
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read CircleCI URL orb allow list entry "+state.Id.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	state.Name = types.StringValue(entry.Name)
	state.Prefix = types.StringValue(entry.Prefix)
	state.Auth = types.StringValue(entry.Auth)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is unreachable: every attribute requires replacement because the API
// has no route that updates an allow list entry. It reports an error rather than
// silently doing nothing, so a future schema change that drops a RequiresReplace
// cannot quietly stop writing to CircleCI.
func (r *urlOrbAllowListEntryResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"CircleCI URL orb allow list entries cannot be updated",
		"The CircleCI API has no route that modifies an existing URL orb allow list entry, so every "+
			"attribute of circleci_url_orb_allow_list_entry forces replacement and this code path "+
			"should be unreachable. Please report this issue to the provider developers.",
	)
}

// Delete removes the entry from the organization's allow list.
func (r *urlOrbAllowListEntryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError(
			"Provider Not Configured",
			"The CircleCI API client is unset. Please report this issue to the provider developers.",
		)

		return
	}

	var state urlOrbAllowListEntryResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteURLOrbAllowListEntry(ctx, state.Organization.ValueString(), state.Id.ValueString())
	if err != nil {
		// Already gone is the outcome Delete wants.
		if circleci.IsNotFound(err) {
			return
		}

		resp.Diagnostics.AddError(
			"Unable to delete CircleCI URL orb allow list entry "+state.Id.ValueString(),
			circleci.Detail(err),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *urlOrbAllowListEntryResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing entry.
//
// The entry id alone is not enough, because reading an entry means listing the
// organization's allow list, so the import id carries the organization too.
func (r *urlOrbAllowListEntryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Expected format: "<organization>/<entry-id>". A slug organization contains
	// a slash of its own, so split from the right on the last one.
	slash := strings.LastIndex(req.ID, "/")
	if slash <= 0 || slash == len(req.ID)-1 {
		resp.Diagnostics.AddError(
			"Invalid Import ID Format",
			fmt.Sprintf(
				"Expected import ID format 'organization/entry_id', for example "+
					"'gh/acme/ba98990a-5a00-4cad-b55e-b44117b92e0c'. Got: %s",
				req.ID,
			),
		)

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization"), req.ID[:slash])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID[slash+1:])...)
}
