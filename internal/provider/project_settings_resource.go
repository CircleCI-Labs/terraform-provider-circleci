// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

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
	_ resource.Resource                     = &projectSettingsResource{}
	_ resource.ResourceWithConfigure        = &projectSettingsResource{}
	_ resource.ResourceWithImportState      = &projectSettingsResource{}
	_ resource.ResourceWithConfigValidators = &projectSettingsResource{}
)

// projectSettingsTypeName is the Terraform type name, used in diagnostics.
const projectSettingsTypeName = "circleci_project_settings"

// projectSlugPattern matches a project slug: exactly three non-empty segments,
// such as "github/acme/repo". Anchoring each segment to [^/]+ rather than .+
// rejects a fourth segment at plan time instead of letting it fail as an opaque
// HTTP 404 during apply.
var projectSlugPattern = regexp.MustCompile(`^[^/]+/[^/]+/[^/]+$`)

// projectSettingsResourceModel maps the resource schema.
//
// Every writable toggle is types.Bool and Optional-only, never Computed. See the
// Schema method for why that matters. OSS is the one exception: it is read-only on
// the API, so it is Computed-only and simply reports what CircleCI holds.
type projectSettingsResourceModel struct {
	Slug                       types.String `tfsdk:"slug"`
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

// projectSettingRequest converts a schema value into an update payload field. A
// null or unknown value becomes nil, and a nil field is omitted from the request
// body, so a setting the configuration says nothing about is left untouched.
func projectSettingRequest(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}

	return v.ValueBoolPointer()
}

// projectSettingRefresh folds an API value back into state.
//
// It deliberately keeps a null configured value null instead of adopting what
// the API reports. Adopting it would make the setting indistinguishable from one
// the practitioner set to false, and the next apply would start writing it.
func projectSettingRefresh(configured types.Bool, remote *bool) types.Bool {
	if configured.IsNull() {
		return types.BoolNull()
	}

	return types.BoolPointerValue(remote)
}

// payload builds the partial update body for the settings this configuration
// manages.
func (m projectSettingsResourceModel) payload(ctx context.Context) (circleci.ProjectSettings, diag.Diagnostics) {
	settings := circleci.ProjectSettings{
		AutocancelBuilds:          projectSettingRequest(m.AutoCancelBuilds),
		BuildForkPrs:              projectSettingRequest(m.BuildForkPrs),
		BuildPrsOnly:              projectSettingRequest(m.BuildPrsOnly),
		DisableSSH:                projectSettingRequest(m.DisableSSH),
		ForksReceiveSecretEnvVars: projectSettingRequest(m.ForksReceiveSecretEnvVars),
		// OSS is absent on purpose: the settings PATCH rejects it, and rejects the
		// whole request with it. See the OSS field in
		// internal/circleci/project_settings.go.
		SetGithubStatus:            projectSettingRequest(m.SetGithubStatus),
		SetupWorkflows:             projectSettingRequest(m.SetupWorkflows),
		WriteSettingsRequiresAdmin: projectSettingRequest(m.WriteSettingsRequiresAdmin),
	}

	if m.PROnlyBranchOverrides.IsNull() || m.PROnlyBranchOverrides.IsUnknown() {
		return settings, nil
	}

	// A configured set is sent even when empty, because sending [] is the only
	// way to clear the overrides the project already has.
	branches, diags := branchOverrides(ctx, m.PROnlyBranchOverrides)
	if diags.HasError() {
		return settings, diags
	}
	if branches == nil {
		branches = []string{}
	}
	settings.PROnlyBranchOverrides = &branches

	return settings, diags
}

