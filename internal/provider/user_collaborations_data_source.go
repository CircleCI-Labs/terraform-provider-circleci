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
	_ datasource.DataSource              = &userCollaborationsDataSource{}
	_ datasource.DataSourceWithConfigure = &userCollaborationsDataSource{}
)

// userCollaborationsDataSourceModel maps the data source schema.
//
// There is no input attribute at all: the collection is scoped entirely by the
// configured token, so there is nothing for a practitioner to narrow it by.
type userCollaborationsDataSourceModel struct {
	Collaborations []collaborationItemModel `tfsdk:"collaborations"`
}

// collaborationItemModel maps one collaboration in the list.
type collaborationItemModel struct {
	ID        types.String `tfsdk:"id"`
	Slug      types.String `tfsdk:"slug"`
	Name      types.String `tfsdk:"name"`
	VCSType   types.String `tfsdk:"vcs_type"`
	AvatarURL types.String `tfsdk:"avatar_url"`
}

// NewUserCollaborationsDataSource is a helper function to simplify the provider
// implementation.
func NewUserCollaborationsDataSource() datasource.DataSource {
	return &userCollaborationsDataSource{}
}

// userCollaborationsDataSource lists the authenticated user's organizations.
type userCollaborationsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *userCollaborationsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_collaborations"
}

// Schema defines the schema for the data source.
func (d *userCollaborationsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the organizations the configured API token can collaborate on, with the " +
			"CircleCI ID and slug of each.\n\n" +
			"This is the discovery counterpart to the rest of the provider. Almost every CircleCI resource is " +
			"keyed by `organization_id`, a UUID that is otherwise only visible in the web UI, and the " +
			"Insights data sources need an organization slug. This is the one way to obtain either from " +
			"Terraform.\n\n" +
			"The set is broader than \"organizations I am a member of\". It also includes the owner of any " +
			"repository the token's user collaborates on without belonging to the organization, and the " +
			"user's own personal account. Filter on `vcs_type` when only standalone (`circleci`) " +
			"organizations are wanted.\n\n" +
			"~> **A user token is required.** The route reports the collaborations of the authenticated " +
			"user, so a project or organization token is rejected.",
		Attributes: map[string]schema.Attribute{
			"collaborations": schema.ListNestedAttribute{
				MarkdownDescription: "The organizations the token can collaborate on. The API returns them " +
					"sorted by name, then by VCS type.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the organization, and the value " +
								"other resources take as `organization_id`.\n\n" +
								"This is `null` for an organization CircleCI does not know yet. " +
								"Collaborations are assembled partly from the VCS, so an organization that " +
								"exists on GitHub or Bitbucket but has never been used on CircleCI appears here " +
								"without an ID. Such an organization cannot be referenced by other resources " +
								"until it has been onboarded.",
							Computed: true,
						},
						"slug": schema.StringAttribute{
							MarkdownDescription: "Slug of the organization, for example `gh/acme` or " +
								"`circleci/<org-uuid>`. This is the form the Insights data sources take.",
							Computed: true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Name of the organization.",
							Computed:            true,
						},
						"vcs_type": schema.StringAttribute{
							MarkdownDescription: "The VCS backing the organization: `circleci` for a standalone " +
								"organization, otherwise `github` or `bitbucket`.",
							Computed: true,
						},
						"avatar_url": schema.StringAttribute{
							MarkdownDescription: "URL of the organization's avatar on its VCS.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// Read lists the authenticated user's collaborations.
func (d *userCollaborationsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		return
	}

	var state userCollaborationsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	collaborations, err := d.client.Users().ListCollaborations(ctx)
	if err != nil {
		detail := circleci.Detail(err)
		if circleci.IsUnauthorized(err) {
			detail += "\n\n/me/collaborations reports the organizations of the authenticated user, so it " +
				"requires a personal API token. A project or organization token cannot read it."
		}

		resp.Diagnostics.AddError("Unable to list CircleCI collaborations for the configured token", detail)

		return
	}

	// An empty, non-null list keeps `for_each` and `length()` working for a token
	// whose user belongs to nothing.
	state.Collaborations = make([]collaborationItemModel, 0, len(collaborations))
	for _, collaboration := range collaborations {
		state.Collaborations = append(state.Collaborations, collaborationItemModel{
			// StringPointerValue rather than StringValue: a nil id must reach
			// Terraform as null, not as "", so that `id != null` is a usable test for
			// "this organization is onboarded onto CircleCI".
			ID:        types.StringPointerValue(collaboration.ID),
			Slug:      types.StringValue(collaboration.Slug),
			Name:      types.StringValue(collaboration.Name),
			VCSType:   types.StringValue(collaboration.VCSType),
			AvatarURL: types.StringValue(collaboration.AvatarURL),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *userCollaborationsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
