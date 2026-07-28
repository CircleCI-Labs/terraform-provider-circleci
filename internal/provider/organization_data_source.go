// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/CircleCI-Public/circleci-sdk-go/organization"
	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource                     = &OrganizationDataSource{}
	_ datasource.DataSourceWithConfigure        = &OrganizationDataSource{}
	_ datasource.DataSourceWithConfigValidators = &OrganizationDataSource{}
)

// organizationDataSourceModel maps the output schema.
type organizationDataSourceModel struct {
	Id      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Slug    types.String `tfsdk:"slug"`
	VcsType types.String `tfsdk:"vcs_type"`
}

// NewOrganizationDataSource is a helper function to simplify the provider implementation.
func NewOrganizationDataSource() datasource.DataSource {
	return &OrganizationDataSource{}
}

// OrganizationDataSource is the data source implementation.
type OrganizationDataSource struct {
	client *organization.OrganizationService
}

// ConfigValidators requires exactly one identifier. The API route segment is
// documented as "org-slug-or-id", so one request serves both lookups.
func (d *OrganizationDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("id"),
			path.MatchRoot("slug"),
		),
	}
}

// Metadata returns the data source type name.
func (d *OrganizationDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization"
}

// Schema defines the schema for the data source.
func (d *OrganizationDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches information about a CircleCI organization.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The unique ID (UUID) of the CircleCI organization. " +
					"Set either this or `slug`.",
				Optional: true,
				Computed: true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the CircleCI organization.",
				Computed:            true,
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "The slug of the CircleCI organization, such as `gh/acme` or " +
					"`circleci/<uuid>`. Set either this or `id`.\n\n" +
					"Looking an organization up by slug is usually the only way to start: every other " +
					"resource in this provider is keyed by organization ID, which is otherwise visible " +
					"only in the CircleCI web application.",
				Optional: true,
				Computed: true,
			},
			"vcs_type": schema.StringAttribute{
				MarkdownDescription: "The VCS type of the CircleCI organization (e.g., github, bitbucket, circleci).",
				Computed:            true,
			},
		},
	}
}

// Read refreshes the Terraform state with the latest data.
func (d *OrganizationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state organizationDataSourceModel
	diags := req.Config.Get(ctx, &state)
	if diags != nil {
		resp.Diagnostics.Append(diags...)
		return
	}

	// The route segment is documented as "org-slug-or-id", so one request serves
	// both lookups.
	identifier := state.Id.ValueString()
	if identifier == "" {
		identifier = state.Slug.ValueString()
	}

	org, err := d.client.Get(ctx, identifier)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Read CircleCI organization "+identifier,
			err.Error(),
		)
		return
	}

	// Map response body to model
	state = organizationDataSourceModel{
		Id:      types.StringValue(org.Id),
		Name:    types.StringValue(org.Name),
		Slug:    types.StringValue(org.Slug),
		VcsType: types.StringValue(org.VcsType),
	}

	// Set state
	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

// Configure adds the provider configured client to the data source.
func (d *OrganizationDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*CircleCiClientWrapper)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *CircleCiClientWrapper, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	d.client = client.OrganizationService
}