// refresh overwrites the managed settings with the values the API reports,
// leaving unmanaged ones null.
func (m *projectSettingsResourceModel) refresh(ctx context.Context, remote *circleci.ProjectSettings) diag.Diagnostics {
	m.AutoCancelBuilds = projectSettingRefresh(m.AutoCancelBuilds, remote.AutocancelBuilds)
	m.BuildForkPrs = projectSettingRefresh(m.BuildForkPrs, remote.BuildForkPrs)
	m.BuildPrsOnly = projectSettingRefresh(m.BuildPrsOnly, remote.BuildPrsOnly)
	m.DisableSSH = projectSettingRefresh(m.DisableSSH, remote.DisableSSH)
	m.ForksReceiveSecretEnvVars = projectSettingRefresh(m.ForksReceiveSecretEnvVars, remote.ForksReceiveSecretEnvVars)
	// oss is always adopted, unlike every other setting: it is Computed-only and
	// cannot be written, so reporting what CircleCI holds can never turn into a
	// write the practitioner did not ask for.
	m.OSS = types.BoolPointerValue(remote.OSS)
	m.SetGithubStatus = projectSettingRefresh(m.SetGithubStatus, remote.SetGithubStatus)
	m.SetupWorkflows = projectSettingRefresh(m.SetupWorkflows, remote.SetupWorkflows)
	m.WriteSettingsRequiresAdmin = projectSettingRefresh(m.WriteSettingsRequiresAdmin, remote.WriteSettingsRequiresAdmin)

	// The branch overrides are only tracked when the configuration manages them,
	// for the same reason as the toggles above.
	if m.PROnlyBranchOverrides.IsNull() {
		return nil
	}

	branches := []string{}
	if remote.PROnlyBranchOverrides != nil {
		branches = *remote.PROnlyBranchOverrides
	}

	overrides, diags := types.SetValueFrom(ctx, types.StringType, branches)
	if diags.HasError() {
		return diags
	}
	m.PROnlyBranchOverrides = overrides

	return diags
}

// NewProjectSettingsResource is a helper function to simplify the provider
// implementation.
func NewProjectSettingsResource() resource.Resource {
	return &projectSettingsResource{}
}

// projectSettingsResource manages the advanced settings of a project that
// already exists.
//
// It is a settings resource: it creates nothing and destroys nothing. The
// settings record lives with the project, so this resource only ever reads it
// and writes over the parts the configuration names. That is what makes it
// usable for projects Terraform did not create, which circleci_project cannot
// manage.
type projectSettingsResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *projectSettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_settings"
}

