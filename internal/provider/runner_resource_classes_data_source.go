// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource                     = &runnerResourceClassesDataSource{}
	_ datasource.DataSourceWithConfigure        = &runnerResourceClassesDataSource{}
	_ datasource.DataSourceWithConfigValidators = &runnerResourceClassesDataSource{}
)

// runnerResourceClassesDataSourceModel maps the data source schema.
type runnerResourceClassesDataSourceModel struct {
	OrganizationId  types.String                   `tfsdk:"organization_id"`
	OrgId           types.String                   `tfsdk:"org_id"`
	Namespace       types.String                   `tfsdk:"namespace"`
	ResourceClasses []runnerResourceClassItemModel `tfsdk:"resource_classes"`
}

// runnerResourceClassItemModel maps one resource class in the list.
type runnerResourceClassItemModel struct {
	Id            types.String `tfsdk:"id"`
	ResourceClass types.String `tfsdk:"resource_class"`
	Description   types.String `tfsdk:"description"`
}

// NewRunnerResourceClassesDataSource is a helper function to simplify the provider implementation.
func NewRunnerResourceClassesDataSource() datasource.DataSource {
	return &runnerResourceClassesDataSource{}
}

// runnerResourceClassesDataSource is the data source implementation.
type runnerResourceClassesDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *runnerResourceClassesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_runner_resource_classes"
}

// ConfigValidators requires at least one filter, because the runner API rejects
// an unfiltered list.
//
// The organization filter counts under either of its two names. As on
// circleci_runners, this is deliberately not orgIDDataSourceConfigValidator:
// requiring exactly one of the pair would break a namespace-only listing, which
// is supported today. Both at once is still refused, by the Conflicting
// validator. See org_id_deprecation.go.
func (d *runnerResourceClassesDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.AtLeastOneOf(
			path.MatchRoot("organization_id"),
			path.MatchRoot("namespace"),
			path.MatchRoot("org_id"),
		),
		datasourcevalidator.Conflicting(
			path.MatchRoot("organization_id"),
			path.MatchRoot("org_id"),
		),
	}
}

// Schema defines the schema for the data source.
func (d *runnerResourceClassesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists CircleCI self-hosted runner resource classes, including ones created " +
			"outside Terraform. At least one of `org_id` (or the deprecated `organization_id`) or " +
			"`namespace` must be set.\n\n" +
			"Available on CircleCI Cloud and CircleCI Server. On Server the runner API is served by " +
			"your own installation, so the provider's `runner_host` attribute must be set to your " +
			"Server hostname.\n\n" +
			"Use `circleci_runner_resource_class` (singular) to look one up by name.",
		Attributes: map[string]schema.Attribute{
			// Only return resource classes owned by this organization. See
			// org_id_deprecation.go for why it is accepted under two names.
			"organization_id": runnerOrgIDDataSourceAttribute(
				deprecatedOrgIDDataSourceAttribute("runner resource classes"),
			),
			"org_id": runnerOrgIDDataSourceAttribute(
				orgIDDataSourceAttribute("runner resource classes"),
			),
			"namespace": schema.StringAttribute{
				MarkdownDescription: "Only return resource classes in this runner namespace.",
				Optional:            true,
			},
			"resource_classes": schema.ListNestedAttribute{
				MarkdownDescription: "The matching resource classes, in the order the API returned them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the runner resource class.",
							Computed:            true,
						},
						"resource_class": schema.StringAttribute{
							MarkdownDescription: "The resource class name, in `namespace/name` format.",
							Computed:            true,
						},
						"description": schema.StringAttribute{
							MarkdownDescription: "Description of the runner resource class.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// Read fetches the matching resource classes from the API.
func (d *runnerResourceClassesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config runnerResourceClassesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	namespace := config.Namespace.ValueString()
	organizationId := effectiveOrgID(config.OrganizationId, config.OrgId)

	classes, err := d.client.ListResourceClasses(ctx, namespace, organizationId)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading CircleCI runner resource classes",
			fmt.Sprintf(
				"Could not list runner resource classes (%s): %s",
				runnerFilterSummary(circleci.ListRunnersParams{Namespace: namespace, OrgID: organizationId}),
				circleci.Detail(err),
			),
		)

		return
	}

	// An empty list is a valid answer, so keep the attribute an empty list rather
	// than null: practitioners iterate over it.
	config.ResourceClasses = make([]runnerResourceClassItemModel, 0, len(classes))
	for _, class := range classes {
		config.ResourceClasses = append(config.ResourceClasses, runnerResourceClassItemModel{
			Id:            types.StringValue(class.ID),
			ResourceClass: types.StringValue(class.ResourceClass),
			Description:   types.StringValue(class.Description),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// Configure adds the provider configured client to the data source.
func (d *runnerResourceClassesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
