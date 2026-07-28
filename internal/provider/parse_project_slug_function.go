// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure the implementation satisfies the expected interfaces.
var _ function.Function = &parseProjectSlugFunction{}

// parseProjectSlugAttrTypes is the attribute type set of parse_project_slug's
// return value, shared between Definition and Run so the two cannot drift.
var parseProjectSlugAttrTypes = map[string]attr.Type{
	"vcs_type": types.StringType,
	"org":      types.StringType,
	"project":  types.StringType,
}

// NewParseProjectSlugFunction is a helper function to simplify the provider implementation.
func NewParseProjectSlugFunction() function.Function {
	return &parseProjectSlugFunction{}
}

type parseProjectSlugFunction struct{}

// Metadata returns the function name.
func (f *parseProjectSlugFunction) Metadata(_ context.Context, _ function.MetadataRequest, resp *function.MetadataResponse) {
	resp.Name = "parse_project_slug"
}

// Definition returns the function definition.
func (f *parseProjectSlugFunction) Definition(_ context.Context, _ function.DefinitionRequest, resp *function.DefinitionResponse) {
	resp.Definition = function.Definition{
		Summary: "Splits a CircleCI project slug into its vcs_type, org and project segments.",
		MarkdownDescription: "The inverse of `project_slug`: given a slug such as `\"gh/my-org/my-repo\"` or " +
			"`\"circleci/<orgUUID>/<projectUUID>\"`, returns an object with `vcs_type`, `org` and `project` " +
			"attributes. Errors if the slug does not have exactly three non-empty, `/`-separated segments — " +
			"a shape mismatch that would otherwise reach the API as a confusing 404.\n\n" +
			"Useful for pulling a slug apart that arrived as a single string (for example from an existing " +
			"resource's `project_slug` attribute, or a data source list keyed by slug) when only one " +
			"segment — usually `org` — is actually needed.",
		Parameters: []function.Parameter{
			function.StringParameter{
				Name:                "slug",
				MarkdownDescription: "A project slug such as `\"gh/my-org/my-repo\"` or `\"circleci/<orgUUID>/<projectUUID>\"`.",
			},
		},
		Return: function.ObjectReturn{
			AttributeTypes: parseProjectSlugAttrTypes,
		},
	}
}

// Run runs the function logic.
func (f *parseProjectSlugFunction) Run(ctx context.Context, req function.RunRequest, resp *function.RunResponse) {
	var slug string

	resp.Error = function.ConcatFuncErrors(resp.Error, req.Arguments.Get(ctx, &slug))
	if resp.Error != nil {
		return
	}

	segments := strings.Split(slug, "/")
	if len(segments) != 3 {
		resp.Error = function.ConcatFuncErrors(resp.Error, function.NewArgumentFuncError(0,
			fmt.Sprintf(`slug %q has %d segment(s), want exactly 3 ("vcs-type/org/project")`, slug, len(segments))))

		return
	}

	for _, segment := range segments {
		if segment == "" {
			resp.Error = function.ConcatFuncErrors(resp.Error, function.NewArgumentFuncError(0,
				fmt.Sprintf("slug %q has an empty segment", slug)))

			return
		}
	}

	result, diags := types.ObjectValue(parseProjectSlugAttrTypes, map[string]attr.Value{
		"vcs_type": types.StringValue(segments[0]),
		"org":      types.StringValue(segments[1]),
		"project":  types.StringValue(segments[2]),
	})
	resp.Error = function.ConcatFuncErrors(resp.Error, function.FuncErrorFromDiags(ctx, diags))
	if resp.Error != nil {
		return
	}

	resp.Error = function.ConcatFuncErrors(resp.Error, resp.Result.Set(ctx, result))
}