// Schema defines the schema for the resource.
func (r *projectSettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// Every setting below is Optional and never Computed, which is the central
	// design decision of this resource.
	//
	// Optional+Computed would read better in the docs, but it would make
	// Terraform adopt whatever the API currently reports into state for a setting
	// the configuration never mentions. From then on "not managed" and "managed
	// as false" look identical in state, and the provider would start writing
	// settings nobody asked it to manage. Optional-only keeps an unmentioned
	// setting null, and only non-null settings are ever sent.
	toggle := func(description string) schema.BoolAttribute {
		return schema.BoolAttribute{
			MarkdownDescription: description +
				"\n\nLeave this unset to let CircleCI manage it; the provider only writes settings that appear in the configuration.",
			Optional: true,
		}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the advanced settings of an existing CircleCI project.\n\n" +
			"Works against both CircleCI Cloud and CircleCI Server, because project settings are " +
			"served by the v2 API on both.\n\n" +
			"Use this rather than `circleci_project` when the project already exists: " +
			"`circleci_project` creates a project and owns its settings, whereas this resource " +
			"adopts the settings of a project it did not create. Do not manage the same project " +
			"with both — see the guidance in the documentation.\n\n" +
			"This is a settings resource rather than a thing that gets created and destroyed. " +
			"The settings record lives with the project, so `terraform destroy` only drops it from " +
			"state and leaves every value as it stands. Each setting is written only when the " +
			"configuration sets it, so several configurations may safely manage disjoint settings " +
			"on the same project.",
		Attributes: map[string]schema.Attribute{
			"slug": schema.StringAttribute{
				MarkdownDescription: "The project's slug in the format `vcs-type/org-name/repo-name`. " +
					"For example, `github/CircleCI-Public/terraform-provider-circleci`. " +
					"Changing this value forces a new resource to be created.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						projectSlugPattern,
						"must be in the format 'vcs-type/org-name/repo-name'",
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"auto_cancel_builds": toggle(
				"Except for the default branch, cancel any outstanding workflows on a branch when a newer pipeline is triggered on that branch. Scheduled workflows and re-runs are never auto-cancelled.",
			),
			"build_fork_prs": toggle(
				"Run builds for pull requests opened from forks of this repository.",
			),
			"build_prs_only": toggle(
				"Build only branches that have an open pull request associated with them. Use `pr_only_branch_overrides` to list branches that should always build.",
			),
			"disable_ssh": toggle(
				"Disable SSH re-runs for this project, so jobs cannot be re-run with SSH debugging access.",
			),
			"forks_receive_secret_env_vars": toggle(
				"Run forked pull requests with this project's configuration, environment variables and secrets. The build cache is also shared between the original repository and all forks, so enabling this exposes both to anyone who can open a pull request.",
			),
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
				// write. See internal/circleci/project_settings.go's OSS field. This is the
				// one attribute here that is Computed, and it is safe to adopt precisely
				// because it can never be written.
				Computed: true,
			},
			"set_github_status": toggle(
				"Report the status of every pushed commit to GitHub's status API. Updates are reported per job.",
			),
			"setup_workflows": toggle(
				"Allow a setup workflow to conditionally trigger configuration outside the primary `.circleci` directory, update pipeline parameters before a build runs, and generate customised configuration.",
			),
			"write_settings_requires_admin": toggle(
				"Require organization administrator permissions to change this project's settings. Enabling this can lock the provider itself out of further changes if its token does not belong to an administrator.",
			),
			// A Set, not a List. The settings API stores these branches as an
			// unordered collection and reports them back in an order of its own
			// choosing — verified live: PATCHing ["zebra","alpha","main","beta"] reads
			// back as ["zebra","main","alpha","beta"], stably, but never in the order
			// sent. Declared as a List, Terraform compared configured order against
			// returned order and planned a change on every run, for ever, with nothing
			// to apply. The circleci_project_settings data source already reports this
			// attribute as a Set for the same reason.
			//
			// No state upgrade accompanies this change: a list and a set of the same
			// element type share one JSON encoding and the framework re-reads prior raw
			// state against the current schema type, so existing state decodes as a set
			// unchanged. TestListToSetNeedsNoStateUpgrade proves it.
			"pr_only_branch_overrides": schema.SetAttribute{
				MarkdownDescription: "Branches that always trigger a build, even when `build_prs_only` is enabled. " +
					"The set replaces whatever CircleCI currently holds, and setting it to `[]` clears every override. " +
					"Leave it unset to leave the project's existing overrides alone. CircleCI accepts at most 100 branches. " +
					"Order is not significant: CircleCI does not preserve the order branches are sent in.",
				Optional:    true,
				ElementType: types.StringType,
			},
		},
	}
}

// ConfigValidators returns the cross-attribute checks that run at validate and
// plan time, before anything is written.
func (r *projectSettingsResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		explicitForkSecretsValidator{},
	}
}

