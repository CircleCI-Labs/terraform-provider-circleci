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

func TestAccContextRestrictionResource(t *testing.T) {
	//t.Skip("Might rise issues given concurrent executions").
	contextID := testContextID(t)
	projectID := testProjectID(t)
	uuidRegex, err := regexp.Compile(`[a-z0-9]{8}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{12}`)
	if err != nil {
		t.Fatalf("Regex to check UUID could not be created")
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccContextRestrictionResourceConfig(contextID, "project", projectID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_context_restriction.test_context_restriction",
						tfjsonpath.New("context_id"),
						knownvalue.StringExact(contextID),
					),
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
					/*
						statecheck.ExpectKnownValue(
							"circleci_context_restriction.test_context_restriction",
							tfjsonpath.New("name"),
							knownvalue.StringExact("All members"),
						),
					*/
				},
			},
			// ImportState testing
			{
				ResourceName:            "circleci_context_restriction.test_context_restriction",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"name"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					// 1. Get the computed 'id' (context ID)
					contextID, found := s.RootModule().Resources["circleci_context_restriction.test_context_restriction"].Primary.Attributes["context_id"]
					if !found {
						return "", errors.New("attribute circleci_contexcircleci_context_restrictiont.test_context_restriction.context_id not found")
					}

					// 2. Get the known 'organization_id'
					restrictionID, found := s.RootModule().Resources["circleci_context_restriction.test_context_restriction"].Primary.Attributes["id"]
					if !found {
						return "", errors.New("attribute circleci_context_restriction.test_context_restriction.id not found")
					}

					// 3. Return the composite ID string: "RESTRICTION_ID/CONTEXT_ID"
					return fmt.Sprintf("%s/%s", contextID, restrictionID), nil
				},
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

func testAccContextRestrictionResourceConfig(contextID, sometype, value string) string {
	return fmt.Sprintf(`
data "circleci_context" "test_context" {
  id = %[3]q
}

resource "circleci_context_restriction" "test_context_restriction" {
	context_id = data.circleci_context.test_context.id
	type = %[1]q
	value = %[2]q
}
`, sometype, value, contextID)
}
