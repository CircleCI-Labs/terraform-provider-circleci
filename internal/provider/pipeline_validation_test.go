// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
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
		// "circleci" is not in circleci.PipelineConfigSourceProviders() at all — the
		// create endpoint has never accepted it for a customer-plausible file path
		// (see that function's doc comment) — so this is rejected regardless of
		// whether a repo id is also set.
		"circleci config source": {
			attributes: `
  config_source_provider           = "circleci"
  config_source_file_path          = ".circleci/config.yml"
  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = "123456"
`,
			wantError: regexp.MustCompile(`(?s)config_source_provider\s+"circleci"\s+is\s+not\s+accepted`),
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

// TestPipelineResourceUnit_CircleCIConfigSourceIsRejected replaces what used to
// be TestPipelineResourceUnit_CircleCIConfigSource, which pinned the opposite
// belief — that a repo-less, CircleCI-hosted config source "can now be expressed
// and applies cleanly." [NET] measurement against the real create endpoint found
// that belief was never true for any file_path a customer configuration would
// plausibly use (see circleci.PipelineConfigSourceProviderCircleCI), so
// "circleci" was removed from circleci.PipelineConfigSourceProviders entirely.
// This pins the replacement behaviour: the rejection happens at plan time, with
// a message a practitioner who already has this in a configuration can act on,
// and — unlike an apply-time API error — before any request reaches the API.
func TestPipelineResourceUnit_CircleCIConfigSourceIsRejected(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: pipelineConfig(host, `
  config_source_provider           = "circleci"
  config_source_file_path          = ".circleci/config.yml"
  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = "123456"
`),
			PlanOnly: true,
			ExpectError: regexp.MustCompile(
				`(?s)config_source_provider\s+"circleci"\s+is\s+not\s+accepted.*create\s+endpoint\s+rejects`,
			),
		}},
	})

	if requests := api.recorded(); len(requests) != 0 {
		t.Errorf("the provider made %d request(s) for a configuration that fails validation, want 0: %+v",
			len(requests), requests)
	}
}
