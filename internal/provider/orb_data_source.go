// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource                     = &orbDataSource{}
	_ datasource.DataSourceWithConfigure        = &orbDataSource{}
	_ datasource.DataSourceWithConfigValidators = &orbDataSource{}
)

// orbDataSourceModel maps the data source schema.
type orbDataSourceModel struct {
	Id                     types.String `tfsdk:"id"`
	FullName               types.String `tfsdk:"full_name"`
	Name                   types.String `tfsdk:"name"`
	Namespace              types.String `tfsdk:"namespace"`
	NamespaceId            types.String `tfsdk:"namespace_id"`
	IsPrivate              types.Bool   `tfsdk:"is_private"`
	IsListed               types.Bool   `tfsdk:"is_listed"`
	CreatedAt              types.String `tfsdk:"created_at"`
	HomeUrl                types.String `tfsdk:"home_url"`
	LatestVersion          types.String `tfsdk:"latest_version"`
	LatestVersionCreatedAt types.String `tfsdk:"latest_version_created_at"`
	Last30DaysBuildCount   types.Int64  `tfsdk:"last_30_days_build_count"`
	Last30DaysProjectCount types.Int64  `tfsdk:"last_30_days_project_count"`
	Last30DaysOrgCount     types.Int64  `tfsdk:"last_30_days_org_count"`
	Categories             types.List   `tfsdk:"categories"`
}

// NewOrbDataSource is a helper function to simplify the provider implementation.
func NewOrbDataSource() datasource.DataSource {
	return &orbDataSource{}
}

// orbDataSource is the data source implementation.
type orbDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *orbDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_orb"
}

// Schema defines the schema for the data source.
func (d *orbDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a single CircleCI orb by id or by its fully qualified " +
			"`<namespace>/<orb>` name.\n\n" +
			"~> **CircleCI Cloud only.** Orbs are served by the CircleCI v3 API, which CircleCI " +
			"Server does not route.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the orb. " +
					"Set exactly one of `id` and `full_name`.",
				Optional: true,
				Computed: true,
			},
			"full_name": schema.StringAttribute{
				MarkdownDescription: "The orb's fully qualified name, `<namespace>/<orb>`. " +
					"Set exactly one of `id` and `full_name`.",
				Optional: true,
				Computed: true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The orb name without its namespace prefix.",
				Computed:            true,
			},
			"namespace": schema.StringAttribute{
				MarkdownDescription: "Name of the namespace that owns the orb.",
				Computed:            true,
			},
			"namespace_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the namespace that owns the orb.",
				Computed:            true,
			},
			"is_private": schema.BoolAttribute{
				MarkdownDescription: "Whether the orb is private to its organization.",
				Computed:            true,
			},
			"is_listed": schema.BoolAttribute{
				MarkdownDescription: "Whether the orb appears in the public registry listing.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the orb was created, as an RFC 3339 timestamp.",
				Computed:            true,
			},
			"home_url": schema.StringAttribute{
				MarkdownDescription: "The orb's home page, when one is set.",
				Computed:            true,
			},
			"latest_version": schema.StringAttribute{
				MarkdownDescription: "The most recently published version, or an empty string when " +
					"nothing has been published yet.",
				Computed: true,
			},
			"latest_version_created_at": schema.StringAttribute{
				MarkdownDescription: "When the most recent version was published, as an RFC 3339 timestamp.",
				Computed:            true,
			},
			"last_30_days_build_count": schema.Int64Attribute{
				MarkdownDescription: "Number of builds that used the orb in the last 30 days.",
				Computed:            true,
			},
			"last_30_days_project_count": schema.Int64Attribute{
				MarkdownDescription: "Number of projects that used the orb in the last 30 days.",
				Computed:            true,
			},
			"last_30_days_org_count": schema.Int64Attribute{
				MarkdownDescription: "Number of organizations that used the orb in the last 30 days.",
				Computed:            true,
			},
			"categories": schema.ListNestedAttribute{
				MarkdownDescription: "The registry categories the orb is listed under.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the category.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Name of the category.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// ConfigValidators requires exactly one lookup key.
func (d *orbDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("id"),
			path.MatchRoot("full_name"),
		),
	}
}

// Configure adds the provider configured client to the data source.
func (d *orbDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// Read fetches the orb and sets the data source state.
func (d *orbDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, "circleci_orb", &resp.Diagnostics) {
		return
	}

	var config orbDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var (
		pkg *circleci.OrbPackage
		err error
	)
	if id := config.Id.ValueString(); id != "" {
		pkg, err = d.client.GetOrbPackage(ctx, id)
	} else {
		pkg, err = d.client.GetOrbPackageByName(ctx, config.FullName.ValueString())
	}

	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI orb",
			circleci.Detail(err),
		)

		return
	}

	categories, categoryDiags := orbCategoriesToList(ctx, pkg.Categories)
	resp.Diagnostics.Append(categoryDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state := orbDataSourceModel{
		Id:                     types.StringValue(pkg.ID),
		FullName:               types.StringValue(pkg.Name),
		Name:                   types.StringValue(orbBareName(pkg.Name, pkg.Namespace)),
		Namespace:              types.StringValue(pkg.Namespace),
		NamespaceId:            types.StringValue(pkg.NamespaceID),
		IsPrivate:              types.BoolValue(pkg.IsPrivate),
		IsListed:               types.BoolValue(pkg.IsListed),
		CreatedAt:              types.StringValue(pkg.CreatedAt),
		HomeUrl:                types.StringValue(pkg.HomeURL),
		LatestVersion:          types.StringValue(pkg.LatestVersion),
		LatestVersionCreatedAt: types.StringValue(pkg.LatestVersionCreatedAt),
		Last30DaysBuildCount:   types.Int64Value(pkg.Last30DaysBuildCount),
		Last30DaysProjectCount: types.Int64Value(pkg.Last30DaysProjectCount),
		Last30DaysOrgCount:     types.Int64Value(pkg.Last30DaysOrgCount),
		Categories:             categories,
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
