// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &checkoutKeysDataSource{}
	_ datasource.DataSourceWithConfigure = &checkoutKeysDataSource{}
)

// checkoutKeysDataSourceModel maps the data source schema.
type checkoutKeysDataSourceModel struct {
	ProjectSlug  types.String           `tfsdk:"project_slug"`
	Digest       types.String           `tfsdk:"digest"`
	CheckoutKeys []checkoutKeyItemModel `tfsdk:"checkout_keys"`
}

// checkoutKeyItemModel maps one checkout key in the list.
type checkoutKeyItemModel struct {
	Fingerprint types.String `tfsdk:"fingerprint"`
	Type        types.String `tfsdk:"type"`
	PublicKey   types.String `tfsdk:"public_key"`
	Preferred   types.Bool   `tfsdk:"preferred"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// NewCheckoutKeysDataSource is a helper function to simplify the provider implementation.
func NewCheckoutKeysDataSource() datasource.DataSource {
	return &checkoutKeysDataSource{}
}

// checkoutKeysDataSource is the data source implementation.
type checkoutKeysDataSource struct {
	client *circleci.Client
}

func (d *checkoutKeysDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_checkout_keys"
}

func (d *checkoutKeysDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every checkout key of a CircleCI project. " +
			"Available on CircleCI Cloud and CircleCI Server.\n\n" +
			"~> **Not available for GitLab or GitHub App projects.** The CircleCI API only exposes checkout " +
			"keys for projects integrated through GitHub OAuth or Bitbucket.",
		Attributes: map[string]schema.Attribute{
			"project_slug": schema.StringAttribute{
				MarkdownDescription: "The project slug in the format `vcs-type/org-name/repo-name`, " +
					"for example `github/my-org/my-repo`.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						checkoutKeyProjectSlugPattern,
						"must be in the format 'vcs-type/org-name/repo-name'",
					),
				},
			},
			"digest": schema.StringAttribute{
				MarkdownDescription: "Which fingerprint digest to return: `md5` (the API default) or " +
					"`sha256`. Note that `circleci_checkout_key` records the MD5 fingerprint.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.OneOf(
						circleci.CheckoutKeyDigestMD5,
						circleci.CheckoutKeyDigestSHA256,
					),
				},
			},
			"checkout_keys": schema.ListNestedAttribute{
				MarkdownDescription: "The project's checkout keys, in the order the API returned them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"fingerprint": schema.StringAttribute{
							MarkdownDescription: "The key's fingerprint, in the requested `digest`.",
							Computed:            true,
						},
						"type": schema.StringAttribute{
							MarkdownDescription: "The type of checkout key, as reported by the API: " +
								"`deploy-key` or `github-user-key`. Note that a key created with " +
								"`type = \"user-key\"` is reported as `github-user-key`.",
							Computed: true,
						},
						"public_key": schema.StringAttribute{
							MarkdownDescription: "The public half of the SSH key.",
							Computed:            true,
						},
						"preferred": schema.BoolAttribute{
							MarkdownDescription: "Whether CircleCI prefers this key when checking the " +
								"project out.",
							Computed: true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "The timestamp when the checkout key was created.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *checkoutKeysDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config checkoutKeysDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	keys, err := d.client.ListCheckoutKeys(ctx, config.ProjectSlug.ValueString(), config.Digest.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading CircleCI checkout keys",
			fmt.Sprintf(
				"Could not list checkout keys for project %s: %s",
				config.ProjectSlug.ValueString(), circleci.Detail(err),
			),
		)

		return
	}

	// An empty list is a valid answer, so keep the attribute an empty list rather
	// than null: practitioners iterate over it.
	config.CheckoutKeys = make([]checkoutKeyItemModel, 0, len(keys))
	for _, key := range keys {
		config.CheckoutKeys = append(config.CheckoutKeys, checkoutKeyItemModel{
			Fingerprint: types.StringValue(key.Fingerprint),
			Type:        types.StringValue(key.Type),
			PublicKey:   types.StringValue(key.PublicKey),
			Preferred:   types.BoolValue(key.Preferred),
			CreatedAt:   types.StringValue(key.CreatedAt),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

func (d *checkoutKeysDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
