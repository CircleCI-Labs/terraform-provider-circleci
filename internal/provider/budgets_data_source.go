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
	_ datasource.DataSource                     = &budgetsDataSource{}
	_ datasource.DataSourceWithConfigure        = &budgetsDataSource{}
	_ datasource.DataSourceWithConfigValidators = &budgetsDataSource{}
)

// budgetsDataSourceModel maps the data source schema.
type budgetsDataSourceModel struct {
	OrganizationID types.String      `tfsdk:"organization_id"`
	OrgID          types.String      `tfsdk:"org_id"`
	Budgets        []budgetItemModel `tfsdk:"budgets"`
}

// budgetItemModel maps one budget in the list.
type budgetItemModel struct {
	ID                types.String  `tfsdk:"id"`
	ProjectID         types.String  `tfsdk:"project_id"`
	Credits           types.Int64   `tfsdk:"credits"`
	EnforcementType   types.String  `tfsdk:"enforcement_type"`
	Consumption       types.Int64   `tfsdk:"consumption"`
	Percentage        types.Float64 `tfsdk:"percentage"`
	ThresholdExceeded types.Bool    `tfsdk:"threshold_exceeded"`
}

// NewBudgetsDataSource is a helper function to simplify the provider
// implementation.
func NewBudgetsDataSource() datasource.DataSource {
	return &budgetsDataSource{}
}

// budgetsDataSource is the data source implementation.
type budgetsDataSource struct {
	client *circleci.Client
}

func (d *budgetsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_budgets"
}

func (d *budgetsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every spend budget configured for a CircleCI organization: the " +
			"organization-level budget (`project_id` null), if one is set, and any per-project budgets, " +
			"including budgets created outside Terraform. There is no pagination on this route — the " +
			"whole set comes back in one response.\n\n" +
			"~> **CircleCI Cloud only, and undocumented.** See `circleci_budget` for why.",
		Attributes: map[string]schema.Attribute{
			// See org_id_deprecation.go for why the organization is accepted under
			// two names.
			"organization_id": deprecatedOrgIDDataSourceAttribute("spend budgets"),
			"org_id":          orgIDDataSourceAttribute("spend budgets"),
			"budgets": schema.ListNestedAttribute{
				MarkdownDescription: "The organization's spend budgets, in the order the API returned them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the budget.",
							Computed:            true,
						},
						"project_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the project this budget " +
								"limits. Null for the organization-level budget.",
							Computed: true,
						},
						"credits": schema.Int64Attribute{
							MarkdownDescription: "The credit limit for this scope.",
							Computed:            true,
						},
						"enforcement_type": schema.StringAttribute{
							MarkdownDescription: "What happens once spend crosses this budget: " +
								"`" + circleci.BudgetEnforcementWarn + "` or " +
								"`" + circleci.BudgetEnforcementBlock + "`.",
							Computed: true,
						},
						"consumption": schema.Int64Attribute{
							MarkdownDescription: "Credits consumed against this budget so far.",
							Computed:            true,
						},
						"percentage": schema.Float64Attribute{
							MarkdownDescription: "`consumption` as a percentage of `credits`.",
							Computed:            true,
						},
						"threshold_exceeded": schema.BoolAttribute{
							MarkdownDescription: "Whether this budget's enforcement threshold is currently " +
								"exceeded.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (d *budgetsDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		orgIDDataSourceConfigValidator(),
	}
}

func (d *budgetsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, "circleci_budgets", &resp.Diagnostics) {
		return
	}

	var config budgetsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(config.OrganizationID, config.OrgID)

	budgets, err := d.client.ListBudgets(ctx, organizationID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading CircleCI budgets",
			fmt.Sprintf(
				"Could not list spend budgets for organization %s: %s",
				organizationID, circleci.Detail(err),
			),
		)

		return
	}

	// An empty list is a valid answer (no budgets configured), so keep the
	// attribute an empty list rather than null: practitioners iterate over it.
	config.Budgets = make([]budgetItemModel, 0, len(budgets))

	for _, b := range budgets {
		item := budgetItemModel{
			ID:                types.StringValue(b.BudgetID),
			Credits:           types.Int64Value(int64(b.Credits)),
			EnforcementType:   types.StringValue(b.EnforcementType),
			Consumption:       types.Int64Value(int64(b.Consumption)),
			Percentage:        types.Float64Value(b.Percentage),
			ThresholdExceeded: types.BoolValue(b.ThresholdExceeded),
			ProjectID:         types.StringNull(),
		}

		if b.ProjectID != nil {
			item.ProjectID = types.StringValue(*b.ProjectID)
		}

		config.Budgets = append(config.Budgets, item)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

func (d *budgetsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
