// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	_ resource.Resource                     = &projectResource{}
	_ resource.ResourceWithConfigure        = &projectResource{}
	_ resource.ResourceWithImportState      = &projectResource{}
	_ resource.ResourceWithConfigValidators = &projectResource{}
)

// projectResourceModel maps the output schema.
type projectResourceModel struct {
	Id                         types.String `tfsdk:"id"`
	Name                       types.String `tfsdk:"name"`
	Slug                       types.String `tfsdk:"slug"`
	OrganizationName           types.String `tfsdk:"organization_name"`
	OrganizationSlug           types.String `tfsdk:"organization_slug"`
	OrganizationId             types.String `tfsdk:"organization_id"`
	OrgId                      types.String `tfsdk:"org_id"`
	VcsInfoUrl                 types.String `tfsdk:"vcs_info_url"`
	VcsInfoProvider            types.String `tfsdk:"vcs_info_provider"`
	VcsInfoDefaultBranch       types.String `tfsdk:"vcs_info_default_branch"`
	AutoCancelBuilds           types.Bool   `tfsdk:"auto_cancel_builds"`
	BuildForkPrs               types.Bool   `tfsdk:"build_fork_prs"`
	BuildPrsOnly               types.Bool   `tfsdk:"build_prs_only"`
	DisableSSH                 types.Bool   `tfsdk:"disable_ssh"`
	ForksReceiveSecretEnvVars  types.Bool   `tfsdk:"forks_receive_secret_env_vars"`
	OSS                        types.Bool   `tfsdk:"oss"`
	SetGithubStatus            types.Bool   `tfsdk:"set_github_status"`
	SetupWorkflows             types.Bool   `tfsdk:"setup_workflows"`
	WriteSettingsRequiresAdmin types.Bool   `tfsdk:"write_settings_requires_admin"`
	// PROnlyBranchOverrides is a Set rather than a List because CircleCI does not
	// preserve the order the branches were sent in. See the schema.
	PROnlyBranchOverrides types.Set `tfsdk:"pr_only_branch_overrides"`
}

// NewProjectResource is a helper function to simplify the provider implementation.
func NewProjectResource() resource.Resource {
	return &projectResource{}
}

// projectResource is the resource implementation.
type projectResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

