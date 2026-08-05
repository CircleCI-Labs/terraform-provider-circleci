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

// A scheduled trigger is a circleci_trigger, which does not exist at all on
// GitLab, GitLab self-managed or Bitbucket Cloud (README.md's compatibility
// matrix). This test's fixtures (CIRCLECI_TEST_SCHEDULED_TRIGGER_ID,
// CIRCLECI_TEST_STATIC_PROJECT_ID) are documented as set in every context, so
// without this gate the test would read against the real API on those
// integrations rather than skip.
func TestAccScheduledTriggerDataSource(t *testing.T) {
	testRequireVCSType(t, "github_app", "github_oauth", "github_server")

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

// TestTriggerDataSourceUnit_BothEventSourceSpellingsAgree is the fake-backed
// companion to TestAccTriggerDataSource, for the one thing that test cannot
// assert without a live account: that the deprecated event source attributes
// (`event_source_repository_name`, `event_source_repository_external_id`,
// `event_source_webhook_url`) and their resource-matching replacements
// (`event_source_repo_full_name`, `event_source_repo_external_id`,
// `event_source_web_hook_url`) report exactly the same values from one read. See
// trigger_event_source_spelling.go.
func TestTriggerDataSourceUnit_BothEventSourceSpellingsAgree(t *testing.T) {
	api, host := newFakeTriggerAPI(t)

	const seededTriggerID = "t1"

	api.mu.Lock()
	api.triggers[fakeTriggerProjectID+"/"+seededTriggerID] = map[string]any{
		"id": seededTriggerID,
		"event_source": map[string]any{
			"provider": "github_app",
			"repo":     map[string]any{"full_name": "acme/api", "external_id": "123456"},
		},
	}
	api.mu.Unlock()

	config := fmt.Sprintf(`
provider "circleci" {
  host       = %[1]q
  key        = "fake-token"
  deployment = "cloud"
}

data "circleci_trigger" "test" {
  id         = %[2]q
  project_id = %[3]q
}
`, host, seededTriggerID, fakeTriggerProjectID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.circleci_trigger.test",
						tfjsonpath.New("event_source_repository_name"), knownvalue.StringExact("acme/api")),
					statecheck.ExpectKnownValue("data.circleci_trigger.test",
						tfjsonpath.New("event_source_repo_full_name"), knownvalue.StringExact("acme/api")),
					statecheck.ExpectKnownValue("data.circleci_trigger.test",
						tfjsonpath.New("event_source_repository_external_id"), knownvalue.StringExact("123456")),
					statecheck.ExpectKnownValue("data.circleci_trigger.test",
						tfjsonpath.New("event_source_repo_external_id"), knownvalue.StringExact("123456")),
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
