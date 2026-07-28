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
	_ datasource.DataSource              = &otelExportersDataSource{}
	_ datasource.DataSourceWithConfigure = &otelExportersDataSource{}
)

// otelExportersDataSourceModel maps the data source schema.
type otelExportersDataSourceModel struct {
	OrganizationID types.String            `tfsdk:"organization_id"`
	Exporters      []otelExporterItemModel `tfsdk:"exporters"`
}

// otelExporterItemModel maps one exporter in the list.
type otelExporterItemModel struct {
	ID       types.String `tfsdk:"id"`
	Endpoint types.String `tfsdk:"endpoint"`
	Protocol types.String `tfsdk:"protocol"`
	Insecure types.Bool   `tfsdk:"insecure"`
	Headers  types.Map    `tfsdk:"headers"`
	Issues   types.List   `tfsdk:"issues"`
}

// NewOTelExportersDataSource is a helper function to simplify the provider implementation.
func NewOTelExportersDataSource() datasource.DataSource {
	return &otelExportersDataSource{}
}

// otelExportersDataSource is the data source implementation.
type otelExportersDataSource struct {
	client *circleci.Client
}

func (d *otelExportersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_otel_exporters"
}

func (d *otelExportersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every OTLP exporter configured for a CircleCI organization, " +
			"including exporters created outside Terraform. " +
			"Available on CircleCI Cloud and CircleCI Server.\n\n" +
			otelExperimentalNote + "\n\n" +
			"~> **Header values are not returned.** CircleCI encrypts them at rest and reports every " +
			"value as `" + circleci.OTelRedactedHeaderValue + "`. Only the header names are usable.",
		Attributes: map[string]schema.Attribute{
			"organization_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization whose exporters are " +
					"listed.",
				Required: true,
			},
			"exporters": schema.ListNestedAttribute{
				MarkdownDescription: fmt.Sprintf(
					"The organization's OTLP exporters, in the order the API returned them. "+
						"There are at most %d.",
					circleci.OTelExporterLimit,
				),
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the exporter.",
							Computed:            true,
						},
						"endpoint": schema.StringAttribute{
							MarkdownDescription: "The OTLP endpoint spans are sent to, as `host:port`.",
							Computed:            true,
						},
						"protocol": schema.StringAttribute{
							MarkdownDescription: "The OTLP transport: `" + circleci.OTelProtocolGRPC +
								"` or `" + circleci.OTelProtocolHTTP + "`.",
							Computed: true,
						},
						"insecure": schema.BoolAttribute{
							MarkdownDescription: "Whether the exporter connects without transport security.",
							Computed:            true,
						},
						"headers": schema.MapAttribute{
							MarkdownDescription: "The names of the extra headers sent with each export. " +
								"Every value reads back as `" + circleci.OTelRedactedHeaderValue +
								"`, never the configured secret.",
							ElementType: types.StringType,
							Computed:    true,
							Sensitive:   true,
						},
						"issues": schema.ListAttribute{
							MarkdownDescription: "Validation problems CircleCI has detected with this " +
								"exporter, such as an endpoint that no longer resolves. Empty when there " +
								"are none.",
							ElementType: types.StringType,
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *otelExportersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config otelExportersDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	exporters, err := d.client.ListOTelExporters(ctx, config.OrganizationID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading CircleCI OTLP exporters",
			fmt.Sprintf(
				"Could not list OTLP exporters for organization %s: %s",
				config.OrganizationID.ValueString(), circleci.Detail(err),
			),
		)

		return
	}

	// An empty list is a valid answer, so keep the attribute an empty list rather
	// than null: practitioners iterate over it.
	config.Exporters = make([]otelExporterItemModel, 0, len(exporters))

	for _, exporter := range exporters {
		headers := types.MapNull(types.StringType)
		if len(exporter.Headers) > 0 {
			value, diags := types.MapValueFrom(ctx, types.StringType, exporter.Headers)
			resp.Diagnostics.Append(diags...)
			if resp.Diagnostics.HasError() {
				return
			}

			headers = value
		}

		issues, diags := otelIssuesValue(ctx, exporter.Issues)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}

		config.Exporters = append(config.Exporters, otelExporterItemModel{
			ID:       types.StringValue(exporter.ID),
			Endpoint: types.StringValue(exporter.Endpoint),
			Protocol: types.StringValue(exporter.Protocol),
			Insecure: types.BoolValue(exporter.Insecure),
			Headers:  headers,
			Issues:   issues,
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

func (d *otelExportersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
