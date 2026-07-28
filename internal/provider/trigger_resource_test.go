// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccTriggerResourceGithub(t *testing.T) {
	projectID := testProjectID(t)
	pipelineID := testPipelineID(t)
	repoExternalID := testGithubAppRepoExternalID(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccTriggerResourceGithubAppConfig(projectID, pipelineID, repoExternalID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_github",
						tfjsonpath.New("project_id"),
						knownvalue.StringExact(projectID),
					),
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_github",
						tfjsonpath.New("pipeline_id"),
						knownvalue.StringExact(pipelineID),
					),
				},
			},
			// ImportState testing
			{
				ResourceName:            "circleci_trigger.test_trigger_github",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"pipeline_id"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					triggerId, found := s.RootModule().Resources["circleci_trigger.test_trigger_github"].Primary.Attributes["id"]
					if !found {
						return "", errors.New("attribute circleci_trigger.test_trigger_github.id not found")
					}
					projectId, found := s.RootModule().Resources["circleci_trigger.test_trigger_github"].Primary.Attributes["project_id"]
					if !found {
						return "", errors.New("attribute circleci_trigger.test_trigger_github.project_id not found")
					}
					return fmt.Sprintf("%s/%s", projectId, triggerId), nil
				},
			},
		},
	})
}

func TestAccTriggerResourceWebhook(t *testing.T) {
	projectID := testProjectID(t)
	pipelineID := testPipelineID(t)
	webhookTriggerName := rand.Text()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccTriggerResourceWebhookConfig(webhookTriggerName, projectID, pipelineID, nil),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_webhook",
						tfjsonpath.New("project_id"),
						knownvalue.StringExact(projectID),
					),
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_webhook",
						tfjsonpath.New("pipeline_id"),
						knownvalue.StringExact(pipelineID),
					),
				},
			},
			// ImportState testing
			{
				ResourceName:            "circleci_trigger.test_trigger_webhook",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"event_source_web_hook_url", "pipeline_id"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					triggerId, found := s.RootModule().Resources["circleci_trigger.test_trigger_webhook"].Primary.Attributes["id"]
					if !found {
						return "", errors.New("attribute circleci_trigger.test_trigger_webhook.id not found")
					}
					projectId, found := s.RootModule().Resources["circleci_trigger.test_trigger_webhook"].Primary.Attributes["project_id"]
					if !found {
						return "", errors.New("attribute circleci_trigger.test_trigger_webhook.project_id not found")
					}
					return fmt.Sprintf("%s/%s", projectId, triggerId), nil
				},
			},
		},
	})
}

func TestAccTriggerResourceGithubServer(t *testing.T) {
	projectID := testGithubServerProjectID(t)
	pipelineID := testGithubServerPipelineID(t)
	repoExternalID := testGithubServerRepoExternalID(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccTriggerResourceGithubServerConfig(projectID, pipelineID, repoExternalID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_github_server",
						tfjsonpath.New("project_id"),
						knownvalue.StringExact(projectID),
					),
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_github_server",
						tfjsonpath.New("pipeline_id"),
						knownvalue.StringExact(pipelineID),
					),
				},
			},
			// ImportState testing
			{
				ResourceName:            "circleci_trigger.test_trigger_github_server",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"pipeline_id"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					triggerId, found := s.RootModule().Resources["circleci_trigger.test_trigger_github_server"].Primary.Attributes["id"]
					if !found {
						return "", errors.New("attribute circleci_trigger.test_trigger_github_server.id not found")
					}
					projectId, found := s.RootModule().Resources["circleci_trigger.test_trigger_github_server"].Primary.Attributes["project_id"]
					if !found {
						return "", errors.New("attribute circleci_trigger.test_trigger_github_server.project_id not found")
					}
					return fmt.Sprintf("%s/%s", projectId, triggerId), nil
				},
			},
		},
	})
}

