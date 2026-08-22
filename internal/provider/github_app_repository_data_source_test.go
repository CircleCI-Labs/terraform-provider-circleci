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

	if fullName, ok := resp.Schema.Attributes["full_name"]; !ok || !fullName.IsRequired() {
		t.Error("full_name is not required, but the lookup cannot be performed without it")
	}

	// The organization is equally necessary, but it is accepted under two names
	// while `organization_id` is deprecated, so both are Optional and
	// orgIDDataSourceConfigValidator requires exactly one. See
	// org_id_deprecation.go.
	for _, name := range []string{"organization_id", "org_id"} {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("schema is missing the %s attribute", name)
		}
		if !attr.IsOptional() {
			t.Errorf("%s is not optional, but one of the pair must be settable", name)
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

	// An installation granted no repositories answers 200 with an empty items
	// array. This is NOT the same wire shape as "no installation at all" — see
	// TestAccGitHubAppRepositoriesDataSource_notInstalled below, [NET, reproduced
	// against the live API on 2026-08-21] — but it must still surface as an
	// empty list rather than an error, so for_each and length() keep working.
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

// TestAccGitHubAppRepositoriesDataSource_notInstalled pins the shape an
// organization with NO GitHub App installation at all gets back from this
// route — distinct from TestAccGitHubAppRepositoriesDataSource_empty above,
// which is an installation granted nothing. [NET, reproduced against the live
// API on 2026-08-21]: the repositories route answers 404 "Organization not
// found." for a GitLab or GitHub-OAuth-only organization, not 200 with an
// empty list. Before the diagnostic in github_app_repositories_data_source.go
// special-cased this, that 404 surfaced as a bare "Unable to list GitHub App
// repositories ...: Organization not found.", which reads like the configured
// organization id itself is wrong rather than naming the missing installation.
func TestAccGitHubAppRepositoriesDataSource_notInstalled(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	api.repositoriesNotInstalled = true

	config := discoveryProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_github_app_repositories" "test" {
  organization_id = %[1]q
}
`, testDiscoveryOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`No GitHub App installation for organization`),
		}},
	})
}

// TestAccGitHubAppRepositoryDataSource_notInstalled is the singular
// data source's version of the same case: a lookup by full_name on an
// organization with no GitHub App installation at all must not be reported the
// same way as a genuine miss (TestAccGitHubAppRepositoryDataSource_notFound
// above) — the fix is telling the two apart in the code
// (errors.Is(err, circleci.ErrNotFound) vs. a plain circleci.IsNotFound), and
// this pins the distinguishable diagnostic on the "no installation" side.
func TestAccGitHubAppRepositoryDataSource_notInstalled(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	api.repositoriesNotInstalled = true

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccGitHubAppRepositoryConfig(host, "acme/api"),
			ExpectError: regexp.MustCompile(`No GitHub App installation for organization`),
		}},
	})
}
