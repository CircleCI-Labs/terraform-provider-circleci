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

func TestAccTriggerDataSource(t *testing.T) {
	triggerID := testTriggerID(t)
	projectID := testTriggerProjectID(t)
	repoExternalID := testGithubAppRepoExternalID(t)
	repoName := testGithubAppRepoName(t)
	dateRegex, err := regexp.Compile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z$`)
	if err != nil {
		t.Fatal("Could not create Date Regex for testing.")
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Read testing
			{
				Config: testTriggerDataSourceConfig(triggerID, projectID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("id"),
						knownvalue.StringExact(triggerID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("project_id"),
						knownvalue.StringExact(projectID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("checkout_ref"),
						knownvalue.StringExact(""),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("created_at"),
						knownvalue.StringRegexp(dateRegex),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("event_preset"),
						knownvalue.StringExact("all-pushes"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("event_source_provider"),
						knownvalue.StringExact("github_app"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("event_source_repository_external_id"),
						knownvalue.StringExact(repoExternalID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("event_source_repository_name"),
						knownvalue.StringExact(repoName),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("event_source_webhook_url"),
						knownvalue.StringExact(""),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("disabled"),
						knownvalue.Bool(false),
					),
				},
			},
		},
	})
}

func TestAccScheduledTriggerDataSource(t *testing.T) {
	triggerID := testScheduledTriggerID(t)
	projectID := testStaticProjectID(t)
	dateRegex, err := regexp.Compile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z$`)
	if err != nil {
		t.Fatal("Could not create Date Regex for testing.")
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Read testing
			{
				Config: testScheduledTriggerDataSourceConfig(triggerID, projectID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("id"),
						knownvalue.StringExact(triggerID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("project_id"),
						knownvalue.StringExact(projectID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("checkout_ref"),
						knownvalue.StringExact("main"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("created_at"),
						knownvalue.StringRegexp(dateRegex),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("event_preset"),
						knownvalue.StringExact(""),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("event_source_provider"),
						knownvalue.StringExact("schedule"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("event_source_repository_external_id"),
						knownvalue.StringExact(""),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("event_source_repository_name"),
						knownvalue.StringExact(""),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("event_source_webhook_url"),
						knownvalue.StringExact(""),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("disabled"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_trigger.trigger_test",
						tfjsonpath.New("disabled"),
						knownvalue.Bool(true),
					),
				},
			},
		},
	})
}

func testTriggerDataSourceConfig(triggerID, projectID string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = "https://circleci.com/api/v2"
}

data "circleci_trigger" "trigger_test" {
  id = %[1]q
  project_id = %[2]q
}
`, triggerID, projectID)
}

func testScheduledTriggerDataSourceConfig(triggerID, projectID string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = "https://circleci.com/api/v2"
}

data "circleci_trigger" "trigger_test" {
  id = %[1]q
  project_id = %[2]q
}
`, triggerID, projectID)
}
