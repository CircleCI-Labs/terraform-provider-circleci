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
	_ datasource.DataSource              = &contextRestrictionsDataSource{}
	_ datasource.DataSourceWithConfigure = &contextRestrictionsDataSource{}
)

// contextRestrictionsDataSourceModel maps the data source schema.
type contextRestrictionsDataSourceModel struct {
	ContextID    types.String                  `tfsdk:"context_id"`
	Restrictions []contextRestrictionItemModel `tfsdk:"restrictions"`
}

// contextRestrictionItemModel maps one restriction in the list. The attribute
// names match `circleci_context_restriction`, so a restriction read here and a
// restriction managed there describe themselves the same way.
type contextRestrictionItemModel struct {
	ID        types.String `tfsdk:"id"`
	ContextID types.String `tfsdk:"context_id"`
	Name      types.String `tfsdk:"name"`
	Type      types.String `tfsdk:"type"`
	Value     types.String `tfsdk:"value"`
	ProjectID types.String `tfsdk:"project_id"`
}

// NewContextRestrictionsDataSource is a helper function to simplify the provider implementation.
func NewContextRestrictionsDataSource() datasource.DataSource {
	return &contextRestrictionsDataSource{}
}

// contextRestrictionsDataSource is the data source implementation.
type contextRestrictionsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *contextRestrictionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_context_restrictions"
}

// Schema defines the schema for the data source.
func (d *contextRestrictionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every restriction on a CircleCI context, including restrictions " +
			"created outside Terraform. Available on CircleCI Cloud and CircleCI Server.\n\n" +
			"Restrictions control which projects, groups or pipeline conditions may use a context.\n\n" +
			"~> **An empty list is the most restricted state, not the least.** [NET, measured on " +
			"2026-08-21] a context created through the API or the UI starts with exactly one " +
			"restriction: a `group` restriction named \"All members\" whose `value` equals the " +
			"organization's own UUID. That is the permissive default — per CircleCI's documentation " +
			"it means every organization member may use the context. An empty `restrictions` list " +
			"means every group grant has been removed, which per CircleCI's documentation leaves the " +
			"context usable by organization administrators only. To ask \"may every member use this " +
			"context\", look for a `group` entry whose `value` equals the organization UUID; to ask " +
			"\"is this context genuinely narrowed to specific teams\", look for a `group` entry whose " +
			"`value` does not. Do not use the list's length for either question, and do not delete the " +
			"default entry to make a `project` restriction \"take effect\" — per CircleCI's " +
			"documentation and support the two combine as an AND already, and removing every `group` " +
			"restriction instead locks the context down to administrators, breaking scheduled and " +
			"bot-triggered pipelines.",
		Attributes: map[string]schema.Attribute{
			"context_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the context whose restrictions are listed.",
				Required:            true,
			},
			"restrictions": schema.ListNestedAttribute{
				MarkdownDescription: "The restrictions on the context, in the order the API returns them. " +
					"A context freshly created through the API or the UI carries one `group` " +
					"restriction naming \"All members\"; an empty list means every group grant has " +
					"been removed, restricting the context to organization administrators.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the restriction.",
							Computed:            true,
						},
						"context_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the restricted context.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Human-readable name of whatever the restriction points at, " +
								"such as a project slug or a group name. Empty for `expression` restrictions.",
							Computed: true,
						},
						"type": schema.StringAttribute{
							MarkdownDescription: "The kind of restriction: `" +
								circleci.ContextRestrictionTypeProject + "`, `" +
								circleci.ContextRestrictionTypeGroup + "` or `" +
								circleci.ContextRestrictionTypeExpression + "`.",
							Computed: true,
						},
						"value": schema.StringAttribute{
							MarkdownDescription: "The value the restriction matches on: a project UUID, a " +
								"group UUID, or an expression.",
							Computed: true,
						},
						"project_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the restricted project. The API " +
								"only sets this for `" + circleci.ContextRestrictionTypeProject +
								"` restrictions, so it is empty for the other kinds.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

// Read lists the context's restrictions.
func (d *contextRestrictionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state contextRestrictionsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	contextID := state.ContextID.ValueString()

	restrictions, err := d.client.ListContextRestrictions(ctx, contextID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list restrictions for CircleCI context "+contextID,
			circleci.Detail(err),
		)

		return
	}

	// An empty, non-null list keeps `for_each` and `length()` working. Note that
	// an empty list here means the context is locked down to organization
	// administrators, not that it is unrestricted; see the schema description.
	state.Restrictions = make([]contextRestrictionItemModel, 0, len(restrictions))
	for _, restriction := range restrictions {
		state.Restrictions = append(state.Restrictions, contextRestrictionItemModel{
			ID:        types.StringValue(restriction.ID),
			ContextID: types.StringValue(restriction.ContextID),
			Name:      types.StringValue(restriction.Name),
			Type:      types.StringValue(restriction.RestrictionType),
			Value:     types.StringValue(restriction.RestrictionValue),
			ProjectID: types.StringValue(restriction.ProjectID),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *contextRestrictionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
