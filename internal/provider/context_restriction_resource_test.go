// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Every acceptance test below manages its own scratch circleci_context rather
// than pointing at testContextID's pre-existing fixture: that fixture is
// shared with (and read by) other acceptance tests, so creating and deleting
// restrictions directly on it would risk a false failure if another test
// happened to run against the same organization around the same time. A
// context created by the config itself is destroyed with it at the end of the
// TestCase, whether or not a later step errors.

// contextRestrictionImportStateID returns the ImportStateIdFunc for a
// circleci_context_restriction resource addressed by resourceName, building
// "context_id/id" from whatever the previous step actually stored in state.
func contextRestrictionImportStateID(resourceName string) func(*terraform.State) (string, error) {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s not found in state", resourceName)
		}

		contextID, found := rs.Primary.Attributes["context_id"]
		if !found {
			return "", errors.New("attribute context_id not found")
		}

		restrictionID, found := rs.Primary.Attributes["id"]
		if !found {
			return "", errors.New("attribute id not found")
		}

		return fmt.Sprintf("%s/%s", contextID, restrictionID), nil
	}
}

// testAccContextRestrictionResourceConfig declares a scratch context in orgID
// and one restriction of type sometype/value on it.
func testAccContextRestrictionResourceConfig(orgID, contextName, sometype, value string) string {
	return fmt.Sprintf(`
resource "circleci_context" "test_context" {
  org_id = %[1]q
  name   = %[2]q
}

resource "circleci_context_restriction" "test_context_restriction" {
  context_id = circleci_context.test_context.id
  type       = %[3]q
  value      = %[4]q
}
`, orgID, contextName, sometype, value)
}

// TestAccContextRestrictionResource covers restriction_type = "project": create,
// read and import, on whichever integration CIRCLECI_TEST_VCS_TYPE selects.
func TestAccContextRestrictionResource(t *testing.T) {
	orgID := testOrgID(t)
	projectID := testProjectID(t)
	contextName := "tf-acc-restriction-project-" + t.Name()
	uuidRegex := regexp.MustCompile(`[a-z0-9]{8}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{12}`)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccContextRestrictionResourceConfig(orgID, contextName, "project", projectID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_context_restriction.test_context_restriction",
						tfjsonpath.New("type"),
						knownvalue.StringExact("project"),
					),
					statecheck.ExpectKnownValue(
						"circleci_context_restriction.test_context_restriction",
						tfjsonpath.New("value"),
						knownvalue.StringExact(projectID),
					),
					statecheck.ExpectKnownValue(
						"circleci_context_restriction.test_context_restriction",
						tfjsonpath.New("id"),
						knownvalue.StringRegexp(uuidRegex),
					),
					statecheck.ExpectKnownValue(
						"circleci_context_restriction.test_context_restriction",
						tfjsonpath.New("project_id"),
						knownvalue.StringExact(projectID),
					),
				},
			},
			// ImportState testing. "name" is ignored: the create response never
			// carries it (see CreateContextRestriction's doc comment), so it reads
			// back as "" right after apply and only picks up its real value —
			// learned by the import-triggered read — afterwards.
			{
				ResourceName:            "circleci_context_restriction.test_context_restriction",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"name"},
				ImportStateIdFunc:       contextRestrictionImportStateID("circleci_context_restriction.test_context_restriction"),
			},
			// Destroy testing happens automatically at the end of the TestCase.
		},
	})
}

// TestAccContextRestrictionResource_ExpressionType covers restriction_type =
// "expression": create, read and import, on whichever integration
// CIRCLECI_TEST_VCS_TYPE selects — expressions are not gated to a particular
// VCS integration.
func TestAccContextRestrictionResource_ExpressionType(t *testing.T) {
	orgID := testOrgID(t)
	contextName := "tf-acc-restriction-expr-" + t.Name()
	expr := `pipeline.git.branch == "main"`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccContextRestrictionResourceConfig(orgID, contextName, "expression", expr),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_context_restriction.test_context_restriction",
						tfjsonpath.New("type"),
						knownvalue.StringExact("expression"),
					),
					statecheck.ExpectKnownValue(
						"circleci_context_restriction.test_context_restriction",
						tfjsonpath.New("value"),
						knownvalue.StringExact(expr),
					),
					// project_id is empty for a non-project restriction, never a copy
					// of the value.
					statecheck.ExpectKnownValue(
						"circleci_context_restriction.test_context_restriction",
						tfjsonpath.New("project_id"),
						knownvalue.StringExact(""),
					),
				},
			},
			{
				ResourceName:            "circleci_context_restriction.test_context_restriction",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"name"},
				ImportStateIdFunc:       contextRestrictionImportStateID("circleci_context_restriction.test_context_restriction"),
			},
		},
	})
}

// TestAccContextRestrictionResource_GroupType covers restriction_type =
// "group" on the one shape it ever succeeds against: an OAuth-backed
// organization (a classic gh/<org> or bitbucket/<org> slug), with value equal
// to that organization's own UUID. [NET, measured against two GitHub OAuth
// organizations on 2026-08-21] — see
// circleci.ContextRestrictionTypeGroup's doc comment for the full
// explanation, including why the restriction's id is the organization's UUID
// rather than a freshly minted one.
func TestAccContextRestrictionResource_GroupType(t *testing.T) {
	testRequireVCSType(t, "github_oauth", "bitbucket")

	orgID := testOrgID(t)
	contextName := "tf-acc-restriction-group-" + t.Name()

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccContextRestrictionResourceConfig(orgID, contextName, "group", orgID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_context_restriction.test_context_restriction",
						tfjsonpath.New("type"),
						knownvalue.StringExact("group"),
					),
					statecheck.ExpectKnownValue(
						"circleci_context_restriction.test_context_restriction",
						tfjsonpath.New("value"),
						knownvalue.StringExact(orgID),
					),
					statecheck.ExpectKnownValue(
						"circleci_context_restriction.test_context_restriction",
						tfjsonpath.New("id"),
						knownvalue.StringExact(orgID),
					),
				},
			},
			{
				ResourceName:            "circleci_context_restriction.test_context_restriction",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"name"},
				ImportStateIdFunc:       contextRestrictionImportStateID("circleci_context_restriction.test_context_restriction"),
			},
		},
	})
}

// TestAccContextRestrictionResource_GroupTypeRequiresOAuthOrg covers the
// complementary case: restriction_type = "group" against a standalone
// (circleci/<uuid>) organization always fails, whatever the value, and the
// provider's diagnostic must explain why rather than forward the API's bare
// message. [NET, measured against a GitHub App and a GitLab organization on
// 2026-08-21].
func TestAccContextRestrictionResource_GroupTypeRequiresOAuthOrg(t *testing.T) {
	testRequireVCSType(t, "github_app", "gitlab", "gitlab_selfmanaged", "github_server")

	orgID := testOrgID(t)
	contextName := "tf-acc-restriction-group-rejected-" + t.Name()

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccContextRestrictionResourceConfig(orgID, contextName, "group", orgID),
				ExpectError: regexp.MustCompile(`(?s)Error creating CircleCI context restriction.*OAuth-backed.*standalone`),
			},
		},
	})
}
