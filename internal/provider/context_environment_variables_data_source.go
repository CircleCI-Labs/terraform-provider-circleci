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
	_ datasource.DataSource              = &contextEnvironmentVariablesDataSource{}
	_ datasource.DataSourceWithConfigure = &contextEnvironmentVariablesDataSource{}
)

// contextEnvironmentVariablesDataSourceModel maps the data source schema.
type contextEnvironmentVariablesDataSourceModel struct {
	ContextId            types.String                          `tfsdk:"context_id"`
	EnvironmentVariables []contextEnvironmentVariableItemModel `tfsdk:"environment_variables"`
}

// contextEnvironmentVariableItemModel maps one environment variable in the
// list. The attribute names match `circleci_context_environment_variable`
// (name, created_at, updated_at), so a variable read here and one read there
// describe themselves the same way. `truncated_value` has no counterpart on
// that data source: the singular one omits it entirely (see its doc comment),
// while here it is the only trace of the value worth surfacing at all.
type contextEnvironmentVariableItemModel struct {
	Name           types.String `tfsdk:"name"`
	TruncatedValue types.String `tfsdk:"truncated_value"`
	CreatedAt      types.String `tfsdk:"created_at"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
}

// NewContextEnvironmentVariablesDataSource is a helper function to simplify the provider implementation.
func NewContextEnvironmentVariablesDataSource() datasource.DataSource {
	return &contextEnvironmentVariablesDataSource{}
}

// contextEnvironmentVariablesDataSource is the data source implementation.
type contextEnvironmentVariablesDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *contextEnvironmentVariablesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_context_environment_variables"
}

// Schema defines the schema for the data source.
func (d *contextEnvironmentVariablesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every environment variable set on a CircleCI context, including " +
			"variables created outside Terraform. Available on CircleCI Cloud and CircleCI Server.\n\n" +
			"Use [`circleci_context_environment_variable`](./context_environment_variable) to fetch one by " +
			"name.\n\n" +
			"~> **A context of more than 100 variables cannot be read.** CircleCI lists at most 100 " +
			"environment variables per context and provides no way to reach the rest: the page token " +
			"the API advertises is ignored on every request, and there is no route that reads a context " +
			"environment variable by name. Rather than return a partial list that nothing downstream " +
			"could tell from a complete one, this data source raises an error. CircleCI caps a context " +
			"at 100 variables but does not apply the cap atomically, so concurrent writes can push a " +
			"context past it.\n\n" +
			"~> **The value is never returned.** CircleCI does not disclose a context environment " +
			"variable's value on any route. `truncated_value` is the only trace of it the API discloses, " +
			"and it is a tail with no mask prefix — see that attribute's description below before using " +
			"it for anything.",
		Attributes: map[string]schema.Attribute{
			"context_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the context whose environment variables " +
					"are listed.",
				Required: true,
			},
			"environment_variables": schema.ListNestedAttribute{
				MarkdownDescription: "The environment variables on the context, in the order the API " +
					"returns them (sorted by name).",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							MarkdownDescription: "Name of the environment variable.",
							Computed:            true,
						},
						"truncated_value": schema.StringAttribute{
							MarkdownDescription: "The last few characters of the value, with no mask " +
								"prefix: CircleCI reveals the last `min(4, floor(length / 2))` " +
								"characters, so a 12-character value gives up 4 and a 4-character " +
								"value gives up 2. This is **not** the same shape as " +
								"`circleci_project_environment_variables`' `value`, which prefixes the " +
								"same kind of tail with `xxxx` — do not treat the two as comparable.\n\n" +
								"Do not use this to detect that a value changed. Rotating a secret while " +
								"keeping its last characters leaves this identical, so comparing it " +
								"silently misses that class of rotation (a community CircleCI provider " +
								"does exactly this and has that gap). Compare `updated_at` instead.",
							Computed:  true,
							Sensitive: true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "Timestamp the variable was created, as the API reported it.",
							Computed:            true,
						},
						"updated_at": schema.StringAttribute{
							MarkdownDescription: "Timestamp the variable was last **written**, as the API " +
								"reported it. CircleCI bumps this on every write, including one that " +
								"stores a byte-identical value, so it means \"last written\" rather than " +
								"\"last changed\".",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

// Read lists the context's environment variables.
func (d *contextEnvironmentVariablesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state contextEnvironmentVariablesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	contextID := state.ContextId.ValueString()

	variables, err := d.client.ListContextEnvironmentVariables(ctx, contextID)
	if err != nil {
		// This data source promises every variable on the context, and a context
		// holding more than CircleCI will list cannot be described at all: the
		// page token the route advertises is never read back, and there is no
		// route that reads one variable by name. Returning the part that was
		// disclosed is the worst option available — a for_each over it would
		// quietly stop managing whatever fell off the end, with nothing in the
		// plan to show it. See circleci.ContextEnvVarsTruncatedError.
		if truncated, ok := circleci.AsContextEnvVarsTruncated(err); ok {
			resp.Diagnostics.AddError(
				"Incomplete environment variable list for CircleCI context "+contextID,
				fmt.Sprintf(
					"CircleCI disclosed %d environment variables for this context and reported that "+
						"there are more, but it provides no way to fetch them: the page token it "+
						"advertises is ignored on every request, and there is no route that reads a "+
						"context environment variable by name.\n\n"+
						"This data source will not return a partial list, because nothing downstream "+
						"could tell it apart from a complete one. Reduce the context to at most %d "+
						"environment variables, or split them across more than one context.",
					len(truncated.Page), len(truncated.Page),
				),
			)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to list CircleCI environment variables for context "+contextID,
			circleci.Detail(err),
		)

		return
	}

	// An empty, non-null list keeps `for_each` and `length()` working against a
	// context that has no environment variables yet.
	state.EnvironmentVariables = make([]contextEnvironmentVariableItemModel, 0, len(variables))
	for _, variable := range variables {
		state.EnvironmentVariables = append(state.EnvironmentVariables, contextEnvironmentVariableItemModel{
			Name:           types.StringValue(variable.Variable),
			TruncatedValue: types.StringValue(variable.TruncatedValue),
			CreatedAt:      types.StringValue(variable.CreatedAt),
			UpdatedAt:      types.StringValue(variable.UpdatedAt),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *contextEnvironmentVariablesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
