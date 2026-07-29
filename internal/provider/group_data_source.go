// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource                     = &groupDataSource{}
	_ datasource.DataSourceWithConfigure        = &groupDataSource{}
	_ datasource.DataSourceWithConfigValidators = &groupDataSource{}
)

// groupDataSourceModel maps the data source schema.
type groupDataSourceModel struct {
	Id             types.String `tfsdk:"id"`
	OrganizationId types.String `tfsdk:"organization_id"`
	OrgId          types.String `tfsdk:"org_id"`
	Name           types.String `tfsdk:"name"`
	Description    types.String `tfsdk:"description"`
}

// NewGroupDataSource is a helper function to simplify the provider implementation.
func NewGroupDataSource() datasource.DataSource {
	return &groupDataSource{}
}

// groupDataSource is the data source implementation.
type groupDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *groupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

// Schema defines the schema for the data source.
func (d *groupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches a single CircleCI group by id. " +
			"Available on CircleCI Cloud only. Groups require a `circleci` type (standalone) " +
			"organization: the API documents group creation as supported only for standalone " +
			"organizations, and a CircleCI Server installation is always a `github` type " +
			"organization.\n\n" +
			"Group ids are unique only within an organization, so the organization is required as well, " +
			"as `org_id` (or the deprecated `organization_id`). " +
			"Use `circleci_groups` to list every group in an organization.\n\n" +
			"~> **Group membership is not managed by Terraform.** Adding and removing group members is " +
			"only possible in the CircleCI web UI, so the users in a group are not exposed here.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the group.",
				Required:            true,
			},
			// See org_id_deprecation.go for why the organization is accepted under
			// two names.
			"organization_id": deprecatedOrgIDDataSourceAttribute("groups"),
			"org_id":          orgIDDataSourceAttribute("groups"),
			"name": schema.StringAttribute{
				MarkdownDescription: "Name of the group.",
				Computed:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description of the group.",
				Computed:            true,
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (d *groupDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		orgIDDataSourceConfigValidator(),
	}
}

// Read fetches the group from the API.
func (d *groupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireStandaloneCapable(d.client, "circleci_group", &resp.Diagnostics) {
		return
	}

	var state groupDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(state.OrganizationId, state.OrgId)
	groupID := state.Id.ValueString()

	group, err := d.client.Groups().Get(ctx, organizationID, groupID)
	if err != nil {
		// A data source that cannot find its target is an error: unlike a
		// resource, there is no state to drop.
		if circleci.IsNotFound(err) {
			resp.Diagnostics.AddError(
				"CircleCI group not found",
				fmt.Sprintf("No group with id %q was found in organization %q.", groupID, organizationID),
			)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read CircleCI group "+groupID,
			circleci.Detail(err),
		)

		return
	}

	state.Id = types.StringValue(group.ID)
	state.Name = types.StringValue(group.Name)
	state.Description = types.StringValue(group.Description)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *groupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
