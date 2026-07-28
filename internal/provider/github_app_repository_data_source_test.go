// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func testAccGitHubAppRepositoryConfig(host, fullName string) string {
	return discoveryProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_github_app_repository" "test" {
  organization_id = %[1]q
  full_name       = %[2]q
}
`, testDiscoveryOrgID, fullName)
}

func TestGitHubAppRepositoryDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewGitHubAppRepositoryDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	for _, name := range []string{"organization_id", "full_name"} {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("schema is missing the %s attribute", name)
		}
		if !attr.IsRequired() {
			t.Errorf("%s is not required, but the lookup cannot be performed without it", name)
		}
	}

	// external_id is the reason this data source exists, so it must be present and
	// API-derived.
	externalID, ok := resp.Schema.Attributes["external_id"]
	if !ok {
		t.Fatal("schema is missing the external_id attribute, which is the point of this data source")
	}
	if !externalID.IsComputed() {
		t.Error("external_id is not computed, but it is entirely API-derived")
	}

	// The documentation must carry the unpublished-API caveat: practitioners are
	// being asked to depend on a route CircleCI does not publish.
	if !strings.Contains(resp.Schema.MarkdownDescription, "unpublished CircleCI API") {
		t.Error("schema description does not warn that the underlying API is unpublished")
	}
}

func TestAccGitHubAppRepositoryDataSource(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGitHubAppRepositoryConfig(host, "acme/api"),
				ConfigStateChecks: []statecheck.StateCheck{
					// The string form is what circleci_pipeline and circleci_trigger take.
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repository.test",
						tfjsonpath.New("external_id"),
						knownvalue.StringExact("123456789"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repository.test",
						tfjsonpath.New("id"),
						knownvalue.Int64Exact(123456789),
					),
					// Decoded from repo_name, not name.
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repository.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact("api"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repository.test",
						tfjsonpath.New("owner"),
						knownvalue.StringExact("acme"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repository.test",
						tfjsonpath.New("default_branch"),
						knownvalue.StringExact("main"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repository.test",
						tfjsonpath.New("private"),
						knownvalue.Bool(true),
					),
				},
			},
		},
	})
}

func TestAccGitHubAppRepositoryDataSource_caseInsensitive(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	// GitHub treats owner and repository names case-insensitively and repo_full_name
	// preserves the creation casing, so a configuration that spells it differently
	// must still resolve. full_name is echoed back exactly as configured, because
	// changing a value Terraform read from configuration is reported as an
	// inconsistent result.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGitHubAppRepositoryConfig(host, "ACME/web-ui"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repository.test",
						tfjsonpath.New("external_id"),
						knownvalue.StringExact("987654321"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repository.test",
						tfjsonpath.New("full_name"),
						knownvalue.StringExact("ACME/web-ui"),
					),
					// name and owner carry the API's own casing, for anyone who needs it.
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repository.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact("Web-UI"),
					),
				},
			},
		},
	})
}

func TestAccGitHubAppRepositoryDataSource_paginates(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	// One repository per page, so the last one is only reachable by draining.
	api.repositoryPageSize = 1

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGitHubAppRepositoryConfig(host, "acme/docs"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repository.test",
						tfjsonpath.New("external_id"),
						knownvalue.StringExact("555555555"),
					),
				},
			},
		},
	})
}

func TestAccGitHubAppRepositoryDataSource_notFound(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	// A miss is far more likely to be an installation scoped to a subset of
	// repositories than a typo, so the error has to say how to widen it rather than
	// just reporting "not found".
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccGitHubAppRepositoryConfig(host, "acme/absent"),
			ExpectError: regexp.MustCompile(`(?s)No GitHub App repository named acme/absent.*selected repositories`),
		}},
	})
}

func TestAccGitHubAppRepositoryDataSource_serverDeployment(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	// The GitHub App integration only exists for `circleci` type (standalone)
	// organizations, and a CircleCI Server installation is always a `github` type
	// organization. So deployment = "server" can never have an installation to
	// report, and must be rejected with an explanation rather than a 404 that looks
	// like an ungranted repository.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config: discoveryProviderConfig(host, "server") + fmt.Sprintf(`
data "circleci_github_app_repository" "test" {
  organization_id = %[1]q
  full_name       = "acme/api"
}
`, testDiscoveryOrgID),
			ExpectError: regexp.MustCompile(
				`circleci_github_app_repository requires a standalone CircleCI organization`,
			),
		}},
	})
}

func TestAccGitHubAppRepositoriesDataSource_serverDeployment(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config: discoveryProviderConfig(host, "server") + fmt.Sprintf(`
data "circleci_github_app_repositories" "test" {
  organization_id = %[1]q
}
`, testDiscoveryOrgID),
			ExpectError: regexp.MustCompile(
				`circleci_github_app_repositories requires a standalone CircleCI organization`,
			),
		}},
	})
}

func TestAccGitHubAppRepositoriesDataSource(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	// Two per page, so the plural data source also has to drain rather than stopping
	// at the first page.
	api.repositoryPageSize = 2

	config := discoveryProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_github_app_repositories" "test" {
  organization_id = %[1]q
}
`, testDiscoveryOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repositories.test",
						tfjsonpath.New("repositories"),
						knownvalue.ListSizeExact(3),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repositories.test",
						tfjsonpath.New("repositories").AtSliceIndex(0).AtMapKey("full_name"),
						knownvalue.StringExact("acme/api"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repositories.test",
						tfjsonpath.New("repositories").AtSliceIndex(0).AtMapKey("external_id"),
						knownvalue.StringExact("123456789"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repositories.test",
						tfjsonpath.New("repositories").AtSliceIndex(2).AtMapKey("full_name"),
						knownvalue.StringExact("acme/docs"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repositories.test",
						tfjsonpath.New("repositories").AtSliceIndex(2).AtMapKey("private"),
						knownvalue.Bool(false),
					),
				},
			},
		},
	})
}

func TestAccGitHubAppRepositoriesDataSource_empty(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	// No installation and an installation granted nothing look identical from here:
	// the API answers 200 with an empty items array for both. Either way the result
	// must be an empty list rather than null, so for_each and length() keep working.
	api.repositories = nil

	config := discoveryProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_github_app_repositories" "test" {
  organization_id = %[1]q
}
`, testDiscoveryOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_repositories.test",
						tfjsonpath.New("repositories"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
		},
	})
}