func TestAccTriggerResourceScheduled(t *testing.T) {
	projectID := testProjectID(t)
	repoExternalID := testGithubAppRepoExternalID(t)
	pipelineName := rand.Text()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccTriggerResourceScheduledConfig(
					projectID,
					repoExternalID,
					pipelineName,
					"0 * * * *",
					false,
					map[string]string{"run_nightly_foo": "true", "branch": "main"},
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_scheduled",
						tfjsonpath.New("project_id"),
						knownvalue.StringExact(projectID),
					),
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_scheduled",
						tfjsonpath.New("event_source_provider"),
						knownvalue.StringExact("schedule"),
					),
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_scheduled",
						tfjsonpath.New("event_source_schedule_cron_expression"),
						knownvalue.StringExact("0 * * * *"),
					),
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_scheduled",
						tfjsonpath.New("disabled"),
						knownvalue.Bool(false),
					),
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_scheduled",
						tfjsonpath.New("parameters"),
						knownvalue.MapExact(map[string]knownvalue.Check{
							"run_nightly_foo": knownvalue.StringExact("true"),
							"branch":          knownvalue.StringExact("main"),
						}),
					),
				},
			},
			// Update testing — change cron expression, disable the trigger, and flip one parameter
			{
				Config: testAccTriggerResourceScheduledConfig(
					projectID,
					repoExternalID,
					pipelineName,
					"0 12 * * *",
					true,
					map[string]string{"run_nightly_foo": "false", "branch": "main"},
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_scheduled",
						tfjsonpath.New("event_source_schedule_cron_expression"),
						knownvalue.StringExact("0 12 * * *"),
					),
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_scheduled",
						tfjsonpath.New("disabled"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_scheduled",
						tfjsonpath.New("parameters"),
						knownvalue.MapExact(map[string]knownvalue.Check{
							"run_nightly_foo": knownvalue.StringExact("false"),
							"branch":          knownvalue.StringExact("main"),
						}),
					),
				},
			},
			// Removing the parameters block should clear them on the API
			{
				Config: testAccTriggerResourceScheduledConfig(
					projectID,
					repoExternalID,
					pipelineName,
					"0 12 * * *",
					true,
					nil,
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_scheduled",
						tfjsonpath.New("parameters"),
						knownvalue.Null(),
					),
				},
			},
			// ImportState testing
			{
				ResourceName:            "circleci_trigger.test_trigger_scheduled",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"pipeline_id", "event_source_schedule_attribution_actor"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					triggerId, found := s.RootModule().Resources["circleci_trigger.test_trigger_scheduled"].Primary.Attributes["id"]
					if !found {
						return "", errors.New("attribute circleci_trigger.test_trigger_scheduled.id not found")
					}
					projectId, found := s.RootModule().Resources["circleci_trigger.test_trigger_scheduled"].Primary.Attributes["project_id"]
					if !found {
						return "", errors.New("attribute circleci_trigger.test_trigger_scheduled.project_id not found")
					}
					return fmt.Sprintf("%s/%s", projectId, triggerId), nil
				},
			},
		},
	})
}

func TestAccTriggerResourceScheduledNoParameters(t *testing.T) {
	projectID := testProjectID(t)
	repoExternalID := testGithubAppRepoExternalID(t)
	pipelineName := rand.Text()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccTriggerResourceScheduledConfig(
					projectID,
					repoExternalID,
					pipelineName,
					"0 * * * *",
					false,
					nil,
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_scheduled",
						tfjsonpath.New("parameters"),
						knownvalue.Null(),
					),
				},
			},
			{
				ResourceName:            "circleci_trigger.test_trigger_scheduled",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"pipeline_id", "event_source_schedule_attribution_actor"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					triggerId, found := s.RootModule().Resources["circleci_trigger.test_trigger_scheduled"].Primary.Attributes["id"]
					if !found {
						return "", errors.New("attribute circleci_trigger.test_trigger_scheduled.id not found")
					}
					projectId, found := s.RootModule().Resources["circleci_trigger.test_trigger_scheduled"].Primary.Attributes["project_id"]
					if !found {
						return "", errors.New("attribute circleci_trigger.test_trigger_scheduled.project_id not found")
					}
					return fmt.Sprintf("%s/%s", projectId, triggerId), nil
				},
			},
		},
	})
}

func TestAccTriggerResourceWebhookRejectsParameters(t *testing.T) {
	projectID := testProjectID(t)
	pipelineID := testPipelineID(t)
	webhookTriggerName := rand.Text()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccTriggerResourceWebhookConfig(
					webhookTriggerName,
					projectID,
					pipelineID,
					map[string]string{"foo": "bar"},
				),
				ExpectError: regexp.MustCompile("does not support parameters"),
			},
		},
	})
}

