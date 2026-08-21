// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"
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
	_ resource.Resource                = &contextRestrictionResource{}
	_ resource.ResourceWithConfigure   = &contextRestrictionResource{}
	_ resource.ResourceWithImportState = &contextRestrictionResource{}
)

// contextRestrictionResourceModel maps the output schema.
type contextRestrictionResourceModel struct {
	Id        types.String `tfsdk:"id"`
	ContextId types.String `tfsdk:"context_id"`
	ProjectId types.String `tfsdk:"project_id"`
	Name      types.String `tfsdk:"name"`
	Type      types.String `tfsdk:"type"`
	Value     types.String `tfsdk:"value"`
}

// NewContextRestrictionResource is a helper function to simplify the provider implementation.
func NewContextRestrictionResource() resource.Resource {
	return &contextRestrictionResource{}
}

// contextRestrictionResource is the resource implementation.
type contextRestrictionResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *contextRestrictionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_context_restriction"
}

// Schema defines the schema for the resource.
func (r *contextRestrictionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a restriction on a CircleCI context. Restrictions control which projects or groups can use a context.\n\n" +
			"~> **Every context already carries one restriction before Terraform ever manages it.** " +
			"[NET, measured on 2026-08-21]: CircleCI creates a `group` restriction named \"All members\" " +
			"(value equal to the context's own organization UUID) on every context as part of creating " +
			"the context itself — visible on read even though nothing ever called this resource's create. " +
			"It is not special or protected: it can be deleted like any other restriction (import it and " +
			"remove it from configuration, or delete it directly through the API), and CircleCI never " +
			"recreates it.\n\n" +
			"This is a **security-group** restriction: per CircleCI's documentation, it governs which " +
			"organization *members* may use the context, and \"All members\" is the permissive default " +
			"naming every member. A `project` restriction governs a separate axis — which *projects* may " +
			"use the context — and per CircleCI's documentation and support the two combine as an AND: a " +
			"context carrying both \"All members\" and a `project` restriction is usable by any org " +
			"member, but only from the listed projects. Adding a `project` restriction is therefore " +
			"already effective with \"All members\" left in place. Do **not** delete the default `group` " +
			"restriction to \"activate\" a `project` restriction — removing every `group` restriction " +
			"narrows the context to organization administrators only and breaks scheduled workflows and " +
			"bot-triggered pipelines (e.g. Renovate), which hold no group membership. (The enforcement " +
			"behavior in this paragraph is documented by CircleCI and confirmed by CircleCI support, not " +
			"independently measured against this provider — the API only lets us observe the listing, " +
			"not the enforcement.) Use `circleci_context_restrictions` (the plural data source) to see " +
			"the full set, including this default entry.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The unique identifier of the restriction.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"context_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the context to restrict. Changing this value forces a new resource to be created.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "The project ID associated with the restriction. Only set for `project` restrictions.",
				Computed:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name associated with the restriction. The create response never " +
					"carries this — CircleCI learns it (e.g. a project's slug) out of band and only reports " +
					"it on a later read — so it reads back as empty immediately after create/apply and " +
					"picks up its real value on the next refresh.",
				Computed: true,
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "The kind of restriction: `project` restricts the context to a " +
					"project, `expression` restricts it with an expression, and `group` restricts " +
					"it to a group.\n\n" +
					"~> [NET, measured on 2026-08-21]: through this resource's create route, `group` " +
					"only ever succeeded on an OAuth-backed organization (a classic `gh/<org>` or " +
					"`bitbucket/<org>` slug), and the only `value` accepted was the organization's " +
					"own UUID — the value CircleCI already assigns to the default \"All members\" " +
					"restriction every context carries (see the resource description above). We did " +
					"not test a `value` naming a real VCS security group, so this does not establish " +
					"what the type can or cannot restrict in general — only that the create route we " +
					"exercised accepted nothing else. Per CircleCI's documentation, `group` " +
					"restrictions are security groups governing which organization members may use " +
					"the context (a members axis, separate from `project`'s projects axis); \"All " +
					"members\" is that axis's permissive default. On a standalone (`circleci/<uuid>`) " +
					"organization, `group` fails outright, whatever the value.\n\n" +
					"~> `expression`'s grammar is checked at apply time — malformed syntax is " +
					"rejected — but the fields an expression names are not: a field that does not " +
					"exist, or a type mismatch (a number field compared to a string), is silently " +
					"accepted. A typo therefore creates a restriction with no error anywhere, that " +
					"may not guard what its author intended. Copy a working expression out of a " +
					"restriction created in the web application rather than writing one from " +
					"scratch, and verify its effect rather than trusting that CircleCI would have " +
					"rejected a mistake.\n\n" +
					"Changing this value forces a new resource to be created.",
				Required: true,
				Validators: []validator.String{
					// The API accepts all three; only `project` was previously
					// documented, so `group` and `expression` worked but were
					// undiscoverable. Constraining the set also turns a typo into a
					// plan-time error rather than an API rejection at apply.
					stringvalidator.OneOf(
						circleci.ContextRestrictionTypeProject,
						circleci.ContextRestrictionTypeGroup,
						circleci.ContextRestrictionTypeExpression,
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"value": schema.StringAttribute{
				MarkdownDescription: "The value associated with the restriction type (e.g., the project ID). Changing this value forces a new resource to be created.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

// contextRestrictionCreateErrorDetail renders a CreateContextRestriction error
// for a Terraform diagnostic, explaining a "group" restriction's 400s instead
// of forwarding the API's bare, easy-to-miss message.
//
// [NET, measured on 2026-08-21] every 400 the API returns for restriction_type
// = "group" traces to one of exactly two constraints — see
// circleci.ContextRestrictionTypeGroup — and neither is guessable from the
// message alone: "This is only supported for OAuth orgs." says nothing about
// which value would have worked, and "Invalid restriction." says nothing about
// organization type at all. A 400 on any other restriction type is left
// exactly as circleci.Detail renders it.
func contextRestrictionCreateErrorDetail(restrictionType string, err error) string {
	detail := circleci.Detail(err)

	if restrictionType != circleci.ContextRestrictionTypeGroup || !circleci.HasStatus(err, http.StatusBadRequest) {
		return detail
	}

	return detail + "\n\nThrough this create route, restriction_type = \"group\" only ever succeeded " +
		"against an OAuth-backed organization (a classic gh/<org> or bitbucket/<org> slug), and the " +
		"only value accepted was that organization's own UUID — not a VCS team id or a circleci_group " +
		"RBAC group id, despite the name. That UUID is the value CircleCI already assigns to the " +
		"default \"All members\" restriction every context carries; on a standalone (circleci/<uuid>) " +
		"organization this type fails outright, whatever the value."
}

// Create creates the resource and sets the initial Terraform state.
func (r *contextRestrictionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan contextRestrictionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreateContextRestriction(ctx, plan.ContextId.ValueString(), circleci.CreateContextRestrictionRequest{
		RestrictionType:  plan.Type.ValueString(),
		RestrictionValue: plan.Value.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI context restriction",
			contextRestrictionCreateErrorDetail(plan.Type.ValueString(), err),
		)

		return
	}

	// The create response never carries a "name" (see CreateContextRestriction's
	// doc comment), so this intentionally reads back as "" until the next
	// refresh — matching the API rather than guessing at a value it never sent.
	plan.Id = types.StringValue(created.ID)
	plan.ProjectId = types.StringValue(created.ProjectID)
	plan.Name = types.StringValue(created.Name)

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *contextRestrictionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state contextRestrictionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	restrictions, err := r.client.ListContextRestrictions(ctx, state.ContextId.ValueString())
	if err != nil {
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		// See context_resource.go's Read for why 403 is not folded into
		// IsNotFound: this route resolves the context id the same way, so a
		// missing context answers 403 rather than 404.
		if circleci.IsUnauthorized(err) {
			resp.Diagnostics.AddError(
				"Unable to read CircleCI context restriction from context "+state.ContextId.ValueString(),
				fmt.Sprintf(
					"The API denied access to context %s. It has either been deleted outside "+
						"Terraform, or the configured token lacks permission — the API returns the same "+
						"response for both and does not distinguish them.\n\n%s",
					state.ContextId.ValueString(), circleci.Detail(err),
				),
			)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read CircleCI context restriction from context "+state.ContextId.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	var found *circleci.ContextRestriction
	for i := range restrictions {
		if restrictions[i].ID == state.Id.ValueString() {
			found = &restrictions[i]

			break
		}
	}

	// A restriction removed outside Terraform is not an error: drop it from
	// state so the next plan recreates it.
	if found == nil {
		resp.State.RemoveResource(ctx)

		return
	}

	state.ContextId = types.StringValue(found.ContextID)
	state.ProjectId = types.StringValue(found.ProjectID)
	state.Name = types.StringValue(found.Name)
	state.Type = types.StringValue(found.RestrictionType)
	state.Value = types.StringValue(found.RestrictionValue)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is unreachable for a real configuration change: every attribute but
// the computed ones is RequiresReplace, because there is no route that updates
// a restriction in place (see the doc comment above the const block in
// internal/circleci/context_restriction.go: [NET, measured on 2026-08-21]
// PATCH and PUT to the restriction route both 404 at the router, not merely
// reject the method). This method only persists the plan so a refresh-driven
// re-apply of an unchanged plan is a no-op.
func (r *contextRestrictionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan contextRestrictionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the resource and removes the Terraform state on success.
//
// [NET, measured on 2026-08-21]: deleting a context's last remaining `group`
// restriction succeeds even when the organization setting that requires every
// context to carry one (IsContextGroupRestrictionRequired — see
// circleci_organization_settings) is enabled; the API does not enforce that
// setting on this route. So there is deliberately no special handling of that
// setting here — an error the API never returns would be dead code — and this
// resource can leave a context in the state that setting is meant to prevent.
func (r *contextRestrictionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state contextRestrictionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteContextRestriction(ctx, state.ContextId.ValueString(), state.Id.ValueString())
	// Already gone is the desired end state. 403 counts, for the same reason as
	// circleci_context's Delete: this route resolves the context id the same
	// way, so it answers 403 for a context that no longer exists, and the
	// restriction cannot outlive its context.
	if err != nil && !circleci.IsNotFound(err) && !circleci.IsUnauthorized(err) {
		resp.Diagnostics.AddError(
			"Error deleting CircleCI context restriction",
			circleci.Detail(err),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *contextRestrictionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

func (r *contextRestrictionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Expected format: "CONTEXT_ID/RESTRICTION_ID"
	parts := strings.SplitN(req.ID, "/", 2)

	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid Import ID Format",
			fmt.Sprintf("Expected import ID format: 'context_id/restriction_id'. Got: %s", req.ID),
		)
		return
	}

	contextID := parts[0]
	restrictionID := parts[1]

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), restrictionID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("context_id"), contextID)...)
}
