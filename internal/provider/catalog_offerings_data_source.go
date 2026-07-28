// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &catalogOfferingsDataSource{}
	_ datasource.DataSourceWithConfigure = &catalogOfferingsDataSource{}
)

// catalogOfferingsTypeName is the data source's type name, used in diagnostics.
const catalogOfferingsTypeName = "circleci_catalog_offerings"

// catalogOfferingsDataSourceModel maps the data source schema.
//
// Each platform is a map from resource class name to the list of machine images
// available on it, which is exactly the API's own shape. Modelling it as a map
// rather than as a list of objects means a configuration can index it directly —
// `lookup(data.circleci_catalog_offerings.this.linux, var.resource_class, null)`
// — which is the check this data source exists to make possible.
type catalogOfferingsDataSourceModel struct {
	Linux           types.Map  `tfsdk:"linux"`
	Windows         types.Map  `tfsdk:"windows"`
	MacOS           types.Map  `tfsdk:"macos"`
	Deprecated      types.Map  `tfsdk:"deprecated"`
	ResourceClasses types.List `tfsdk:"resource_classes"`
}

// NewCatalogOfferingsDataSource is a helper function to simplify the provider
// implementation.
func NewCatalogOfferingsDataSource() datasource.DataSource {
	return &catalogOfferingsDataSource{}
}

// catalogOfferingsDataSource reads the execution catalog.
type catalogOfferingsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *catalogOfferingsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_catalog_offerings"
}

// Schema defines the schema for the data source.
func (d *catalogOfferingsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	platform := func(description string) schema.MapAttribute {
		return schema.MapAttribute{
			MarkdownDescription: description,
			Computed:            true,
			ElementType:         types.ListType{ElemType: types.StringType},
		}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches CircleCI's execution catalog: the resource classes jobs can run on, and " +
			"the machine images available on each.\n\n" +
			"The practical use is validating a `resource_class` string before an apply, rather than " +
			"discovering at job runtime that the class does not exist or is not available to the " +
			"organization. Because each platform is a map keyed by resource class name, a configuration can " +
			"check one directly:\n\n" +
			"```terraform\n" +
			"lifecycle {\n" +
			"  precondition {\n" +
			"    condition     = contains(data.circleci_catalog_offerings.this.resource_classes, var.resource_class)\n" +
			"    error_message = \"${var.resource_class} is not an available CircleCI resource class.\"\n" +
			"  }\n" +
			"}\n" +
			"```\n\n" +
			"**CircleCI Cloud only.** The catalog is served by the CircleCI v3 API, which CircleCI Server " +
			"does not route. Using this data source with `deployment = \"server\"` reports an error.\n\n" +
			"~> **The catalog reflects the token's organization entitlements, not a universal list.** A " +
			"resource class present for one organization may be absent for another, so a check against this " +
			"data source is only meaningful when the provider is configured with a token for the " +
			"organization the jobs will run in.",
		Attributes: map[string]schema.Attribute{
			"linux": platform(
				"Linux resource classes, mapped to the machine images available on each. Docker jobs draw " +
					"their resource classes from here too.",
			),
			"windows": platform(
				"Windows resource classes, mapped to the machine images available on each. Empty when the " +
					"organization has no Windows entitlement.",
			),
			"macos": platform(
				"macOS resource classes, mapped to the machine images available on each. Empty when the " +
					"organization has no macOS entitlement.",
			),
			"deprecated": platform(
				"Resource classes that still run but are scheduled for removal, mapped to their remaining " +
					"images. An organization with `enable_resource_class_brownouts` set will see jobs on these " +
					"classes fail during a brownout window, so treat anything listed here as needing " +
					"migration.",
			),
			"resource_classes": schema.ListAttribute{
				MarkdownDescription: "Every non-deprecated resource class name across all three platforms, " +
					"sorted and deduplicated. This is the flat list to validate a `resource_class` string " +
					"against with `contains()`. Deprecated-only classes are excluded deliberately, so a check " +
					"against this list does not pass a class that is on its way out.",
				Computed:    true,
				ElementType: types.StringType,
			},
		},
	}
}

// Read fetches the execution catalog.
func (d *catalogOfferingsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil || !requireCloud(d.client, catalogOfferingsTypeName, &resp.Diagnostics) {
		return
	}

	var state catalogOfferingsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	offerings, err := d.client.GetCatalogOfferings(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the CircleCI execution catalog", circleci.Detail(err))

		return
	}

	state.Linux = catalogPlatformMap(ctx, offerings.Linux, &resp.Diagnostics)
	state.Windows = catalogPlatformMap(ctx, offerings.Windows, &resp.Diagnostics)
	state.MacOS = catalogPlatformMap(ctx, offerings.MacOS, &resp.Diagnostics)
	state.Deprecated = catalogPlatformMap(ctx, offerings.Deprecated, &resp.Diagnostics)

	classes, diags := types.ListValueFrom(ctx, types.StringType, offerings.ResourceClasses())
	resp.Diagnostics.Append(diags...)
	state.ResourceClasses = classes

	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// catalogPlatformMap converts one platform's resource classes into a Terraform
// map of lists.
//
// A nil map becomes an empty map rather than null, so that `lookup()`, `keys()`
// and `for_each` keep working for a platform the organization is not entitled to.
// The API sends {} for an unentitled platform, but a defensive empty here also
// covers a response that omits the key.
func catalogPlatformMap(ctx context.Context, classes map[string][]string, diags *diag.Diagnostics) types.Map {
	if classes == nil {
		classes = map[string][]string{}
	}

	value, valueDiags := types.MapValueFrom(ctx, types.ListType{ElemType: types.StringType}, classes)
	diags.Append(valueDiags...)

	return value
}

// Configure adds the provider configured client to the data source.
func (d *catalogOfferingsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
