// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	_ resource.Resource                     = &oidcCustomClaimsResource{}
	_ resource.ResourceWithConfigure        = &oidcCustomClaimsResource{}
	_ resource.ResourceWithImportState      = &oidcCustomClaimsResource{}
	_ resource.ResourceWithConfigValidators = &oidcCustomClaimsResource{}
)

// oidcCustomClaimsTypeName is the Terraform type name.
const oidcCustomClaimsTypeName = "circleci_oidc_custom_claims"

// oidcCustomClaimsResourceModel maps the resource schema.
//
// Audience is a types.List rather than a []string so that "no audience
// configured" (null) stays distinguishable from "an audience configured as
// empty" ([]). Those mean different things to the API: the first omits the claim
// from the PATCH body, the second overwrites it with an empty list.
type oidcCustomClaimsResourceModel struct {
	OrganizationID    types.String  `tfsdk:"organization_id"`
	OrgID             types.String  `tfsdk:"org_id"`
	ProjectID         types.String  `tfsdk:"project_id"`
	Audience          types.List    `tfsdk:"audience"`
	TTL               durationValue `tfsdk:"ttl"`
	AudienceUpdatedAt types.String  `tfsdk:"audience_updated_at"`
	TTLUpdatedAt      types.String  `tfsdk:"ttl_updated_at"`
}

// scope returns a human-readable description of which claims this instance
// manages, for diagnostics.
func (m oidcCustomClaimsResourceModel) scope() string {
	orgID := effectiveOrgID(m.OrganizationID, m.OrgID)

	if m.ProjectID.IsNull() || m.ProjectID.ValueString() == "" {
		return fmt.Sprintf("organization %s", orgID)
	}

	return fmt.Sprintf("project %s in organization %s", m.ProjectID.ValueString(), orgID)
}

// NewOIDCCustomClaimsResource is a helper function to simplify the provider implementation.
func NewOIDCCustomClaimsResource() resource.Resource {
	return &oidcCustomClaimsResource{}
}

// oidcCustomClaimsResource is the resource implementation.
type oidcCustomClaimsResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *oidcCustomClaimsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_oidc_custom_claims"
}

// ConfigValidators rejects a configuration that customizes nothing. The API has
// no way to express "create the customization but set no claim", so such a
// configuration would apply cleanly and then read back as absent forever.
func (r *oidcCustomClaimsResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.AtLeastOneOf(
			path.MatchRoot("audience"),
			path.MatchRoot("ttl"),
		),
		orgIDConfigValidator(),
	}
}

// Schema defines the schema for the resource.
func (r *oidcCustomClaimsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Customizes the claims of the OIDC identity tokens CircleCI issues to jobs, " +
			"either for a whole organization or for a single project within it. " +
			"Available on CircleCI Cloud and CircleCI Server.\n\n" +
			"Set `project_id` to customize one project's tokens; omit it to customize the organization's. " +
			"Project-level claims take precedence over organization-level claims, so the two can be " +
			"managed by separate instances of this resource.\n\n" +
			"~> **This is a settings object, not a created entity.** CircleCI always has a claim " +
			"customization record for every scope, so there is nothing to create and nothing to destroy. " +
			"Applying this resource writes the claims you set; destroying it resets exactly the claims it " +
			"manages back to CircleCI's defaults.\n\n" +
			"~> **The resource is authoritative for the claims it declares.** Removing `ttl` (or " +
			"`audience`) from your configuration does not stop managing it, it resets it: the next apply " +
			"deletes that claim. Use two resources on different scopes rather than two on the same one.",
		Attributes: map[string]schema.Attribute{
			// See org_id_deprecation.go for why these are Optional+Computed and why
			// replacement is conditional on being configured.
			"organization_id": deprecatedOrgIDAttribute("this OIDC claim customization", true),
			"org_id":          orgIDAttribute("this OIDC claim customization", true),
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the project whose identity tokens are " +
					"customized. Omit it to customize the organization's tokens instead. " +
					"Changing this value forces a new resource to be created.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"audience": schema.ListAttribute{
				MarkdownDescription: "The values placed in the token's `aud` claim, replacing CircleCI's " +
					"default audience. Set it to the audience your identity provider expects, for example " +
					"`[\"sts.amazonaws.com\"]` for AWS. An empty list is sent as an explicit empty " +
					"audience; omit the attribute entirely to leave the claim at CircleCI's default.",
				ElementType: types.StringType,
				Optional:    true,
			},
			"ttl": schema.StringAttribute{
				CustomType: durationType{},
				MarkdownDescription: "How long an identity token stays valid, as a duration string: one or " +
					"more unit-suffixed integers with no separator, for example `1h`, `90m` or `1h30m`. " +
					"The accepted units are `ms`, `s`, `m`, `h`, `d` and `w`. Note that fractional values " +
					"such as `1.5h` are rejected even though Go accepts them.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						circleci.OIDCTTLPattern,
						"must be a duration of unit-suffixed integers such as \"1h\", \"90m\" or \"1h30m\", "+
							"using only the units ms, s, m, h, d and w",
					),
				},
			},
			"audience_updated_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the audience claim was last written.",
				Computed:            true,
			},
			"ttl_updated_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the ttl claim was last written.",
				Computed:            true,
			},
		},
	}
}

