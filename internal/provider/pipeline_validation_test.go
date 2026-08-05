// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Tests for pipeline_validation.go: the cross-attribute rules for
// `circleci_pipeline_definition` that config_source_provider's split between a
// VCS-backed contract and the repo-less "circleci" one, and every provider's
// numeric repository id, cannot be schema validators alone. Same reasoning, and
// the same table shape, as trigger_validation_test.go.

// pipelineConfig renders a circleci_pipeline_definition with the given attribute
// lines, leaving project_id/name/description fixed.
func pipelineConfig(host, attributes string) string {
	return pipelineFakeProviderConfig(host, "cloud") + fmt.Sprintf(`
resource "circleci_pipeline_definition" "test" {
  project_id  = %[1]q
  name        = "pipe-1"
  description = "d"
%[2]s}
`, fakePipelineProjectID, attributes)
}

// TestPipelineResourceUnit_RejectsInvalidCombinationsAtPlanTime is the table for
// the cross-attribute rules: one case per rule, each proving the rule fires at
// plan time rather than surfacing as an apply-time API error.
func TestPipelineResourceUnit_RejectsInvalidCombinationsAtPlanTime(t *testing.T) {
	cases := map[string]struct {
		attributes string
		wantError  *regexp.Regexp
	}{
		"circleci config source with a repo external id": {
			attributes: `
  config_source_provider           = "circleci"
  config_source_file_path          = ".circleci/config.yml"
  config_source_repo_external_id   = "123456"
  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = "123456"
`,
			wantError: regexp.MustCompile(`(?s)must\s+not\s+set\s+config_source_repo_external_id`),
		},
		"github_app config source without a repo external id": {
			attributes: `
  config_source_provider           = "github_app"
  config_source_file_path          = ".circleci/config.yml"
  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = "123456"
`,
			wantError: regexp.MustCompile(`(?s)requires\s+config_source_repo_external_id`),
		},
		"github_app config source with an empty repo external id": {
			attributes: `
  config_source_provider           = "github_app"
  config_source_file_path          = ".circleci/config.yml"
  config_source_repo_external_id   = ""
  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = "123456"
`,
			wantError: regexp.MustCompile(`(?s)requires\s+config_source_repo_external_id`),
		},
		// The API parses the external id with strconv.ParseInt and answers a bare
		// "bad request" when that fails — no attribute named, nothing to act on.
		"github_server config source with a repository name instead of its numeric id": {
			attributes: `
  config_source_provider           = "github_server"
  config_source_file_path          = ".circleci/config.yml"
  config_source_repo_external_id   = "acme-org/some-repo"
  checkout_source_provider         = "github_server"
  checkout_source_repo_external_id = "123456"
`,
			wantError: regexp.MustCompile(`(?s)numeric ID, not its\s+name`),
		},
		"checkout source with a repository name instead of its numeric id": {
			attributes: `
  config_source_provider           = "github_app"
  config_source_file_path          = ".circleci/config.yml"
  config_source_repo_external_id   = "123456"
  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = "acme-org/some-repo"
`,
			wantError: regexp.MustCompile(`(?s)numeric ID, not its\s+name`),
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			api, host := newFakePipelineDefAPI(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: pipelineConfig(host, testCase.attributes),
					// PlanOnly is what makes this a test of *when* the rule fires.
					PlanOnly:    true,
					ExpectError: testCase.wantError,
				}},
			})

			if requests := api.recorded(); len(requests) != 0 {
				t.Errorf("the provider made %d request(s) for a configuration that fails validation, "+
					"want 0: %+v", len(requests), requests)
			}
		})
	}
}

// TestPipelineResourceUnit_CircleCIConfigSource is the fix for the unreachable
// capability: a pipeline definition whose configuration is hosted by CircleCI
// itself, rather than a VCS repository, can now be expressed and applies cleanly.
// checkout_source still needs a real repository — checkout_source has no
// repo-less branch — so this is the one combination the API accepts with
// config_source_provider "circleci".
func TestPipelineResourceUnit_CircleCIConfigSource(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: pipelineConfig(host, `
  config_source_provider           = "circleci"
  config_source_file_path          = ".circleci/config.yml"
  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = "123456"
`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_pipeline_definition.test",
						tfjsonpath.New("config_source_provider"), knownvalue.StringExact("circleci")),
					statecheck.ExpectKnownValue("circleci_pipeline_definition.test",
						tfjsonpath.New("config_source_repo_external_id"), knownvalue.Null()),
					statecheck.ExpectKnownValue("circleci_pipeline_definition.test",
						tfjsonpath.New("config_source_repo_full_name"), knownvalue.StringExact("")),
					statecheck.ExpectKnownValue("circleci_pipeline_definition.test",
						tfjsonpath.New("checkout_source_repo_full_name"), knownvalue.StringExact(resolveFullName("123456"))),
				},
			},
			{
				// Refresh and re-plan: a repo-less config source must not show
				// perpetual drift.
				Config: pipelineConfig(host, `
  config_source_provider           = "circleci"
  config_source_file_path          = ".circleci/config.yml"
  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = "123456"
`),
			},
		},
	})

	create := api.lastRequest(t, "POST", "/api/v2/projects/"+fakePipelineProjectID+"/pipeline-definitions")
	configSource, _ := create.Body["config_source"].(map[string]any)
	if configSource["provider"] != "circleci" {
		t.Errorf("create config_source.provider = %v, want circleci", configSource["provider"])
	}
	if _, present := configSource["repo"]; present {
		t.Errorf("create config_source carries repo = %v for a circleci-hosted config source, want it omitted entirely", configSource["repo"])
	}
}
