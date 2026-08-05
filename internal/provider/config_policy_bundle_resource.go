// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &configPolicyBundleResource{}
	_ resource.ResourceWithConfigure   = &configPolicyBundleResource{}
	_ resource.ResourceWithImportState = &configPolicyBundleResource{}
)

// configPolicyBundleTypeName is the Terraform type name.
const configPolicyBundleTypeName = "circleci_config_policy_bundle"

// configPolicyBundleResourceModel maps the resource schema.
type configPolicyBundleResourceModel struct {
	OwnerID       types.String `tfsdk:"owner_id"`
	PolicyContext types.String `tfsdk:"policy_context"`
	Policies      types.Map    `tfsdk:"policies"`
}

// NewConfigPolicyBundleResource is a helper function to simplify the provider implementation.
func NewConfigPolicyBundleResource() resource.Resource {
	return &configPolicyBundleResource{}
}

// configPolicyBundleResource is the resource implementation.
type configPolicyBundleResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *configPolicyBundleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_config_policy_bundle"
}

// Schema defines the schema for the resource.
//
// The resource is a whole bundle rather than a single policy, and that is forced
// by the API: the only write route replaces every policy in a policy context at
// once. Two per-policy resources pointed at the same context would each delete
// the other's policy on every apply, so the bundle is the smallest unit that can
// be managed coherently.
func (r *configPolicyBundleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an organization's whole bundle of CircleCI config policies: the Rego " +
			"documents evaluated against a pipeline's configuration before it runs. " +
			"Available on CircleCI Cloud and CircleCI Server 4.2 or later.\n\n" +
			"!> **This resource owns every policy in the policy context.** CircleCI has no route that " +
			"adds or removes a single policy: uploading a bundle replaces all of it. The `policies` map " +
			"is therefore the complete desired state, a policy missing from it is deleted, and you must " +
			"not declare two `" + configPolicyBundleTypeName + "` resources for the same " +
			"`owner_id` and `policy_context` — they would clobber each other on every apply.\n\n" +
			"~> **Requires the Scale plan on CircleCI Cloud**, or CircleCI Server 4.2 or later. On other " +
			"plans the API rejects these requests.\n\n" +
			"~> Uploading policies does not by itself enforce them. Use " +
			"`circleci_config_policy_settings` to enable policy evaluation for the policy context.",
		Attributes: map[string]schema.Attribute{
			"owner_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization that owns the policy " +
					"bundle. Changing this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"policy_context": schema.StringAttribute{
				MarkdownDescription: "Which policy context the bundle belongs to. `" +
					circleci.PolicyContextConfig + "` is the only accepted value, and the default: it " +
					"is the context evaluated against pipeline configuration. Changing this value " +
					"forces a new resource to be created.\n\n" +
					"~> A policy context is **not** a CircleCI context. It has nothing to do with " +
					"`circleci_context` or the environment variables that live there: it is a namespace " +
					"for a bundle of policies. CircleCI's documentation mentions a `" +
					circleci.PolicyContextCustom + "` context for policies evaluated against " +
					"caller-supplied data, but no route accepts it — every handler that takes this path " +
					"segment validates it against the single value `" + circleci.PolicyContextConfig +
					"` and answers 400 for anything else — so it is not offered here.",
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(circleci.PolicyContextConfig),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf(
						circleci.PolicyContextConfig,
					),
				},
			},
			"policies": schema.MapAttribute{
				MarkdownDescription: "The complete bundle, mapping each policy's name to its Rego source. " +
					"Names are conventionally filenames ending in `.rego`. Reading policies from disk with " +
					"`file()` keeps them reviewable:\n\n" +
					"```terraform\npolicies = {\n  \"allow-docker.rego\" = file(\"${path.module}/policies/allow-docker.rego\")\n}\n```\n\n" +
					"Setting this to `{}` removes every policy from the context. The whole bundle must " +
					"stay under roughly 2.5 MiB, which the API enforces.",
				ElementType: types.StringType,
				Required:    true,
				Validators: []validator.Map{
					mapvalidator.KeysAre(stringvalidator.LengthAtLeast(1)),
					mapvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
				},
			},
		},
	}
}