// Schema defines the schema for the resource.
func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI project and its advanced settings.\n\n" +
			"~> **What an apply does depends on the organization.** On a **standalone** " +
			"(CircleCI-native, `circleci/…`) organization this creates a new project with no " +
			"repository behind it. On a **classic**, VCS-backed (`gh/…`, `bb/…`) organization it " +
			"can only **adopt a repository that already exists** — a repository named `name` must " +
			"already be present in that organization and be visible to the token, or the apply " +
			"fails with `404 GitHub response: Not Found`. See the resource documentation.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The unique identifier of the project.",
				Computed:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the project. Changing this value forces a new resource to be " +
					"created.\n\nOn a **classic**, VCS-backed organization this must be the name of a " +
					"repository that **already exists** in that organization: CircleCI adopts the " +
					"repository, it does not create one. On a **standalone** organization it is simply the " +
					"name of the new project, and no repository is involved.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "The project slug, as CircleCI reports it. On a **classic**, VCS-backed " +
					"organization that is `vcs-type/org-name/repo-name`, for example " +
					"`gh/acme/my-repo`. On a **standalone** organization it is " +
					"`circleci/<org-fragment>/<project-fragment>`, where both segments are opaque " +
					"identifiers — the second is neither the project name nor its UUID. Use this value " +
					"verbatim when importing; a slug assembled from names is rejected there.",
				Computed: true,
			},
			"organization_name": schema.StringAttribute{
				MarkdownDescription: "The name of the owning organization.",
				Computed:            true,
			},
			"organization_slug": schema.StringAttribute{
				MarkdownDescription: "The slug of the owning organization.",
				Computed:            true,
			},
			// See org_id_deprecation.go for why these are Optional+Computed and why
			// replacement is conditional on being configured. Getting that wrong
			// destroys the project, and its build history, on migration.
			"organization_id": deprecatedOrgIDAttribute("this project", true),
			"org_id":          orgIDAttribute("this project", true),
			"vcs_info_url": schema.StringAttribute{
				MarkdownDescription: "The VCS URL of the project repository.",
				Computed:            true,
			},
			"vcs_info_provider": schema.StringAttribute{
				MarkdownDescription: "The VCS provider (e.g., `github`, `bitbucket`).",
				Computed:            true,
			},
			"vcs_info_default_branch": schema.StringAttribute{
				MarkdownDescription: "The default branch of the project repository.",
				Computed:            true,
			},
			"auto_cancel_builds": schema.BoolAttribute{
				MarkdownDescription: "Whether to automatically cancel redundant builds.",
				Optional:            true,
				Computed:            true,
			},
			"build_fork_prs": schema.BoolAttribute{
				MarkdownDescription: "Whether to build pull requests from forked repositories.",
				Optional:            true,
				Computed:            true,
			},
			"build_prs_only": schema.BoolAttribute{
				MarkdownDescription: "Whether to build only branches that have an open pull request. " +
					"Use `pr_only_branch_overrides` to list branches that should always build.\n\n" +
					"~> On GitLab this is not a project setting but a per-trigger filter, so it has no " +
					"effect there.",
				Optional: true,
				Computed: true,
			},
			"disable_ssh": schema.BoolAttribute{
				MarkdownDescription: "Whether to disable SSH access to builds.",
				Optional:            true,
				Computed:            true,
			},
			"forks_receive_secret_env_vars": schema.BoolAttribute{
				MarkdownDescription: "Whether forked pull requests can access secret environment variables.",
				Optional:            true,
				Computed:            true,
			},
			"oss": schema.BoolAttribute{
				MarkdownDescription: "Whether the project is treated as free and open source, which grants additional " +
					"credits and makes builds visible to everyone.\n\n" +
					"~> **Read-only.** This is reported by the API but cannot be set through it. The " +
					"settings endpoint rejects the field outright — `400 Unexpected field 'advanced.oss'.`" +
					" — and because it rejects the whole request, including it broke every project " +
					"create and settings update. CircleCI derives it from whether the repository is " +
					"public together with an organization-level flag, so set it in the CircleCI web " +
					"application rather than here.",
				// Computed only, deliberately not Optional: the API rejects this field on
				// write. See internal/circleci/project_settings.go's OSS field.
				Computed: true,
			},
			"set_github_status": schema.BoolAttribute{
				MarkdownDescription: "Whether to set GitHub commit status on builds.",
				Optional:            true,
				Computed:            true,
			},
			"setup_workflows": schema.BoolAttribute{
				MarkdownDescription: "Whether setup workflows are enabled.",
				Optional:            true,
				Computed:            true,
			},
			"write_settings_requires_admin": schema.BoolAttribute{
				MarkdownDescription: "Whether admin permissions are required to change project settings.",
				Optional:            true,
				Computed:            true,
			},
			// A Set, not a List. The settings API stores these branches as an
			// unordered collection and reports them back in an order of its own
			// choosing — verified live: PATCHing
			// ["zebra","alpha","main","beta"] reads back as
			// ["zebra","main","alpha","beta"], stably, but never in the order sent.
			// Declared as a List, Terraform compared configured order against
			// returned order and planned a change on every run, for ever, with
			// nothing to apply. The circleci_project_settings data source already reports
			// this attribute as a Set for the same reason.
			//
			// No state upgrade accompanies this change: a list and a set of the same
			// element type share one JSON encoding and the framework re-reads prior
			// raw state against the current schema type, so existing state decodes as
			// a set unchanged. TestListToSetNeedsNoStateUpgrade proves it.
			"pr_only_branch_overrides": schema.SetAttribute{
				MarkdownDescription: "Branches that override the PR-only build setting. " +
					"Order is not significant: CircleCI does not preserve the order branches are sent in.\n\n" +
					"~> **Cannot be cleared.** Setting this to `[]` is rejected at plan time. CircleCI's API " +
					"accepts an empty list with HTTP 200 but silently leaves the existing branches in place, so " +
					"there is no way to clear the list through this route. Remove the attribute from the " +
					"configuration instead: that stops managing it and leaves the existing branches as they are.",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
			},
		},
	}
}

