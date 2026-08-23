// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// triggerAccImportID builds the "project_id/pipeline_definition_id/trigger_id"
// import id trigger_resource.go's ImportState requires (see its own comment: the
// definition id has to come from somewhere other than the API, because a read
// carries no reference back to it). It reads pipeline_id rather than
// pipeline_definition_id because every config below still uses the deprecated
// name; Create/Read populate both identically (see setPipelineDefinitionIDs), so
// either would do.
//
// The two-segment "project_id/trigger_id" form these acceptance tests used to
// build here is what earlier provider versions accepted, and ImportState now
// rejects it outright with "Invalid Import ID Format" rather than silently
// importing a trigger with no definition id — so every ImportState step below
// used to fail before ever reaching ImportStateVerify's attribute comparison,
// regardless of what it ignored.
func triggerAccImportID(resourceAddr string) func(s *terraform.State) (string, error) {
	return func(s *terraform.State) (string, error) {
		res := s.RootModule().Resources[resourceAddr]
		if res == nil {
			return "", fmt.Errorf("resource %s not found in state", resourceAddr)
		}

		for _, attr := range []string{"project_id", "pipeline_id", "id"} {
			if _, found := res.Primary.Attributes[attr]; !found {
				return "", fmt.Errorf("attribute %s.%s not found", resourceAddr, attr)
			}
		}

		return fmt.Sprintf(
			"%s/%s/%s",
			res.Primary.Attributes["project_id"],
			res.Primary.Attributes["pipeline_id"],
			res.Primary.Attributes["id"],
		), nil
	}
}

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
				ResourceName:      "circleci_trigger.test_trigger_github",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: triggerAccImportID("circleci_trigger.test_trigger_github"),
			},
		},
	})
}

// circleci_trigger does not exist at all on GitLab, GitLab self-managed or
// Bitbucket Cloud (README.md's compatibility matrix), and this test's
// fixtures (project_id, pipeline_id) are the ones set in every context — so
// nothing else here would skip on those integrations, and the create below
// would fail against the real API rather than the provider.
func TestAccTriggerResourceWebhook(t *testing.T) {
	testRequireVCSType(t, "github_app", "github_oauth", "github_server")

	projectID := testProjectID(t)
	pipelineID := testPipelineID(t)
	webhookTriggerName := rand.Text()

	// GET redacts event_source_web_hook_url on the real API (see the
	// attribute's own MarkdownDescription in trigger_resource.go), so the step
	// below that re-applies the identical configuration forces a real refresh
	// through that redaction — proving, against the live API rather than only
	// TestTriggerResourceUnit_WebhookURLSurvivesRefreshAndUpdate's fake, that an
	// ordinary refresh does not clobber the real, working URL captured at
	// create time with the placeholder.
	urlSurvivesRefresh := statecheck.CompareValue(compare.ValuesSame())

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
					urlSurvivesRefresh.AddStateValue(
						"circleci_trigger.test_trigger_webhook",
						tfjsonpath.New("event_source_web_hook_url"),
					),
				},
			},
			// Refresh testing: an identical re-apply, so the only thing that
			// happens between the two urlSurvivesRefresh checks is a real GET.
			{
				Config: testAccTriggerResourceWebhookConfig(webhookTriggerName, projectID, pipelineID, nil),
				ConfigStateChecks: []statecheck.StateCheck{
					urlSurvivesRefresh.AddStateValue(
						"circleci_trigger.test_trigger_webhook",
						tfjsonpath.New("event_source_web_hook_url"),
					),
				},
			},
			// ImportState testing
			{
				ResourceName:      "circleci_trigger.test_trigger_webhook",
				ImportState:       true,
				ImportStateVerify: true,
				// event_source_web_hook_url only: a fresh import has no prior state to
				// preserve the real URL in, and GET always redacts it (proved by the
				// refresh step above, against this same live API) — so import leaves
				// this attribute null rather than the value the earlier steps hold.
				// See the attribute's own MarkdownDescription in trigger_resource.go.
				ImportStateVerifyIgnore: []string{"event_source_web_hook_url"},
				ImportStateIdFunc:       triggerAccImportID("circleci_trigger.test_trigger_webhook"),
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
				ResourceName:      "circleci_trigger.test_trigger_github_server",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: triggerAccImportID("circleci_trigger.test_trigger_github_server"),
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
				ResourceName:      "circleci_trigger.test_trigger_scheduled",
				ImportState:       true,
				ImportStateVerify: true,
				// event_source_schedule_attribution_actor only: a read reports the
				// resolved actor id, never the "system"/"current" alias the config
				// above sets, and import is a read with no prior state to preserve
				// the alias from — see the attribute's MarkdownDescription in
				// trigger_resource.go for the mutation-tested detail.
				ImportStateVerifyIgnore: []string{"event_source_schedule_attribution_actor"},
				ImportStateIdFunc:       triggerAccImportID("circleci_trigger.test_trigger_scheduled"),
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
				ResourceName:      "circleci_trigger.test_trigger_scheduled",
				ImportState:       true,
				ImportStateVerify: true,
				// event_source_schedule_attribution_actor only: a read reports the
				// resolved actor id, never the "system"/"current" alias the config
				// above sets, and import is a read with no prior state to preserve
				// the alias from — see the attribute's MarkdownDescription in
				// trigger_resource.go for the mutation-tested detail.
				ImportStateVerifyIgnore: []string{"event_source_schedule_attribution_actor"},
				ImportStateIdFunc:       triggerAccImportID("circleci_trigger.test_trigger_scheduled"),
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
			{
				// Back to the valid configuration, purely so the run can clean up
				// after itself. The step above leaves the *invalid* configuration as
				// the last one written to disk, and the framework's post-test destroy
				// plans against whatever configuration is there — so ValidateConfig
				// rejected it again during teardown and the destroy never ran:
				//
				//   Error running post-test destroy, there may be dangling resources:
				//   Error: Invalid CircleCI trigger configuration
				//   CircleCI trigger with github_app provider requires
				//   event_source_repo_external_id (the GitHub repository ID)
				//
				// That failed the test and left a real trigger behind in the test
				// organization on every run. This step is a no-op against state —
				// step 2 never applied — but it makes the last configuration on disk
				// valid, which is what teardown needs. TestAccTriggerResourceMissingRepoExternalId
				// below gets away without one because its single ExpectError step
				// creates nothing, and the framework skips the destroy entirely when
				// state is empty.
				Config: testAccTriggerResourceGithubAppConfig(projectID, pipelineID, repoExternalID),
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
