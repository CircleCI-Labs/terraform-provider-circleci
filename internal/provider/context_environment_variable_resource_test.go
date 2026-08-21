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

// Every name below is unique per run, including the replacement one. This
// resource is created on the *pre-existing* fixture context
// (CIRCLECI_TEST_<key>_CONTEXT_ID), which is shared with
// TestAccContextEnvironmentVariableDataSource and is not torn down between
// runs, and the write route is a PUT upsert (see
// UpsertContextEnvironmentVariable in internal/circleci/environment_variable.go)
// — so a fixed name here does not fail loudly on a re-run, it silently
// overwrites whatever variable of that name is already on the context and then
// deletes it on destroy. The replacement step used the literal name "one",
// which is exactly the kind of name a maintainer might have seeded as
// CIRCLECI_TEST_<key>_CONTEXT_ENV_VAR_NAME.
func TestAccContextEnvironmentVariableResource(t *testing.T) {
	contextID := testContextID(t)
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
				Config: testAccContextEnvironmentVariableResourceConfig(contextID, name, value),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_context_environment_variable.test_env",
						tfjsonpath.New("context_id"),
						knownvalue.StringExact(contextID),
					),
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
			// created, which is the behaviour the step is here to exercise.
			{
				Config: testAccContextEnvironmentVariableResourceConfig(contextID, replacementName, replacementValue),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_context_environment_variable.test_env",
						tfjsonpath.New("context_id"),
						knownvalue.StringExact(contextID),
					),
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
				Config: testAccContextEnvironmentVariableResourceConfig(contextID, replacementName, replacementValue),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_context_environment_variable.test_env",
						tfjsonpath.New("context_id"),
						knownvalue.StringExact(contextID),
					),
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
			// Delete testing automatically occurs in TestCase
		},
	})
}

func testAccContextEnvironmentVariableResourceConfig(contextID, name, value string) string {
	return fmt.Sprintf(`
data "circleci_context" "test_context" {
  id = %[3]q
}

resource "circleci_context_environment_variable" "test_env" {
  context_id = data.circleci_context.test_context.id
  name       = %[1]q
  value      = %[2]q
}
`, name, value, contextID)
}
