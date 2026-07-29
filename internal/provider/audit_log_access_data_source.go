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
	_ datasource.DataSource                     = &auditLogAccessDataSource{}
	_ datasource.DataSourceWithConfigure        = &auditLogAccessDataSource{}
	_ datasource.DataSourceWithConfigValidators = &auditLogAccessDataSource{}
)

// auditLogAccessDataSourceModel maps the data source schema.
type auditLogAccessDataSourceModel struct {
	OrganizationID types.String `tfsdk:"organization_id"`
	OrgID          types.String `tfsdk:"org_id"`
	HasAccess      types.Bool   `tfsdk:"has_access"`
}

// NewAuditLogAccessDataSource is a helper function to simplify the provider
// implementation.
func NewAuditLogAccessDataSource() datasource.DataSource {
	return &auditLogAccessDataSource{}
}

// auditLogAccessDataSource is the data source implementation.
type auditLogAccessDataSource struct {
	client *circleci.Client
}

func (d *auditLogAccessDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_audit_log_access"
}

func (d *auditLogAccessDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reports whether a CircleCI organization is entitled to audit log streaming " +
			"at all — i.e. whether it is on the required billing plan tier — as distinct from whether it " +
			"has actually configured a destination with `circleci_audit_log_config`.\n\n" +
			"Check this before creating a `circleci_audit_log_config`: creating one against an ineligible " +
			"organization fails with a 403 at apply time, and this data source lets a configuration " +
			"produce a clearer, earlier diagnostic instead.\n\n" +
			"~> **CircleCI Cloud only, and only on a Scale plan.** See `circleci_audit_log_config` for why.",
		Attributes: map[string]schema.Attribute{
			// See org_id_deprecation.go for why the organization is accepted under
			// two names.
			"organization_id": deprecatedOrgIDDataSourceAttribute("audit log access"),
			"org_id":          orgIDDataSourceAttribute("audit log access"),
			"has_access": schema.BoolAttribute{
				MarkdownDescription: "Whether the organization is entitled to audit log streaming.",
				Computed:            true,
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (d *auditLogAccessDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		orgIDDataSourceConfigValidator(),
	}
}

func (d *auditLogAccessDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, "circleci_audit_log_access", &resp.Diagnostics) {
		return
	}

	var config auditLogAccessDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(config.OrganizationID, config.OrgID)

	hasAccess, err := d.client.GetAuditLogAccess(ctx, organizationID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading CircleCI audit log access",
			fmt.Sprintf(
				"Could not check audit log access for organization %s: %s",
				organizationID, circleci.Detail(err),
			),
		)

		return
	}

	config.HasAccess = types.BoolValue(hasAccess)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

func (d *auditLogAccessDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
