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
	_ datasource.DataSource                     = &orbNamespaceDataSource{}
	_ datasource.DataSourceWithConfigure        = &orbNamespaceDataSource{}
	_ datasource.DataSourceWithConfigValidators = &orbNamespaceDataSource{}
)

// orbNamespaceDataSourceModel maps the data source schema.
type orbNamespaceDataSourceModel struct {
	Id   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
}

// NewOrbNamespaceDataSource is a helper function to simplify the provider implementation.
func NewOrbNamespaceDataSource() datasource.DataSource {
	return &orbNamespaceDataSource{}
}

// orbNamespaceDataSource is the data source implementation.
type orbNamespaceDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *orbNamespaceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_orb_namespace"
}

// Schema defines the schema for the data source.
func (d *orbNamespaceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a CircleCI orb registry namespace by name or by id.\n\n" +
			"This is the usual way to get the `namespace_id` a `circleci_orb` needs for a " +
			"namespace this configuration does not manage.\n\n" +
			"~> **CircleCI Cloud only.** Namespaces are served by the CircleCI v3 API, which " +
			"CircleCI Server does not route.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the namespace. " +
					"Set exactly one of `id` and `name`.",
				Optional: true,
				Computed: true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The namespace name. Set exactly one of `id` and `name`.",
				Optional:            true,
				Computed:            true,
			},
		},
	}
}

// ConfigValidators requires exactly one lookup key, since the two are alternative
// ways of naming the same namespace.
func (d *orbNamespaceDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("id"),
			path.MatchRoot("name"),
		),
	}
}

// Configure adds the provider configured client to the data source.
func (d *orbNamespaceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// Read fetches the namespace and sets the data source state.
func (d *orbNamespaceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, "circleci_orb_namespace", &resp.Diagnostics) {
		return
	}

	var config orbNamespaceDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var (
		ns  *circleci.Namespace
		err error
	)
	if id := config.Id.ValueString(); id != "" {
		ns, err = d.client.GetNamespaceByID(ctx, id)
	} else {
		ns, err = d.client.GetNamespace(ctx, config.Name.ValueString())
	}

	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI orb namespace",
			circleci.Detail(err),
		)

		return
	}

	state := orbNamespaceDataSourceModel{
		Id:   types.StringValue(ns.ID),
		Name: types.StringValue(ns.Name),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