// Create writes the configured claims. There is no create route: the API spells
// create and update the same way, as a PATCH that establishes the customization
// when none exists.
func (r *oidcCustomClaimsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan oidcCustomClaimsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	update, diags := oidcClaimsUpdate(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := effectiveOrgID(plan.OrganizationID, plan.OrgID)

	claims, err := r.client.UpdateOIDCCustomClaims(ctx, orgID, plan.ProjectID.ValueString(), update)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI OIDC custom claims",
			fmt.Sprintf("Could not write OIDC custom claims for %s: %s", plan.scope(), circleci.Detail(err)),
		)

		return
	}

	// Only the timestamps come from the response. The claims themselves stay as
	// planned: a scope may already carry a claim this configuration says nothing
	// about, and adopting it here would make the applied state differ from the
	// plan. The next refresh surfaces it as drift instead.
	// Store the duration exactly as the API spells it, so state is identical
	// whether the resource was created or imported. durationValue compares
	// semantically, so a configuration of "30m" does not diff against "30m0s".
	plan.TTL = newDurationValue(claims.TTL)
	setOIDCClaimTimestamps(&plan, claims)
	setOrgIDs(&plan.OrganizationID, &plan.OrgID, orgID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *oidcCustomClaimsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state oidcCustomClaimsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := effectiveOrgID(state.OrganizationID, state.OrgID)

	claims, err := r.client.GetOIDCCustomClaims(ctx, orgID, state.ProjectID.ValueString())
	if err != nil {
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Error reading CircleCI OIDC custom claims",
			fmt.Sprintf("Could not read OIDC custom claims for %s: %s", state.scope(), circleci.Detail(err)),
		)

		return
	}

	// A scope with no customization answers 200 carrying only its ids, never 404,
	// so IsNotFound cannot detect a reset. An empty customization is the "gone"
	// signal: something reset the claims outside Terraform, and the resource has
	// nothing left to manage.
	if claims.IsZero() {
		resp.State.RemoveResource(ctx)

		return
	}

	audience, diags := types.ListValueFrom(ctx, types.StringType, claims.Audience)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Audience = audience
	// The API reformats the duration it stores, so a configured "1h" reads back as
	// "1h0m0s". durationValue compares the two semantically, so storing the API's
	// spelling is safe and survives an import, which has no configuration to
	// normalize towards.
	state.TTL = newDurationValue(claims.TTL)
	setOIDCClaimTimestamps(&state, claims)
	setOrgIDs(&state.OrganizationID, &state.OrgID, orgID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update writes the claims the plan sets and resets the ones it drops.
//
// Resetting is what makes the resource authoritative. The PATCH route is a
// partial update, so a claim removed from the configuration would simply stop
// being sent and keep its old value forever. Deleting it instead means the
// configuration always describes the whole customization.
func (r *oidcCustomClaimsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state oidcCustomClaimsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := effectiveOrgID(plan.OrganizationID, plan.OrgID)
	projectID := plan.ProjectID.ValueString()

	// Reset first, so that a configuration which swaps one claim for the other
	// cannot leave the scope momentarily carrying neither.
	var dropped []string
	if !state.Audience.IsNull() && plan.Audience.IsNull() {
		dropped = append(dropped, circleci.OIDCClaimAudience)
	}
	if !state.TTL.IsNull() && plan.TTL.IsNull() {
		dropped = append(dropped, circleci.OIDCClaimTTL)
	}

	if len(dropped) > 0 {
		if _, err := r.client.DeleteOIDCCustomClaims(ctx, orgID, projectID, dropped); err != nil {
			resp.Diagnostics.AddError(
				"Error resetting CircleCI OIDC custom claims",
				fmt.Sprintf(
					"Could not reset the %s claim(s) for %s: %s",
					strings.Join(dropped, ", "), plan.scope(), circleci.Detail(err),
				),
			)

			return
		}
	}

	update, diags := oidcClaimsUpdate(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	claims, err := r.client.UpdateOIDCCustomClaims(ctx, orgID, projectID, update)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error updating CircleCI OIDC custom claims",
			fmt.Sprintf("Could not write OIDC custom claims for %s: %s", plan.scope(), circleci.Detail(err)),
		)

		return
	}

	// Store the duration exactly as the API spells it, so state is identical
	// whether the resource was created or imported. durationValue compares
	// semantically, so a configuration of "30m" does not diff against "30m0s".
	plan.TTL = newDurationValue(claims.TTL)
	setOIDCClaimTimestamps(&plan, claims)
	setOrgIDs(&plan.OrganizationID, &plan.OrgID, orgID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete resets the claims this resource manages back to CircleCI's defaults.
//
// Unlike most settings objects, resetting is a real operation here: the DELETE
// route removes the customization and jobs go back to receiving CircleCI's
// default audience and token lifetime. Only the claims in state are reset, so a
// claim this resource never managed is left alone.
func (r *oidcCustomClaimsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state oidcCustomClaimsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var claims []string
	if !state.Audience.IsNull() {
		claims = append(claims, circleci.OIDCClaimAudience)
	}
	if !state.TTL.IsNull() {
		claims = append(claims, circleci.OIDCClaimTTL)
	}

	if len(claims) == 0 {
		// The "claims" query parameter is required, so there is no request to
		// make that would reset nothing.
		return
	}

	_, err := r.client.DeleteOIDCCustomClaims(ctx,
		effectiveOrgID(state.OrganizationID, state.OrgID), state.ProjectID.ValueString(), claims)
	if err != nil {
		// Claims someone already reset outside Terraform are not a failure: the
		// desired end state is reached either way.
		if circleci.IsNotFound(err) {
			return
		}

		resp.Diagnostics.AddError(
			"Error deleting CircleCI OIDC custom claims",
			fmt.Sprintf(
				"Could not reset the %s claim(s) for %s: %s",
				strings.Join(claims, ", "), state.scope(), circleci.Detail(err),
			),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *oidcCustomClaimsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports existing OIDC custom claims into Terraform state.
// Expected import ID format: "organization_id" for organization-level claims, or
// "organization_id/project_id" for project-level claims.
//
// Both organization attribute names are set from the import ID, so a
// configuration written against either one imports cleanly. See
// org_id_deprecation.go.
func (r *oidcCustomClaimsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")

	switch len(parts) {
	case 1:
		if parts[0] == "" {
			break
		}

		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), parts[0])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("org_id"), parts[0])...)

		return
	case 2:
		if parts[0] == "" || parts[1] == "" {
			break
		}

		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), parts[0])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("org_id"), parts[0])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[1])...)

		return
	}

	resp.Diagnostics.AddError(
		"Invalid Import ID Format",
		fmt.Sprintf(
			"Expected an import ID of \"organization_id\" for organization-level claims, or "+
				"\"organization_id/project_id\" for project-level claims, got: %q.\n\n"+
				"For example:\n  terraform import %s.example "+
				"\"00000000-0000-0000-0000-000000000000\"",
			req.ID, oidcCustomClaimsTypeName,
		),
	)
}

