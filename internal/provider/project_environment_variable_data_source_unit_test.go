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

// These tests back the circleci_project_environment_variable data source
// (project_environment_variable_data_source.go) with the fake defined in
// project_environment_variable_fake_test.go, so they run without TF_ACC or
// credentials. Previously this data source had only a resource.Test
// acceptance test gated on CIRCLE_TOKEN.

func projectEnvVarDataSourceConfig(host, slug, name string) string {
	return projectEnvVarResourceProviderConfig(host) + fmt.Sprintf(`
data "circleci_project_environment_variable" "test" {
  project_slug = %q
  name         = %q
}
`, slug, name)
}

// TestProjectEnvVarDataSourceUnit_Read checks that the masked value the fake
// (and the real API) returns reaches state as-is: a data source has no prior
// value to preserve, unlike the resource, so the masked value *is* the
// correct value here.
func TestProjectEnvVarDataSourceUnit_Read(t *testing.T) {
	api, host := newFakeEnvVarAPI(t)
	api.seed(testEnvVarProjectSlug, testEnvVarName, "a-fairly-long-secret-value", "2024-01-02T03:04:05.000Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectEnvVarDataSourceConfig(host, testEnvVarProjectSlug, testEnvVarName),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.circleci_project_environment_variable.test", tfjsonpath.New("name"), knownvalue.StringExact(testEnvVarName)),
					statecheck.ExpectKnownValue("data.circleci_project_environment_variable.test", tfjsonpath.New("value"), knownvalue.StringExact("xxxxalue")),
					statecheck.ExpectKnownValue("data.circleci_project_environment_variable.test", tfjsonpath.New("project_slug"), knownvalue.StringExact(testEnvVarProjectSlug)),
					statecheck.ExpectKnownValue("data.circleci_project_environment_variable.test", tfjsonpath.New("created_at"), knownvalue.StringExact("2024-01-02T03:04:05.000Z")),
				},
			},
		},
	})

	var sawGet bool
	for _, req := range api.recordedRequests() {
		if req == "GET /api/v2/project/"+testEnvVarProjectSlug+"/envvar/"+testEnvVarName {
			sawGet = true
		}
	}
	if !sawGet {
		t.Errorf("no read request seen, got %v", api.recordedRequests())
	}
}

// TestProjectEnvVarDataSourceUnit_MissingErrorsCleanly checks that a 404
// surfaces as a diagnostic. Unlike the resource, a data source has no
// "drift" concept to special-case: an error here is correct, not a gap.
func TestProjectEnvVarDataSourceUnit_MissingErrorsCleanly(t *testing.T) {
	_, host := newFakeEnvVarAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      projectEnvVarDataSourceConfig(host, testEnvVarProjectSlug, "NO_SUCH_VAR"),
			ExpectError: regexp.MustCompile(`(?s)Unable to Read CircleCI project environment variable`),
		}},
	})
}

// TestProjectEnvVarDataSourceUnit_NoCreatedAt checks that a variable with no
// creation timestamp reads back as an empty string rather than an error, the
// same as the resource and the plural data source (environment_variables_data_source_test.go's
// TestAccProjectEnvironmentVariablesDataSource).
func TestProjectEnvVarDataSourceUnit_NoCreatedAt(t *testing.T) {
	api, host := newFakeEnvVarAPI(t)
	api.seed(testEnvVarProjectSlug, testEnvVarName, "short", "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: projectEnvVarDataSourceConfig(host, testEnvVarProjectSlug, testEnvVarName),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("data.circleci_project_environment_variable.test", tfjsonpath.New("created_at"), knownvalue.StringExact("")),
			},
		}},
	})
}
