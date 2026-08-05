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

// These are fake-server-backed unit tests for context_data_source.go: they
// need no TF_ACC and no CircleCI credentials, unlike TestAccContextDataSource.
//
// context_data_source.go reads a context by id or name and its restrictions
// both through internal/circleci.Client, against the same /api/v2/context
// routes, so contextFakeAPI (context_fake_test.go) serves both calls.

const contextDataSourceUnitContextID = "ctx-fixed-3"

func contextDataSourceByIDConfig(host, contextID string) string {
	return contextFakeProviderConfig(host) + fmt.Sprintf(`
data "circleci_context" "test" {
  id = %[1]q
}
`, contextID)
}

func contextDataSourceByNameConfig(host, name, orgID string) string {
	return contextFakeProviderConfig(host) + fmt.Sprintf(`
data "circleci_context" "test" {
  name             = %[1]q
  organization_id  = %[2]q
}
`, name, orgID)
}

func TestContextDataSourceUnit_ByID(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextDataSourceUnitContextID, contextUnitOrgID, "2024-01-18T02:16:55Z")
	api.seedRestriction(contextDataSourceUnitContextID, "r1", "acme/api", "project", "22222222-2222-2222-2222-222222222222")
	api.seedRestriction(contextDataSourceUnitContextID, "r2", "", "expression", `pipeline.git.branch == "main"`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextDataSourceByIDConfig(host, contextDataSourceUnitContextID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.circleci_context.test", tfjsonpath.New("id"), knownvalue.StringExact(contextDataSourceUnitContextID)),
					statecheck.ExpectKnownValue("data.circleci_context.test", tfjsonpath.New("name"), knownvalue.StringExact("build")),
					statecheck.ExpectKnownValue("data.circleci_context.test", tfjsonpath.New("created_at"), knownvalue.StringExact("2024-01-18T02:16:55Z")),
					statecheck.ExpectKnownValue("data.circleci_context.test", tfjsonpath.New("restrictions"), knownvalue.ListSizeExact(2)),
					statecheck.ExpectKnownValue(
						"data.circleci_context.test",
						tfjsonpath.New("restrictions").AtSliceIndex(0).AtMapKey("project_id"),
						knownvalue.StringExact("22222222-2222-2222-2222-222222222222"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context.test",
						tfjsonpath.New("restrictions").AtSliceIndex(1).AtMapKey("project_id"),
						knownvalue.StringExact(""),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context.test",
						tfjsonpath.New("restrictions").AtSliceIndex(1).AtMapKey("name"),
						knownvalue.StringExact(""),
					),
				},
			},
		},
	})
}

func TestContextDataSourceUnit_ByName(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextDataSourceUnitContextID, contextUnitOrgID, "2024-01-18T02:16:55Z")
	// Another organization's context of the same name must not be matched.
	api.seedContext("ctx-other", "org-other", "2024-01-01T00:00:00Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextDataSourceByNameConfig(host, "build", contextUnitOrgID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.circleci_context.test", tfjsonpath.New("id"), knownvalue.StringExact(contextDataSourceUnitContextID)),
					statecheck.ExpectKnownValue("data.circleci_context.test", tfjsonpath.New("organization_id"), knownvalue.StringExact(contextUnitOrgID)),
				},
			},
		},
	})
}

func TestContextDataSourceUnit_NotFoundByID(t *testing.T) {
	_, host := newContextFakeAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      contextDataSourceByIDConfig(host, "does-not-exist"),
			ExpectError: regexp.MustCompile(`(?s)Unable to read CircleCI context`),
		}},
	})
}

func TestContextDataSourceUnit_NotFoundByName(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextDataSourceUnitContextID, contextUnitOrgID, "2024-01-18T02:16:55Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      contextDataSourceByNameConfig(host, "does-not-exist", contextUnitOrgID),
			ExpectError: regexp.MustCompile(`(?s)No CircleCI context named "does-not-exist"`),
		}},
	})
}

func TestContextDataSourceUnit_ExactlyOneOf(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Neither id nor name set.
				Config:      contextFakeProviderConfig("http://127.0.0.1:1") + `data "circleci_context" "test" {}`,
				ExpectError: regexp.MustCompile(`(?s)Missing Attribute Configuration.*[Ee]xactly one`),
			},
			{
				// Both id and name set.
				Config: contextFakeProviderConfig("http://127.0.0.1:1") + `
data "circleci_context" "test" {
  id               = "ctx-1"
  name             = "build"
  organization_id  = "org-1"
}
`,
				ExpectError: regexp.MustCompile(`(?s)Invalid Attribute Combination.*[Ee]xactly one`),
			},
		},
	})
}

func TestContextDataSourceUnit_RequiredTogether(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			// name without organization_id.
			Config: contextFakeProviderConfig("http://127.0.0.1:1") + `
data "circleci_context" "test" {
  name = "build"
}
`,
			ExpectError: regexp.MustCompile(`(?s)Invalid Attribute Combination|Required.*organization_id|organization_id.*[Rr]equired`),
		}},
	})
}
