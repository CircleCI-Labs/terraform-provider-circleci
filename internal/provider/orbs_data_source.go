// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &orbsDataSource{}
	_ datasource.DataSourceWithConfigure = &orbsDataSource{}
)

// orbSummaryModel is one entry of the orbs list.
//
// It carries only what the /orb/packages collection returns. The collection's
// records are thinner than the by-id route's: there is no created_at, home_url or
// usage count, and the namespace reference has an id but no name. Use the
// `circleci_orb` data source for those.
type orbSummaryModel struct {
	Id                     types.String `tfsdk:"id"`
	FullName               types.String `tfsdk:"full_name"`
	Name                   types.String `tfsdk:"name"`
	Namespace              types.String `tfsdk:"namespace"`
	NamespaceId            types.String `tfsdk:"namespace_id"`
	IsPrivate              types.Bool   `tfsdk:"is_private"`
	IsListed               types.Bool   `tfsdk:"is_listed"`
	LatestVersion          types.String `tfsdk:"latest_version"`
	LatestVersionCreatedAt types.String `tfsdk:"latest_version_created_at"`
	Categories             types.List   `tfsdk:"categories"`
}

// orbSummaryObjectType is the object type of an orbSummaryModel.
var orbSummaryObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"id":                        types.StringType,
	"full_name":                 types.StringType,
	"name":                      types.StringType,
	"namespace":                 types.StringType,
	"namespace_id":              types.StringType,
	"is_private":                types.BoolType,
	"is_listed":                 types.BoolType,
	"latest_version":            types.StringType,
	"latest_version_created_at": types.StringType,
	"categories":                types.ListType{ElemType: orbCategoryObjectType},
}}

// orbsDataSourceModel maps the data source schema.
type orbsDataSourceModel struct {
	NamespaceId types.String `tfsdk:"namespace_id"`
	Name        types.String `tfsdk:"name"`
	Certified   types.Bool   `tfsdk:"certified"`
	Visibility  types.String `tfsdk:"visibility"`
	Orbs        types.List   `tfsdk:"orbs"`
}

// NewOrbsDataSource is a helper function to simplify the provider implementation.
func NewOrbsDataSource() datasource.DataSource {
	return &orbsDataSource{}
}

// orbsDataSource is the data source implementation.
type orbsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *orbsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_orbs"
}

// Schema defines the schema for the data source.
func (d *orbsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists CircleCI orbs, optionally scoped to a namespace, a name, a " +
			"visibility or the certified set. Every page of results is fetched.\n\n" +
			"~> **The filters are not additive, and the API's precedence is not obvious.** `name` " +
			"overrides everything else; `visibility` is read only alongside `namespace_id`, where " +
			"it selects public-only or private-only rather than widening the set; `certified` is " +
			"read only when neither `name` nor `namespace_id` is set. Each attribute below says so " +
			"for itself.\n\n" +
			"~> **CircleCI Cloud only.** Orbs are served by the CircleCI v3 API, which CircleCI " +
			"Server does not route.",
		Attributes: map[string]schema.Attribute{
			"namespace_id": schema.StringAttribute{
				MarkdownDescription: "Only return orbs in this namespace (UUID).",
				Optional:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Only return orbs matching this fully qualified " +
					"`<namespace>/<orb>` name.\n\n" +
					"This is an exact lookup that the API serves on its own, so setting it makes " +
					"`namespace_id`, `certified` and `visibility` have no effect. It does find a " +
					"private orb without being asked to.",
				Optional: true,
			},
			"certified": schema.BoolAttribute{
				MarkdownDescription: "Set to `true` to return only CircleCI-certified orbs.\n\n" +
					"The API reads `false` as \"no certification filter\", exactly as if the " +
					"attribute were omitted, so it cannot be used to *exclude* certified orbs. It " +
					"is also ignored when `namespace_id` or `name` is set.",
				Optional: true,
			},
			"visibility": schema.StringAttribute{
				MarkdownDescription: "Restrict the listing to `public` or `private` orbs.\n\n" +
					"~> This is only honoured together with `namespace_id`, and it *selects* one " +
					"set rather than widening the other: a namespace listing returns public orbs " +
					"only, unless `visibility = \"private\"` makes it return private orbs only. " +
					"There is no single request that returns both. Set without `namespace_id` it " +
					"has no effect at all.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.OneOf(circleci.OrbVisibilityPublic, circleci.OrbVisibilityPrivate),
				},
			},
			"orbs": schema.ListNestedAttribute{
				MarkdownDescription: "The matching orbs.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the orb.",
							Computed:            true,
						},
						"full_name": schema.StringAttribute{
							MarkdownDescription: "The orb's fully qualified name, `<namespace>/<orb>`.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The orb name without its namespace prefix.",
							Computed:            true,
						},
						"namespace": schema.StringAttribute{
							MarkdownDescription: "Name of the namespace that owns the orb, taken from " +
								"the qualified name because the collection returns only a namespace id.",
							Computed: true,
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
						"latest_version": schema.StringAttribute{
							MarkdownDescription: "The most recently published version, or an empty " +
								"string when nothing has been published yet.",
							Computed: true,
						},
						"latest_version_created_at": schema.StringAttribute{
							MarkdownDescription: "When the most recent version was published, as an " +
								"RFC 3339 timestamp.",
							Computed: true,
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
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *orbsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// Read lists the orbs and sets the data source state.
func (d *orbsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, "circleci_orbs", &resp.Diagnostics) {
		return
	}

	var config orbsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := circleci.ListOrbPackagesOptions{
		NamespaceID: config.NamespaceId.ValueString(),
		Name:        config.Name.ValueString(),
		Visibility:  config.Visibility.ValueString(),
	}
	if !config.Certified.IsNull() && !config.Certified.IsUnknown() {
		// Sent as configured even though the API treats false as "no filter", so
		// that the request reflects the configuration rather than the client's
		// opinion of it. See ListOrbPackagesOptions.Certified.
		certified := config.Certified.ValueBool()
		opts.Certified = &certified
	}

	pkgs, err := d.client.ListOrbPackages(ctx, opts)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI orbs",
			circleci.Detail(err),
		)

		return
	}

	summaries := make([]orbSummaryModel, 0, len(pkgs))
	for _, pkg := range pkgs {
		categories, categoryDiags := orbCategoriesToList(ctx, pkg.Categories)
		resp.Diagnostics.Append(categoryDiags...)
		if resp.Diagnostics.HasError() {
			return
		}

		namespace := pkg.Namespace
		if namespace == "" {
			namespace, _, _ = strings.Cut(pkg.Name, "/")
		}

		summaries = append(summaries, orbSummaryModel{
			Id:                     types.StringValue(pkg.ID),
			FullName:               types.StringValue(pkg.Name),
			Name:                   types.StringValue(orbBareName(pkg.Name, namespace)),
			Namespace:              types.StringValue(namespace),
			NamespaceId:            types.StringValue(pkg.NamespaceID),
			IsPrivate:              types.BoolValue(pkg.IsPrivate),
			IsListed:               types.BoolValue(pkg.IsListed),
			LatestVersion:          types.StringValue(pkg.LatestVersion),
			LatestVersionCreatedAt: types.StringValue(pkg.LatestVersionCreatedAt),
			Categories:             categories,
		})
	}

	orbs, listDiags := types.ListValueFrom(ctx, orbSummaryObjectType, summaries)
	resp.Diagnostics.Append(listDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	config.Orbs = orbs

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