// ConfigValidators returns the cross-attribute checks that run at validate and
// plan time, before anything is written.
func (r *projectResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		explicitForkSecretsValidator{},
		orgIDConfigValidator(),
		noClearingBranchOverridesValidator{},
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	// Retrieve values from plan
	var plan projectResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Create new context
	newCreatedProject, err := r.client.CreateProject(ctx, effectiveOrgID(plan.OrganizationId, plan.OrgId), plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI project",
			projectCreateFailureDetail(plan.Name.ValueString(), err),
		)
		return
	}

	// The project exists in CircleCI, and is now followed, from here on: every
	// failure below must write state with what create already returned instead
	// of returning early and leaving a project CircleCI is tracking with
	// nothing on the Terraform side pointing at it. See trigger_resource.go's
	// Create for the model this follows — the same shape issue #6 fixed there.
	//
	// Map response body to schema and populate Computed attribute values, ahead
	// of the settings call below so that every return past this point has
	// something to write.
	plan.Id = types.StringValue(newCreatedProject.ID)
	plan.Name = types.StringValue(newCreatedProject.Name)
	plan.Slug = types.StringValue(newCreatedProject.Slug)
	plan.OrganizationName = types.StringValue(newCreatedProject.OrganizationName)
	plan.OrganizationSlug = types.StringValue(newCreatedProject.OrganizationSlug)
	setOrgIDs(&plan.OrganizationId, &plan.OrgId, newCreatedProject.OrganizationID)
	plan.VcsInfoUrl = types.StringValue(newCreatedProject.VCSInfo.VCSURL)
	plan.VcsInfoProvider = types.StringValue(newCreatedProject.VCSInfo.Provider)
	plan.VcsInfoDefaultBranch = types.StringValue(newCreatedProject.VCSInfo.DefaultBranch)

	vcsType, orgName, projectName, slugDiags := parseProjectSlug(newCreatedProject.Slug)
	resp.Diagnostics.Append(slugDiags...)
	if resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)

		return
	}

	// Only the settings this configuration actually sets are sent; every other one
	// is left out so that CircleCI applies its own default. The defaults are
	// listed in templates/resources/project.md.tmpl.
	//
	// projectSettingRequest is what makes that work, and it checks IsUnknown as
	// well as IsNull for a reason. These attributes are Optional+Computed, and
	// Terraform plans an omitted Optional+Computed attribute as *unknown* at create
	// time, not null — so a guard that only asks IsNull is taken for every toggle,
	// and ValueBoolPointer on an unknown value yields a pointer to false. That is
	// how every toggle came to be written as false on create whatever the
	// configuration said, which forced set_github_status off (CircleCI defaults it
	// to true) and cleared pr_only_branch_overrides.
	//
	// oss is deliberately absent: it is read-only on this API version, and the
	// PATCH rejects the whole request when it is present. See the OSS field in
	// internal/circleci/project_settings.go.
	newAdvancedSettings := circleci.ProjectSettings{
		AutocancelBuilds:           projectSettingRequest(plan.AutoCancelBuilds),
		BuildForkPrs:               projectSettingRequest(plan.BuildForkPrs),
		BuildPrsOnly:               projectSettingRequest(plan.BuildPrsOnly),
		DisableSSH:                 projectSettingRequest(plan.DisableSSH),
		ForksReceiveSecretEnvVars:  projectSettingRequest(plan.ForksReceiveSecretEnvVars),
		SetGithubStatus:            projectSettingRequest(plan.SetGithubStatus),
		SetupWorkflows:             projectSettingRequest(plan.SetupWorkflows),
		WriteSettingsRequiresAdmin: projectSettingRequest(plan.WriteSettingsRequiresAdmin),
	}

	if !plan.PROnlyBranchOverrides.IsNull() && !plan.PROnlyBranchOverrides.IsUnknown() {
		branches, diags := branchOverrides(ctx, plan.PROnlyBranchOverrides)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)

			return
		}
		newAdvancedSettings.PROnlyBranchOverrides = &branches
	}

	// A configuration that sets no setting has nothing to write, and the API
	// rejects a body with no fields ("No JSON fields found."), so the settings the
	// new project already has are read instead. They are needed either way: every
	// toggle is Computed, so it must hold a known value in state.
	var newProjectSettings *circleci.ProjectSettings

	if newAdvancedSettings.IsEmpty() {
		newProjectSettings, err = r.client.GetProjectSettings(ctx, vcsType, orgName, projectName)
		if err != nil {
			resp.Diagnostics.AddError(
				"Error reading CircleCI project settings",
				fmt.Sprintf("Could not read the settings of the recently created CircleCI project %s: %s", newCreatedProject.Slug, err.Error()),
			)
			resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)

			return
		}
	} else {
		newProjectSettings, err = r.client.UpdateProjectSettings(ctx, vcsType, orgName, projectName, newAdvancedSettings)
		if err != nil {
			resp.Diagnostics.AddError(
				"Error updating CircleCI project settings",
				fmt.Sprintf("Could not update recently created CircleCI project settings:\n\nsettings: %+v\norg: %s\nproject_id: %s\nproject_name: %s\nslug: %s\n\nUnexpected error: %s\n", newAdvancedSettings, effectiveOrgID(plan.OrganizationId, plan.OrgId), newCreatedProject.ID, newCreatedProject.Name, newCreatedProject.Slug, err.Error()),
			)
			resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)

			return
		}
	}

	plan.AutoCancelBuilds = types.BoolPointerValue(newProjectSettings.AutocancelBuilds)
	plan.BuildForkPrs = types.BoolPointerValue(newProjectSettings.BuildForkPrs)
	plan.BuildPrsOnly = types.BoolPointerValue(newProjectSettings.BuildPrsOnly)
	plan.DisableSSH = types.BoolPointerValue(newProjectSettings.DisableSSH)
	plan.ForksReceiveSecretEnvVars = types.BoolPointerValue(newProjectSettings.ForksReceiveSecretEnvVars)
	plan.OSS = types.BoolPointerValue(newProjectSettings.OSS)
	plan.SetGithubStatus = types.BoolPointerValue(newProjectSettings.SetGithubStatus)
	plan.SetupWorkflows = types.BoolPointerValue(newProjectSettings.SetupWorkflows)
	plan.WriteSettingsRequiresAdmin = types.BoolPointerValue(newProjectSettings.WriteSettingsRequiresAdmin)

	plan.PROnlyBranchOverrides, diags = branchOverrideSet(ctx, newProjectSettings.PROnlyBranchOverrides)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)

		return
	}

	// Set state to fully populated data
	diags = resp.State.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Read refreshes the Terraform state with the latest data.
