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
	_ datasource.DataSource              = &ContextEnvironmentVariableDataSource{}
	_ datasource.DataSourceWithConfigure = &ContextEnvironmentVariableDataSource{}
)

// contextEnvironmentVariableDataSourceModel maps the output schema.
//
// There is deliberately no "value" attribute: the API never returns one (see
// internal/circleci/environment_variable.go), and TruncatedValue is not
// exposed either, following the project's rule that a value the API only ever
// masks is not surfaced as a string a configuration could mistake for the real
// thing (see DESIGN.md, "Values the API never returns are not exposed as
// strings").
type contextEnvironmentVariableDataSourceModel struct {
	Name      types.String `tfsdk:"name"`
	UpdatedAt types.String `tfsdk:"updated_at"`
	CreatedAt types.String `tfsdk:"created_at"`
	ContextId types.String `tfsdk:"context_id"`
}

// NewContextEnvironmentVariableDataSource is a helper function to simplify the provider implementation.
func NewContextEnvironmentVariableDataSource() datasource.DataSource {
	return &ContextEnvironmentVariableDataSource{}
}

// ContextEnvironmentVariableDataSource is the data source implementation.
type ContextEnvironmentVariableDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *ContextEnvironmentVariableDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_context_environment_variable"
}

// Schema defines the schema for the data source.
func (d *ContextEnvironmentVariableDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches metadata about a CircleCI context environment variable. The value " +
			"is never exposed: the API does not return it on any route, so there is no `value` attribute " +
			"to read.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the environment variable.",
				Required:            true,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the environment variable was last updated.",
				Computed:            true,
			},
			"context_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the context that owns the environment variable.",
				Required:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the environment variable was created.",
				Computed:            true,
			},
		},
	}
}

// Read refreshes the Terraform state with the latest data.
func (d *ContextEnvironmentVariableDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state contextEnvironmentVariableDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// There is no route that reads one context environment variable by name —
	// GET /context/{id}/environment-variable/{name} answers 404 — so this data
	// source has to list and filter.
	vars, err := d.client.ListContextEnvironmentVariables(ctx, state.ContextId.ValueString())
	if err != nil {
		// A context holding more variables than CircleCI will list is only a
		// problem for a name that is not among the ones it disclosed. One that
		// is there is described exactly as well as a complete list would have
		// described it, so it is not worth failing the read over. See
		// circleci.ContextEnvVarsTruncatedError.
		truncated, ok := circleci.AsContextEnvVarsTruncated(err)
		if !ok {
			resp.Diagnostics.AddError(
				"Unable to read CircleCI context environment variable "+state.Name.ValueString(),
				circleci.Detail(err),
			)

			return
		}

		if _, found := truncated.Find(state.Name.ValueString()); !found {
			resp.Diagnostics.AddError(
				"Unable to read CircleCI context environment variable "+state.Name.ValueString(),
				fmt.Sprintf(
					"Context %s holds more environment variables than CircleCI will list, and %s was "+
						"not among the %d it disclosed, so Terraform cannot say whether it exists. "+
						"CircleCI provides no way to reach the rest: the page token it advertises is "+
						"ignored on every request, and there is no route that reads a context "+
						"environment variable by name.\n\n"+
						"Reduce the context to at most %d environment variables, or split them across "+
						"more than one context.",
					state.ContextId.ValueString(), state.Name.ValueString(),
					len(truncated.Page), len(truncated.Page),
				),
			)

			return
		}

		vars = truncated.Page
	}

	// Documents current behavior when no environment variable matches the
	// requested name: this does not error, it just leaves created_at/updated_at
	// unset (see TestContextEnvVarDataSourceUnit_NotFound).
	for _, elem := range vars {
		if elem.Variable == state.Name.ValueString() {
			state = contextEnvironmentVariableDataSourceModel{
				Name:      types.StringValue(elem.Variable),
				UpdatedAt: types.StringValue(elem.UpdatedAt),
				CreatedAt: types.StringValue(elem.CreatedAt),
				ContextId: types.StringValue(elem.ContextID),
			}
			break
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *ContextEnvironmentVariableDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
