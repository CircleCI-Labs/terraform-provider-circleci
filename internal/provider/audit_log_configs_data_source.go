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
	_ datasource.DataSource                     = &auditLogConfigsDataSource{}
	_ datasource.DataSourceWithConfigure        = &auditLogConfigsDataSource{}
	_ datasource.DataSourceWithConfigValidators = &auditLogConfigsDataSource{}
)

// auditLogConfigsDataSourceModel maps the data source schema.
type auditLogConfigsDataSourceModel struct {
	OrganizationID types.String              `tfsdk:"organization_id"`
	OrgID          types.String              `tfsdk:"org_id"`
	Configs        []auditLogConfigItemModel `tfsdk:"audit_log_configs"`
}

// auditLogConfigItemModel maps one config in the list.
type auditLogConfigItemModel struct {
	ID               types.String `tfsdk:"id"`
	TargetType       types.String `tfsdk:"target_type"`
	IsDisabled       types.Bool   `tfsdk:"is_disabled"`
	ARN              types.String `tfsdk:"arn"`
	Region           types.String `tfsdk:"region"`
	BucketName       types.String `tfsdk:"bucket_name"`
	BucketPrefix     types.String `tfsdk:"bucket_prefix"`
	Endpoint         types.String `tfsdk:"endpoint"`
	ConnectionStatus types.String `tfsdk:"connection_status"`
	CreatedBy        types.String `tfsdk:"created_by"`
	CreatedAt        types.String `tfsdk:"created_at"`
	UpdatedAt        types.String `tfsdk:"updated_at"`
}

// NewAuditLogConfigsDataSource is a helper function to simplify the provider
// implementation.
func NewAuditLogConfigsDataSource() datasource.DataSource {
	return &auditLogConfigsDataSource{}
}

// auditLogConfigsDataSource is the data source implementation.
type auditLogConfigsDataSource struct {
	client *circleci.Client
}

func (d *auditLogConfigsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_audit_log_configs"
}

func (d *auditLogConfigsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every audit log streaming config configured for a CircleCI " +
			"organization, including configs created outside Terraform. An organization has at most one " +
			"config per `target_type`, so this never needs paginating.\n\n" +
			"~> **CircleCI Cloud only, and only on a Scale plan.** See `circleci_audit_log_config` for why.",
		Attributes: map[string]schema.Attribute{
			// See org_id_deprecation.go for why the organization is accepted under
			// two names.
			"organization_id": deprecatedOrgIDDataSourceAttribute("audit log configs"),
			"org_id":          orgIDDataSourceAttribute("audit log configs"),
			"audit_log_configs": schema.ListNestedAttribute{
				MarkdownDescription: "The organization's audit log streaming configs, in the order the API " +
					"returned them.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the config.",
							Computed:            true,
						},
						"target_type": schema.StringAttribute{
							MarkdownDescription: "The destination type: `" + circleci.AuditLogTargetTypeS3 +
								"` or `" + circleci.AuditLogTargetTypeS3Compatible + "`.",
							Computed: true,
						},
						"is_disabled": schema.BoolAttribute{
							MarkdownDescription: "Whether streaming is turned off.",
							Computed:            true,
						},
						"arn": schema.StringAttribute{
							MarkdownDescription: "The AWS IAM role CircleCI assumes to write to the bucket.",
							Computed:            true,
						},
						"region": schema.StringAttribute{
							MarkdownDescription: "The bucket's AWS region. Null when unset for an " +
								"`" + circleci.AuditLogTargetTypeS3Compatible + "` config.",
							Computed: true,
						},
						"bucket_name": schema.StringAttribute{
							MarkdownDescription: "The destination bucket.",
							Computed:            true,
						},
						"bucket_prefix": schema.StringAttribute{
							MarkdownDescription: "The key prefix under the bucket. Null when unset.",
							Computed:            true,
						},
						"endpoint": schema.StringAttribute{
							MarkdownDescription: "The S3-compatible endpoint URL. Null for an " +
								"`" + circleci.AuditLogTargetTypeS3 + "` config.",
							Computed: true,
						},
						"connection_status": schema.StringAttribute{
							MarkdownDescription: "CircleCI's report of the most recent delivery attempts: " +
								"`CONNECTED`, `DISCONNECTED`, `UNKNOWN` or `DISABLED`.",
							Computed: true,
						},
						"created_by": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the user who created the config.",
							Computed:            true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "When the config was created, as an RFC 3339 timestamp.",
							Computed:            true,
						},
						"updated_at": schema.StringAttribute{
							MarkdownDescription: "When the config was last updated, as an RFC 3339 timestamp.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (d *auditLogConfigsDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		orgIDDataSourceConfigValidator(),
	}
}

func (d *auditLogConfigsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, "circleci_audit_log_configs", &resp.Diagnostics) {
		return
	}

	var config auditLogConfigsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(config.OrganizationID, config.OrgID)

	configs, err := d.client.ListAuditLogConfigs(ctx, organizationID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading CircleCI audit log configs",
			fmt.Sprintf(
				"Could not list audit log configs for organization %s: %s",
				organizationID, circleci.Detail(err),
			),
		)

		return
	}

	// An empty list is a valid answer, so keep the attribute an empty list
	// rather than null: practitioners iterate over it.
	config.Configs = make([]auditLogConfigItemModel, 0, len(configs))

	for _, c := range configs {
		item := auditLogConfigItemModel{
			ID:               types.StringValue(c.ID),
			TargetType:       types.StringValue(c.TargetType),
			IsDisabled:       types.BoolValue(c.IsDisabled),
			ARN:              types.StringValue(c.Config.ARN),
			BucketName:       types.StringValue(c.Config.BucketName),
			ConnectionStatus: types.StringValue(c.ConnectionStatus),
			CreatedBy:        types.StringValue(c.CreatedBy),
			CreatedAt:        types.StringValue(c.CreatedAt),
			UpdatedAt:        types.StringValue(c.UpdatedAt),
			Region:           types.StringNull(),
			BucketPrefix:     types.StringNull(),
			Endpoint:         types.StringNull(),
		}

		if c.Config.Region != "" {
			item.Region = types.StringValue(c.Config.Region)
		}
		if c.Config.BucketPrefix != "" {
			item.BucketPrefix = types.StringValue(c.Config.BucketPrefix)
		}
		if c.Config.Endpoint != "" {
			item.Endpoint = types.StringValue(c.Config.Endpoint)
		}

		config.Configs = append(config.Configs, item)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

func (d *auditLogConfigsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
