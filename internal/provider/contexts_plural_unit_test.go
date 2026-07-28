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

// contexts_data_source_test.go and context_restrictions_data_source_test.go
// already exercise contexts_data_source.go and
// context_restrictions_data_source.go against the pluralAPI fake
// (plural_fake_test.go) — but every one of those tests uses resource.Test,
// which resource.Test itself skips unless TF_ACC=1 is set, even though the
// fake needs no real credentials. These are the resource.UnitTest equivalents,
// so the two files actually get exercised without TF_ACC.

func TestContextsDataSourceUnit(t *testing.T) {
	api, host := newPluralAPI(t)

	api.pageSize = 1
	api.seedContext(testPluralOrgID, "c1", "build", "2024-01-18T02:16:55Z")
	api.seedContext(testPluralOrgID, "c2", "deploy", "2024-02-18T02:16:55Z")
	api.seedContext("99999999-9999-9999-9999-999999999999", "other", "elsewhere", "2024-04-18T02:16:55Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccContextsDataSourceConfig(host, "cloud"),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_contexts.test", tfjsonpath.New("contexts"), knownvalue.ListSizeExact(2),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_contexts.test",
					tfjsonpath.New("contexts").AtSliceIndex(0).AtMapKey("name"),
					knownvalue.StringExact("build"),
				),
			},
		}},
	})
}

func TestContextsDataSourceUnit_Empty(t *testing.T) {
	_, host := newPluralAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccContextsDataSourceConfig(host, "cloud"),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_contexts.test", tfjsonpath.New("contexts"), knownvalue.ListSizeExact(0),
				),
			},
		}},
	})
}

func TestContextsDataSourceUnit_APIError(t *testing.T) {
	api, host := newPluralAPI(t)
	api.fail(403, "Permission denied.")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccContextsDataSourceConfig(host, "cloud"),
			ExpectError: regexp.MustCompile(`(?s)Unable to list CircleCI contexts.*Permission denied\.`),
		}},
	})
}

func TestContextRestrictionsDataSourceUnit(t *testing.T) {
	api, host := newPluralAPI(t)

	api.seedRestriction(testPluralContextID, "r1", "acme/api", "project", "22222222-2222-2222-2222-222222222222")
	api.seedRestriction(testPluralContextID, "r2", "All members", "group", "96d25399-ab81-4499-969f-f890a383209e")
	api.seedRestriction(testPluralContextID, "r3", "", "expression", `pipeline.git.branch == "main"`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccContextRestrictionsDataSourceConfig(host, "cloud"),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_context_restrictions.test", tfjsonpath.New("restrictions"), knownvalue.ListSizeExact(3),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_context_restrictions.test",
					tfjsonpath.New("restrictions").AtSliceIndex(0).AtMapKey("type"),
					knownvalue.StringExact("project"),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_context_restrictions.test",
					tfjsonpath.New("restrictions").AtSliceIndex(1).AtMapKey("project_id"),
					knownvalue.StringExact(""),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_context_restrictions.test",
					tfjsonpath.New("restrictions").AtSliceIndex(2).AtMapKey("type"),
					knownvalue.StringExact("expression"),
				),
			},
		}},
	})
}

func TestContextRestrictionsDataSourceUnit_Empty(t *testing.T) {
	_, host := newPluralAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccContextRestrictionsDataSourceConfig(host, "cloud"),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_context_restrictions.test", tfjsonpath.New("restrictions"), knownvalue.ListSizeExact(0),
				),
			},
		}},
	})
}

func TestContextRestrictionsDataSourceUnit_APIError(t *testing.T) {
	api, host := newPluralAPI(t)
	api.fail(404, "Context not found.")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccContextRestrictionsDataSourceConfig(host, "cloud"),
			ExpectError: regexp.MustCompile(`(?s)Unable to list restrictions for CircleCI context.*Context not found\.`),
		}},
	})
}
