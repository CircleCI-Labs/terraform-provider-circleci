// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource                     = &orbVersionDataSource{}
	_ datasource.DataSourceWithConfigure        = &orbVersionDataSource{}
	_ datasource.DataSourceWithConfigValidators = &orbVersionDataSource{}
)

// orbVersionDataSourceModel maps the data source schema.
type orbVersionDataSourceModel struct {
	Id        types.String `tfsdk:"id"`
	OrbId     types.String `tfsdk:"orb_id"`
	OrbName   types.String `tfsdk:"orb_name"`
	Version   types.String `tfsdk:"version"`
	Source    types.String `tfsdk:"source"`
	CreatedAt types.String `tfsdk:"created_at"`
}

// NewOrbVersionDataSource is a helper function to simplify the provider implementation.
func NewOrbVersionDataSource() datasource.DataSource {
	return &orbVersionDataSource{}
}

// orbVersionDataSource is the data source implementation.
type orbVersionDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *orbVersionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_orb_version"
}

// Schema defines the schema for the data source.
func (d *orbVersionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up one published version of a CircleCI orb, by version id, or " +
			"by orb id plus a version reference.\n\n" +
			"The version reference may be an exact version such as `1.2.3`, a `dev:<label>` " +
			"version, or an alias such as `volatile` for whatever the newest version is. Use this " +
			"to read the published source, or to resolve the id needed to import a " +
			"`circleci_orb_version`.\n\n" +
			"~> **CircleCI Cloud only.** Orb versions are served by the CircleCI v3 API, which " +
			"CircleCI Server does not route.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the orb version. Set either `id`, " +
					"or `orb_id` together with `version`.",
				Optional: true,
				Computed: true,
			},
			"orb_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the orb the version belongs to. " +
					"Required unless `id` is set, because the version collection is always scoped " +
					"to one orb.",
				Optional: true,
				Computed: true,
			},
			"version": schema.StringAttribute{
				MarkdownDescription: "The version reference to resolve, such as `1.2.3`, " +
					"`dev:alpha` or `volatile`. Required unless `id` is set.",
				Optional: true,
				Computed: true,
			},
			"orb_name": schema.StringAttribute{
				MarkdownDescription: "Fully qualified name of the orb, `<namespace>/<orb>`.",
				Computed:            true,
			},
			"source": schema.StringAttribute{
				MarkdownDescription: "The YAML source CircleCI stored for this version.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the version was published, as an RFC 3339 timestamp.",
				Computed:            true,
			},
		},
	}
}

// ConfigValidators enforces the two alternative lookup forms: an id on its own,
// or an orb id together with a version reference.
func (d *orbVersionDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("id"),
			path.MatchRoot("orb_id"),
		),
		datasourcevalidator.RequiredTogether(
			path.MatchRoot("orb_id"),
			path.MatchRoot("version"),
		),
	}
}

// Configure adds the provider configured client to the data source.
func (d *orbVersionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}

// Read fetches the orb version and sets the data source state.
func (d *orbVersionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloud(d.client, "circleci_orb_version", &resp.Diagnostics) {
		return
	}

	var config orbVersionDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var (
		version *circleci.OrbVersion
		err     error
	)
	if id := config.Id.ValueString(); id != "" {
		version, err = d.client.GetOrbVersion(ctx, id)
	} else {
		version, err = d.client.GetOrbVersionByRef(ctx, config.OrbId.ValueString(), config.Version.ValueString())
	}

	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI orb version",
			circleci.Detail(err),
		)

		return
	}

	source := version.Source
	if source == "" {
		source, err = d.client.GetOrbSource(ctx, version.ID)
		if err != nil {
			resp.Diagnostics.AddError(
				"Unable to read the source of CircleCI orb version "+version.Version,
				circleci.Detail(err),
			)

			return
		}
	}

	state := orbVersionDataSourceModel{
		Id:        types.StringValue(version.ID),
		OrbId:     types.StringValue(version.OrbID),
		OrbName:   types.StringValue(version.OrbName),
		Version:   types.StringValue(version.Version),
		Source:    types.StringValue(source),
		CreatedAt: types.StringValue(version.CreatedAt),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
