// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// The tests in this file cover BOTH Terraform type names this resource answers
// to. circleci_pipeline is the deprecated alias; circleci_pipeline_definition is
// the canonical name and what practitioners are told to write
// (pipeline_resource_rename.go).
//
// Until the TestAccPipelineDefinitionResource* tests below existed, every
// acceptance configuration in the repository used the alias, so the canonical
// name had never made a single request to a real API — the two names share one
// implementation, but they do not share their Metadata, their schema
// DeprecationMessage, or their registration, and nothing proved the canonical
// registration was reachable at all.
//
// Explicit pipeline definitions are only creatable where the API accepts a
// create at all: measured over the network, POST works on GitHub App and hybrid
// organizations and answers 400 on pure GitHub OAuth and on GitLab, where every
// definition is implicit and created by CircleCI itself. Hence
// testRequireVCSType on each test below rather than a bare skip: a run pointed
// at GitLab reports "did not cover this" instead of failing in a way that reads
// like a regression.

// TestAccPipelineResource is the DEPRECATED circleci_pipeline alias against a
// real API. It stays covered alongside the canonical name below: the two are one
// implementation but not one registration, and the alias is what existing
// configurations still say.
func TestAccPipelineResource(t *testing.T) {
	// This test pairs the active integration's project (testProjectID) with a
	// GitHub App repository and a github_app config_source, so it only makes
	// sense where explicit definitions are creatable. Without the gate, a run
	// pointed at GitLab fixtures fails on a create the API was never going to
	// accept, which reads exactly like a regression in this resource.
	testRequireVCSType(t, "github_app")

	projectID := testProjectID(t)
	repoExternalID := testGithubAppRepoExternalID(t)
	uuidRegex, err := regexp.Compile(`[a-z0-9]{8}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{12}`)
	if err != nil {
		t.Fatalf("Regex to check UUID could not be created")
	}
	dateRegex, err := regexp.Compile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z$`)
	if err != nil {
		t.Fatal("Could not create Date Regex for testing.")
	}
	pipelineName := rand.Text()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccPipelineResourceConfig(projectID, repoExternalID, pipelineName, "original description"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test_pipeline",
						tfjsonpath.New("project_id"),
						knownvalue.StringExact(projectID),
					),
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test_pipeline",
						tfjsonpath.New("name"),
						knownvalue.StringExact(pipelineName),
					),
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test_pipeline",
						tfjsonpath.New("id"),
						knownvalue.StringRegexp(uuidRegex),
					),
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test_pipeline",
						tfjsonpath.New("created_at"),
						knownvalue.StringRegexp(dateRegex),
					),
				},
			},
			{
				Config: testAccPipelineResourceConfig(projectID, repoExternalID, pipelineName, "updated description"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test_pipeline",
						tfjsonpath.New("project_id"),
						knownvalue.StringExact(projectID),
					),
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test_pipeline",
						tfjsonpath.New("name"),
						knownvalue.StringExact(pipelineName),
					),
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test_pipeline",
						tfjsonpath.New("description"),
						knownvalue.StringExact("updated description"),
					),
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test_pipeline",
						tfjsonpath.New("id"),
						knownvalue.StringRegexp(uuidRegex),
					),
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test_pipeline",
						tfjsonpath.New("created_at"),
						knownvalue.StringRegexp(dateRegex),
					),
				},
			},
			// ImportState testing
			{
				ResourceName:      "circleci_pipeline.test_pipeline",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					pipelineId, found := s.RootModule().Resources["circleci_pipeline.test_pipeline"].Primary.Attributes["id"]
					if !found {
						return "", errors.New("attribute circleci_pipeline.test_pipeline.id not found")
					}
					projectId, found := s.RootModule().Resources["circleci_pipeline.test_pipeline"].Primary.Attributes["project_id"]
					if !found {
						return "", errors.New("attribute circleci_pipeline.test_pipeline.project_id not found")
					}
					return fmt.Sprintf("%s/%s", projectId, pipelineId), nil
				},
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

func TestAccPipelineResourceGithubServer(t *testing.T) {
	projectID := testGithubServerProjectID(t)
	repoExternalID := testGithubServerRepoExternalID(t)
	uuidRegex, err := regexp.Compile(`[a-z0-9]{8}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{12}`)
	if err != nil {
		t.Fatalf("Regex to check UUID could not be created")
	}
	dateRegex, err := regexp.Compile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z$`)
	if err != nil {
		t.Fatal("Could not create Date Regex for testing.")
	}
	pipelineName := rand.Text()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccPipelineResourceGithubServerConfig(projectID, repoExternalID, pipelineName, "original description"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test_pipeline_github_server",
						tfjsonpath.New("project_id"),
						knownvalue.StringExact(projectID),
					),
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test_pipeline_github_server",
						tfjsonpath.New("name"),
						knownvalue.StringExact(pipelineName),
					),
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test_pipeline_github_server",
						tfjsonpath.New("id"),
						knownvalue.StringRegexp(uuidRegex),
					),
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test_pipeline_github_server",
						tfjsonpath.New("created_at"),
						knownvalue.StringRegexp(dateRegex),
					),
				},
			},
			{
				Config: testAccPipelineResourceGithubServerConfig(projectID, repoExternalID, pipelineName, "updated description"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test_pipeline_github_server",
						tfjsonpath.New("description"),
						knownvalue.StringExact("updated description"),
					),
				},
			},
			// ImportState testing
			{
				ResourceName:      "circleci_pipeline.test_pipeline_github_server",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					pipelineId, found := s.RootModule().Resources["circleci_pipeline.test_pipeline_github_server"].Primary.Attributes["id"]
					if !found {
						return "", errors.New("attribute circleci_pipeline.test_pipeline_github_server.id not found")
					}
					projectId, found := s.RootModule().Resources["circleci_pipeline.test_pipeline_github_server"].Primary.Attributes["project_id"]
					if !found {
						return "", errors.New("attribute circleci_pipeline.test_pipeline_github_server.project_id not found")
					}
					return fmt.Sprintf("%s/%s", projectId, pipelineId), nil
				},
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