func testAccTriggerResourceScheduledConfig(project_id, repo_external_id, pipeline_name, cron_expression string, disabled bool, parameters map[string]string) string {
	return fmt.Sprintf(`
resource "circleci_pipeline" "test_pipeline_scheduled" {
  project_id                       = %[5]q
  name                             = %[1]q
  description                      = "pipeline for scheduled trigger acceptance test"
  config_source_provider           = "github_app"
  config_source_file_path          = ".circleci/config.yml"
  config_source_repo_external_id   = %[6]q
  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = %[6]q
}

resource "circleci_trigger" "test_trigger_scheduled" {
  project_id                              = circleci_pipeline.test_pipeline_scheduled.project_id
  pipeline_id                             = circleci_pipeline.test_pipeline_scheduled.id
  event_source_provider                   = "schedule"
  event_name                              = "scheduled_pipeline"
  checkout_ref                            = "main"
  config_ref                              = "main"
  event_source_schedule_cron_expression   = %[2]q
  event_source_schedule_attribution_actor = "system"
  disabled                                = %[3]t
%[4]s}
`, pipeline_name, cron_expression, disabled, renderParametersHCL(parameters), project_id, repo_external_id)
}

func renderParametersHCL(parameters map[string]string) string {
	if len(parameters) == 0 {
		return ""
	}
	keys := make([]string, 0, len(parameters))
	for k := range parameters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("  parameters = {\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "    %q = %q\n", k, parameters[k])
	}
	b.WriteString("  }\n")
	return b.String()
}

func testAccTriggerResourceGithubServerConfig(project_id, pipeline_id, repo_external_id string) string {
	return fmt.Sprintf(`
resource "circleci_trigger" "test_trigger_github_server" {
  project_id                     = %[1]q
  pipeline_id                    = %[2]q
  event_source_provider          = "github_server"
  event_source_repo_external_id  = %[3]q
  event_preset                   = "all-pushes"
  disabled                       = false
}
`, project_id, pipeline_id, repo_external_id)
}

func testAccTriggerResourceGithubAppConfig(project_id, pipeline_id, repo_external_id string) string {
	return fmt.Sprintf(`
resource "circleci_trigger" "test_trigger_github" {
  project_id 				= %[1]q
  pipeline_id 				= %[2]q
  event_source_provider = "github_app"
  event_source_repo_external_id = %[3]q
  event_preset = "all-pushes"
  checkout_ref = "some checkout ref github"
  config_ref = "some config ref github"
  disabled = false
}
`, project_id, pipeline_id, repo_external_id)
}

func testAccTriggerResourceGithubAppConfigNoRepoExternalId(project_id, pipeline_id string) string {
	return fmt.Sprintf(`
resource "circleci_trigger" "test_trigger_github" {
  project_id            = %[1]q
  pipeline_id           = %[2]q
  event_source_provider = "github_app"
  event_preset          = "all-pushes"
  checkout_ref          = "some checkout ref github"
  config_ref            = "some config ref github"
  disabled              = false
}
`, project_id, pipeline_id)
}

func testAccTriggerResourceWebhookConfig(event_name, project_id, pipeline_id string, parameters map[string]string) string {
	return fmt.Sprintf(`
resource "circleci_trigger" "test_trigger_webhook" {
  event_name				= %[1]q
  project_id 				= %[2]q
  pipeline_id 				= %[3]q
  event_source_provider = "webhook"
  checkout_ref = "some checkout ref webhook"
  config_ref = "some config ref webhook"
  event_source_web_hook_sender = "web hook sender"
  disabled = false
%[4]s}
`, event_name, project_id, pipeline_id, renderParametersHCL(parameters))
}

func TestAccTriggerResourceUpdateRemovesRepoExternalId(t *testing.T) {
	projectID := testProjectID(t)
	pipelineID := testPipelineID(t)
	repoExternalID := testGithubAppRepoExternalID(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccTriggerResourceGithubAppConfig(projectID, pipelineID, repoExternalID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_trigger.test_trigger_github",
						tfjsonpath.New("event_source_repo_external_id"),
						knownvalue.StringExact(repoExternalID),
					),
				},
			},
			{
				Config:      testAccTriggerResourceGithubAppConfigNoRepoExternalId(projectID, pipelineID),
				ExpectError: regexp.MustCompile(`requires[\s]+event_source_repo_external_id`),
			},
		},
	})
}

func TestAccTriggerResourceMissingRepoExternalId(t *testing.T) {
	projectID := testProjectID(t)
	pipelineID := testPipelineID(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "circleci_trigger" "test_missing_repo_id" {
  project_id            = %[1]q
  pipeline_id           = %[2]q
  event_source_provider = "github_app"
  event_preset          = "all-pushes"
}
`, projectID, pipelineID),
				// [\s]+ tolerates the newline Terraform CLI inserts when word-wrapping diagnostics.
				ExpectError: regexp.MustCompile(`requires[\s]+event_source_repo_external_id`),
			},
		},
	})
}
