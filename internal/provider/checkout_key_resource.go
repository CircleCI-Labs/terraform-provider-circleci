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

// checkoutKeyProjectSlugPattern matches the documented project slug shape,
// vcs-type/org-name/repo-name. Rejecting anything else in the schema turns what
// would be a confusing HTTP 404 from the API into a plan-time error.
var checkoutKeyProjectSlugPattern = regexp.MustCompile(`^[^/]+/[^/]+/[^/]+$`)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &checkoutKeyResource{}
	_ resource.ResourceWithConfigure   = &checkoutKeyResource{}
	_ resource.ResourceWithImportState = &checkoutKeyResource{}
)

// checkoutKeyResourceModel maps the resource schema.
//
// There is no id attribute: the fingerprint is the identifier the API assigns and
// the only handle a checkout key has, so a synthetic id would just duplicate it.
type checkoutKeyResourceModel struct {
	ProjectSlug types.String `tfsdk:"project_slug"`
	Type        types.String `tfsdk:"type"`
	Fingerprint types.String `tfsdk:"fingerprint"`
	PublicKey   types.String `tfsdk:"public_key"`
	Preferred   types.Bool   `tfsdk:"preferred"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// NewCheckoutKeyResource is a helper function to simplify the provider implementation.
func NewCheckoutKeyResource() resource.Resource {
	return &checkoutKeyResource{}
}

// checkoutKeyResource is the resource implementation.
type checkoutKeyResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *checkoutKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_checkout_key"
}

// Schema defines the schema for the resource.
func (r *checkoutKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI project checkout key: the SSH key CircleCI uses to check out " +
			"your project's source. Available on CircleCI Cloud and CircleCI Server.\n\n" +
			"Checkout keys have no update endpoint, so every configurable attribute forces a new resource. " +
			"Creating a key never returns its private half, and a replacement gets a new fingerprint.\n\n" +
			"~> **Not available for GitLab or GitHub App projects.** The CircleCI API only manages checkout " +
			"keys for projects integrated through GitHub OAuth or Bitbucket. Requests for a GitLab or " +
			"GitHub App project (that is, a project whose slug starts with `circleci/`) are rejected.\n\n" +
			"~> **Creating a `user-key` requires a user API token**, not a project token, and the user must " +
			"have authorized their VCS account with CircleCI first (Project Settings > SSH Keys).",
		Attributes: map[string]schema.Attribute{
			"project_slug": schema.StringAttribute{
				MarkdownDescription: "The project slug in the format `vcs-type/org-name/repo-name`, " +
					"for example `github/my-org/my-repo`. Changing this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						checkoutKeyProjectSlugPattern,
						"must be in the format 'vcs-type/org-name/repo-name'",
					),
				},
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "The type of checkout key to create: `deploy-key` (scoped to this " +
					"repository) or `user-key` (carries the permissions of the user who created it, and so " +
					"can check out other repositories such as private submodules). " +
					"Changing this value forces a new resource to be created.\n\n" +
					"The API reports a user key as `github-user-key`; the provider normalizes that back to " +
					"`user-key` so the value always matches your configuration.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf(
						circleci.CheckoutKeyTypeUserKey,
						circleci.CheckoutKeyTypeDeployKey,
					),
				},
			},
			"fingerprint": schema.StringAttribute{
				MarkdownDescription: "The MD5 fingerprint of the key, assigned by CircleCI. This is the " +
					"identifier used to read and delete the key, and the value to reference from a job's " +
					"`add_ssh_keys` step.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"public_key": schema.StringAttribute{
				MarkdownDescription: "The public half of the SSH key. The private half is never returned by " +
					"the API.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"preferred": schema.BoolAttribute{
				MarkdownDescription: "Whether CircleCI prefers this key when checking the project out.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the checkout key was created.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *checkoutKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan checkoutKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key, err := r.client.CreateCheckoutKey(ctx, plan.ProjectSlug.ValueString(), plan.Type.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI checkout key",
			fmt.Sprintf(
				"Could not create a %s for project %s: %s\n\n"+
					"Checkout keys are not available for GitLab or GitHub App projects, and creating a "+
					"user-key requires a user API token rather than a project token.",
				plan.Type.ValueString(), plan.ProjectSlug.ValueString(), circleci.Detail(err),
			),
		)

		return
	}

	// Keep the configured type rather than the reported one: the API answers
	// "github-user-key" for a key created as "user-key", and returning that would
	// make the applied state differ from the plan.
	setCheckoutKeyState(&plan, key, plan.Type.ValueString())

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *checkoutKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state checkoutKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key, err := r.client.GetCheckoutKey(ctx, state.ProjectSlug.ValueString(), state.Fingerprint.ValueString())
	if err != nil {
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Error reading CircleCI checkout key",
			fmt.Sprintf(
				"Could not read checkout key %s for project %s: %s",
				state.Fingerprint.ValueString(), state.ProjectSlug.ValueString(), circleci.Detail(err),
			),
		)

		return
	}

	setCheckoutKeyState(&state, key, key.InputType())

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is a no-op. The CircleCI API has no endpoint for updating a checkout
// key, so every configurable attribute is marked RequiresReplace and Terraform
// destroys and recreates the key instead of ever calling this. It exists only to
// satisfy the resource.Resource interface.
func (r *checkoutKeyResource) Update(_ context.Context, _ resource.UpdateRequest, _ *resource.UpdateResponse) {
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *checkoutKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state checkoutKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteCheckoutKey(ctx, state.ProjectSlug.ValueString(), state.Fingerprint.ValueString())
	if err != nil {
		// A key someone already removed outside Terraform is not a failure: the
		// desired end state is reached either way.
		if circleci.IsNotFound(err) {
			return
		}

		resp.Diagnostics.AddError(
			"Error deleting CircleCI checkout key",
			fmt.Sprintf(
				"Could not delete checkout key %s for project %s: %s",
				state.Fingerprint.ValueString(), state.ProjectSlug.ValueString(), circleci.Detail(err),
			),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *checkoutKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing checkout key into Terraform state.
// Expected import ID format: "project_slug/fingerprint",
// e.g. "github/my-org/my-repo/c9:0b:1c:4f:d5:65:56:b9:ad:88:f9:81:2b:37:74:2f".
func (r *checkoutKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// The project slug contains slashes (e.g. "github/my-org/my-repo"), so split
	// from the right to extract the fingerprint. An MD5 fingerprint, which is what
	// the API returns by default, contains no slashes.
	lastSlash := strings.LastIndex(req.ID, "/")
	if lastSlash == -1 || lastSlash == 0 || lastSlash == len(req.ID)-1 {
		resp.Diagnostics.AddError(
			"Invalid Import ID Format",
			fmt.Sprintf(
				"Expected import ID format: 'project_slug/fingerprint' "+
					"(e.g. 'github/my-org/my-repo/c9:0b:1c:4f:d5:65:56:b9:ad:88:f9:81:2b:37:74:2f'). Got: %s",
				req.ID,
			),
		)

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_slug"), req.ID[:lastSlash])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("fingerprint"), req.ID[lastSlash+1:])...)
}

// setCheckoutKeyState copies an API response into the resource model. keyType is
// passed separately because Create must preserve the configured type while Read
// normalizes the type the API reports.
func setCheckoutKeyState(model *checkoutKeyResourceModel, key *circleci.CheckoutKey, keyType string) {
	model.Type = types.StringValue(keyType)
	model.Fingerprint = types.StringValue(key.Fingerprint)
	model.PublicKey = types.StringValue(key.PublicKey)
	model.Preferred = types.BoolValue(key.Preferred)
	model.CreatedAt = types.StringValue(key.CreatedAt)
}
