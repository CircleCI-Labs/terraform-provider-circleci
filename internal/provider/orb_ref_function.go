// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework/function"
)

// Ensure the implementation satisfies the expected interfaces.
var _ function.Function = &orbRefFunction{}

// orbRefNamePattern matches a bare namespace or orb name: no "/" (which would
// separate namespace from orb), no "@" (which separates the ref from its
// version) and no whitespace. It mirrors the Validators on
// circleci_orb_namespace's name attribute and circleci_orb's name attribute.
var orbRefNamePattern = regexp.MustCompile(`^[^/\s@]+$`)

// orbRefVersionPattern matches a version acceptable to filter[ref]: a semantic
// version, a "dev:<label>" development release, or the "volatile" alias (see
// GetOrbVersionByRef's doc comment in internal/circleci/orb.go).
var orbRefVersionPattern = regexp.MustCompile(`^(\d+\.\d+\.\d+|dev:\S+|volatile)$`)

// NewOrbRefFunction is a helper function to simplify the provider implementation.
func NewOrbRefFunction() function.Function {
	return &orbRefFunction{}
}

type orbRefFunction struct{}

// Metadata returns the function name.
func (f *orbRefFunction) Metadata(_ context.Context, _ function.MetadataRequest, resp *function.MetadataResponse) {
	resp.Name = "orb_ref"
}

// Definition returns the function definition.
func (f *orbRefFunction) Definition(_ context.Context, _ function.DefinitionRequest, resp *function.DefinitionResponse) {
	resp.Definition = function.Definition{
		Summary: "Builds a fully qualified orb version reference, namespace/orb@version.",
		MarkdownDescription: "Builds the `namespace/orb@version` reference that the CircleCI orb version " +
			"API's `filter[ref]` parameter requires.\n\n" +
			"Getting this wrong was a real bug in this provider: `filter[ref]` silently resolves nothing " +
			"unless it is given the fully qualified form — passing a bare version string such as `\"1.2.3\"` " +
			"does not fall back to filtering by orb, it just matches no version at all (see " +
			"`GetOrbVersionByRef` in `internal/circleci/orb.go`). This function validates each part so a " +
			"malformed reference (a namespace or orb name containing `/`, `@` or whitespace, or a version " +
			"that is not a semantic version, a `dev:` label or `volatile`) fails at plan time instead of " +
			"resolving to nothing at apply time.",
		Parameters: []function.Parameter{
			function.StringParameter{
				Name:                "namespace",
				MarkdownDescription: "The orb namespace (e.g. `circleci`), with no `/`, `@` or whitespace.",
			},
			function.StringParameter{
				Name:                "orb",
				MarkdownDescription: "The orb name alone, without its namespace prefix (e.g. `node`), with no `/`, `@` or whitespace.",
			},
			function.StringParameter{
				Name:                "version",
				MarkdownDescription: "A semantic version such as `\"1.2.3\"`, a `\"dev:<label>\"` development release, or `\"volatile\"`.",
			},
		},
		Return: function.StringReturn{},
	}
}

// Run runs the function logic.
func (f *orbRefFunction) Run(ctx context.Context, req function.RunRequest, resp *function.RunResponse) {
	var namespace, orb, version string

	resp.Error = function.ConcatFuncErrors(resp.Error, req.Arguments.Get(ctx, &namespace, &orb, &version))
	if resp.Error != nil {
		return
	}

	if !orbRefNamePattern.MatchString(namespace) {
		resp.Error = function.ConcatFuncErrors(resp.Error, function.NewArgumentFuncError(0,
			fmt.Sprintf(`namespace %q must not contain "/", "@" or whitespace`, namespace)))
	}
	if !orbRefNamePattern.MatchString(orb) {
		resp.Error = function.ConcatFuncErrors(resp.Error, function.NewArgumentFuncError(1,
			fmt.Sprintf(`orb %q must not contain "/", "@" or whitespace`, orb)))
	}
	if !orbRefVersionPattern.MatchString(version) {
		resp.Error = function.ConcatFuncErrors(resp.Error, function.NewArgumentFuncError(2,
			fmt.Sprintf(`version %q must be a semantic version such as "1.2.3", a "dev:<label>" release, or "volatile"`, version)))
	}

	if resp.Error != nil {
		return
	}

	resp.Error = function.ConcatFuncErrors(resp.Error, resp.Result.Set(ctx, namespace+"/"+orb+"@"+version))
}
