// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

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
	_ resource.Resource                = &contextResource{}
	_ resource.ResourceWithConfigure   = &contextResource{}
	_ resource.ResourceWithImportState = &contextResource{}
)

// contextResourceModel maps the output schema.
type contextResourceModel struct {
	OrganizationId types.String `tfsdk:"organization_id"`
	Id             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

// NewContextResource is a helper function to simplify the provider implementation.
func NewContextResource() resource.Resource {
	return &contextResource{}
}

// contextResource is the resource implementation.
type contextResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *contextResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_context"
}

// Schema defines the schema for the resource.
func (r *contextResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI context. Contexts provide a mechanism for securing and sharing environment variables across projects.",
		Attributes: map[string]schema.Attribute{
			"organization_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the organization that owns this context. There is no API " +
					"route to move a context between organizations, so changing this value forces a new " +
					"resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					// See the BUG note this replaces, below: without this, changing
					// organization_id planned a silent in-place update that Update()
					// could never actually perform.
					stringplanmodifier.RequiresReplace(),
				},
			},
			"id": schema.StringAttribute{
				MarkdownDescription: "The unique identifier of the context.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the CircleCI context. Changing this value forces a new resource to be created.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the context was created.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *contextResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan contextResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreateContext(ctx, plan.OrganizationId.ValueString(), plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI context",
			circleci.Detail(err),
		)

		return
	}

	plan.Id = types.StringValue(created.ID)
	plan.Name = types.StringValue(created.Name)
	plan.CreatedAt = types.StringValue(created.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *contextResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state contextResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	found, err := r.client.GetContext(ctx, state.Id.ValueString())
	if err != nil {
		// A context deleted outside Terraform is not an error: drop it from
		// state so the next plan recreates it. This is only reachable for the
		// rare case documented on GetContext (a lookup that resolves the id but
		// then fails to read it); see the 403 handling just below for the
		// common case.
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		// A context that no longer exists answers 403, not 404: every route
		// addressing a context by id sits behind the API's the context-resolution step
		// middleware, which maps a context that cannot be resolved — deleted,
		// in another organization, or simply inaccessible to this token — to
		// the same Forbidden response (see internal/circleci/context.go's
		// GetContext comment).
		//
		// Silently removing the resource on 403 would mean a token that merely
		// lost permission causes Terraform to recreate a live context on the
		// next apply. Erroring instead — and naming both possible causes — is
		// the safer default, matching circleci_group's Read.
		if circleci.IsUnauthorized(err) {
			resp.Diagnostics.AddError(
				"Unable to read CircleCI context "+state.Id.ValueString(),
				fmt.Sprintf(
					"The API denied access to this context. It has either been deleted outside "+
						"Terraform, or belongs to a different organization, or the configured token "+
						"lacks permission — the API returns the same response for all three and does "+
						"not distinguish them.\n\n"+
						"If the context was deleted, remove it from state with:\n"+
						"  terraform state rm %s\n\n%s",
					"circleci_context."+state.Name.ValueString(),
					circleci.Detail(err),
				),
			)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read CircleCI context "+state.Id.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	state.Id = types.StringValue(found.ID)
	state.Name = types.StringValue(found.Name)
	state.CreatedAt = types.StringValue(found.CreatedAt)
	// OrganizationId is preserved from state: the read route does not report
	// which organization a context belongs to.

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is unreachable for a real configuration change: every attribute is
// RequiresReplace, so Terraform never calls this for anything but a
// refresh-driven re-apply of an unchanged plan. It persists the plan so that
// case is a no-op rather than leaving the prior state's zero-value fields
// behind.
func (r *contextResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan contextResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *contextResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state contextResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteContext(ctx, state.Id.ValueString())
	if err != nil {
		// Already gone is the desired end state. 403 counts as gone here,
		// unlike in Read: DeleteContext documents that a missing context
		// answers 403 through the same context-resolution step as GetContext,
		// so it is the response an already-deleted context produces on delete.
		// A destroy is not expected to distinguish "gone" from "never had
		// permission" the way a refresh is, because there is no live resource
		// left to protect either way.
		if circleci.IsNotFound(err) || circleci.IsUnauthorized(err) {
			return
		}

		resp.Diagnostics.AddError(
			"Error deleting CircleCI context",
			circleci.Detail(err),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *contextResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

func (r *contextResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Expected format: "ORGANIZATION_ID/CONTEXT_ID"
	parts := strings.SplitN(req.ID, "/", 2)

	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid Import ID Format",
			fmt.Sprintf("Expected import ID format: 'organization_id/context_id'. Got: %s", req.ID),
		)

		return
	}

	organizationID := parts[0]
	contextID := parts[1]

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), contextID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), organizationID)...)
}