// oidcClaimsUpdate builds the PATCH body from a plan. A null attribute becomes a
// nil field, which the API omits and therefore leaves untouched; Update handles
// resetting such a claim separately.
func oidcClaimsUpdate(ctx context.Context, plan oidcCustomClaimsResourceModel) (circleci.OIDCCustomClaimsUpdate, diag.Diagnostics) {
	var (
		update circleci.OIDCCustomClaimsUpdate
		diags  diag.Diagnostics
	)

	if !plan.Audience.IsNull() && !plan.Audience.IsUnknown() {
		audience := []string{}
		diags.Append(plan.Audience.ElementsAs(ctx, &audience, false)...)
		if diags.HasError() {
			return update, diags
		}

		update.Audience = &audience
	}

	if !plan.TTL.IsNull() && !plan.TTL.IsUnknown() {
		update.TTL = plan.TTL.ValueStringPointer()
	}

	return update, diags
}

// setOIDCClaimTimestamps copies the server-assigned timestamps into the model.
func setOIDCClaimTimestamps(model *oidcCustomClaimsResourceModel, claims *circleci.OIDCCustomClaims) {
	model.AudienceUpdatedAt = oidcOptionalString(claims.AudienceUpdatedAt)
	model.TTLUpdatedAt = oidcOptionalString(claims.TTLUpdatedAt)
}

// oidcOptionalString maps an absent API string to a null value rather than "",
// so that "no ttl configured" does not read back as an empty duration.
func oidcOptionalString(value string) types.String {
	if value == "" {
		return types.StringNull()
	}

	return types.StringValue(value)
}
