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
	_ datasource.DataSource              = &deployEnvironmentsDataSource{}
	_ datasource.DataSourceWithConfigure = &deployEnvironmentsDataSource{}
)

// deployEnvironmentsTypeName is the data source's type name, used in diagnostics.
const deployEnvironmentsTypeName = "circleci_deploy_environments"

// deployEnvironmentsDataSourceModel maps the data source schema.
type deployEnvironmentsDataSourceModel struct {
	OrganizationId types.String             `tfsdk:"organization_id"`
	Environments   []deployEnvironmentModel `tfsdk:"environments"`
}

// deployEnvironmentModel maps one environment in the list, and the singular
// circleci_deploy_environment data source's result.
type deployEnvironmentModel struct {
	Id          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
	Labels      types.Map    `tfsdk:"labels"`
}

// deployEnvironmentAttributes is the schema for one environment, shared
// between this plural data source and the singular circleci_deploy_environment.
// idAttribute is supplied by the caller because the two data sources disagree
// on whether id is an input (singular: Required) or output-only
// (plural: Computed, inside a nested list).
func deployEnvironmentAttributes(idAttribute schema.Attribute) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": idAttribute,
		"name": schema.StringAttribute{
			MarkdownDescription: "Name of the environment, e.g. `staging` or `prod`.",
			Computed:            true,
		},
		"description": schema.StringAttribute{
			MarkdownDescription: "Description of the environment.",
			Computed:            true,
		},
		"created_at": schema.StringAttribute{
			MarkdownDescription: "When the environment was first observed, as an RFC 3339 timestamp.",
			Computed:            true,
		},
		"updated_at": schema.StringAttribute{
			MarkdownDescription: "When the environment was last updated, as an RFC 3339 timestamp.",
			Computed:            true,
		},
		"labels": schema.MapAttribute{
			MarkdownDescription: "Labels attached to the environment, as a map of label key to value.",
			Computed:            true,
			ElementType:         types.StringType,
		},
	}
}

// NewDeployEnvironmentsDataSource is a helper function to simplify the
// provider implementation.
func NewDeployEnvironmentsDataSource() datasource.DataSource {
	return &deployEnvironmentsDataSource{}
}

// deployEnvironmentsDataSource lists CircleCI deploy/release environments.
type deployEnvironmentsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *deployEnvironmentsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deploy_environments"
}

// Schema defines the schema for the data source.
func (d *deployEnvironmentsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every CircleCI deploy/release environment in an organization. " +
			"Environments are declared in a project's `.circleci/config.yml` (the `environment` key on a " +
			"deploy job) rather than created through the API, so this is read-only.\n\n" +
			"Pagination is followed internally, so the result covers every environment rather than one page.\n\n" +
			"~> **CircleCI Cloud only.** Deploy/release tracking is not part of CircleCI Server.",
		Attributes: map[string]schema.Attribute{
			"organization_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization whose environments are listed.",
				Required:            true,
			},
			"environments": schema.ListNestedAttribute{
				MarkdownDescription: "The environments in the organization, sorted by name.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: deployEnvironmentAttributes(schema.StringAttribute{
						MarkdownDescription: "Unique identifier (UUID) of the environment.",
						Computed:            true,
					}),
				},
			},
		},
	}
}

// Read lists the organization's deploy environments.
func (d *deployEnvironmentsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, deployEnvironmentsTypeName, &resp.Diagnostics) {
		return
	}

	var state deployEnvironmentsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := state.OrganizationId.ValueString()

	envs, err := d.client.DeployEnvironments().List(ctx, orgID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI deploy environments for organization "+orgID,
			circleci.Detail(err),
		)

		return
	}

	state.Environments = make([]deployEnvironmentModel, 0, len(envs))
	for _, env := range envs {
		model, diags := deployEnvironmentToModel(ctx, env)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}

		state.Environments = append(state.Environments, model)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *deployEnvironmentsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// deployEnvironmentToModel converts a circleci.DeployEnvironment into its
// Terraform model, shared between the plural and singular data sources.
func deployEnvironmentToModel(ctx context.Context, env circleci.DeployEnvironment) (deployEnvironmentModel, diag.Diagnostics) {
	labels, diags := entityLabelsToMap(ctx, env.Labels)

	return deployEnvironmentModel{
		Id:          types.StringValue(env.ID),
		Name:        types.StringValue(env.Name),
		Description: types.StringValue(env.Description),
		CreatedAt:   types.StringValue(env.CreatedAt.Format(time.RFC3339)),
		UpdatedAt:   types.StringValue(env.UpdatedAt.Format(time.RFC3339)),
		Labels:      labels,
	}, diags
}

// entityLabelsToMap converts a slice of circleci.EntityLabel into the map of
// string to string that both the deploy environment and deploy component data
// sources expose. A nil slice becomes an empty (non-null) map, so `lookup()`
// and `keys()` keep working for a resource with no labels.
func entityLabelsToMap(ctx context.Context, labels []circleci.EntityLabel) (types.Map, diag.Diagnostics) {
	values := make(map[string]string, len(labels))
	for _, label := range labels {
		values[label.Key] = label.Value
	}

	return types.MapValueFrom(ctx, types.StringType, values)
}
