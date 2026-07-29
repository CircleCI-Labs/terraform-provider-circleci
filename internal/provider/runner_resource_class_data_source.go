// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource                     = &runnerResourceClassDataSource{}
	_ datasource.DataSourceWithConfigure        = &runnerResourceClassDataSource{}
	_ datasource.DataSourceWithConfigValidators = &runnerResourceClassDataSource{}
)

// runnerResourceClassDataSourceModel maps the data source schema.
type runnerResourceClassDataSourceModel struct {
	OrganizationId types.String `tfsdk:"organization_id"`
	OrgId          types.String `tfsdk:"org_id"`
	ResourceClass  types.String `tfsdk:"resource_class"`
	Id             types.String `tfsdk:"id"`
	Description    types.String `tfsdk:"description"`
}

// NewRunnerResourceClassDataSource is a helper function to simplify the provider implementation.
func NewRunnerResourceClassDataSource() datasource.DataSource {
	return &runnerResourceClassDataSource{}
}

// runnerResourceClassDataSource is the data source implementation.
type runnerResourceClassDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *runnerResourceClassDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_runner_resource_class"
}

// Schema defines the schema for the data source.
func (d *runnerResourceClassDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads a CircleCI runner resource class.",
		Attributes: map[string]schema.Attribute{
			// See org_id_deprecation.go for why the organization is accepted under
			// two names. The UUID check used to live in Read, which meant a slug was
			// only rejected once the plan was being applied.
			"organization_id": runnerOrgIDDataSourceAttribute(
				deprecatedOrgIDDataSourceAttribute("runner resource classes"),
			),
			"org_id": runnerOrgIDDataSourceAttribute(
				orgIDDataSourceAttribute("runner resource classes"),
			),
			"resource_class": schema.StringAttribute{
				MarkdownDescription: "The resource class name in `namespace/name` format (e.g. `myorg/myrunner`).",
				Required:            true,
			},
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the runner resource class.",
				Computed:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description of the runner resource class.",
				Computed:            true,
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (d *runnerResourceClassDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		orgIDDataSourceConfigValidator(),
	}
}

// Read fetches the resource class from the API.
func (d *runnerResourceClassDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state runnerResourceClassDataSourceModel
	diags := req.Config.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The organization's UUID format is enforced by the attribute validators, so
	// by here it is either well-formed or the plan already failed.
	organizationId := effectiveOrgID(state.OrganizationId, state.OrgId)

	rcName := state.ResourceClass.ValueString()
	slashIdx := strings.Index(rcName, "/")
	if slashIdx == -1 {
		resp.Diagnostics.AddError(
			"Invalid resource_class format",
			fmt.Sprintf("Expected namespace/name format, got: %s", rcName),
		)
		return
	}
	namespace := rcName[:slashIdx]

	classes, err := d.client.ListResourceClasses(ctx, namespace, organizationId)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading CircleCI runner resource classes",
			"Could not list runner resource classes for namespace "+namespace+": "+circleci.Detail(err),
		)
		return
	}

	var found *circleci.ResourceClass
	for i := range classes {
		if classes[i].ResourceClass == rcName {
			found = &classes[i]
			break
		}
	}

	if found == nil {
		resp.Diagnostics.AddError(
			"Runner resource class not found",
			fmt.Sprintf("No runner resource class with name %q was found in namespace %q.", rcName, namespace),
		)
		return
	}

	state.Id = types.StringValue(found.ID)
	state.ResourceClass = types.StringValue(found.ResourceClass)
	state.Description = types.StringValue(found.Description)

	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

// Configure adds the provider configured client to the data source.
func (d *runnerResourceClassDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
