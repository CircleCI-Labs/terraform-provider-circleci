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

func testAccProjectEnvironmentVariablesDataSourceConfig(host, deployment, projectSlug string) string {
	return pluralProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_project_environment_variables" "test" {
  project_slug = %[1]q
}
`, projectSlug)
}

func TestProjectEnvironmentVariablesDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewProjectEnvironmentVariablesDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	projectSlug, ok := resp.Schema.Attributes["project_slug"]
	if !ok {
		t.Fatal("schema is missing the project_slug attribute")
	}
	if !projectSlug.IsRequired() {
		t.Error("project_slug is not required, but it is the scope of the listing")
	}

	variables, ok := resp.Schema.Attributes["environment_variables"]
	if !ok {
		t.Fatal("schema is missing the environment_variables attribute")
	}
	if !variables.IsComputed() {
		t.Error("environment_variables is not computed, but it is entirely API-derived")
	}
}

func TestAccProjectEnvironmentVariablesDataSource(t *testing.T) {
	api, host := newPluralAPI(t)

	// One variable per page, so the result only covers both if the pagination is
	// drained. The second has no created_at, which the API sends as null.
	api.pageSize = 1

	createdAt := "2023-04-14T21:20:14.000Z"
	api.seedEnvVar(testPluralProjectSlug, "API_TOKEN", "xxxx1234", &createdAt)
	api.seedEnvVar(testPluralProjectSlug, "ZONE", "xxxxst-1", nil)

	// Another project's variables must not leak into the result.
	api.seedEnvVar("circleci/Other/Project", "ELSEWHERE", "xxxxelse", nil)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectEnvironmentVariablesDataSourceConfig(host, "cloud", testPluralProjectSlug),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variables.test",
						tfjsonpath.New("environment_variables"),
						knownvalue.ListSizeExact(2),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variables.test",
						tfjsonpath.New("environment_variables").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("API_TOKEN"),
					),
					// The value is whatever the API reported, which is always masked:
					// four x characters plus the last four of the real value.
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variables.test",
						tfjsonpath.New("environment_variables").AtSliceIndex(0).AtMapKey("value"),
						knownvalue.StringExact("xxxx1234"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variables.test",
						tfjsonpath.New("environment_variables").AtSliceIndex(0).AtMapKey("created_at"),
						knownvalue.StringExact(createdAt),
					),
					// A null created_at reads back as "", not as an error.
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variables.test",
						tfjsonpath.New("environment_variables").AtSliceIndex(1).AtMapKey("created_at"),
						knownvalue.StringExact(""),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variables.test",
						tfjsonpath.New("environment_variables").AtSliceIndex(1).AtMapKey("name"),
						knownvalue.StringExact("ZONE"),
					),
				},
			},
		},
	})
}

// TestAccProjectEnvironmentVariablesDataSource_serverDeployment covers CircleCI
// Server: project environment variables are v2 on both deployments, so this must
// work rather than be rejected.
func TestAccProjectEnvironmentVariablesDataSource_serverDeployment(t *testing.T) {
	api, host := newPluralAPI(t)

	api.seedEnvVar(testPluralProjectSlug, "API_TOKEN", "xxxx1234", nil)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectEnvironmentVariablesDataSourceConfig(host, "server", testPluralProjectSlug),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variables.test",
						tfjsonpath.New("environment_variables"),
						knownvalue.ListSizeExact(1),
					),
				},
			},
		},
	})
}

func TestAccProjectEnvironmentVariablesDataSource_empty(t *testing.T) {
	_, host := newPluralAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectEnvironmentVariablesDataSourceConfig(host, "cloud", testPluralProjectSlug),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_project_environment_variables.test",
						tfjsonpath.New("environment_variables"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
		},
	})
}

func TestAccProjectEnvironmentVariablesDataSource_malformedSlug(t *testing.T) {
	_, host := newPluralAPI(t)

	// A slug that is not vcs-slug/org-name/repo-name is rejected before any
	// request, because the HTTP 404 it would otherwise produce reads as a missing
	// project rather than a configuration mistake.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccProjectEnvironmentVariablesDataSourceConfig(host, "cloud", "just-an-org"),
			ExpectError: regexp.MustCompile(`(?s)Unable to list CircleCI environment variables.*project slug`),
		}},
	})
}
