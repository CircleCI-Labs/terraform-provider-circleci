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
	_ datasource.DataSource                     = &contextsDataSource{}
	_ datasource.DataSourceWithConfigure        = &contextsDataSource{}
	_ datasource.DataSourceWithConfigValidators = &contextsDataSource{}
)

// contextsDataSourceModel maps the data source schema.
//
// The shape follows the provider's convention for plural data sources: the scope
// the collection is listed under is the input, and the collection itself is a
// single list-nested attribute named after the entity.
type contextsDataSourceModel struct {
	OrganizationID types.String       `tfsdk:"organization_id"`
	OrgID          types.String       `tfsdk:"org_id"`
	Contexts       []contextItemModel `tfsdk:"contexts"`
}

// contextItemModel maps one context in the list.
type contextItemModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	CreatedAt types.String `tfsdk:"created_at"`
}

// NewContextsDataSource is a helper function to simplify the provider implementation.
func NewContextsDataSource() datasource.DataSource {
	return &contextsDataSource{}
}

// contextsDataSource is the data source implementation.
type contextsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *contextsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_contexts"
}

// Schema defines the schema for the data source.
func (d *contextsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every CircleCI context in an organization, including contexts " +
			"created outside Terraform. Available on CircleCI Cloud and CircleCI Server.\n\n" +
			"Pagination is followed internally, so the result covers every context rather than one page.\n\n" +
			"-> **Values are not returned.** This data source reports the contexts themselves, not their " +
			"environment variables. Use [`circleci_context_environment_variable`](./context_environment_variable) " +
			"to read one variable, whose value the API masks in any case.",
		Attributes: map[string]schema.Attribute{
			// See org_id_deprecation.go for why the organization is accepted under
			// two names.
			"organization_id": deprecatedOrgIDDataSourceAttribute("contexts"),
			"org_id":          orgIDDataSourceAttribute("contexts"),
			"contexts": schema.ListNestedAttribute{
				MarkdownDescription: "The contexts in the organization, in the order the API returns them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the context.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Name of the context.",
							Computed:            true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "Timestamp the context was created, as the API reported it.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (d *contextsDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		orgIDDataSourceConfigValidator(),
	}
}

// Read lists the organization's contexts.
func (d *contextsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state contextsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(state.OrganizationID, state.OrgID)

	contexts, err := d.client.ListContexts(ctx, organizationID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI contexts for organization "+organizationID,
			circleci.Detail(err),
		)

		return
	}

	// An empty, non-null list keeps `for_each` and `length()` working against an
	// organization that has no contexts yet.
	state.Contexts = make([]contextItemModel, 0, len(contexts))
	for _, item := range contexts {
		state.Contexts = append(state.Contexts, contextItemModel{
			ID:        types.StringValue(item.ID),
			Name:      types.StringValue(item.Name),
			CreatedAt: types.StringValue(item.CreatedAt),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *contextsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
