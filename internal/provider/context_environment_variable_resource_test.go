// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"errors"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// This resource used to be created on the pre-existing fixture context
// (CIRCLECI_TEST_<key>_CONTEXT_ID), which none of the four CI jobs'
// environment blocks set (see .circleci/config.yml), so this test never ran
// there. It now creates its own scratch context — a resource this test owns
// outright and that Terraform destroys at the end of the TestCase — so the
// only fixture it needs is testOrgID, which every CI job does set. Every name
// below, including the context's own, is unique per run for the same reason
// the old comment gave for its env-var names: PutContextEnvironmentVariable
// is an upsert, so a fixed name would silently overwrite whatever a
// concurrent or crashed run left behind rather than failing loudly.
func TestAccContextEnvironmentVariableResource(t *testing.T) {
	organizationID := testOrgID(t)
	contextName := "tf-acc-ctx-envvar-" + rand.Text()
	name := fmt.Sprintf("N%s", rand.Text())
	value := rand.Text()
	replacementName := fmt.Sprintf("N%s", rand.Text())
	replacementValue := rand.Text()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccContextEnvironmentVariableResourceConfig(organizationID, contextName, name, value),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_context_environment_variable.test_env",
						tfjsonpath.New("name"),
						knownvalue.StringExact(name),
					),
					statecheck.ExpectKnownValue(
						"circleci_context_environment_variable.test_env",
						tfjsonpath.New("value"),
						knownvalue.StringExact(value),
					),
				},
			},
			// Update and Read testing. name has RequiresReplace, so this step is a
			// replacement: the variable created above is deleted and this one
			// created, which is the behaviour the step is here to exercise. The
			// context itself is unchanged across every step, so it is not
			// replaced.
			{
				Config: testAccContextEnvironmentVariableResourceConfig(organizationID, contextName, replacementName, replacementValue),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_context_environment_variable.test_env",
						tfjsonpath.New("name"),
						knownvalue.StringExact(replacementName),
					),
					statecheck.ExpectKnownValue(
						"circleci_context_environment_variable.test_env",
						tfjsonpath.New("value"),
						knownvalue.StringExact(replacementValue),
					),
				},
			},
			// ImportState testing
			{
				ResourceName:                         "circleci_context_environment_variable.test_env",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
				ImportStateVerifyIgnore:              []string{"value"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					contextID, found := s.RootModule().Resources["circleci_context_environment_variable.test_env"].Primary.Attributes["context_id"]
					if !found {
						return "", errors.New("attribute circleci_context_environment_variable.test_env.context_id not found")
					}
					envName, found := s.RootModule().Resources["circleci_context_environment_variable.test_env"].Primary.Attributes["name"]
					if !found {
						return "", errors.New("attribute circleci_context_environment_variable.test_env.name not found")
					}
					return fmt.Sprintf("%s/%s", contextID, envName), nil
				},
			},
			// Re-apply config after import to reconcile value in state
			{
				Config: testAccContextEnvironmentVariableResourceConfig(organizationID, contextName, replacementName, replacementValue),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_context_environment_variable.test_env",
						tfjsonpath.New("name"),
						knownvalue.StringExact(replacementName),
					),
					statecheck.ExpectKnownValue(
						"circleci_context_environment_variable.test_env",
						tfjsonpath.New("value"),
						knownvalue.StringExact(replacementValue),
					),
				},
			},
			// Delete testing automatically occurs in TestCase, for both the
			// environment variable and the scratch context it lives on.
		},
	})
}

func testAccContextEnvironmentVariableResourceConfig(organizationID, contextName, name, value string) string {
	return fmt.Sprintf(`
resource "circleci_context" "test_context" {
  organization_id = %[1]q
  name             = %[2]q
}

resource "circleci_context_environment_variable" "test_env" {
  context_id = circleci_context.test_context.id
  name       = %[3]q
  value      = %[4]q
}
`, organizationID, contextName, name, value)
}