func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var projectState projectResourceModel
	diags := req.State.Get(ctx, &projectState)

	if diags != nil {
		resp.Diagnostics.Append(diags...)
		return
	}

	if projectState.Slug.IsNull() {
		resp.Diagnostics.AddError(
			"Missing slug",
			"Missing slug",
		)
		return
	}

	// ImportState (below) writes nothing but slug, so this Read is the only
	// opportunity Terraform gives the provider to populate the rest of an
	// imported project. Every attribute other than slug is null here and only
	// here: Create and Update both resolve id, and no other path can reach Read
	// with a null one. See the settings block below, and BUG P3 in
	// TestProjectResourceUnit_ImportPopulatesEverySetting.
	importing := projectState.Id.IsNull()

	apiProject, err := r.client.GetProject(ctx, projectState.Slug.ValueString())
	if err != nil {
		// A project that no longer exists is drift, not an error. Without this,
		// unfollowing or deleting the project in the CircleCI UI made every
		// subsequent `terraform plan` fail outright instead of proposing to
		// recreate it — and this is the most widely used resource here.
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to Read CircleCI project with Slug "+projectState.Slug.ValueString(),
			circleci.Detail(err),
		)
		return
	}

	// Map response body to model
	projectState.Id = types.StringValue(apiProject.ID)
	projectState.Name = types.StringValue(apiProject.Name)
	projectState.Slug = types.StringValue(apiProject.Slug)
	setOrgIDs(&projectState.OrganizationId, &projectState.OrgId, apiProject.OrganizationID)
	projectState.OrganizationName = types.StringValue(apiProject.OrganizationName)
	projectState.OrganizationSlug = types.StringValue(apiProject.OrganizationSlug)
	projectState.VcsInfoDefaultBranch = types.StringValue(apiProject.VCSInfo.DefaultBranch)
	projectState.VcsInfoProvider = types.StringValue(apiProject.VCSInfo.Provider)
	projectState.VcsInfoUrl = types.StringValue(apiProject.VCSInfo.VCSURL)

	vcsType, orgName, projectName, slugDiags := parseProjectSlug(projectState.Slug.ValueString())
	resp.Diagnostics.Append(slugDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectSettings, err := r.client.GetProjectSettings(
		ctx,
		vcsType,
		orgName,
		projectName,
	)

	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Read CircleCI project settings",
			err.Error(),
		)
		return
	}
	// Two different rules, and which applies turns on `importing`. See
	// projectSettingImport for the whole argument; in outline:
	//
	//   - On an ORDINARY refresh, a toggle that is already null in state stays
	//     null. Adopting the API's value there would make a setting nobody
	//     configured indistinguishable from one a practitioner set outright, and
	//     because these attributes are Optional+Computed the adopted value would
	//     be carried into the next plan and written back on the next Update —
	//     pinning whatever CircleCI happened to report at the moment of that one
	//     Read. That is what projectSettingRefresh (project_settings_resource.go)
	//     exists for, and circleci_project_settings uses it for the same reason.
	//   - On the Read that FOLLOWS AN IMPORT, every value the API can supply is
	//     adopted. An import whose state omits nine of this resource's attributes
	//     is not an import: `terraform plan` against it proposes a change for
	//     every one the practitioner then writes down, and ImportStateVerify — the
	//     framework's own definition of a faithful import — fails against the same
	//     project created through Create, which resolves all nine. Adopting here
	//     makes an imported project indistinguishable from a created one, which is
	//     the whole contract of import. The pinning argument above does not apply,
	//     because Create pins exactly the same way and always has.
	projectState.AutoCancelBuilds = projectSettingImport(importing, projectState.AutoCancelBuilds, projectSettings.AutocancelBuilds)
	projectState.BuildForkPrs = projectSettingImport(importing, projectState.BuildForkPrs, projectSettings.BuildForkPrs)
	projectState.BuildPrsOnly = projectSettingImport(importing, projectState.BuildPrsOnly, projectSettings.BuildPrsOnly)
	projectState.DisableSSH = projectSettingImport(importing, projectState.DisableSSH, projectSettings.DisableSSH)
	projectState.ForksReceiveSecretEnvVars = projectSettingImport(importing, projectState.ForksReceiveSecretEnvVars, projectSettings.ForksReceiveSecretEnvVars)
	// oss is always adopted, unlike every other setting: it is Computed-only and
	// cannot be written, so reporting what CircleCI holds can never turn into a
	// write the practitioner did not ask for. See project_settings_resource.go's
	// refresh method, which documents the same exception.
	projectState.OSS = types.BoolPointerValue(projectSettings.OSS)
	projectState.SetGithubStatus = projectSettingImport(importing, projectState.SetGithubStatus, projectSettings.SetGithubStatus)
	projectState.SetupWorkflows = projectSettingImport(importing, projectState.SetupWorkflows, projectSettings.SetupWorkflows)
	projectState.WriteSettingsRequiresAdmin = projectSettingImport(importing, projectState.WriteSettingsRequiresAdmin, projectSettings.WriteSettingsRequiresAdmin)

	// The ninth attribute, and it follows the same two rules as the eight booleans
	// above for the same reasons: on an ordinary refresh an undeclared
	// pr_only_branch_overrides stays null, so that "not managed" does not become
	// "managed as whatever the API reports"; on the Read after an import it is
	// adopted, so that the imported state matches what Create would have produced.
	if importing || !projectState.PROnlyBranchOverrides.IsNull() {
		overrides, overrideDiags := branchOverrideSet(ctx, projectSettings.PROnlyBranchOverrides)
		resp.Diagnostics.Append(overrideDiags...)
		if resp.Diagnostics.HasError() {
			return
		}
		projectState.PROnlyBranchOverrides = overrides
	}

	// Set state
	diags = resp.State.Set(ctx, &projectState)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan projectResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state projectResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	prOnlybranchOverrides, diags := branchOverrides(ctx, plan.PROnlyBranchOverrides)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// projectSettingRequest rather than ValueBoolPointer for the same reason as in
	// Create: it turns a null or unknown value into nil, which omits the field,
	// instead of sending false for a value Terraform has not decided on yet. oss is
	// absent because the API rejects it on write.
	advanceSettings := circleci.ProjectSettings{
		AutocancelBuilds: projectSettingRequest(plan.AutoCancelBuilds),
		BuildForkPrs:     projectSettingRequest(plan.BuildForkPrs),
		// BuildPrsOnly was absent from this payload entirely while every other
		// toggle was present, so changing build_prs_only on an existing project was
		// silently never sent. Update then wrote state from what the API reported —
		// still the old value — so the saved state contradicted the plan and
		// Terraform failed with "provider produced inconsistent result after apply".
		BuildPrsOnly:               projectSettingRequest(plan.BuildPrsOnly),
		DisableSSH:                 projectSettingRequest(plan.DisableSSH),
		ForksReceiveSecretEnvVars:  projectSettingRequest(plan.ForksReceiveSecretEnvVars),
		SetGithubStatus:            projectSettingRequest(plan.SetGithubStatus),
		SetupWorkflows:             projectSettingRequest(plan.SetupWorkflows),
		WriteSettingsRequiresAdmin: projectSettingRequest(plan.WriteSettingsRequiresAdmin),
	}

	// A nil pointer omits the list; a pointer to a nil slice would send JSON null.
	if prOnlybranchOverrides != nil {
		advanceSettings.PROnlyBranchOverrides = &prOnlybranchOverrides
	}
	vcsType, orgName, projectName, slugDiags := parseProjectSlug(state.Slug.ValueString())
	resp.Diagnostics.Append(slugDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectSettings := advanceSettings

	// Nothing to write, as in Create: read the current settings so that state still
	// holds a known value for every Computed attribute.
	var (
		updatedProject *circleci.ProjectSettings
		err            error
	)

	if projectSettings.IsEmpty() {
		updatedProject, err = r.client.GetProjectSettings(ctx, vcsType, orgName, projectName)
	} else {
		updatedProject, err = r.client.UpdateProjectSettings(ctx, vcsType, orgName, projectName, projectSettings)
	}

	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Update CircleCI project settings for project: "+state.Slug.String(),
			err.Error(),
		)
		return
	}

	state.AutoCancelBuilds = types.BoolPointerValue(updatedProject.AutocancelBuilds)
	state.BuildForkPrs = types.BoolPointerValue(updatedProject.BuildForkPrs)
	state.BuildPrsOnly = types.BoolPointerValue(updatedProject.BuildPrsOnly)
	state.DisableSSH = types.BoolPointerValue(updatedProject.DisableSSH)
	state.ForksReceiveSecretEnvVars = types.BoolPointerValue(updatedProject.ForksReceiveSecretEnvVars)
	state.OSS = types.BoolPointerValue(updatedProject.OSS)
	state.SetGithubStatus = types.BoolPointerValue(updatedProject.SetGithubStatus)
	state.SetupWorkflows = types.BoolPointerValue(updatedProject.SetupWorkflows)
	state.WriteSettingsRequiresAdmin = types.BoolPointerValue(updatedProject.WriteSettingsRequiresAdmin)

	// Guarded on what was requested (projectSettings), not on what came back:
	// an unmanaged pr_only_branch_overrides sends nothing, and this must not
	// adopt whatever CircleCI happens to already hold, for the same reason as
	// Read's guard above. The value written to state, though, comes from
	// updatedProject — what the API reported — like every other field in this
	// function, rather than from the request that was just sent. CircleCI is
	// free to normalise, sort, deduplicate or partially reject the list, so
	// state must record what it now holds, not what Terraform asked for.
	if len(derefBranches(projectSettings.PROnlyBranchOverrides)) > 0 {
		overrides, overrideDiags := branchOverrideSet(ctx, updatedProject.PROnlyBranchOverrides)
		resp.Diagnostics.Append(overrideDiags...)
		if resp.Diagnostics.HasError() {
			return
		}
		state.PROnlyBranchOverrides = overrides
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Retrieve values from state
	var state projectResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Delete existing project
	err := r.client.DeleteProject(ctx, state.Slug.ValueString())
	if err != nil {
		// Already gone is the desired end state, so a destroy of a project someone
		// removed in the UI succeeds rather than erroring on the way out — but only
		// once "gone" has been distinguished from "not visible to this token". See
		// deletedProjectIsReallyGone.
		if circleci.IsNotFound(err) {
			r.deletedProjectIsReallyGone(ctx, state, &resp.Diagnostics)

			return
		}

		resp.Diagnostics.AddError(
			"Error Deleting CircleCI Project",
			"Could not delete project: "+circleci.Detail(err),
		)
		return
	}
}

