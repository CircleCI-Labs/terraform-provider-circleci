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
var _ function.Function = &projectSlugFunction{}

// projectSlugSegmentPattern rejects a segment that would silently corrupt a
// slug: an empty string, or one containing "/" or whitespace (which would
// change the number of segments the API sees, or the boundary between them).
var projectSlugSegmentPattern = regexp.MustCompile(`^[^/\s]+$`)

// NewProjectSlugFunction is a helper function to simplify the provider implementation.
func NewProjectSlugFunction() function.Function {
	return &projectSlugFunction{}
}

type projectSlugFunction struct{}

// Metadata returns the function name.
func (f *projectSlugFunction) Metadata(_ context.Context, _ function.MetadataRequest, resp *function.MetadataResponse) {
	resp.Name = "project_slug"
}

// Definition returns the function definition.
func (f *projectSlugFunction) Definition(_ context.Context, _ function.DefinitionRequest, resp *function.DefinitionResponse) {
	resp.Definition = function.Definition{
		Summary: "Builds a CircleCI project slug from its parts, validating the result.",
		MarkdownDescription: "Builds the `vcs-type/org/project` identifier that every `project_slug` " +
			"argument in this provider takes (`circleci_project`, `circleci_checkout_key`, and so on).\n\n" +
			"CircleCI's slug convention has a footgun this function exists to catch: classic GitHub OAuth " +
			"and Bitbucket projects are addressed by name — `gh/my-org/my-repo` — but GitLab, GitHub App " +
			"and GitHub Enterprise Server projects, and any project in a standalone (`circleci`-native) " +
			"organization, are addressed by UUID instead — `circleci/<orgUUID>/<projectUUID>`. There is no " +
			"`gitlab` or `github_app` slug segment; all of those collapse to `circleci`. Passing an " +
			"organization or project *name* where `vcs_type` is `\"circleci\"` produces a slug that looks " +
			"plausible but resolves nothing, which otherwise surfaces as a confusing 404 far from this call. " +
			"This function validates `org`/`project` look like UUIDs in that case, so the mistake fails at " +
			"plan time with a clear message instead.",
		Parameters: []function.Parameter{
			function.StringParameter{
				Name: "vcs_type",
				MarkdownDescription: "One of `\"gh\"` (GitHub OAuth), `\"bb\"` (Bitbucket), or `\"circleci\"` " +
					"(GitLab, GitHub App, GHES, or a standalone organization — always addressed by UUID).",
			},
			function.StringParameter{
				Name:                "org",
				MarkdownDescription: "The organization name (for `gh`/`bb`) or organization UUID (for `circleci`).",
			},
			function.StringParameter{
				Name:                "project",
				MarkdownDescription: "The project/repository name (for `gh`/`bb`) or project UUID (for `circleci`).",
			},
		},
		Return: function.StringReturn{},
	}
}

// Run runs the function logic.
func (f *projectSlugFunction) Run(ctx context.Context, req function.RunRequest, resp *function.RunResponse) {
	var vcsType, org, project string

	resp.Error = function.ConcatFuncErrors(resp.Error, req.Arguments.Get(ctx, &vcsType, &org, &project))
	if resp.Error != nil {
		return
	}

	switch vcsType {
	case "gh", "bb":
		if !projectSlugSegmentPattern.MatchString(org) {
			resp.Error = function.ConcatFuncErrors(resp.Error, function.NewArgumentFuncError(1,
				fmt.Sprintf("org %q must be non-empty and contain no \"/\" or whitespace", org)))
		}
		if !projectSlugSegmentPattern.MatchString(project) {
			resp.Error = function.ConcatFuncErrors(resp.Error, function.NewArgumentFuncError(2,
				fmt.Sprintf("project %q must be non-empty and contain no \"/\" or whitespace", project)))
		}
	case "circleci":
		if !orbIsUUID(org) {
			resp.Error = function.ConcatFuncErrors(resp.Error, function.NewArgumentFuncError(1,
				fmt.Sprintf(
					"org %q must be a UUID when vcs_type is \"circleci\": GitLab, GitHub App, GHES and "+
						"standalone organizations are addressed by UUID, not name", org,
				)))
		}
		if !orbIsUUID(project) {
			resp.Error = function.ConcatFuncErrors(resp.Error, function.NewArgumentFuncError(2,
				fmt.Sprintf(
					"project %q must be a UUID when vcs_type is \"circleci\": GitLab, GitHub App, GHES and "+
						"standalone organizations are addressed by UUID, not name", project,
				)))
		}
	default:
		resp.Error = function.ConcatFuncErrors(resp.Error, function.NewArgumentFuncError(0,
			fmt.Sprintf(
				`vcs_type %q is not recognized: want "gh" (GitHub OAuth), "bb" (Bitbucket), or "circleci" `+
					`(GitLab, GitHub App, GHES, or a standalone organization)`, vcsType,
			)))
	}

	if resp.Error != nil {
		return
	}

	resp.Error = function.ConcatFuncErrors(resp.Error, resp.Result.Set(ctx, vcsType+"/"+org+"/"+project))
}
