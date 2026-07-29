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
	_ datasource.DataSource                     = &iosSigningConfigsDataSource{}
	_ datasource.DataSourceWithConfigure        = &iosSigningConfigsDataSource{}
	_ datasource.DataSourceWithConfigValidators = &iosSigningConfigsDataSource{}
)

// iosSigningConfigsDataSourceModel maps the data source schema.
type iosSigningConfigsDataSourceModel struct {
	OrganizationId types.String                `tfsdk:"organization_id"`
	OrgId          types.String                `tfsdk:"org_id"`
	Configs        []iosSigningConfigItemModel `tfsdk:"configs"`
}

// iosSigningConfigItemModel maps one signing configuration in the list.
type iosSigningConfigItemModel struct {
	Id                   types.String                       `tfsdk:"id"`
	Name                 types.String                       `tfsdk:"name"`
	CertificateId        types.String                       `tfsdk:"certificate_id"`
	CertificateFileName  types.String                       `tfsdk:"certificate_file_name"`
	CertificateType      types.String                       `tfsdk:"certificate_type"`
	ProvisioningProfiles []iosSigningConfigItemProfileModel `tfsdk:"provisioning_profiles"`
}

// iosSigningConfigItemProfileModel maps one provisioning profile in the list.
// Only file_name is reported: the profile's content is write-only, exactly
// like a certificate's cert_blob, and is never echoed back by the API.
type iosSigningConfigItemProfileModel struct {
	FileName types.String `tfsdk:"file_name"`
}

// NewIOSSigningConfigsDataSource is a helper function to simplify the provider implementation.
func NewIOSSigningConfigsDataSource() datasource.DataSource {
	return &iosSigningConfigsDataSource{}
}

// iosSigningConfigsDataSource is the data source implementation.
type iosSigningConfigsDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *iosSigningConfigsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ios_signing_configs"
}

// Schema defines the schema for the data source.
func (d *iosSigningConfigsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every iOS signing configuration in a CircleCI organization, " +
			"including configurations created outside Terraform. Pagination is followed " +
			"internally, so the result covers every configuration rather than one page.\n\n" +
			"~> **CircleCI Cloud only.** Signing configurations are served by the CircleCI v3 " +
			"API, which CircleCI Server does not route.\n\n" +
			"-> **There is no singular data source.** The API has no route to fetch a single " +
			"signing configuration by id; use this data source and match on `name` or `id` in " +
			"your configuration.\n\n" +
			"-> **Provisioning profile content is not returned.** Each profile's `.mobileprovision` " +
			"content is write-only -- only `file_name` is reported, exactly like " +
			"`circleci_ios_signing_certificate`'s content; see that resource's \"Security\" " +
			"section.",
		Attributes: map[string]schema.Attribute{
			// See org_id_deprecation.go for why the organization is accepted under
			// two names.
			"organization_id": deprecatedOrgIDDataSourceAttribute("iOS signing configurations"),
			"org_id":          orgIDDataSourceAttribute("iOS signing configurations"),
			"configs": schema.ListNestedAttribute{
				MarkdownDescription: "The organization's signing configurations, in the order " +
					"the API returned them.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the configuration.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The configuration's name.",
							Computed:            true,
						},
						"certificate_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the paired " +
								"signing certificate.",
							Computed: true,
						},
						"certificate_file_name": schema.StringAttribute{
							MarkdownDescription: "The paired certificate's display name.",
							Computed:            true,
						},
						"certificate_type": schema.StringAttribute{
							MarkdownDescription: "The paired certificate's type, `distribution` " +
								"or `development`.",
							Computed: true,
						},
						"provisioning_profiles": schema.ListNestedAttribute{
							MarkdownDescription: "The provisioning profiles paired with the " +
								"certificate.",
							Computed: true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"file_name": schema.StringAttribute{
										MarkdownDescription: "The profile's display name.",
										Computed:            true,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *iosSigningConfigsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (d *iosSigningConfigsDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		orgIDDataSourceConfigValidator(),
	}
}

// Read lists the organization's signing configurations.
func (d *iosSigningConfigsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, "circleci_ios_signing_configs", &resp.Diagnostics) {
		return
	}

	var state iosSigningConfigsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	organizationID := effectiveOrgID(state.OrganizationId, state.OrgId)

	configs, err := d.client.ListSigningConfigs(ctx, organizationID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI iOS signing configurations for organization "+organizationID,
			circleci.Detail(err),
		)

		return
	}

	// An empty, non-null list keeps `for_each` and `length()` working against an
	// organization that has no signing configurations yet.
	state.Configs = make([]iosSigningConfigItemModel, 0, len(configs))
	for _, cfg := range configs {
		profiles := make([]iosSigningConfigItemProfileModel, 0, len(cfg.ProvisioningProfiles))
		for _, p := range cfg.ProvisioningProfiles {
			profiles = append(profiles, iosSigningConfigItemProfileModel{
				FileName: types.StringValue(p.FileName),
			})
		}

		state.Configs = append(state.Configs, iosSigningConfigItemModel{
			Id:                   types.StringValue(cfg.ID),
			Name:                 types.StringValue(cfg.Name),
			CertificateId:        types.StringValue(cfg.CertificateID),
			CertificateFileName:  types.StringValue(cfg.CertificateFileName),
			CertificateType:      types.StringValue(cfg.CertificateType),
			ProvisioningProfiles: profiles,
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