// deletedProjectIsReallyGone decides whether a 404 from DELETE means the project
// no longer exists, or only that this token cannot see it. It reports true when
// the destroy should be allowed to succeed, and appends an error and reports
// false when it must not.
//
// WHY THIS EXISTS. `DELETE /api/v2/project/{slug}` answers
// 404 {"message":"Project not found"} in two situations that look identical from
// the client, and one of them is not a deletion at all. Measured over the
// network, with two personal API tokens belonging to two different accounts:
//
//	# token A creates a project in an organization only A can reach
//	POST /api/v2/organization/e75c…/project  {"name":"my-project"}
//	→ 200, slug circleci/Va2k…/TFtestProjFragment012
//
//	# token B, which cannot see that organization, tries to delete it
//	DELETE /api/v2/project/circleci/Va2k…/TFtestProjFragment012   (token B)
//	→ 404 {"message":"Project not found"}
//
//	# and the project is still there
//	GET /api/v2/project/circleci/Va2k…/TFtestProjFragment012      (token A)
//	→ 200
//
// Treating that 404 as "already gone" — which this function's caller did
// unconditionally before — makes Terraform report a successful destroy and drop
// the resource from state while the project is still live, still runnable and now
// invisible to Terraform. That is silent orphaning, the same shape as the bug
// already fixed in the URL orb allow list. Retrying the DELETE, or following it
// with a GET, cannot tell the two apart: the same token gets 404 either way.
//
// The ORGANIZATION lookup can tell them apart, and it is the only thing that can
// without a second credential. Also measured, same two tokens:
//
//	GET /api/v2/organization/e75c…   (token B) → 404 {"message":"Org not found."}
//	GET /api/v2/organization/e75c…   (token A) → 200
//
// So: if the token can still reach the organization the project belonged to, a
// 404 for the project is taken at face value and the destroy succeeds. If it
// cannot reach the organization either, the 404 says nothing about the project
// and the destroy fails loudly with the resource left in state.
//
// TWO LIMITS, both deliberate:
//
//   - A whole organization deleted outside Terraform now fails this destroy
//     instead of quietly succeeding, because its projects are unreachable for
//     the same reason a permissions loss makes them unreachable. The diagnostic
//     names that case and says what to do about it. Making a practitioner run
//     one `terraform state rm` is a much smaller harm than orphaning a live
//     project without telling them.
//   - A token that can see the organization but not this particular project —
//     if CircleCI's project-level permissions ever produce that — is still
//     mistaken for a deleted project. This narrows the hole rather than closing
//     it, because the API offers nothing else to distinguish the two.
//
// Any error from the organization lookup other than 404 leaves the destroy
// succeeding: a transient 5xx on a confirmation request must not fail a destroy
// whose DELETE has already been answered.
func (r *projectResource) deletedProjectIsReallyGone(
	ctx context.Context,
	state projectResourceModel,
	diags *diag.Diagnostics,
) bool {
	organizationID := effectiveOrgID(state.OrganizationId, state.OrgId)
	if organizationID == "" {
		// Nothing to check against. State written by a version of this provider
		// that predates the organization pair, or a hand-edited state file.
		return true
	}

	_, orgErr := r.client.GetOrganization(ctx, organizationID)
	if orgErr == nil {
		return true
	}

	if !circleci.IsNotFound(orgErr) {
		return true
	}

	diags.AddError(
		"Could not confirm the CircleCI project was deleted",
		fmt.Sprintf(
			"Deleting project %s answered HTTP 404 \"Project not found\", and organization %s is "+
				"not visible to this token either (\"Org not found.\").\n\n"+
				"CircleCI answers 404 both for a project that no longer exists and for one this "+
				"token is not allowed to see, so the project may still exist. Terraform has kept "+
				"it in state rather than reporting a successful destroy: dropping it would leave a "+
				"live project running with nothing managing it.\n\n"+
				"If the token has lost access to the organization, restore its access and destroy "+
				"again. If the organization itself was deleted, the project is gone with it — "+
				"remove the resource from state with:\n\n"+
				"    terraform state rm <resource address>",
			state.Slug.ValueString(),
			organizationID,
		),
	)

	return false
}

