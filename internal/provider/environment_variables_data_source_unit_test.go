// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// These tests cover the same ground as TestAccProjectEnvironmentVariablesDataSource
// and its siblings in environment_variables_data_source_test.go, but through
// resource.UnitTest rather than resource.Test.
//
// Those existing tests are already fake-backed (they use the same newPluralAPI
// stand-in reused here) and need no credentials, but resource.Test still
// skips them unless TF_ACC=1 — so despite needing nothing real, they do not
// run in a plain `go test ./...`. This file exercises
// environment_variables_data_source.go's Read without that gate, reusing
// plural_fake_test.go's helpers rather than duplicating the fake.

// TestProjectEnvironmentVariablesDataSourceUnit_Read covers pagination
// draining and a null created_at, without TF_ACC.
func TestProjectEnvironmentVariablesDataSourceUnit_Read(t *testing.T) {
	api, host := newPluralAPI(t)
	api.pageSize = 1

	createdAt := "2023-04-14T21:20:14.000Z"
	api.seedEnvVar(testPluralProjectSlug, "API_TOKEN", "xxxx1234", &createdAt)
	api.seedEnvVar(testPluralProjectSlug, "ZONE", "xxxxst-1", nil)
	api.seedEnvVar("circleci/Other/Project", "ELSEWHERE", "xxxxelse", nil)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectEnvironmentVariablesDataSourceConfig(host, "cloud", testPluralProjectSlug),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variables.test",
						tfjsonpath.New("environment_variables"),
						knownvalue.ListSizeExact(2),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variables.test",
						tfjsonpath.New("environment_variables").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("API_TOKEN"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variables.test",
						tfjsonpath.New("environment_variables").AtSliceIndex(0).AtMapKey("value"),
						knownvalue.StringExact("xxxx1234"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variables.test",
						tfjsonpath.New("environment_variables").AtSliceIndex(0).AtMapKey("created_at"),
						knownvalue.StringExact(createdAt),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variables.test",
						tfjsonpath.New("environment_variables").AtSliceIndex(1).AtMapKey("created_at"),
						knownvalue.StringExact(""),
					),
				},
			},
		},
	})
}

// TestProjectEnvironmentVariablesDataSourceUnit_Empty checks that a project
// with no variables reads back an empty (not null) list.
func TestProjectEnvironmentVariablesDataSourceUnit_Empty(t *testing.T) {
	_, host := newPluralAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectEnvironmentVariablesDataSourceConfig(host, "cloud", testPluralProjectSlug),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variables.test",
						tfjsonpath.New("environment_variables"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
		},
	})
}

// TestProjectEnvironmentVariablesDataSourceUnit_MalformedSlug checks that a
// slug the schema does not reject at plan time (this data source has no
// slug-shape validator at all) still fails cleanly via the client's own
// segment-count guard, rather than panicking.
func TestProjectEnvironmentVariablesDataSourceUnit_MalformedSlug(t *testing.T) {
	_, host := newPluralAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccProjectEnvironmentVariablesDataSourceConfig(host, "cloud", "just-an-org"),
			ExpectError: regexp.MustCompile(`(?s)Unable to list CircleCI environment variables.*project slug`),
		}},
	})
}

// TestProjectEnvironmentVariablesDataSourceUnit_APIError checks that a
// non-2xx list response surfaces as a diagnostic rather than propagating a
// raw error.
func TestProjectEnvironmentVariablesDataSourceUnit_APIError(t *testing.T) {
	api, host := newPluralAPI(t)
	api.fail(500, "internal error")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccProjectEnvironmentVariablesDataSourceConfig(host, "cloud", testPluralProjectSlug),
			ExpectError: regexp.MustCompile(`(?s)Unable to list CircleCI environment variables`),
		}},
	})
}
