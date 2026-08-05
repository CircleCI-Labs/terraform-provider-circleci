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
	_ datasource.DataSource                     = &iosSigningCertificatesDataSource{}
	_ datasource.DataSourceWithConfigure        = &iosSigningCertificatesDataSource{}
	_ datasource.DataSourceWithConfigValidators = &iosSigningCertificatesDataSource{}
)

// iosSigningCertificatesDataSourceModel maps the data source schema.
type iosSigningCertificatesDataSourceModel struct {
	OrganizationId types.String                     `tfsdk:"organization_id"`
	OrgId          types.String                     `tfsdk:"org_id"`
	Certificates   []iosSigningCertificateItemModel `tfsdk:"certificates"`
}

// iosSigningCertificateItemModel maps one certificate in the list.
type iosSigningCertificateItemModel struct {
	Id          types.String `tfsdk:"id"`
	FileName    types.String `tfsdk:"file_name"`
	CertType    types.String `tfsdk:"cert_type"`
	Fingerprint types.String `tfsdk:"fingerprint"`
	CreatedAt   types.String `tfsdk:"created_at"`
	ExpiresAt   types.String `tfsdk:"expires_at"`
}

// NewIOSSigningCertificatesDataSource is a helper function to simplify the provider implementation.
func NewIOSSigningCertificatesDataSource() datasource.DataSource {
	return &iosSigningCertificatesDataSource{}
}

// iosSigningCertificatesDataSource is the data source implementation.
type iosSigningCertificatesDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *iosSigningCertificatesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ios_signing_certificates"
}

// Schema defines the schema for the data source.
func (d *iosSigningCertificatesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every iOS signing certificate uploaded to a CircleCI " +
			"organization, including certificates uploaded outside Terraform.\n\n" +
			"Pagination is followed internally. The signing routes do not paginate today -- the " +
			"handler renders the whole collection with no cursor set, so the response carries no " +
			"page object at all -- but a cursor would be followed if one appeared, so the whole " +
			"collection is covered either way.\n\n" +
			"~> **CircleCI Cloud only.** Signing certificates are served by the CircleCI v3 API, " +
			"which CircleCI Server does not route.\n\n" +
			"-> **Certificate content is not returned.** Neither this data source nor the API " +
			"can report a certificate's `.p12` content or password -- see " +
			"`circleci_ios_signing_certificate`'s \"Security\" section.",
		Attributes: map[string]schema.Attribute{
			// See org_id_deprecation.go for why the organization is accepted under
			// two names.
			"organization_id": deprecatedOrgIDDataSourceAttribute("iOS signing certificates"),
			"org_id":          orgIDDataSourceAttribute("iOS signing certificates"),
			"certificates": schema.ListNestedAttribute{
				MarkdownDescription: "The organization's signing certificates, in the order the " +
					"API returned them.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the certificate.",
							Computed:            true,
						},
						"file_name": schema.StringAttribute{
							MarkdownDescription: "The certificate's display name.",
							Computed:            true,
						},
						"cert_type": schema.StringAttribute{
							MarkdownDescription: "The certificate's type: one of `distribution`, " +
								"`development`, `developer-id-application`, " +
								"`developer-id-installer`, `mac-development`, " +
								"`mac-app-distribution` or `mac-installer-distribution`.",
							Computed: true,
						},
						"fingerprint": schema.StringAttribute{
							MarkdownDescription: "The certificate's fingerprint.",
							Computed:            true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "When the certificate was uploaded, as an " +
								"RFC 3339 timestamp with millisecond precision.",
							Computed: true,
						},
						"expires_at": schema.StringAttribute{
							MarkdownDescription: "When the certificate expires, as an RFC 3339 " +
								"timestamp with millisecond precision, or an empty string if " +
								"CircleCI could not determine an expiry from the certificate.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *iosSigningCertificatesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (d *iosSigningCertificatesDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		orgIDDataSourceConfigValidator(),
	}
}

// Read lists the organization's signing certificates.
func (d *iosSigningCertificatesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, "circleci_ios_signing_certificates", &resp.Diagnostics) {
		return
	}

	var state iosSigningCertificatesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(state.OrganizationId, state.OrgId)

	certs, err := d.client.ListSigningCertificates(ctx, organizationID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI iOS signing certificates for organization "+organizationID,
			circleci.Detail(err),
		)

		return
	}

	// An empty, non-null list keeps `for_each` and `length()` working against an
	// organization that has no certificates yet.
	state.Certificates = make([]iosSigningCertificateItemModel, 0, len(certs))
	for _, cert := range certs {
		state.Certificates = append(state.Certificates, iosSigningCertificateItemModel{
			Id:          types.StringValue(cert.ID),
			FileName:    types.StringValue(cert.FileName),
			CertType:    types.StringValue(cert.CertType),
			Fingerprint: types.StringValue(cert.Fingerprint),
			CreatedAt:   types.StringValue(stringOrEmpty(cert.CreatedAt)),
			ExpiresAt:   types.StringValue(stringOrEmpty(cert.ExpiresAt)),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
