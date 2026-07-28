// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccOrganizationDataSource(t *testing.T) {
	organizationID := testOrgID(t)
	organizationName := testOrgName(t)
	organizationSlug := testOrgSlug(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Read testing
			{
				Config: testOrganizationDataSourceConfig(organizationID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_organization.test_organization",
						tfjsonpath.New("id"),
						knownvalue.StringExact(organizationID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_organization.test_organization",
						tfjsonpath.New("name"),
						knownvalue.StringExact(organizationName),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_organization.test_organization",
						tfjsonpath.New("slug"),
						knownvalue.StringExact(organizationSlug),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_organization.test_organization",
						tfjsonpath.New("vcs_type"),
						knownvalue.StringExact("circleci"),
					),
				},
			},
		},
	})
}

func testOrganizationDataSourceConfig(organizationID string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = "https://circleci.com/api/v2"
}

data "circleci_organization" "test_organization" {
  id = %[1]q
}
`, organizationID)
}