// Create applies the configured settings to the project's existing settings
// record.
//
// Nothing is created: the record already exists for as long as the project does.
// The current settings are read first so that a wrong or unreachable slug fails
// before anything is written.
func (r *projectSettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		addUnconfiguredClientError(&resp.Diagnostics)

		return
	}

	var plan projectSettingsResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	slug := plan.Slug.ValueString()

	vcsType, orgName, projectName, diags := parseProjectSlug(slug)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	current, err := r.client.GetProjectSettings(ctx, vcsType, orgName, projectName)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI project settings for "+slug,
			circleci.Detail(err),
		)

		return
	}

	settings, diags := plan.payload(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if settings.IsEmpty() {
		// No setting is configured, so there is nothing to write. The resource
		// still tracks the project, which is a legitimate way to adopt it before
		// deciding which settings to manage.
		resp.Diagnostics.AddWarning(
			"No CircleCI project settings configured",
			fmt.Sprintf(
				"%s for project %s sets no settings, so nothing was written. "+
					"Set at least one setting for this resource to have an effect.",
				projectSettingsTypeName, slug,
			),
		)

		resp.Diagnostics.Append(plan.refresh(ctx, current)...)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

		return
	}

	updated, err := r.client.UpdateProjectSettings(ctx, vcsType, orgName, projectName, settings)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to update CircleCI project settings for "+slug,
			circleci.Detail(err),
		)

		return
	}

	resp.Diagnostics.Append(plan.refresh(ctx, updated)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the managed settings from the API.
func (r *projectSettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		addUnconfiguredClientError(&resp.Diagnostics)

		return
	}

	var state projectSettingsResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	slug := state.Slug.ValueString()

	vcsType, orgName, projectName, diags := parseProjectSlug(slug)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	settings, err := r.client.GetProjectSettings(ctx, vcsType, orgName, projectName)
	if err != nil {
		// The project itself is gone, so there is nothing left to manage.
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read CircleCI project settings for "+slug,
			circleci.Detail(err),
		)

		return
	}

	resp.Diagnostics.Append(state.refresh(ctx, settings)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update writes the configured settings. Every setting is updatable, so unlike
// most resources here this is a real implementation rather than a no-op.
func (r *projectSettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		addUnconfiguredClientError(&resp.Diagnostics)

		return
	}

	var plan, state projectSettingsResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	slug := plan.Slug.ValueString()

	vcsType, orgName, projectName, diags := parseProjectSlug(slug)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Removing a setting from the configuration stops managing it; it does not
	// revert it. There is no route that restores a CircleCI default, so say so
	// rather than leave the practitioner to discover it.
	warnAbandonedProjectSettings(state, plan, slug, &resp.Diagnostics)

	settings, diags := plan.payload(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if settings.IsEmpty() {
		current, err := r.client.GetProjectSettings(ctx, vcsType, orgName, projectName)
		if err != nil {
			resp.Diagnostics.AddError(
				"Unable to read CircleCI project settings for "+slug,
				circleci.Detail(err),
			)

			return
		}

		resp.Diagnostics.Append(plan.refresh(ctx, current)...)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

		return
	}

	updated, err := r.client.UpdateProjectSettings(ctx, vcsType, orgName, projectName, settings)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to update CircleCI project settings for "+slug,
			circleci.Detail(err),
		)

		return
	}

	resp.Diagnostics.Append(plan.refresh(ctx, updated)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the resource from state without calling the API.
//
// This is deliberate and is not a missing implementation. A project's settings
// record cannot be deleted: it exists as long as the project does, and every
// setting has a live value at all times. There is no "unset" route either, so
// the closest thing to destroying this resource is to stop tracking it. The
// project keeps whatever values were last applied. Deleting the project itself
// is what circleci_project is for.
func (r *projectSettingsResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	// No API call. The framework drops the resource from state when Delete
	// returns without error.
	resp.Diagnostics.AddWarning(
		"CircleCI project settings left in place",
		fmt.Sprintf(
			"%s was removed from Terraform state, but CircleCI has no way to delete or reset a "+
				"project's advanced settings, so every setting keeps its current value. Change the "+
				"values explicitly if that is not what you want, or destroy the project itself with "+
				"circleci_project.",
			projectSettingsTypeName,
		),
	)
}

// Configure adds the provider configured client to the resource.
func (r *projectSettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports the settings of an existing project by its slug.
//
// Only slug is set: the settings are left null on purpose, so the first plan
// after an import shows exactly the settings the configuration asks to manage
// instead of every setting the project happens to have.
func (r *projectSettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !projectSlugPattern.MatchString(req.ID) {
		resp.Diagnostics.AddError(
			"Invalid import ID for "+projectSettingsTypeName,
			fmt.Sprintf(
				"Expected a project slug of the form \"vcs-type/org-name/repo-name\", such as "+
					"\"github/acme/repo\", but got %q.",
				req.ID,
			),
		)

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("slug"), req.ID)...)
}

// addUnconfiguredClientError reports a resource whose Configure never ran, which
// can only be a provider bug.
func addUnconfiguredClientError(diags *diag.Diagnostics) {
	diags.AddError(
		"Unconfigured CircleCI Client",
		"Expected a configured CircleCI API client. Please report this issue to the provider developers.",
	)
}

// explicitForkSecretsValidator requires forks_receive_secret_env_vars to be set
// explicitly whenever build_fork_prs is enabled. It is shared by
// circleci_project and circleci_project_settings, which expose the same pair of
// settings.
//
// Leaving an unset setting out of the request is the right Terraform semantic and
// is what both resources now do — but it makes CircleCI's own default apply, and
// forks_receive_secret_env_vars defaults to *true* on a private project
// CircleCI's own default)) in the API's feature registry).
// So a configuration that enables fork builds without mentioning it hands the
// project's secrets to anyone who can open a pull request, silently.
//
// The check is deliberately narrow: CircleCI only exposes secrets to a fork build
// when both settings are on, so it fires only for that combination rather than
// nagging every configuration that omits the setting.
type explicitForkSecretsValidator struct{}

func (explicitForkSecretsValidator) Description(_ context.Context) string {
	return "forks_receive_secret_env_vars must be set explicitly when build_fork_prs is true"
}

func (v explicitForkSecretsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (explicitForkSecretsValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var buildForkPrs, forkSecrets types.Bool

	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("build_fork_prs"), &buildForkPrs)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("forks_receive_secret_env_vars"), &forkSecrets)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Unknown means the value comes from an expression that is not resolved yet.
	// For build_fork_prs there is nothing to check yet; for the secrets toggle it
	// means the practitioner did configure it, which is all this asks for.
	if buildForkPrs.IsNull() || buildForkPrs.IsUnknown() || !buildForkPrs.ValueBool() {
		return
	}

	if !forkSecrets.IsNull() {
		return
	}

	resp.Diagnostics.AddAttributeError(
		path.Root("forks_receive_secret_env_vars"),
		"Set forks_receive_secret_env_vars explicitly when build_fork_prs is true",
		"build_fork_prs is true, so CircleCI will run pull requests opened from forks of this "+
			"repository. Whether those runs receive this project's environment variables, secrets "+
			"and build cache is decided by forks_receive_secret_env_vars, which this configuration "+
			"does not set.\n\n"+
			"There is no safe default to fall back on: CircleCI leaves an unset "+
			"forks_receive_secret_env_vars at true on a private project, so fork pull requests "+
			"would receive the project's secrets and anyone who can open one could read them. On a "+
			"public (open source) project the unset value is false.\n\n"+
			"Set forks_receive_secret_env_vars to false to keep secrets out of fork builds, or to "+
			"true to state that exposing them is intended.",
	)
}

