// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccProjectEnvironmentVariableDataSource(t *testing.T) {
	projectSlug := testProjectSlug(t)
	name := fmt.Sprintf("N%s", rand.Text())
	value := rand.Text()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Read testing - create a resource first, then read it via data source
			{
				Config: testAccProjectEnvironmentVariableDataSourceConfig(name, value, projectSlug),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variable.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact(name),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variable.test",
						tfjsonpath.New("project_slug"),
						knownvalue.StringExact(projectSlug),
					),
				},
			},
		},
	})
}

func testAccProjectEnvironmentVariableDataSourceConfig(name, value, projectSlug string) string {
	return fmt.Sprintf(`
resource "circleci_project_environment_variable" "test_env" {
  project_slug = %[3]q
  name         = %[1]q
  value        = %[2]q
}

data "circleci_project_environment_variable" "test" {
  project_slug = circleci_project_environment_variable.test_env.project_slug
  name         = circleci_project_environment_variable.test_env.name
}
`, name, value, projectSlug)
}
