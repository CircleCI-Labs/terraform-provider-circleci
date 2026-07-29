// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource                     = &runnersDataSource{}
	_ datasource.DataSourceWithConfigure        = &runnersDataSource{}
	_ datasource.DataSourceWithConfigValidators = &runnersDataSource{}
)

// runnersDataSourceModel maps the data source schema.
type runnersDataSourceModel struct {
	ResourceClass  types.String      `tfsdk:"resource_class"`
	Namespace      types.String      `tfsdk:"namespace"`
	OrganizationId types.String      `tfsdk:"organization_id"`
	OrgId          types.String      `tfsdk:"org_id"`
	Runners        []runnerItemModel `tfsdk:"runners"`
}

// runnerOrgIDDataSourceAttribute adds the runner API's UUID-only organization
// check to one of the shared organization attributes from org_id_deprecation.go.
//
// The runner routes take an organization UUID and reject a `vcs/org` slug with an
// opaque HTTP 400, so the format is enforced at plan time instead. The shared
// builders carry no validators, so the two runner data sources wrap them here
// rather than each spelling the attribute out again and drifting from the
// deprecation wording.
func runnerOrgIDDataSourceAttribute(attribute schema.StringAttribute) schema.StringAttribute {
	attribute.Validators = []validator.String{
		stringvalidator.RegexMatches(runnerOrgIDPattern, "must be an organization UUID"),
	}

	return attribute
}

// runnerItemModel maps one registered runner agent in the list.
type runnerItemModel struct {
	Name           types.String `tfsdk:"name"`
	Hostname       types.String `tfsdk:"hostname"`
	IP             types.String `tfsdk:"ip"`
	Version        types.String `tfsdk:"version"`
	Status         types.String `tfsdk:"status"`
	ResourceClass  types.String `tfsdk:"resource_class"`
	FirstConnected types.String `tfsdk:"first_connected"`
	LastConnected  types.String `tfsdk:"last_connected"`
	LastUsed       types.String `tfsdk:"last_used"`
}

// NewRunnersDataSource is a helper function to simplify the provider implementation.
func NewRunnersDataSource() datasource.DataSource {
	return &runnersDataSource{}
}

// runnersDataSource is the data source implementation.
type runnersDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *runnersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_runners"
}

// ConfigValidators requires at least one filter. The runner API rejects an
// unfiltered list, so refusing it at plan time is clearer than a 400.
//
// The organization filter counts under either of its two names, so this is not
// orgIDDataSourceConfigValidator: the organization is optional here, and
// requiring exactly one of the pair would break a namespace-only listing, which
// is a supported configuration today. What the pair still must not be is both at
// once, which the Conflicting validator covers. See org_id_deprecation.go.
func (d *runnersDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.AtLeastOneOf(
			path.MatchRoot("resource_class"),
			path.MatchRoot("namespace"),
			path.MatchRoot("organization_id"),
			path.MatchRoot("org_id"),
		),
		datasourcevalidator.Conflicting(
			path.MatchRoot("organization_id"),
			path.MatchRoot("org_id"),
		),
	}
}

