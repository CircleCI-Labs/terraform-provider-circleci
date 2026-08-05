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
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccRunnerTokenResource(t *testing.T) {
	organizationId := testOrgID(t)
	resourceClass := fmt.Sprintf("%s/acc-test-runner", testRunnerNamespace(t))
	nickname := "acc-test-token"
	uuidRegex := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccRunnerTokenConfig(organizationId, resourceClass, nickname),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("resource_class"),
						knownvalue.StringExact(resourceClass),
					),
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("nickname"),
						knownvalue.StringExact(nickname),
					),
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("id"),
						knownvalue.StringRegexp(uuidRegex),
					),
					// token is sensitive but should be non-empty after create
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("token"),
						knownvalue.NotNull(),
					),
				},
			},
			// ImportState testing — token value will be empty after import since it's write-once
			{
				ResourceName:      "circleci_runner_token.test",
				ImportState:       true,
				ImportStateVerify: true,
				// Neither organization attribute survives an import: the import ID
				// is "resource_class/token_id" and the token representation carries
				// no organization, so there is nothing for Read to fill them from.
				ImportStateVerifyIgnore: []string{"token", "organization_id", "org_id"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rc, found := s.RootModule().Resources["circleci_runner_token.test"].Primary.Attributes["resource_class"]
					if !found {
						return "", errors.New("attribute resource_class not found")
					}
					id, found := s.RootModule().Resources["circleci_runner_token.test"].Primary.Attributes["id"]
					if !found {
						return "", errors.New("attribute id not found")
					}
					return fmt.Sprintf("%s/%s", rc, id), nil
				},
			},
			// Update after import testing. organization_id/org_id are not
			// RequiresReplace on this resource (see the Schema method's comment
			// on why), so filling one back in from configuration is a genuine
			// in-place update, not a replacement -- unlike before this was
			// fixed, when supplying the organization ConfigValidators requires
			// would destroy and recreate the imported token. See
			// TestRunnerTokenImport for the fake-backed version of this.
			{
				Config: testAccRunnerTokenConfig(organizationId, resourceClass, nickname),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(organizationId),
					),
				},
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

// TestRunnerTokenImport is the fake-backed counterpart to
// TestAccRunnerTokenResource's import step, and exists for the same reason
// TestRunnerResourceClassImport does: the acceptance test needs TF_ACC and
// real credentials, so it never runs for a developer running `go test ./...`.
//
// It is also a regression test for a real defect this review found: before it
// was fixed, organization_id/org_id were RequiresReplaceIfConfigured on this
// resource. The token representation carries no organization for Read to
// recover, so neither attribute survives an import -- and because
// ConfigValidators requires exactly one of them to be configured, the very
// first plan after import always supplied one, which forced a replacement:
// destroying the imported token (permanently invalidating it) and creating a
// brand new one in its place. Importing a runner token was therefore actively
// destructive, not merely unable to recover the secret value. This asserts
// the fixed behaviour: a genuine, in-place Update, matching
// circleci_runner_resource_class.
func TestRunnerTokenImport(t *testing.T) {
	api := newRunnerFakeAPI(t)
	api.respond("GET", "/api/v3/runner/token", `{"items":[
	  {"id": "11111111-2222-3333-4444-555555555555", "nickname": "ci", "resource_class": "acc-ns/linux", "created_at": "2026-01-01T00:00:00Z"}
	]}`)

	const orgID = "00000000-1111-2222-3333-444444444444"

	config := runnerProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_runner_token" "test" {
  organization_id = %q
  resource_class  = "acc-ns/linux"
  nickname        = "ci"
}
`, orgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "circleci_runner_token.test",
				ImportState:        true,
				ImportStateId:      "acc-ns/linux/11111111-2222-3333-4444-555555555555",
				ImportStatePersist: true,
				Config:             config,
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_runner_token.test", plancheck.ResourceActionUpdate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(orgID),
					),
					// token stays null: the API never discloses it again after create,
					// so nothing ever fills it in after an import.
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("token"),
						knownvalue.Null(),
					),
				},
			},
		},
	})
}

func testAccRunnerTokenConfig(organizationId, resourceClass, nickname string) string {
	return fmt.Sprintf(`
resource "circleci_runner_resource_class" "test" {
  organization_id = %[1]q
  resource_class  = %[2]q
}

resource "circleci_runner_token" "test" {
  organization_id = %[1]q
  resource_class  = circleci_runner_resource_class.test.resource_class
  nickname        = %[3]q
}
`, organizationId, resourceClass, nickname)
}
