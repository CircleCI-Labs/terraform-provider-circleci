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
	_ datasource.DataSource              = &urlOrbAllowListDataSource{}
	_ datasource.DataSourceWithConfigure = &urlOrbAllowListDataSource{}
)

// urlOrbAllowListEntryModel is one element of the entries list.
type urlOrbAllowListEntryModel struct {
	Id     types.String `tfsdk:"id"`
	Name   types.String `tfsdk:"name"`
	Prefix types.String `tfsdk:"prefix"`
	Auth   types.String `tfsdk:"auth"`
}

// urlOrbAllowListEntryAttrTypes describes urlOrbAllowListEntryModel to the
// framework when building the entries list value.
var urlOrbAllowListEntryAttrTypes = map[string]attr.Type{
	"id":     types.StringType,
	"name":   types.StringType,
	"prefix": types.StringType,
	"auth":   types.StringType,
}

// urlOrbAllowListDataSourceModel maps the data source schema.
type urlOrbAllowListDataSourceModel struct {
	Organization types.String `tfsdk:"organization"`
	Entries      types.List   `tfsdk:"entries"`
}

// NewURLOrbAllowListDataSource is a helper function to simplify the provider
// implementation.
func NewURLOrbAllowListDataSource() datasource.DataSource {
	return &urlOrbAllowListDataSource{}
}

// urlOrbAllowListDataSource reads every entry in an organization's URL orb allow
// list.
type urlOrbAllowListDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *urlOrbAllowListDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_url_orb_allow_list"
}

// Schema defines the schema for the data source.
func (d *urlOrbAllowListDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every entry in a CircleCI organization's URL orb allow list.\n\n" +
			"Available on CircleCI Cloud **and on CircleCI Server**: this is a v2 organization " +
			"route served by CircleCI itself rather than by a service behind the public API proxy, " +
			"and a Server installation's gateway sends every API path it does not route elsewhere " +
			"to that component. Being v2 would not be evidence on its own — " +
			"`circleci_pipeline_definition` is v2 and unavailable on Server — but the owner is. " +
			"Nothing gates this data source.\n\n" +
			"~> A 404 from this data source is about the *organization*, not the allow list: an " +
			"organization that does not exist and one the API token may not view answer " +
			"identically.",
		Attributes: map[string]schema.Attribute{
			"organization": schema.StringAttribute{
				MarkdownDescription: urlOrbAllowListOrganizationDescription,
				Required:            true,
			},
			"entries": schema.ListNestedAttribute{
				MarkdownDescription: "The allow list entries, in the order the API returns them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "The UUID of the allow list entry.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The human-readable name of the entry.",
							Computed:            true,
						},
						"prefix": schema.StringAttribute{
							MarkdownDescription: "The URL prefix the entry permits.",
							Computed:            true,
						},
						"auth": schema.StringAttribute{
							MarkdownDescription: "The authentication method used when fetching a URL that matches `prefix`: " +
								"`bitbucket-oauth`, `github-app`, `github-oauth` or `none`.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

// Read fetches the organization's allow list.
func (d *urlOrbAllowListDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError(
			"Provider Not Configured",
			"The CircleCI API client is unset. Please report this issue to the provider developers.",
		)

		return
	}

	var data urlOrbAllowListDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := data.Organization.ValueString()

	entries, err := d.client.ListURLOrbAllowList(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI URL orb allow list for organization "+org,
			circleci.Detail(err),
		)

		return
	}

	models := make([]urlOrbAllowListEntryModel, 0, len(entries))
	for _, entry := range entries {
		models = append(models, urlOrbAllowListEntryModel{
			Id:     types.StringValue(entry.ID),
			Name:   types.StringValue(entry.Name),
			Prefix: types.StringValue(entry.Prefix),
			Auth:   types.StringValue(entry.Auth),
		})
	}

	list, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: urlOrbAllowListEntryAttrTypes}, models)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.Entries = list

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// Configure adds the provider configured client to the data source.
func (d *urlOrbAllowListDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
