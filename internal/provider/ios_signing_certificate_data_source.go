// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &iosSigningCertificateDataSource{}
	_ datasource.DataSourceWithConfigure = &iosSigningCertificateDataSource{}
)

// iosSigningCertificateDataSourceModel maps the data source schema.
//
// There is no name-based lookup: the API's only single-entity route is
// GET .../signing/certificates/{id}, so id is the sole input. Use
// circleci_ios_signing_certificates to resolve a certificate by file_name
// within an organization first.
type iosSigningCertificateDataSourceModel struct {
	Id             types.String `tfsdk:"id"`
	OrganizationId types.String `tfsdk:"organization_id"`
	FileName       types.String `tfsdk:"file_name"`
	CertType       types.String `tfsdk:"cert_type"`
	Fingerprint    types.String `tfsdk:"fingerprint"`
	CreatedAt      types.String `tfsdk:"created_at"`
	ExpiresAt      types.String `tfsdk:"expires_at"`
}

// NewIOSSigningCertificateDataSource is a helper function to simplify the provider implementation.
func NewIOSSigningCertificateDataSource() datasource.DataSource {
	return &iosSigningCertificateDataSource{}
}

// iosSigningCertificateDataSource is the data source implementation.
type iosSigningCertificateDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *iosSigningCertificateDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ios_signing_certificate"
}

// Schema defines the schema for the data source.
func (d *iosSigningCertificateDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches an iOS signing certificate by id. The certificate's `.p12` " +
			"content and password are never returned by the API, so they are not attributes here " +
			"-- see `circleci_ios_signing_certificate`'s \"Security\" section.\n\n" +
			"~> **CircleCI Cloud only.** Signing certificates are served by the CircleCI v3 API, " +
			"which CircleCI Server does not route.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the certificate.",
				Required:            true,
			},
			"organization_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization the " +
					"certificate belongs to.",
				Computed: true,
			},
			"file_name": schema.StringAttribute{
				MarkdownDescription: "The certificate's display name.",
				Computed:            true,
			},
			"cert_type": schema.StringAttribute{
				MarkdownDescription: "The certificate's type, `distribution` or `development`.",
				Computed:            true,
			},
			"fingerprint": schema.StringAttribute{
				MarkdownDescription: "The certificate's fingerprint.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the certificate was uploaded, as an RFC 3339 " +
					"timestamp with millisecond precision.",
				Computed: true,
			},
			"expires_at": schema.StringAttribute{
				MarkdownDescription: "When the certificate expires, as an RFC 3339 timestamp " +
					"with millisecond precision, or an empty string if CircleCI could not " +
					"determine an expiry from the certificate.",
				Computed: true,
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *iosSigningCertificateDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// Read fetches the certificate and sets the data source state.
func (d *iosSigningCertificateDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, "circleci_ios_signing_certificate", &resp.Diagnostics) {
		return
	}

	var config iosSigningCertificateDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cert, err := d.client.GetSigningCertificate(ctx, config.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI iOS signing certificate "+config.Id.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	state := iosSigningCertificateDataSourceModel{
		Id:             types.StringValue(cert.ID),
		OrganizationId: types.StringValue(cert.OrganizationID),
		FileName:       types.StringValue(cert.FileName),
		CertType:       types.StringValue(cert.CertType),
		Fingerprint:    types.StringValue(cert.Fingerprint),
		CreatedAt:      types.StringValue(stringOrEmpty(cert.CreatedAt)),
		ExpiresAt:      types.StringValue(stringOrEmpty(cert.ExpiresAt)),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
