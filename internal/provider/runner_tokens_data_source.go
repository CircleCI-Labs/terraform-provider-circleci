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
	_ datasource.DataSource              = &runnerTokensDataSource{}
	_ datasource.DataSourceWithConfigure = &runnerTokensDataSource{}
)

// runnerTokensDataSourceModel maps the data source schema.
type runnerTokensDataSourceModel struct {
	ResourceClass types.String           `tfsdk:"resource_class"`
	Tokens        []runnerTokenItemModel `tfsdk:"tokens"`
}

// runnerTokenItemModel maps one token in the list.
//
// There is deliberately no token attribute: the runner API only returns the
// secret in the response to a create, so it is unreadable afterwards and a
// data source could never populate it. Use the circleci_runner_token resource
// if the secret itself is needed.
type runnerTokenItemModel struct {
	Id            types.String `tfsdk:"id"`
	Nickname      types.String `tfsdk:"nickname"`
	ResourceClass types.String `tfsdk:"resource_class"`
	CreatedAt     types.String `tfsdk:"created_at"`
}

// NewRunnerTokensDataSource is a helper function to simplify the provider implementation.
func NewRunnerTokensDataSource() datasource.DataSource {
	return &runnerTokensDataSource{}
}

// runnerTokensDataSource is the data source implementation.
type runnerTokensDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *runnerTokensDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_runner_tokens"
}

// Schema defines the schema for the data source.
func (d *runnerTokensDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the runner authentication tokens of a resource class, including tokens " +
			"created outside Terraform.\n\n" +
			"Available on CircleCI Cloud and CircleCI Server. On Server the runner API is served by " +
			"your own installation, so the provider's `runner_host` attribute must be set to your " +
			"Server hostname.\n\n" +
			"~> **The token secret is not available here.** The runner API returns a token's value " +
			"only in the response to the request that created it, so it can never be read back. " +
			"This data source therefore exposes each token's metadata only. Use the " +
			"`circleci_runner_token` resource when the secret itself is needed.",
		Attributes: map[string]schema.Attribute{
			"resource_class": schema.StringAttribute{
				MarkdownDescription: "The resource class whose tokens to list, in `namespace/name` " +
					"format (e.g. `myorg/myrunner`).",
				Required: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(runnerResourceClassPattern, "must be in the format 'namespace/name'"),
				},
			},
			"tokens": schema.ListNestedAttribute{
				MarkdownDescription: "The resource class's tokens, in the order the API returned them. " +
					"The token secrets are not included; see the note above.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the token.",
							Computed:            true,
						},
						"nickname": schema.StringAttribute{
							MarkdownDescription: "The human-readable label given to the token.",
							Computed:            true,
						},
						"resource_class": schema.StringAttribute{
							MarkdownDescription: "The resource class the token grants access to.",
							Computed:            true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "The time at which the token was created.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// Read fetches the resource class's tokens from the API.
func (d *runnerTokensDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config runnerTokensDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resourceClass := config.ResourceClass.ValueString()

	tokens, err := d.client.ListTokens(ctx, resourceClass)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading CircleCI runner tokens",
			fmt.Sprintf("Could not list runner tokens for resource class %s: %s", resourceClass, circleci.Detail(err)),
		)

		return
	}

	// An empty list is a valid answer, so keep the attribute an empty list rather
	// than null: practitioners iterate over it.
	config.Tokens = make([]runnerTokenItemModel, 0, len(tokens))
	for _, token := range tokens {
		config.Tokens = append(config.Tokens, runnerTokenItemModel{
			Id:            types.StringValue(token.ID),
			Nickname:      types.StringValue(token.Nickname),
			ResourceClass: types.StringValue(token.ResourceClass),
			CreatedAt:     types.StringValue(token.CreatedAt),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// Configure adds the provider configured client to the data source.
func (d *runnerTokensDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
