// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
// These are v2 routes owned by CircleCI's own v2 organization API rather than by
// a service behind the public API proxy, and a CircleCI Server installation
// forwards unmatched API paths there, so they work on Server as well as Cloud and
// must not be gated on requireCloud. See urlOrbAllowListRoute for the evidence,
// and contrast circleci_otel_exporter, which is also v2 and is gated because its
// backend is absent from Server.
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
			"Available on CircleCI Cloud **and on CircleCI Server**. Being a v2 route is not " +
			"evidence of that on its own — `circleci_pipeline_definition` is v2 and unavailable on " +
			"Server — but the owner is: these are v2 organization routes served by CircleCI itself " +
			"rather than by a separate service, and a Server installation's gateway sends every API " +
			"path it does not route elsewhere to exactly that component. Nothing gates this " +
			"resource.\n\n" +
			"The API has no update route for an entry, so changing any attribute replaces the entry.\n\n" +
			"~> **An organization's allow list is capped at five entries.** Creating a sixth fails " +
			"with an error naming the limit. Duplicate prefixes are permitted on purpose, so the " +
			"same prefix may be listed more than once under different `auth` values.",
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
					"prefix as narrow as possible. Changing this value forces a new resource to be created.\n\n" +
					"The API requires the `https` scheme and a path that ends in `/`, so " +
					"`https://example.com/orbs/` is accepted and `https://example.com/orbs` is not. " +
					"That is checked at plan time here rather than left to fail at apply time.",
				Required: true,
				Validators: []validator.String{
					urlOrbAllowListPrefix(),
				},
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
		// Only an entry absent from a successful listing — the ErrNotFound sentinel —
		// means the entry is gone.
		if errors.Is(err, circleci.ErrNotFound) {
			resp.State.RemoveResource(ctx)

			return
		}

		// A transport 404 is a statement about the organization, not the entry: the
		// list handler throws not-found both when the organization cannot be resolved
		// and when the token may not view it. IsNotFound would say yes to it, and
		// dropping state on it would recreate a live entry on the next apply — a
		// duplicate, against a limit of five per organization.
		if circleci.HasStatus(err, http.StatusNotFound) {
			resp.Diagnostics.AddError(
				"Unable to read CircleCI URL orb allow list entry "+state.Id.ValueString(),
				fmt.Sprintf(
					"CircleCI answered 404 for organization %s. An entry is read by listing the "+
						"organization's allow list, so this is about the organization rather than the "+
						"entry: either %s does not exist, or the API token may not view it.\n\n"+
						"The entry has been left in Terraform state, because removing it would add a "+
						"duplicate entry on the next apply while the original is still there.",
					org, org,
				),
			)

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

var _ validator.String = urlOrbAllowListPrefixValidator{}

// urlOrbAllowListPrefixValidator enforces the two rules the API applies to an
// allow list prefix, so that a typo fails at plan time instead of after a
// half-applied change.
//
// The API parses the prefix as a URL and requires the https scheme and a path
// that ends in "/". The trailing slash is what stops a prefix from matching more
// than it names: "https://example.com/org" would also permit
// "https://example.com/org-evil/...", so the API refuses it. Both checks mirror
// the server rule exactly rather than guessing at a stricter shape, so a prefix
// carrying a query string — whose path can still end in "/" — is still accepted.
type urlOrbAllowListPrefixValidator struct{}

// urlOrbAllowListPrefix returns the prefix validator.
func urlOrbAllowListPrefix() validator.String { return urlOrbAllowListPrefixValidator{} }

// Description describes the validation in plain text formatting.
func (urlOrbAllowListPrefixValidator) Description(_ context.Context) string {
	return `must be an https URL whose path ends in "/"`
}

// MarkdownDescription describes the validation in Markdown formatting.
func (v urlOrbAllowListPrefixValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateString performs the validation.
func (urlOrbAllowListPrefixValidator) ValidateString(
	_ context.Context,
	req validator.StringRequest,
	resp *validator.StringResponse,
) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	value := req.ConfigValue.ValueString()

	parsed, err := url.Parse(value)
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid URL orb allow list prefix",
			fmt.Sprintf("Expected an https URL, but %q could not be parsed as one: %s.", value, err),
		)

		return
	}

	if parsed.Scheme != "https" {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid URL orb allow list prefix",
			fmt.Sprintf(
				"The CircleCI API accepts only https prefixes, and %q uses %q. Orb source is "+
					"fetched over the network, so an unencrypted prefix is rejected.",
				value, parsed.Scheme,
			),
		)

		return
	}

	if !strings.HasSuffix(parsed.Path, "/") {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid URL orb allow list prefix",
			fmt.Sprintf(
				"The CircleCI API requires the prefix path to end in \"/\", and %q does not. "+
					"Without it the prefix would also match sibling paths that merely start with "+
					"the same characters. Add a trailing slash, for example %q.",
				value, value+"/",
			),
		)
	}
}
