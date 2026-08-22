// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource                     = &githubAppRepositoryDataSource{}
	_ datasource.DataSourceWithConfigure        = &githubAppRepositoryDataSource{}
	_ datasource.DataSourceWithConfigValidators = &githubAppRepositoryDataSource{}
)

// Type names, used in diagnostics.
const (
	githubAppRepositoryTypeName   = "circleci_github_app_repository"
	githubAppRepositoriesTypeName = "circleci_github_app_repositories"
)

// githubAppRepositoryUnpublishedAPINote is the caveat every GitHub App data
// source repeats. The route is real and stable enough to depend on today, but it
// is deliberately excluded from CircleCI's published API surface, and a
// practitioner deserves to know that before writing it into a module.
const githubAppRepositoryUnpublishedAPINote = "~> **This data source reads an unpublished CircleCI API.** " +
	"`GET /api/v2/github-app/organization/{org_id}/repositories` is marked internal in CircleCI's own API " +
	"definitions and is excluded from the published OpenAPI spec and API reference, so it carries no " +
	"compatibility guarantee and may change or be withdrawn without notice. It is used here because there is " +
	"no published way to resolve a repository name to the numeric external ID that `circleci_pipeline` and " +
	"`circleci_trigger` require."

// githubAppRepositoryDataSourceModel maps the data source schema.
type githubAppRepositoryDataSourceModel struct {
	OrganizationID types.String `tfsdk:"organization_id"`
	OrgID          types.String `tfsdk:"org_id"`
	FullName       types.String `tfsdk:"full_name"`
	ExternalID     types.String `tfsdk:"external_id"`
	ID             types.Int64  `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	Owner          types.String `tfsdk:"owner"`
	DefaultBranch  types.String `tfsdk:"default_branch"`
	Private        types.Bool   `tfsdk:"private"`
}

// NewGitHubAppRepositoryDataSource is a helper function to simplify the provider
// implementation.
func NewGitHubAppRepositoryDataSource() datasource.DataSource {
	return &githubAppRepositoryDataSource{}
}

// githubAppRepositoryDataSource resolves one GitHub App repository by name.
type githubAppRepositoryDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *githubAppRepositoryDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_github_app_repository"
}

// Schema defines the schema for the data source.
func (d *githubAppRepositoryDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Resolves a repository reachable through the CircleCI GitHub App to its numeric " +
			"GitHub ID.\n\n" +
			"`circleci_pipeline` and `circleci_trigger` both require that ID — as " +
			"`config_source_repo_external_id`, `checkout_source_repo_external_id` and " +
			"`event_source_repo_external_id` — and it is not otherwise obtainable from Terraform, so it " +
			"tends to be copied out of the GitHub UI by hand. Use `external_id` from this data source " +
			"instead.\n\n" +
			"There is deliberately no matching resource. A GitHub App installation, and the set of " +
			"repositories it can reach, live on GitHub's side of the integration and cannot be created, " +
			"changed or removed through CircleCI's API.\n\n" +
			githubAppRepositoryUnpublishedAPINote,
		Attributes: map[string]schema.Attribute{
			// See org_id_deprecation.go for why the organization is accepted under
			// two names.
			"organization_id": deprecatedOrgIDDataSourceAttribute("GitHub App repositories"),
			"org_id":          orgIDDataSourceAttribute("GitHub App repositories"),
			"full_name": schema.StringAttribute{
				MarkdownDescription: "Fully-qualified repository name, in `owner/repo` form. The comparison is " +
					"case-insensitive, matching how GitHub treats owner and repository names.",
				Required: true,
			},
			"external_id": schema.StringAttribute{
				MarkdownDescription: "The repository's numeric GitHub ID as a string. This is the value to pass " +
					"to `circleci_pipeline` and `circleci_trigger`, whose external ID arguments are strings.",
				Computed: true,
			},
			"id": schema.Int64Attribute{
				MarkdownDescription: "The repository's numeric GitHub ID as a number, for arithmetic or " +
					"comparison. `external_id` carries the same value in the string form the pipeline and " +
					"trigger resources accept.",
				Computed: true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Repository name without its owner.",
				Computed:            true,
			},
			"owner": schema.StringAttribute{
				MarkdownDescription: "GitHub account that owns the repository.",
				Computed:            true,
			},
			"default_branch": schema.StringAttribute{
				MarkdownDescription: "The repository's default branch on GitHub.",
				Computed:            true,
			},
			"private": schema.BoolAttribute{
				MarkdownDescription: "Whether the repository is private on GitHub.",
				Computed:            true,
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (d *githubAppRepositoryDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		orgIDDataSourceConfigValidator(),
	}
}

// Read resolves the repository.
func (d *githubAppRepositoryDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	// The GitHub App integration only exists for `circleci` type (standalone)
	// organizations, and a CircleCI Server installation is always a `github` type
	// organization, so Server can never have an installation to report. Saying that
	// outright beats the HTTP 404 the request would otherwise produce, which is
	// indistinguishable from a repository that simply is not granted.
	if d.client == nil || !requireStandaloneCapable(d.client, githubAppRepositoryTypeName, &resp.Diagnostics) {
		return
	}

	var state githubAppRepositoryDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(state.OrganizationID, state.OrgID)
	fullName := state.FullName.ValueString()

	repository, err := d.client.GitHubApp().FindRepository(ctx, organizationID, fullName)
	if err != nil {
		// FindRepository's two failure shapes are NOT the same situation, even
		// though both satisfy circleci.IsNotFound, and conflating them used to
		// produce a misleading diagnostic for the second one:
		//
		//   - errors.Is(err, circleci.ErrNotFound): the listing itself succeeded —
		//     an installation exists — but no repository in it matched fullName. A
		//     miss here is much more likely to be a repository outside the
		//     installation's scope than a typo.
		//   - circleci.IsNotFound(err) without the sentinel: the listing itself
		//     failed with an HTTP 404, which [NET, reproduced against the live API
		//     on 2026-08-21] is what an organization with no GitHub App
		//     installation at all gets back (see
		//     circleci.GitHubAppService.ListRepositories). Reporting this the same
		//     way as the first case — "check the name, or widen the installation on
		//     GitHub" — tells a practitioner on an OAuth-only or GitLab organization
		//     to fix something that was never there to begin with.
		if errors.Is(err, circleci.ErrNotFound) {
			resp.Diagnostics.AddError(
				"No GitHub App repository named "+fullName,
				"The CircleCI GitHub App installation for organization "+organizationID+" does not report a "+
					"repository with that full name.\n\n"+
					"Either the name is wrong, or the installation is not granted access to the repository. "+
					"An installation configured for selected repositories only reports the ones it was granted, "+
					"so a repository the organization owns can still be invisible here. Use the "+
					"`circleci_github_app_repositories` data source to list what the installation can see, and "+
					"grant access on GitHub if the repository is missing from that list.",
			)

			return
		}

		if circleci.IsNotFound(err) {
			resp.Diagnostics.AddError(
				"No GitHub App installation for organization "+organizationID,
				"Listing GitHub App repositories answered \"not found\" for this organization, which means "+
					"either the CircleCI GitHub App is not installed here, or the organization id is wrong — "+
					"this is not about "+fullName+" specifically. Check circleci_github_app_installation to "+
					"tell those apart, or install the app from the organization's VCS integration settings "+
					"in the CircleCI web app.\n\n"+
					"Underlying error: "+circleci.Detail(err),
			)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to list GitHub App repositories for organization "+organizationID,
			circleci.Detail(err),
		)

		return
	}

	// full_name is deliberately left exactly as configured. Overwriting it with the
	// API's spelling would change a value Terraform read from configuration, which
	// it reports as an inconsistent result; owner and name below carry the API's
	// own casing for anyone who needs it.
	state.ExternalID = types.StringValue(repository.ExternalID())
	state.ID = types.Int64Value(repository.ID)
	state.Name = types.StringValue(repository.Name)
	state.Owner = types.StringValue(repository.Owner)
	state.DefaultBranch = types.StringValue(repository.DefaultBranch)
	state.Private = types.BoolValue(repository.Private)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *githubAppRepositoryDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