// warnAbandonedProjectSettings reports settings that state managed but the new
// plan no longer sets.
func warnAbandonedProjectSettings(state, plan projectSettingsResourceModel, slug string, diags *diag.Diagnostics) {
	// Ordered so the warning text is stable between runs.
	managed := []struct {
		name       string
		was        bool
		stillWants bool
	}{
		{"auto_cancel_builds", !state.AutoCancelBuilds.IsNull(), !plan.AutoCancelBuilds.IsNull()},
		{"build_fork_prs", !state.BuildForkPrs.IsNull(), !plan.BuildForkPrs.IsNull()},
		{"build_prs_only", !state.BuildPrsOnly.IsNull(), !plan.BuildPrsOnly.IsNull()},
		{"disable_ssh", !state.DisableSSH.IsNull(), !plan.DisableSSH.IsNull()},
		{"forks_receive_secret_env_vars", !state.ForksReceiveSecretEnvVars.IsNull(), !plan.ForksReceiveSecretEnvVars.IsNull()},
		// oss is absent: it is read-only, so it is never managed and can never be
		// abandoned.
		{"pr_only_branch_overrides", !state.PROnlyBranchOverrides.IsNull(), !plan.PROnlyBranchOverrides.IsNull()},
		{"set_github_status", !state.SetGithubStatus.IsNull(), !plan.SetGithubStatus.IsNull()},
		{"setup_workflows", !state.SetupWorkflows.IsNull(), !plan.SetupWorkflows.IsNull()},
		{"write_settings_requires_admin", !state.WriteSettingsRequiresAdmin.IsNull(), !plan.WriteSettingsRequiresAdmin.IsNull()},
	}

	abandoned := make([]string, 0, len(managed))
	for _, setting := range managed {
		if setting.was && !setting.stillWants {
			abandoned = append(abandoned, setting.name)
		}
	}

	if len(abandoned) == 0 {
		return
	}

	diags.AddWarning(
		"CircleCI project settings no longer managed",
		fmt.Sprintf(
			"These settings were removed from the configuration for project %s: %s.\n\n"+
				"CircleCI has no route that reverts a setting to its default, so each keeps the value "+
				"Terraform last applied. Set it explicitly if you need a different value.",
			slug, strings.Join(abandoned, ", "),
		),
	)
}