// Configure adds the provider configured client to the resource.
func (r *projectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Add a nil check when handling ProviderData because Terraform
	// sets that data after it calls the ConfigureProvider RPC.
	if req.ProviderData == nil {
		return
	}

	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports a project by its slug.
//
// It writes only slug, which is the only thing the import ID carries. Everything
// else — id, name, the organization pair, vcs_info, and all nine settings
// attributes — is filled in by the Read that Terraform runs immediately
// afterwards, which detects the import from the null id and adopts every value
// the API can supply. See the settings block in Read.
//
// It deliberately does not validate the slug either: circleci.GetProject's own
// segment-count guard (projectSlugPath) rejects a malformed one during that Read
// with a diagnostic naming the expected shape, and duplicating the check here
// would mean two places to keep in step. See
// TestProjectResourceUnit_ImportMalformedSlugFailsCleanly.
func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(
		ctx, path.Root("slug"), req.ID,
	)...)

	if resp.Diagnostics.HasError() {
		return
	}
}

// projectSettingImport folds an API-reported setting back into state, choosing
// between the two rules Read needs.
//
// importing is true only for the Read that Terraform runs straight after
// ImportState, detected there by a null id. Then the API's value is adopted
// outright: an import must leave state describing the project as it actually is,
// so that `terraform plan` afterwards is empty and ImportStateVerify — which
// compares an imported instance against the same resource created through
// Create — passes. Import previously wrote nothing but slug, leaving these nine
// attributes null while Create resolved every one of them, which is BUG P3: it
// failed ImportStateVerify in all four of this resource's acceptance tests and
// made the first plan after an import propose a change for every setting the
// practitioner wrote down, whether or not it already held that value.
//
// Otherwise this defers to projectSettingRefresh, which keeps an
// already-null toggle null. See the settings block in Read for why the
// distinction matters, and why the "adopting pins the value" objection that
// shapes projectSettingRefresh does not apply to an import.
func projectSettingImport(importing bool, prior types.Bool, reported *bool) types.Bool {
	if importing {
		return types.BoolPointerValue(reported)
	}

	return projectSettingRefresh(prior, reported)
}

