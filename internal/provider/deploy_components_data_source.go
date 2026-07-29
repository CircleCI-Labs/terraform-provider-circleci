// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource                     = &deployComponentsDataSource{}
	_ datasource.DataSourceWithConfigure        = &deployComponentsDataSource{}
	_ datasource.DataSourceWithConfigValidators = &deployComponentsDataSource{}
)

// deployComponentsTypeName is the data source's type name, used in diagnostics.
const deployComponentsTypeName = "circleci_deploy_components"

// deployComponentsDataSourceModel maps the data source schema.
type deployComponentsDataSourceModel struct {
	OrganizationId types.String           `tfsdk:"organization_id"`
	OrgId          types.String           `tfsdk:"org_id"`
	ProjectId      types.String           `tfsdk:"project_id"`
	Name           types.String           `tfsdk:"name"`
	Components     []deployComponentModel `tfsdk:"components"`
}

// deployComponentModel maps one component in the list, and the singular
// circleci_deploy_component data source's non-version fields.
type deployComponentModel struct {
	Id           types.String `tfsdk:"id"`
	ProjectId    types.String `tfsdk:"project_id"`
	Name         types.String `tfsdk:"name"`
	ReleaseCount types.Int64  `tfsdk:"release_count"`
	Labels       types.Map    `tfsdk:"labels"`
	CreatedAt    types.String `tfsdk:"created_at"`
	UpdatedAt    types.String `tfsdk:"updated_at"`
	ArchivedAt   types.String `tfsdk:"archived_at"`
}

// deployComponentAttributes is the schema for one component, shared between
// this plural data source and the singular circleci_deploy_component (which
// adds a `versions` attribute on top).
func deployComponentAttributes(idAttribute schema.Attribute) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": idAttribute,
		"project_id": schema.StringAttribute{
			MarkdownDescription: "Unique identifier (UUID) of the CircleCI project the component is " +
				"associated with. Null when the component has no associated project.",
			Computed: true,
		},
		"name": schema.StringAttribute{
			MarkdownDescription: "Name of the component.",
			Computed:            true,
		},
		"release_count": schema.Int64Attribute{
			MarkdownDescription: "Total number of releases recorded for this component.",
			Computed:            true,
		},
		"labels": schema.MapAttribute{
			MarkdownDescription: "Labels attached to the component, as a map of label key to value.",
			Computed:            true,
			ElementType:         types.StringType,
		},
		"created_at": schema.StringAttribute{
			MarkdownDescription: "When the component was first observed, as an RFC 3339 timestamp.",
			Computed:            true,
		},
		"updated_at": schema.StringAttribute{
			MarkdownDescription: "When the component was last updated, as an RFC 3339 timestamp.",
			Computed:            true,
		},
		"archived_at": schema.StringAttribute{
			MarkdownDescription: "When the component was archived, as an RFC 3339 timestamp. Null when " +
				"the component is not archived.",
			Computed: true,
		},
	}
}

// NewDeployComponentsDataSource is a helper function to simplify the provider
// implementation.
func NewDeployComponentsDataSource() datasource.DataSource {
	return &deployComponentsDataSource{}
}

// deployComponentsDataSource lists CircleCI deploy/release components.
type deployComponentsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *deployComponentsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deploy_components"
}

// Schema defines the schema for the data source.
func (d *deployComponentsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every CircleCI deploy/release component in an organization, " +
			"optionally scoped to one project. Components are declared in a project's " +
			"`.circleci/config.yml` (the `component` key on a deploy job) rather than created through " +
			"the API, so this is read-only.\n\n" +
			"Pagination is followed internally, so the result covers every matching component rather " +
			"than one page.\n\n" +
			"~> **CircleCI Cloud only.** Deploy/release tracking is not part of CircleCI Server.",
		Attributes: map[string]schema.Attribute{
			// See org_id_deprecation.go for why the organization is accepted under
			// two names.
			"organization_id": deprecatedOrgIDDataSourceAttribute("deploy components"),
			"org_id":          orgIDDataSourceAttribute("deploy components"),
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of a CircleCI project to filter by. " +
					"Leave unset to list components across every project in the organization.",
				Optional: true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Component name to filter by. Leave unset to list every component.",
				Optional:            true,
			},
			"components": schema.ListNestedAttribute{
				MarkdownDescription: "The components matching the given filters, sorted by name and project id.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: deployComponentAttributes(schema.StringAttribute{
						MarkdownDescription: "Unique identifier (UUID) of the component.",
						Computed:            true,
					}),
				},
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (d *deployComponentsDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		orgIDDataSourceConfigValidator(),
	}
}

// Read lists the organization's deploy components.
func (d *deployComponentsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, deployComponentsTypeName, &resp.Diagnostics) {
		return
	}

	var state deployComponentsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := effectiveOrgID(state.OrganizationId, state.OrgId)

	components, err := d.client.DeployComponents().List(ctx, orgID, state.ProjectId.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI deploy components for organization "+orgID,
			circleci.Detail(err),
		)

		return
	}

	state.Components = make([]deployComponentModel, 0, len(components))
	for _, component := range components {
		model, diags := deployComponentToModel(ctx, component)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}

		state.Components = append(state.Components, model)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *deployComponentsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// deployComponentToModel converts a circleci.DeployComponent into its
// Terraform model, shared between the plural and singular data sources.
func deployComponentToModel(ctx context.Context, component circleci.DeployComponent) (deployComponentModel, diag.Diagnostics) {
	labels, diags := entityLabelsToMap(ctx, component.Labels)

	model := deployComponentModel{
		Id:           types.StringValue(component.ID),
		ProjectId:    types.StringPointerValue(component.ProjectID),
		Name:         types.StringValue(component.Name),
		ReleaseCount: types.Int64Value(component.ReleaseCount),
		Labels:       labels,
		CreatedAt:    types.StringValue(component.CreatedAt.Format(time.RFC3339)),
		UpdatedAt:    types.StringValue(component.UpdatedAt.Format(time.RFC3339)),
		ArchivedAt:   types.StringNull(),
	}

	if component.ArchivedAt != nil {
		model.ArchivedAt = types.StringValue(component.ArchivedAt.Format(time.RFC3339))
	}

	return model, diags
}
