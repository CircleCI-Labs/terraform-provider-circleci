// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccProjectDataSource(t *testing.T) {
	organizationID := testOrgID(t)
	organizationName := testOrgName(t)
	organizationSlug := testOrgSlug(t)
	projectID := testStaticProjectID(t)
	projectName := testStaticProjectName(t)
	projectSlug := testStaticProjectSlug(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Read testing
			{
				Config: testProjectDataSourceConfig(projectSlug),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_project.test_project",
						tfjsonpath.New("id"),
						knownvalue.StringExact(projectID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project.test_project",
						tfjsonpath.New("name"),
						knownvalue.StringExact(projectName),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project.test_project",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(organizationID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project.test_project",
						tfjsonpath.New("organization_name"),
						knownvalue.StringExact(organizationName),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project.test_project",
						tfjsonpath.New("organization_slug"),
						knownvalue.StringExact(organizationSlug),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project.test_project",
						tfjsonpath.New("slug"),
						knownvalue.StringExact(projectSlug),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project.test_project",
						tfjsonpath.New("vcs_info").AtMapKey("default_branch"),
						knownvalue.StringExact("main"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project.test_project",
						tfjsonpath.New("vcs_info").AtMapKey("provider"),
						knownvalue.StringExact("CircleCI"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project.test_project",
						tfjsonpath.New("vcs_info").AtMapKey("vcs_url"),
						knownvalue.StringExact(fmt.Sprintf("//circleci.com/%s/%s", organizationID, projectID)),
					),
				},
			},
		},
	})
}

func testProjectDataSourceConfig(projectSlug string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = "https://circleci.com/api/v2"
}

data "circleci_project" "test_project" {
  slug = %[1]q
}
`, projectSlug)
}

func TestProjectDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	schemaRequest := datasource.SchemaRequest{}
	schemaResponse := &datasource.SchemaResponse{}

	NewProjectDataSource().Schema(ctx, schemaRequest, schemaResponse)

	if schemaResponse.Diagnostics.HasError() {
		t.Fatalf("Schema method diagnostics: %+v", schemaResponse.Diagnostics)
	}

	diagnostics := schemaResponse.Schema.ValidateImplementation(ctx)

	if diagnostics.HasError() {
		t.Fatalf("Schema validation diagnostics: %+v", diagnostics)
	}
}