// projectCreateFailureDetail renders a failed project create for a diagnostic.
//
// It exists because of the single most confusing failure this resource has (BUG
// P4): POST /api/v2/organization/{id}/project is TWO different operations
// depending on what kind of organization it is pointed at, and only one of them
// can create anything.
//
//   - On a STANDALONE (CircleCI-native, `circleci/…`) organization it genuinely
//     creates a new, repository-less project. Measured over the network against a
//     standalone organization created seconds earlier and connected to no VCS at
//     all: 200, with vcs_info.provider "CircleCI".
//
//   - On a CLASSIC, VCS-backed (`gh/…`, `bb/…`) organization it can only ADOPT a
//     repository that already exists on the VCS. It never creates one. Measured
//     over the network against two different GitHub-backed organizations, with a
//     repository name that does not exist in either:
//
//     POST /api/v2/organization/{uuid}/project  {"name":"no-such-repo"}
//     → 404  {"message":"GitHub response: Not Found"}
//
// The practitioner used to get neither of those facts, nor even the API's own
// message: Create passed err.Error() straight through, so the whole diagnostic
// was `POST /api/v2/organization/<uuid>/project: 404 Not Found`. circleci.Detail
// recovers the body's message, and on a 404 the precondition is spelled out,
// because a missing repository is by far the likeliest cause and is not
// guessable from the response.
//
// The hint is attached on 404 only, and it names both possibilities rather than
// asserting one: the organization ID alone does not say which class the
// organization is, and finding out would cost a second request on an error path.
func projectCreateFailureDetail(name string, err error) string {
	detail := "Could not create CircleCI project, unexpected error: " + circleci.Detail(err)

	if !circleci.IsNotFound(err) {
		return detail
	}

	return detail + fmt.Sprintf(
		"\n\nThis route behaves differently depending on the organization:\n\n"+
			"  * On a standalone (CircleCI-native, \"circleci/…\") organization it creates a new, "+
			"repository-less project.\n"+
			"  * On a classic, VCS-backed (\"gh/…\", \"bb/…\") organization it can only ADOPT a "+
			"repository that already exists — it never creates one.\n\n"+
			"So on a classic organization this 404 most likely means CircleCI could not find a "+
			"repository named %q in that organization. Create the repository on the VCS first, and "+
			"check that the token's VCS account can see it. Otherwise, check that the organization "+
			"ID is correct and that this token can reach that organization.",
		name,
	)
}

