// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func testAccPipelinesDataSourceConfig(host, deployment string) string {
	return pluralProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_pipeline_definitions" "test" {
  project_id = %[1]q
}
`, testPluralProjectID)
}

func TestPipelinesDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewPipelineDefinitionsDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	projectID, ok := resp.Schema.Attributes["project_id"]
	if !ok {
		t.Fatal("schema is missing the project_id attribute")
	}
	if !projectID.IsRequired() {
		t.Error("project_id is not required, but it is the scope of the listing")
	}

	definitions, ok := resp.Schema.Attributes["pipeline_definitions"]
	if !ok {
		t.Fatal("schema is missing the pipeline_definitions attribute")
	}
	if !definitions.IsComputed() {
		t.Error("pipeline_definitions is not computed, but it is entirely API-derived")
	}
}

func TestAccPipelinesDataSource(t *testing.T) {
	api, host := newPluralAPI(t)

	// The second definition has no description and no repo, both of which the API
	// omits entirely rather than sending empty.
	api.seedDefinition(testPluralProjectID, "p1", "build", "Main pipeline",
		"github_app", ".circleci/config.yml", "acme/api", "123456")
	api.seedDefinition(testPluralProjectID, "p2", "inbound", "",
		"webhook", "", "", "")

	// Another project's definitions must not leak into the result.
	api.seedDefinition("99999999-9999-9999-9999-999999999999", "other", "elsewhere", "Elsewhere",
		"github_app", ".circleci/config.yml", "acme/other", "999")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPipelinesDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_definitions.test",
						tfjsonpath.New("pipeline_definitions"),
						knownvalue.ListSizeExact(2),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_definitions.test",
						tfjsonpath.New("pipeline_definitions").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("build"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_definitions.test",
						tfjsonpath.New("pipeline_definitions").AtSliceIndex(0).AtMapKey("description"),
						knownvalue.StringExact("Main pipeline"),
					),
					// The nested config and checkout sources are flattened, matching
					// circleci_pipeline.
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_definitions.test",
						tfjsonpath.New("pipeline_definitions").AtSliceIndex(0).AtMapKey("config_source_provider"),
						knownvalue.StringExact("github_app"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_definitions.test",
						tfjsonpath.New("pipeline_definitions").AtSliceIndex(0).AtMapKey("config_source_file_path"),
						knownvalue.StringExact(".circleci/config.yml"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_definitions.test",
						tfjsonpath.New("pipeline_definitions").AtSliceIndex(0).AtMapKey("config_source_repo_full_name"),
						knownvalue.StringExact("acme/api"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_definitions.test",
						tfjsonpath.New("pipeline_definitions").AtSliceIndex(0).AtMapKey("config_source_repo_external_id"),
						knownvalue.StringExact("123456"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_definitions.test",
						tfjsonpath.New("pipeline_definitions").AtSliceIndex(0).AtMapKey("checkout_source_repo_full_name"),
						knownvalue.StringExact("acme/api"),
					),
					// An omitted description, created_at and repo all read back as ""
					// rather than making the whole read fail.
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_definitions.test",
						tfjsonpath.New("pipeline_definitions").AtSliceIndex(1).AtMapKey("description"),
						knownvalue.StringExact(""),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_definitions.test",
						tfjsonpath.New("pipeline_definitions").AtSliceIndex(1).AtMapKey("created_at"),
						knownvalue.StringExact(""),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_definitions.test",
						tfjsonpath.New("pipeline_definitions").AtSliceIndex(1).AtMapKey("config_source_repo_full_name"),
						knownvalue.StringExact(""),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_definitions.test",
						tfjsonpath.New("pipeline_definitions").AtSliceIndex(1).AtMapKey("checkout_source_provider"),
						knownvalue.StringExact("webhook"),
					),
				},
			},
		},
	})
}

// TestAccPipelinesDataSource_serverDeployment covers the Cloud-only gate:
// CircleCI Server does not route pipeline-definitions, so the request must be
// refused with an explanatory error rather than sent and answered with a 404 that
// reads as a missing project.
func TestAccPipelinesDataSource_serverDeployment(t *testing.T) {
	api, host := newPluralAPI(t)

	// Seeded deliberately: even with data available, `server` must be refused
	// before the request rather than succeeding by accident.
	api.seedDefinition(testPluralProjectID, "p1", "build", "Main pipeline",
		"github_app", ".circleci/config.yml", "acme/api", "123456")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccPipelinesDataSourceConfig(host, "server"),
			// The message must name the type, say Cloud is required, and report the
			// configured deployment.
			ExpectError: regexp.MustCompile(`(?s)circleci_pipeline_definitions requires CircleCI Cloud.*"server"`),
		}},
	})
}

func TestAccPipelinesDataSource_empty(t *testing.T) {
	_, host := newPluralAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPipelinesDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_definitions.test",
						tfjsonpath.New("pipeline_definitions"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
		},
	})
}

func TestAccPipelinesDataSource_apiError(t *testing.T) {
	api, host := newPluralAPI(t)

	api.fail(404, "Project not found.")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccPipelinesDataSourceConfig(host, "cloud"),
			ExpectError: regexp.MustCompile(`(?s)Unable to list CircleCI pipeline definitions.*Project not found\.`),
		}},
	})
}