func testAccPipelineResourceGithubServerConfig(project_id, repo_external_id, name, description string) string {
	return fmt.Sprintf(`
resource "circleci_pipeline" "test_pipeline_github_server" {
	project_id = %[1]q
	name = %[2]q
	description = %[3]q
	config_source_provider = "github_server"
	config_source_file_path = "config_source_file_path"
	config_source_repo_external_id = %[4]q
	checkout_source_provider = "github_server"
	checkout_source_repo_external_id = %[4]q
}
`, project_id, name, description, repo_external_id)
}

func testAccPipelineResourceConfig(project_id, repo_external_id, name, description string) string {
	return fmt.Sprintf(`
resource "circleci_pipeline" "test_pipeline" {
	project_id = %[1]q
	name = %[2]q
	description = %[3]q
	config_source_provider = "github_app"
	config_source_file_path = "config_source_file_path"
	config_source_repo_external_id = %[4]q
	checkout_source_provider = "github_app"
	checkout_source_repo_external_id = %[4]q
}
`, project_id, name, description, repo_external_id)
}

// pipelineDefinitionResourceConfig is the canonical-name configuration:
// circleci_pipeline_definition, not the circleci_pipeline alias. Everything else
// is identical to testAccPipelineResourceConfig, which is the point — the two
// names must behave the same against a real API.
func pipelineDefinitionResourceConfig(projectID, repoExternalID, name, description, configFilePath, provider string) string {
	return fmt.Sprintf(`
resource "circleci_pipeline_definition" "test" {
	project_id                       = %[1]q
	name                             = %[2]q
	description                      = %[3]q
	config_source_provider           = %[6]q
	config_source_file_path          = %[5]q
	config_source_repo_external_id   = %[4]q
	checkout_source_provider         = %[6]q
	checkout_source_repo_external_id = %[4]q
}
`, projectID, name, description, repoExternalID, configFilePath, provider)
}

