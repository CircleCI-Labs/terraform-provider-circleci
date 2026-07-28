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
	_ datasource.DataSource              = &githubAppRepositoriesDataSource{}
	_ datasource.DataSourceWithConfigure = &githubAppRepositoriesDataSource{}
)

// githubAppRepositoriesDataSourceModel maps the data source schema.
//
// The shape follows the provider's convention for plural data sources: the scope
// the collection is listed under is the input, and the collection itself is a
// single list-nested attribute named after the entity.
type githubAppRepositoriesDataSourceModel struct {
	OrganizationID types.String                   `tfsdk:"organization_id"`
	Repositories   []githubAppRepositoryItemModel `tfsdk:"repositories"`
}

// githubAppRepositoryItemModel maps one repository in the list.
type githubAppRepositoryItemModel struct {
	ID            types.Int64  `tfsdk:"id"`
	ExternalID    types.String `tfsdk:"external_id"`
	FullName      types.String `tfsdk:"full_name"`
	Name          types.String `tfsdk:"name"`
	Owner         types.String `tfsdk:"owner"`
	DefaultBranch types.String `tfsdk:"default_branch"`
	Private       types.Bool   `tfsdk:"private"`
}

// NewGitHubAppRepositoriesDataSource is a helper function to simplify the provider
// implementation.
func NewGitHubAppRepositoriesDataSource() datasource.DataSource {
	return &githubAppRepositoriesDataSource{}
}

// githubAppRepositoriesDataSource lists the GitHub App's repositories.
type githubAppRepositoriesDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *githubAppRepositoriesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_github_app_repositories"
}

// Schema defines the schema for the data source.
func (d *githubAppRepositoriesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists every repository the CircleCI GitHub App can access for an organization, " +
			"with the numeric GitHub ID of each.\n\n" +
			"Use this to drive `for_each` over the repositories CircleCI can see, or to discover what an " +
			"installation is scoped to when `circleci_github_app_repository` cannot find a name. Pagination " +
			"is followed internally, so the result covers every repository rather than one page.\n\n" +
			"Note that an installation configured for selected repositories only reports the repositories it " +
			"was granted. A repository the organization owns but has not granted the app is absent here, and " +
			"the only fix is to widen the installation on GitHub.\n\n" +
			githubAppRepositoryUnpublishedAPINote,
		Attributes: map[string]schema.Attribute{
			"organization_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the CircleCI organization whose GitHub App " +
					"repositories are listed.",
				Required: true,
			},
			"repositories": schema.ListNestedAttribute{
				MarkdownDescription: "The repositories the GitHub App can access, in the order the API returns " +
					"them.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.Int64Attribute{
							MarkdownDescription: "The repository's numeric GitHub ID as a number.",
							Computed:            true,
						},
						"external_id": schema.StringAttribute{
							MarkdownDescription: "The repository's numeric GitHub ID as a string, ready to pass " +
								"to `circleci_pipeline` or `circleci_trigger`.",
							Computed: true,
						},
						"full_name": schema.StringAttribute{
							MarkdownDescription: "Fully-qualified repository name, in `owner/repo` form.",
							Computed:            true,
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
				},
			},
		},
	}
}

// Read lists the organization's GitHub App repositories.
func (d *githubAppRepositoriesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	// See the note on the singular data source: a GitHub App installation requires a
	// standalone organization, which a CircleCI Server installation never is.
	if d.client == nil || !requireStandaloneCapable(d.client, githubAppRepositoriesTypeName, &resp.Diagnostics) {
		return
	}

	var state githubAppRepositoriesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := state.OrganizationID.ValueString()

	repositories, err := d.client.GitHubApp().ListRepositories(ctx, organizationID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list GitHub App repositories for organization "+organizationID,
			circleci.Detail(err),
		)

		return
	}

	// An empty, non-null list keeps `for_each` and `length()` working against an
	// organization with no GitHub App installation. Note that no installation and
	// an installation granted nothing are indistinguishable here: the API answers
	// 200 with an empty items array for both.
	state.Repositories = make([]githubAppRepositoryItemModel, 0, len(repositories))
	for _, repository := range repositories {
		state.Repositories = append(state.Repositories, githubAppRepositoryItemModel{
			ID:            types.Int64Value(repository.ID),
			ExternalID:    types.StringValue(repository.ExternalID()),
			FullName:      types.StringValue(repository.FullName),
			Name:          types.StringValue(repository.Name),
			Owner:         types.StringValue(repository.Owner),
			DefaultBranch: types.StringValue(repository.DefaultBranch),
			Private:       types.BoolValue(repository.Private),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *githubAppRepositoriesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
