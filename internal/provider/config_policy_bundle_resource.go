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
			"!> **Each map key MUST equal the `policy_name` its Rego declares.** CircleCI does not " +
			"store the map key at all: it parses each policy's Rego, requires the first rule to be " +
			"`policy_name[\"some_name\"]`, and keys the bundle by that declared name on every read — " +
			"discarding whatever key this configuration used. A key that does not match its own " +
			"policy's declared name (a filename like `allow-docker.rego`, say, for Rego whose rule is " +
			"`policy_name[\"allow_docker\"]`) applies successfully once and then shows that policy " +
			"being removed and re-added on every plan thereafter, forever — Terraform cannot resolve " +
			"the mismatch because the map is a required, non-computed attribute. This provider warns " +
			"when it detects the mismatch right after an apply, but the fix is on the configuration " +
			"side: name each map key after its policy's `policy_name`.\n\n" +
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
				MarkdownDescription: "The complete bundle, mapping each policy's key to its Rego source. " +
					"**The key must equal the `policy_name` the Rego declares as its first rule** " +
					"(`policy_name[\"allow_docker\"]`, for example) — CircleCI keys the stored bundle by " +
					"that declared name, not by this map's key, so the two must already match or every " +
					"plan after the first apply shows the policy being removed and re-added. Reading " +
					"policies from disk with `file()` keeps them reviewable:\n\n" +
					"```terraform\npolicies = {\n  \"allow_docker\" = file(\"${path.module}/policies/allow-docker.rego\")\n}\n```\n\n" +
					"Setting this to `{}` removes every policy from the context. The whole bundle must " +
					"stay under roughly 2.5 MiB, which the API enforces. Rego that does not parse — " +
					"including Rego whose first rule is not the required `policy_name` declaration — is " +
					"rejected with an error naming the offending file.",
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

	r.warnOnKeyMismatch(ctx, plan, policies, diags)

	return true
}

// warnOnKeyMismatch re-reads the bundle just uploaded and warns about every
// policy whose map key in the configuration does not match the key CircleCI
// actually stored it under.
//
// [NET, measured against the live API on 2026-08-21] The upload route does
// NOT key a bundle entry by the map key the caller submits. It parses each
// policy's Rego, requires the first rule to be a "policy_name" declaration
// (`policy_name["some_name"]`), and keys the bundle — on every subsequent GET,
// and in this very upload's created/modified/deleted arrays — by that
// declared name instead. The submitted map key is not stored anywhere and is
// not even required to look like a filename.
//
// Concretely: uploading {"probe.rego": "package org\n\npolicy_name[\"probe_policy\"]\n"}
// is followed by a bundle GET returning {"probe_policy": {...}} — "probe.rego"
// appears nowhere in the response. Re-uploading the identical configuration
// produces a diff of created:["probe_policy"], deleted:["probe_policy"] as
// far as Terraform can tell, forever: Read() populates state from the server's
// keys, the configuration keeps the filename-style keys, and no apply can ever
// make the two agree, because the act of applying is what the server
// re-derives its key from.
//
// This resource cannot correct the mismatch itself: `policies` is a Required,
// non-Computed map, so the framework rejects any attempt to hand back state
// with different keys than the plan ("Provider produced inconsistent result
// after apply"). The only real fix is a configuration whose map key already
// equals the Rego's declared policy_name — verified stable in the same
// session: a key of "stable_name" holding `policy_name["stable_name"]`
// round-trips with no server-side rename at all. So this warns loudly instead
// of silently leaving a permanent diff for the next plan to discover.
func (r *configPolicyBundleResource) warnOnKeyMismatch(
	ctx context.Context, plan configPolicyBundleResourceModel, submitted map[string]string, diags *diag.Diagnostics,
) {
	stored, err := r.client.GetPolicyBundle(ctx, plan.OwnerID.ValueString(), plan.PolicyContext.ValueString())
	if err != nil {
		// The upload itself already succeeded; failing to verify it is not fatal,
		// but nothing else can check the key mapping either.
		diags.AddWarning(
			"Could not verify CircleCI config policy bundle keys",
			fmt.Sprintf(
				"The policy bundle upload succeeded, but re-reading it to confirm every policy's key "+
					"matches this configuration failed: %s. If a policy's map key does not equal the "+
					"policy_name its Rego declares, every future plan for this resource will show that "+
					"policy being removed and re-added.",
				circleci.Detail(err),
			),
		)

		return
	}

	// Match by content rather than by count: two configured keys can collapse
	// onto the same declared policy_name, in which case the stored bundle has
	// fewer entries than were submitted and one policy silently overwrote
	// another.
	byContent := make(map[string][]string, len(stored))
	for storedKey, policy := range stored {
		byContent[policy.Content] = append(byContent[policy.Content], storedKey)
	}

	for configuredKey, content := range submitted {
		storedKeys := byContent[content]
		switch len(storedKeys) {
		case 0:
			// Should not happen right after a successful upload; a concurrent
			// write is the only plausible cause, and the next Read will surface it.
			continue
		case 1:
			if storedKeys[0] != configuredKey {
				diags.AddWarning(
					"CircleCI renamed a policy's key",
					fmt.Sprintf(
						"The policy configured under the map key %q was stored by CircleCI under the key "+
							"%q instead — the API keys each policy by the policy_name its Rego declares "+
							"(`policy_name[%q]`), not by the map key in this configuration. Every future "+
							"plan for this resource will show %q being removed and %q being added, forever, "+
							"unless the map key is renamed to %q to match.",
						configuredKey, storedKeys[0], storedKeys[0], configuredKey, storedKeys[0], storedKeys[0],
					),
				)
			}
		default:
			diags.AddWarning(
				"Multiple policies collapsed onto one CircleCI-assigned key",
				fmt.Sprintf(
					"The policy configured under the map key %q declares the same policy_name as %d other "+
						"policy/policies in this bundle; CircleCI stores only one of them. Give each policy "+
						"a distinct policy_name.",
					configuredKey, len(storedKeys)-1,
				),
			)
		}
	}
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