// Schema defines the schema for the data source.
func (d *runnersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the self-hosted runner agents registered with CircleCI. " +
			"At least one of `resource_class`, `namespace` or `org_id` (or the deprecated " +
			"`organization_id`) must be set.\n\n" +
			"Available on CircleCI Cloud and CircleCI Server. On Server the runner API is served by " +
			"your own installation, so the provider's `runner_host` attribute must be set to your " +
			"Server hostname.\n\n" +
			"~> **This lists runner agents, not resource classes.** A runner only appears here once " +
			"its agent has connected at least once, so the list reflects live registration state and " +
			"changes outside Terraform.",
		Attributes: map[string]schema.Attribute{
			"resource_class": schema.StringAttribute{
				MarkdownDescription: "Only return runners in this resource class, in `namespace/name` " +
					"format (e.g. `myorg/myrunner`).",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(runnerResourceClassPattern, "must be in the format 'namespace/name'"),
				},
			},
			"namespace": schema.StringAttribute{
				MarkdownDescription: "Only return runners in this runner namespace.",
				Optional:            true,
			},
			// Only return runners owned by this organization. See
			// org_id_deprecation.go for why it is accepted under two names.
			"organization_id": runnerOrgIDDataSourceAttribute(
				deprecatedOrgIDDataSourceAttribute("runners"),
			),
			"org_id": runnerOrgIDDataSourceAttribute(
				orgIDDataSourceAttribute("runners"),
			),
			"runners": schema.ListNestedAttribute{
				MarkdownDescription: "The matching runner agents, in the order the API returned them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							MarkdownDescription: "The runner's name, as reported by the agent.",
							Computed:            true,
						},
						"hostname": schema.StringAttribute{
							MarkdownDescription: "Hostname of the machine the agent runs on.",
							Computed:            true,
						},
						"ip": schema.StringAttribute{
							MarkdownDescription: "IP address the agent last connected from.",
							Computed:            true,
						},
						"version": schema.StringAttribute{
							MarkdownDescription: "Version of the runner agent.",
							Computed:            true,
						},
						"status": schema.StringAttribute{
							MarkdownDescription: "The agent's status as reported by the API, for example `running` or `idle`.",
							Computed:            true,
						},
						"resource_class": schema.StringAttribute{
							MarkdownDescription: "The resource class the runner is registered to, in `namespace/name` format.",
							Computed:            true,
						},
						"first_connected": schema.StringAttribute{
							MarkdownDescription: "Timestamp of the agent's first connection.",
							Computed:            true,
						},
						"last_connected": schema.StringAttribute{
							MarkdownDescription: "Timestamp of the agent's most recent connection.",
							Computed:            true,
						},
						"last_used": schema.StringAttribute{
							MarkdownDescription: "Timestamp at which the runner last claimed a task.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// Read fetches the matching runners from the API.
func (d *runnersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config runnersDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	params := circleci.ListRunnersParams{
		ResourceClass: config.ResourceClass.ValueString(),
		Namespace:     config.Namespace.ValueString(),
		OrgID:         effectiveOrgID(config.OrganizationId, config.OrgId),
	}

	runners, err := d.client.ListRunners(ctx, params)
	if err != nil {
		// The SDK returns untyped errors, so a 404 cannot be told apart from any
		// other failure here. Report the error rather than guessing.
		resp.Diagnostics.AddError(
			"Error reading CircleCI runners",
			fmt.Sprintf("Could not list runners (%s): %s", runnerFilterSummary(params), err.Error()),
		)

		return
	}

	// An empty list is a valid answer, so keep the attribute an empty list rather
	// than null: practitioners iterate over it.
	config.Runners = make([]runnerItemModel, 0, len(runners))
	for _, r := range runners {
		config.Runners = append(config.Runners, runnerItemModel{
			Name:           types.StringValue(r.Name),
			Hostname:       types.StringValue(r.Hostname),
			IP:             types.StringValue(r.IP),
			Version:        types.StringValue(r.Version),
			Status:         types.StringValue(r.Status),
			ResourceClass:  types.StringValue(r.ResourceClass),
			FirstConnected: types.StringValue(r.FirstConnected),
			LastConnected:  types.StringValue(r.LastConnected),
			// last_used is null for an agent that has never claimed a task, which
			// is meaningfully different from the empty string.
			LastUsed: types.StringPointerValue(r.LastUsed),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// runnerFilterSummary renders the filters used for a list request, for error messages.
func runnerFilterSummary(params circleci.ListRunnersParams) string {
	summary := ""
	for _, filter := range []struct{ name, value string }{
		{"resource class", params.ResourceClass},
		{"namespace", params.Namespace},
		{"organization", params.OrgID},
	} {
		if filter.value == "" {
			continue
		}
		if summary != "" {
			summary += ", "
		}
		summary += fmt.Sprintf("%s %s", filter.name, filter.value)
	}

	if summary == "" {
		return "no filters"
	}

	return summary
}

// Configure adds the provider configured client to the data source.
func (d *runnersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
