// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &deployComponentDataSource{}
	_ datasource.DataSourceWithConfigure = &deployComponentDataSource{}
)

// deployComponentTypeName is the data source's type name, used in diagnostics.
const deployComponentTypeName = "circleci_deploy_component"

// deployComponentDataSourceModel maps the data source schema: the component's
// own fields (embedded via deployComponentModel's tfsdk tags) plus its
// versions.
type deployComponentDataSourceModel struct {
	Id           types.String                  `tfsdk:"id"`
	ProjectId    types.String                  `tfsdk:"project_id"`
	Name         types.String                  `tfsdk:"name"`
	ReleaseCount types.Int64                   `tfsdk:"release_count"`
	Labels       types.Map                     `tfsdk:"labels"`
	CreatedAt    types.String                  `tfsdk:"created_at"`
	UpdatedAt    types.String                  `tfsdk:"updated_at"`
	ArchivedAt   types.String                  `tfsdk:"archived_at"`
	Versions     []deployComponentVersionModel `tfsdk:"versions"`
}

// deployComponentVersionModel maps one published version of a component.
type deployComponentVersionModel struct {
	Name           types.String `tfsdk:"name"`
	Namespace      types.String `tfsdk:"namespace"`
	EnvironmentId  types.String `tfsdk:"environment_id"`
	IsLive         types.Bool   `tfsdk:"is_live"`
	PipelineId     types.String `tfsdk:"pipeline_id"`
	WorkflowId     types.String `tfsdk:"workflow_id"`
	JobId          types.String `tfsdk:"job_id"`
	JobNumber      types.Int64  `tfsdk:"job_number"`
	LastDeployedAt types.String `tfsdk:"last_deployed_at"`
}

// NewDeployComponentDataSource is a helper function to simplify the provider
// implementation.
func NewDeployComponentDataSource() datasource.DataSource {
	return &deployComponentDataSource{}
}

// deployComponentDataSource fetches one CircleCI deploy/release component,
// including its published versions.
type deployComponentDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *deployComponentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deploy_component"
}

// Schema defines the schema for the data source.
func (d *deployComponentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attributes := deployComponentAttributes(schema.StringAttribute{
		MarkdownDescription: "Unique identifier (UUID) of the component.",
		Required:            true,
	})
	attributes["versions"] = schema.ListNestedAttribute{
		MarkdownDescription: "Every version published for this component, across all environments.",
		Computed:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{
					MarkdownDescription: "Name of the version, e.g. `1.2.3`.",
					Computed:            true,
				},
				"namespace": schema.StringAttribute{
					MarkdownDescription: "Namespace the version was deployed into.",
					Computed:            true,
				},
				"environment_id": schema.StringAttribute{
					MarkdownDescription: "Unique identifier (UUID) of the environment this version was deployed to.",
					Computed:            true,
				},
				"is_live": schema.BoolAttribute{
					MarkdownDescription: "Whether this is the version currently live in its environment.",
					Computed:            true,
				},
				"pipeline_id": schema.StringAttribute{
					MarkdownDescription: "Unique identifier (UUID) of the pipeline run that deployed this " +
						"version. Null when no pipeline run was recorded.",
					Computed: true,
				},
				"workflow_id": schema.StringAttribute{
					MarkdownDescription: "Unique identifier (UUID) of the workflow that deployed this " +
						"version. Null when no workflow was recorded.",
					Computed: true,
				},
				"job_id": schema.StringAttribute{
					MarkdownDescription: "Unique identifier (UUID) of the job that deployed this version. " +
						"Null when no job was recorded.",
					Computed: true,
				},
				"job_number": schema.Int64Attribute{
					MarkdownDescription: "Number of the job that deployed this version. Null when no job " +
						"was recorded.",
					Computed: true,
				},
				"last_deployed_at": schema.StringAttribute{
					MarkdownDescription: "When this version was last deployed, as an RFC 3339 timestamp.",
					Computed:            true,
				},
			},
		},
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches one CircleCI deploy/release component by id, including every " +
			"version published for it. Use [`circleci_deploy_components`](deploy_components) to list " +
			"components in an organization, e.g. to look one up by name.\n\n" +
			"~> **CircleCI Cloud only.** Deploy/release tracking is not part of CircleCI Server.",
		Attributes: attributes,
	}
}

// Read fetches the component and its versions, and sets the data source state.
func (d *deployComponentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, deployComponentTypeName, &resp.Diagnostics) {
		return
	}

	var config deployComponentDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := config.Id.ValueString()

	component, err := d.client.DeployComponents().Get(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI deploy component "+id,
			circleci.Detail(err),
		)

		return
	}

	componentModel, diags := deployComponentToModel(ctx, *component)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	versions, err := d.client.DeployComponents().ListVersions(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read versions for CircleCI deploy component "+id,
			circleci.Detail(err),
		)

		return
	}

	state := deployComponentDataSourceModel{
		Id:           componentModel.Id,
		ProjectId:    componentModel.ProjectId,
		Name:         componentModel.Name,
		ReleaseCount: componentModel.ReleaseCount,
		Labels:       componentModel.Labels,
		CreatedAt:    componentModel.CreatedAt,
		UpdatedAt:    componentModel.UpdatedAt,
		ArchivedAt:   componentModel.ArchivedAt,
		Versions:     make([]deployComponentVersionModel, 0, len(versions)),
	}

	for _, version := range versions {
		state.Versions = append(state.Versions, deployComponentVersionToModel(version))
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *deployComponentDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// deployComponentVersionToModel converts a circleci.DeployComponentVersion
// into its Terraform model.
//
// PipelineID, WorkflowID and JobID are converted to null rather than the
// all-zero UUID the API sends for an association that was never
// recorded; see the comment on circleci.DeployComponentVersion.
func deployComponentVersionToModel(version circleci.DeployComponentVersion) deployComponentVersionModel {
	model := deployComponentVersionModel{
		Name:           types.StringValue(version.Name),
		Namespace:      types.StringValue(version.Namespace),
		EnvironmentId:  types.StringValue(version.EnvironmentID),
		IsLive:         types.BoolValue(version.IsLive),
		PipelineId:     zeroUUIDToNull(version.PipelineID),
		WorkflowId:     zeroUUIDToNull(version.WorkflowID),
		JobId:          zeroUUIDToNull(version.JobID),
		JobNumber:      types.Int64Null(),
		LastDeployedAt: types.StringValue(version.LastDeployedAt.Format(time.RFC3339)),
	}

	if version.JobNumber != 0 {
		model.JobNumber = types.Int64Value(version.JobNumber)
	}

	return model
}

// zeroUUIDToNull converts the all-zero UUID sentinel to a null string value,
// so an unrecorded association reads as null rather than a fake identifier.
func zeroUUIDToNull(id string) types.String {
	if id == "" || id == circleci.ZeroUUID {
		return types.StringNull()
	}

	return types.StringValue(id)
}
