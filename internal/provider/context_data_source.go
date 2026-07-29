// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource                     = &ContextDataSource{}
	_ datasource.DataSourceWithConfigure        = &ContextDataSource{}
	_ datasource.DataSourceWithConfigValidators = &ContextDataSource{}
)

// contextDataSourceModel maps the output schema.
type contextDataSourceModel struct {
	Id             types.String                 `tfsdk:"id"`
	Name           types.String                 `tfsdk:"name"`
	OrganizationId types.String                 `tfsdk:"organization_id"`
	OrgId          types.String                 `tfsdk:"org_id"`
	CreatedAt      types.String                 `tfsdk:"created_at"`
	Restrictions   []restrictionDataSourceModel `tfsdk:"restrictions"`
}

type restrictionDataSourceModel struct {
	Id        types.String `tfsdk:"id"`
	ProjectId types.String `tfsdk:"project_id"`
	Name      types.String `tfsdk:"name"`
	Type      types.String `tfsdk:"type"`
	Value     types.String `tfsdk:"value"`
}

// NewContextDataSource is a helper function to simplify the provider implementation.
func NewContextDataSource() datasource.DataSource {
	return &ContextDataSource{}
}

// contextDataSource is the data source implementation.
type ContextDataSource struct {
	client *circleci.Client
}

// resolveContext finds the context the configuration identifies, by id or by name.
func (d *ContextDataSource) resolveContext(
	ctx context.Context,
	config contextDataSourceModel,
) (*circleci.Context, diag.Diagnostics) {
	var diags diag.Diagnostics

	if !config.Id.IsNull() && config.Id.ValueString() != "" {
		found, err := d.client.GetContext(ctx, config.Id.ValueString())
		if err != nil {
			diags.AddError(
				"Unable to read CircleCI context "+config.Id.ValueString(),
				circleci.Detail(err),
			)

			return nil, diags
		}

		return found, diags
	}

	name := config.Name.ValueString()
	organizationID := effectiveOrgID(config.OrganizationId, config.OrgId)

	found, err := d.client.FindContextByName(ctx, organizationID, name)
	if err != nil {
		if circleci.IsNotFound(err) {
			diags.AddError(
				fmt.Sprintf("No CircleCI context named %q", name),
				fmt.Sprintf(
					"Organization %s has no context named %q. Context names are matched exactly and "+
						"are case-sensitive.",
					organizationID, name,
				),
			)

			return nil, diags
		}

		diags.AddError(
			fmt.Sprintf("Unable to look up the CircleCI context named %q", name),
			circleci.Detail(err),
		)

		return nil, diags
	}

	return found, diags
}

// ConfigValidators requires exactly one identifier, and an organization when
// looking up by name.
//
// The organization is deliberately not validated with
// orgIDDataSourceConfigValidator: it is only needed for a lookup by name, so
// requiring exactly one of `organization_id` and `org_id` would break the
// lookup by id, which names neither. What is required instead is that a lookup
// by name carries one of the two — expressed as "either name and
// organization_id are configured together, or name and org_id are" — and that
// the pair is never set at once. See org_id_deprecation.go.
func (d *ContextDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("id"),
			path.MatchRoot("name"),
		),
		datasourcevalidator.Conflicting(
			path.MatchRoot("organization_id"),
			path.MatchRoot("org_id"),
		),
		datasourcevalidator.Any(
			datasourcevalidator.RequiredTogether(
				path.MatchRoot("name"),
				path.MatchRoot("organization_id"),
			),
			datasourcevalidator.RequiredTogether(
				path.MatchRoot("name"),
				path.MatchRoot("org_id"),
			),
		),
	}
}

// Metadata returns the data source type name.
func (d *ContextDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_context"
}

// Schema defines the schema for the data source.
func (d *ContextDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches information about a CircleCI context, including its restrictions.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The ID of the context. Set either this or `name`.",
				Optional:            true,
				Computed:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the context. Set either this or `id`. " +
					"Looking a context up by name also requires the organization, as `org_id` (or the " +
					"deprecated `organization_id`), because the API has no lookup-by-name route: the " +
					"provider lists the organization's contexts and matches on the name, which is " +
					"unique within an organization. Neither is needed when `id` is set, and both are " +
					"ignored in that case.",
				Optional: true,
				Computed: true,
			},
			// See org_id_deprecation.go for why the organization is accepted under
			// two names.
			"organization_id": deprecatedOrgIDDataSourceAttribute("contexts"),
			"org_id":          orgIDDataSourceAttribute("contexts"),
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the context was created.",
				Computed:            true,
			},
			"restrictions": schema.ListNestedAttribute{
				MarkdownDescription: "The access restrictions for this context.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "The unique identifier of the restriction.",
							Computed:            true,
						},
						"project_id": schema.StringAttribute{
							MarkdownDescription: "The project ID associated with the restriction.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The name associated with the restriction.",
							Computed:            true,
						},
						"type": schema.StringAttribute{
							MarkdownDescription: "The type of restriction.",
							Computed:            true,
						},
						"value": schema.StringAttribute{
							MarkdownDescription: "The value associated with the restriction type.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// Read refreshes the Terraform state with the latest data.
func (d *ContextDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var contextState contextDataSourceModel
	diags := req.Config.Get(ctx, &contextState)
	if diags != nil {
		resp.Diagnostics.Append(diags...)
		return
	}

	found, diags := d.resolveContext(ctx, contextState)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	restrictions, err := d.client.ListContextRestrictions(ctx, found.ID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI context restrictions for "+found.ID,
			circleci.Detail(err),
		)
		return
	}

	// Fill restrictions
	restrictionsAttributeValues := make([]restrictionDataSourceModel, len(restrictions))
	for index, elem := range restrictions {
		restrictionsAttributeValues[index] =
			restrictionDataSourceModel{
				Id:        types.StringValue(elem.ID),
				Name:      types.StringValue(elem.Name),
				ProjectId: types.StringValue(elem.ProjectID),
				Type:      types.StringValue(elem.RestrictionType),
				Value:     types.StringValue(elem.RestrictionValue),
			}
	}

	// Map response body to model, preserving the organization the caller supplied:
	// the API does not report a context's owner, so echoing it back keeps the
	// configuration and the state consistent. Both names are echoed exactly as
	// configured — filling in the one the configuration left out would report a
	// value Terraform never read from the configuration for an attribute that is
	// Optional rather than Computed.
	contextState = contextDataSourceModel{
		Id:             types.StringValue(found.ID),
		Name:           types.StringValue(found.Name),
		OrganizationId: contextState.OrganizationId,
		OrgId:          contextState.OrgId,
		CreatedAt:      types.StringValue(found.CreatedAt),
		Restrictions:   restrictionsAttributeValues,
	}

	// Set state
	diags = resp.State.Set(ctx, &contextState)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Configure adds the provider configured client to the data source.
func (d *ContextDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
