// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &githubAppInstallationDataSource{}
	_ datasource.DataSourceWithConfigure = &githubAppInstallationDataSource{}
)

// githubAppInstallationTypeName is used in diagnostics.
const githubAppInstallationTypeName = "circleci_github_app_installation"

// githubAppInstallationDataSourceModel maps the data source schema.
type githubAppInstallationDataSourceModel struct {
	OrganizationID      types.String `tfsdk:"organization_id"`
	ID                  types.Int64  `tfsdk:"id"`
	TargetType          types.String `tfsdk:"target_type"`
	Login               types.String `tfsdk:"login"`
	RepositorySelection types.String `tfsdk:"repository_selection"`
}

// NewGitHubAppInstallationDataSource is a helper function to simplify the
// provider implementation.
func NewGitHubAppInstallationDataSource() datasource.DataSource {
	return &githubAppInstallationDataSource{}
}

// githubAppInstallationDataSource reports whether, and how, the CircleCI
// GitHub App is installed for an organization.
type githubAppInstallationDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *githubAppInstallationDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_github_app_installation"
}

// Schema defines the schema for the data source.
func (d *githubAppInstallationDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reports the CircleCI GitHub App installation for an organization: whether it is " +
			"installed at all, and if so, which GitHub account it is attached to and how much of it the " +
			"installation can reach.\n\n" +
			"This is the precondition for `circleci_github_app_repository` and " +
			"`circleci_github_app_repositories`: both of those answer with an empty result whether the app " +
			"is not installed at all, or is installed but was not granted the repository asked for, and this " +
			"data source is the only way to tell those two situations apart.\n\n" +
			"There is deliberately no matching resource. A GitHub App installation is created through a " +
			"browser consent flow on GitHub's side of the integration, not through this API — CircleCI's own " +
			"install route only hands back a redirect URL for a human to open.\n\n" +
			githubAppRepositoryUnpublishedAPINote,
		Attributes: map[string]schema.Attribute{
			"organization_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the CircleCI organization whose GitHub App " +
					"installation is looked up.",
				Required: true,
			},
			"id": schema.Int64Attribute{
				MarkdownDescription: "The GitHub App installation's own numeric id. This identifies the " +
					"installation itself, not any repository it can reach.",
				Computed: true,
			},
			"target_type": schema.StringAttribute{
				MarkdownDescription: "The kind of GitHub account the app is installed on: `Organization` or `User`.",
				Computed:            true,
			},
			"login": schema.StringAttribute{
				MarkdownDescription: "The GitHub account login (organization or user) the installation belongs to.",
				Computed:            true,
			},
			"repository_selection": schema.StringAttribute{
				MarkdownDescription: "`all` when the installation can reach every repository the GitHub account " +
					"owns, or `selected` when it is limited to a chosen subset. Either way, " +
					"`circleci_github_app_repositories` reports only the repositories actually reachable.",
				Computed: true,
			},
		},
	}
}

// Read resolves the installation.
func (d *githubAppInstallationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	// The GitHub App integration only exists for `circleci` type (standalone)
	// organizations, and a CircleCI Server installation is always a `github` type
	// organization, so Server can never have an installation to report.
	if d.client == nil || !requireStandaloneCapable(d.client, githubAppInstallationTypeName, &resp.Diagnostics) {
		return
	}

	var state githubAppInstallationDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := state.OrganizationID.ValueString()

	installation, err := d.client.GitHubApp().GetInstallation(ctx, organizationID)
	if err != nil {
		if circleci.IsNotFound(err) {
			resp.Diagnostics.AddError(
				"No GitHub App installation for organization "+organizationID,
				"The CircleCI GitHub App is not installed for this organization. Install it from the "+
					"organization's VCS integration settings in the CircleCI web app; there is no API to "+
					"create the installation itself.",
			)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read the CircleCI GitHub App installation for organization "+organizationID,
			circleci.Detail(err),
		)

		return
	}

	state.ID = types.Int64Value(installation.ID)
	state.TargetType = types.StringValue(installation.TargetType)
	state.Login = types.StringValue(installation.Login)
	state.RepositorySelection = types.StringValue(installation.RepositorySelection)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *githubAppInstallationDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