// Create uploads the bundle. The upload route creates and replaces alike, so
// this is the same call Update makes.
func (r *configPolicyBundleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan configPolicyBundleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !r.upload(ctx, plan, "creating", &resp.Diagnostics) {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *configPolicyBundleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state configPolicyBundleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	bundle, err := r.client.GetPolicyBundle(ctx,
		state.OwnerID.ValueString(), state.PolicyContext.ValueString())
	if err != nil {
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Error reading CircleCI config policy bundle",
			fmt.Sprintf(
				"Could not read the %q policy bundle for organization %s: %s",
				state.PolicyContext.ValueString(), state.OwnerID.ValueString(), circleci.Detail(err),
			),
		)

		return
	}

	// A policy context is not an entity that can be missing: an empty one answers
	// 200 with {}, never 404. An empty bundle where Terraform recorded policies
	// therefore means the bundle was emptied outside Terraform, which for this
	// resource is the same thing as being gone.
	//
	// A configuration that deliberately declares policies = {} keeps its empty
	// state instead, since that is exactly what it asked for.
	if len(bundle) == 0 && !state.Policies.IsNull() && len(state.Policies.Elements()) > 0 {
		resp.State.RemoveResource(ctx)

		return
	}

	policies, diags := types.MapValueFrom(ctx, types.StringType, bundle.Contents())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Policies = policies

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update replaces the bundle with the planned one.
func (r *configPolicyBundleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan configPolicyBundleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !r.upload(ctx, plan, "updating", &resp.Diagnostics) {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete empties the policy context.
//
// There is no delete route, for the bundle or for a single policy, so an upload
// of an empty bundle is how a bundle is removed. Because this resource owns the
// whole context, emptying it removes exactly what the resource created.
func (r *configPolicyBundleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state configPolicyBundleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.SetPolicyBundle(ctx,
		state.OwnerID.ValueString(), state.PolicyContext.ValueString(), nil, false)
	if err != nil {
		if circleci.IsNotFound(err) {
			return
		}

		resp.Diagnostics.AddError(
			"Error deleting CircleCI config policy bundle",
			fmt.Sprintf(
				"Could not empty the %q policy bundle for organization %s: %s",
				state.PolicyContext.ValueString(), state.OwnerID.ValueString(), circleci.Detail(err),
			),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *configPolicyBundleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing policy bundle into Terraform state.
// Expected import ID format: "owner_id" (which assumes the default policy
// context) or "owner_id/policy_context".
func (r *configPolicyBundleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ownerID, policyContext, ok := parsePolicyImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError(
			"Invalid Import ID Format",
			policyImportIDError(req.ID, configPolicyBundleTypeName),
		)

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owner_id"), ownerID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("policy_context"), policyContext)...)
}

// upload replaces the remote bundle with the one in plan. It reports whether the
// call succeeded, having recorded a diagnostic if it did not.
func (r *configPolicyBundleResource) upload(
	ctx context.Context, plan configPolicyBundleResourceModel, verb string, diags *diag.Diagnostics,
) bool {
	policies := map[string]string{}
	diags.Append(plan.Policies.ElementsAs(ctx, &policies, false)...)
	if diags.HasError() {
		return false
	}

	_, err := r.client.SetPolicyBundle(ctx,
		plan.OwnerID.ValueString(), plan.PolicyContext.ValueString(), policies, false)
	if err != nil {
		diags.AddError(
			fmt.Sprintf("Error %s CircleCI config policy bundle", verb),
			fmt.Sprintf(
				"Could not upload the %q policy bundle for organization %s: %s\n\n"+
					"Config policies require the Scale plan on CircleCI Cloud, or CircleCI Server 4.2 "+
					"or later.",
				plan.PolicyContext.ValueString(), plan.OwnerID.ValueString(), circleci.Detail(err),
			),
		)

		return false
	}

	return true
}

// parsePolicyImportID splits an import ID shared by the policy resources into an
// owner ID and a policy context, defaulting the context when it is omitted.
func parsePolicyImportID(id string) (ownerID, policyContext string, ok bool) {
	parts := strings.Split(id, "/")

	switch len(parts) {
	case 1:
		if parts[0] == "" {
			return "", "", false
		}

		return parts[0], circleci.PolicyContextConfig, true
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", false
		}

		return parts[0], parts[1], true
	default:
		return "", "", false
	}
}

// policyImportIDError renders the diagnostic detail for a malformed policy
// import ID.
func policyImportIDError(id, typeName string) string {
	return fmt.Sprintf(
		"Expected an import ID of \"owner_id\" (which assumes the %q policy context) or "+
			"\"owner_id/policy_context\", got: %q.\n\n"+
			"For example:\n  terraform import %s.example \"00000000-0000-0000-0000-000000000000\"",
		circleci.PolicyContextConfig, id, typeName,
	)
}