// branchOverrides converts a Terraform set of branch names into plain strings.
//
// It exists because attr.Value.String() renders a value the way Terraform
// displays it, so a branch name comes back quoted (`"main"` rather than `main`).
// Sending that to the API set literally-quoted branch names, which is why
// pr_only_branch_overrides did not work.
func branchOverrides(ctx context.Context, branchSet types.Set) ([]string, diag.Diagnostics) {
	if branchSet.IsNull() || branchSet.IsUnknown() {
		return nil, nil
	}

	branches := make([]string, 0, len(branchSet.Elements()))
	diags := branchSet.ElementsAs(ctx, &branches, false)

	return branches, diags
}

// branchOverrideSet is the other direction: the branch list the API reported,
// folded back into a Terraform set.
//
// A branch list the API omitted becomes an empty set rather than a null one,
// because pr_only_branch_overrides is Computed on circleci_project and a Computed
// attribute has to hold a known value once an apply is finished.
func branchOverrideSet(ctx context.Context, branches *[]string) (types.Set, diag.Diagnostics) {
	reported := derefBranches(branches)
	if reported == nil {
		reported = []string{}
	}

	return types.SetValueFrom(ctx, types.StringType, reported)
}

// parseProjectSlug splits a project slug into its VCS provider, organization and
// project name.
//
// A slug is always three segments, e.g. "gh/acme/repo". Indexing the split
// result directly panics on anything shorter, which crashes the provider process
// and surfaces to the practitioner as an opaque plugin crash rather than an
// error they can act on.
func parseProjectSlug(slug string) (vcsType, orgName, projectName string, diags diag.Diagnostics) {
	parts := strings.Split(slug, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		diags.AddError(
			"Invalid CircleCI project slug",
			fmt.Sprintf(
				"Expected a project slug of the form \"vcs-type/org-name/repo-name\", such as \"gh/acme/repo\", but got %q.",
				slug,
			),
		)

		return "", "", "", diags
	}

	return parts[0], parts[1], parts[2], diags
}

// derefBranches reads a branch-override list the API may have omitted. A nil
// pointer means the API reported no list, which is equivalent to an empty one for
// reading purposes; the pointer only carries extra meaning when *sending*, where
// nil omits the field and an empty slice clears the list.
func derefBranches(branches *[]string) []string {
	if branches == nil {
		return nil
	}

	return *branches
}