// pipelineDefinitionImportID builds the two-segment import id this resource
// takes, "project_id/definition_id", from state.
func pipelineDefinitionImportID(address string) func(*terraform.State) (string, error) {
	return func(s *terraform.State) (string, error) {
		res, found := s.RootModule().Resources[address]
		if !found {
			return "", fmt.Errorf("resource %s not found in state", address)
		}

		definitionID, found := res.Primary.Attributes["id"]
		if !found {
			return "", errors.New("attribute " + address + ".id not found")
		}
		projectID, found := res.Primary.Attributes["project_id"]
		if !found {
			return "", errors.New("attribute " + address + ".project_id not found")
		}

		return projectID + "/" + definitionID, nil
	}
}

// pipelineDefinitionSteps is the body of the canonical-name acceptance test,
// shared by the GitHub App and GitHub Server variants because the contract is
// the same on both and only the fixtures differ.
//
// The steps are, in order: create; update description in place; update
// config_source_file_path in place (the only config_source field the update
// route accepts, so this is the one that proves the PATCH really lands); and
// import.
//
// The import step carries NO ImportStateVerifyIgnore. That is deliberate and is
// itself the assertion: every attribute this resource stores is recoverable from
// project_id plus the definition id, so a single ignored attribute here would
// mean import silently produces state that differs from what apply produced.
func pipelineDefinitionSteps(t *testing.T, projectID, repoExternalID, vcsProvider string) []resource.TestStep {
	t.Helper()

	const address = "circleci_pipeline_definition.test"

	uuidRegex := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	dateRegex := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z$`)
	name := "tf-acc-" + rand.Text()

	config := func(description, configFilePath string) string {
		return pipelineDefinitionResourceConfig(projectID, repoExternalID, name, description, configFilePath, vcsProvider)
	}

	return []resource.TestStep{
		{
			Config: config("original description", ".circleci/config.yml"),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(address, tfjsonpath.New("id"), knownvalue.StringRegexp(uuidRegex)),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("created_at"), knownvalue.StringRegexp(dateRegex)),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("project_id"), knownvalue.StringExact(projectID)),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("name"), knownvalue.StringExact(name)),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("description"), knownvalue.StringExact("original description")),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("config_source_provider"), knownvalue.StringExact(vcsProvider)),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("config_source_file_path"), knownvalue.StringExact(".circleci/config.yml")),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("config_source_repo_external_id"), knownvalue.StringExact(repoExternalID)),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("checkout_source_provider"), knownvalue.StringExact(vcsProvider)),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("checkout_source_repo_external_id"), knownvalue.StringExact(repoExternalID)),
				// full_name is resolved by the API from external_id and never
				// sent on the wire by this provider, so a non-empty value here
				// is what proves the read path decoded the nested repo object.
				statecheck.ExpectKnownValue(address, tfjsonpath.New("checkout_source_repo_full_name"), knownvalue.NotNull()),
			},
		},
		{
			Config: config("updated description", ".circleci/config.yml"),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(address, tfjsonpath.New("description"), knownvalue.StringExact("updated description")),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("name"), knownvalue.StringExact(name)),
			},
		},
		{
			// config_source.file_path is the ONLY config_source field the update
			// route accepts. If the PATCH body were built wrong, the API would
			// keep the old path and this step would fail as a non-empty plan
			// after apply rather than passing quietly.
			Config: config("updated description", ".circleci/other-config.yml"),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(address, tfjsonpath.New("config_source_file_path"), knownvalue.StringExact(".circleci/other-config.yml")),
			},
		},
		{
			ResourceName:      address,
			ImportState:       true,
			ImportStateVerify: true,
			ImportStateIdFunc: pipelineDefinitionImportID(address),
		},
	}
}

// TestAccPipelineDefinitionResource covers the CANONICAL type name against a
// real GitHub App organization: create, two in-place updates and an import that
// round-trips with no ignored attributes.
func TestAccPipelineDefinitionResource(t *testing.T) {
	testRequireVCSType(t, "github_app")

	projectID := testProjectID(t)
	repoExternalID := testGithubAppRepoExternalID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    pipelineDefinitionSteps(t, projectID, repoExternalID, "github_app"),
	})
}

// TestAccPipelineDefinitionResourceGithubServer is the same coverage for the
// canonical type name on a GitHub Server organization, whose fixtures are
// separate because github_server is a distinct config_source provider.
func TestAccPipelineDefinitionResourceGithubServer(t *testing.T) {
	testRequireVCSType(t, "github_server")

	projectID := testGithubServerProjectID(t)
	repoExternalID := testGithubServerRepoExternalID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    pipelineDefinitionSteps(t, projectID, repoExternalID, "github_server"),
	})
}

// testAccDeleteDefinitionOutOfBand deletes a pipeline definition through the API
// directly, behind Terraform's back, using the same token the provider is
// configured with.
//
// It creates nothing and changes no organization or project setting: the only
// thing it removes is a definition the test itself created moments earlier, so
// the organization is left exactly as the test found it. The Terraform destroy
// at the end of the case then finds it already gone, which DELETE reports as
// success (measured: repeat deletes answer 200).
func testAccDeleteDefinitionOutOfBand(t *testing.T, projectID, definitionID string) {
	t.Helper()

	// The same environment the provider itself configures from (see
	// provider.go's Configure), so the out-of-band call reaches the same
	// installation as the resource under test rather than always circleci.com.
	client := circleci.New(circleci.Config{
		Host:       os.Getenv("CIRCLE_HOST"),
		Token:      os.Getenv("CIRCLE_TOKEN"),
		Deployment: circleci.DeploymentCloud,
	})

	if err := client.DeletePipelineDefinition(context.Background(), projectID, definitionID); err != nil {
		t.Fatalf("deleting pipeline definition %s out of band: %v", definitionID, err)
	}
}

// TestAccPipelineDefinitionResource_DeletedOutOfBandRecovers is the real-API
// proof for the singular route's 400.
//
// Measured over the network: after a definition is deleted, GET on
// /projects/{project}/pipeline-definitions/{id} answers 400
// {"message":"Failed to get pipeline definition."} — not 404. Because Read only
// recognised 404 as absence, this exact sequence used to put the resource into a
// state it could never leave: every plan and every apply failed on refresh, and
// the only way out was editing the state file by hand.
//
// Step 2 asserts the recovery: a refresh-only plan must come back non-empty
// (the definition will be recreated) rather than erroring. Step 3 applies it, so
// the case ends with a definition that Terraform's own destroy can clean up.
func TestAccPipelineDefinitionResource_DeletedOutOfBandRecovers(t *testing.T) {
	testRequireVCSType(t, "github_app")

	projectID := testProjectID(t)
	repoExternalID := testGithubAppRepoExternalID(t)

	const address = "circleci_pipeline_definition.test"

	name := "tf-acc-oob-" + rand.Text()
	config := pipelineDefinitionResourceConfig(projectID, repoExternalID, name, "deleted out of band", ".circleci/config.yml", "github_app")

	// Captured after the first apply, so the out-of-band delete in step 2 targets
	// exactly the definition this test created and nothing else in the
	// organization.
	var definitionID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					res, found := s.RootModule().Resources[address]
					if !found {
						return fmt.Errorf("resource %s not found in state", address)
					}

					definitionID = res.Primary.Attributes["id"]
					if definitionID == "" {
						return errors.New("the created definition has no id in state")
					}

					return nil
				},
			},
			{
				// Delete it behind Terraform's back, then plan. Before the fix
				// this step failed with "Unable to Read CircleCI pipeline" and
				// the resource was stuck there permanently: the API answers 400
				// for a deleted definition, and Read only recognised 404.
				PreConfig:          func() { testAccDeleteDefinitionOutOfBand(t, projectID, definitionID) },
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Recreate it so the case's own destroy has something to remove,
				// and confirm the recreated definition is fully populated.
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("description"), knownvalue.StringExact("deleted out of band")),
				},
			},
		},
	})
}
