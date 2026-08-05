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
	_ resource.Resource                     = &checkoutKeyResource{}
	_ resource.ResourceWithConfigure        = &checkoutKeyResource{}
	_ resource.ResourceWithImportState      = &checkoutKeyResource{}
	_ resource.ResourceWithConfigValidators = &checkoutKeyResource{}
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
			"Three conditions make checkout keys unavailable for a project. Only the first is checked here, " +
			"at plan time, because it is the only one this provider can know without asking the API:\n\n" +
			"~> **Not available for GitHub App, GitLab or standalone projects.** The API answers " +
			"`400 This API is not supported for this project.` for any project whose slug starts with " +
			"`circleci/` — that prefix covers all three integration types. This configuration is rejected " +
			"before anything is sent, with a diagnostic naming the reason, rather than surfacing that message " +
			"at apply.\n\n" +
			"~> **Not available when the organization disables user keys.** This is an organization-level " +
			"setting this provider has no route to read, so it is left to the API: creating a checkout key " +
			"for such an organization fails at apply with a 400.\n\n" +
			"~> **Creating a `user-key` requires a user API token**, not a project token, and the user must " +
			"have authorized their VCS account with CircleCI first (Project Settings > SSH Keys). A project " +
			"token fails at apply with a 403.",
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

// ConfigValidators returns the cross-attribute checks that run at validate and
// plan time, before anything is written.
func (r *checkoutKeyResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		standaloneSlugRejectsCheckoutKeyValidator{},
	}
}

// standaloneSlugRejectsCheckoutKeyValidator rejects a circleci/-prefixed
// project_slug at plan time.
//
// That prefix is shared by every GitHub App, GitLab and standalone project, and
// the checkout keys API answers all three with the same
// 400 "This API is not supported for this project." The slug shape is known from
// configuration alone, so there is no need to reach the API to catch this one —
// unlike the other two ways a checkout key create can fail (the organization
// disabling user keys, and a user-key created with a project rather than a user
// token), which cannot be determined without asking it.
type standaloneSlugRejectsCheckoutKeyValidator struct{}

func (standaloneSlugRejectsCheckoutKeyValidator) Description(_ context.Context) string {
	return "project_slug must not start with circleci/: checkout keys are only available for GitHub OAuth and Bitbucket Cloud projects"
}

func (v standaloneSlugRejectsCheckoutKeyValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (standaloneSlugRejectsCheckoutKeyValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var slug types.String

	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("project_slug"), &slug)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if slug.IsNull() || slug.IsUnknown() {
		return
	}

	if !strings.HasPrefix(slug.ValueString(), "circleci/") {
		return
	}

	resp.Diagnostics.AddAttributeError(
		path.Root("project_slug"),
		"Checkout keys are not available for this project",
		fmt.Sprintf(
			"project_slug %q starts with \"circleci/\", which is the prefix every GitHub App, GitLab and "+
				"standalone project slug shares. CircleCI's checkout keys API rejects all three with "+
				"400 \"This API is not supported for this project.\" — it only manages checkout keys for "+
				"projects integrated through GitHub OAuth or Bitbucket Cloud.\n\n"+
				"This is a property of the project's VCS integration, not something Terraform or this "+
				"provider can change.",
			slug.ValueString(),
		),
	)
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
