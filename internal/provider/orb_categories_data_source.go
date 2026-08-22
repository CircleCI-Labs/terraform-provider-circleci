// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &orbCategoriesDataSource{}
	_ datasource.DataSourceWithConfigure = &orbCategoriesDataSource{}
)

// orbCategoriesDataSourceModel maps the data source schema.
type orbCategoriesDataSourceModel struct {
	Categories types.List `tfsdk:"categories"`
	IdsByName  types.Map  `tfsdk:"ids_by_name"`
}

// NewOrbCategoriesDataSource is a helper function to simplify the provider implementation.
func NewOrbCategoriesDataSource() datasource.DataSource {
	return &orbCategoriesDataSource{}
}

// orbCategoriesDataSource is the data source implementation.
type orbCategoriesDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *orbCategoriesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_orb_categories"
}

// Schema defines the schema for the data source.
func (d *orbCategoriesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the CircleCI orb registry categories, such as `Build` or " +
			"`Notifications`. The set is fixed by CircleCI.\n\n" +
			"`circleci_orb.category_ids` takes category ids, so this data source is how you turn " +
			"a category name into the id to set: `data.circleci_orb_categories.all.ids_by_name[\"Build\"]`.\n\n" +
			"~> **CircleCI Cloud only.** Orb categories are served by the CircleCI v3 API, which " +
			"CircleCI Server does not route.",
		Attributes: map[string]schema.Attribute{
			"categories": schema.ListNestedAttribute{
				MarkdownDescription: "Every registry category, sorted by name. The API's own " +
					"order is not part of its contract and is not what this returns: it is sorted " +
					"here (via the same orbCategoriesToList used by circleci_orb) so the value is " +
					"stable across reads instead of following the API's ordering.",
				Computed: true,
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
			"ids_by_name": schema.MapAttribute{
				MarkdownDescription: "Category ids keyed by category name, for looking up the value " +
					"to put in `circleci_orb.category_ids`.",
				Computed:    true,
				ElementType: types.StringType,
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *orbCategoriesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// Read lists the categories and sets the data source state.
func (d *orbCategoriesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, "circleci_orb_categories", &resp.Diagnostics) {
		return
	}

	categories, err := d.client.ListOrbCategories(ctx)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI orb categories",
			circleci.Detail(err),
		)

		return
	}

	list, listDiags := orbCategoriesToList(ctx, categories)
	resp.Diagnostics.Append(listDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	byName := make(map[string]attr.Value, len(categories))
	for _, category := range categories {
		byName[category.Name] = types.StringValue(category.ID)
	}

	idsByName, mapDiags := types.MapValue(types.StringType, byName)
	resp.Diagnostics.Append(mapDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state := orbCategoriesDataSourceModel{
		Categories: list,
		IdsByName:  idsByName,
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
