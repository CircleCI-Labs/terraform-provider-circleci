// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// This file is the real-API counterpart to github_app_installation_data_source_test.go
// and github_app_repository_data_source_test.go, which exercise these three
// data sources exclusively against newMockDiscoveryAPI /
// newMockGitHubAppInstallationAPI. Before these tests existed, every claim
// this family made about the live `github-app/organization/{id}/...` routes —
// including the ones now corrected in github_app.go's comments — was
// [FAKE] only, because circleci_github_app_installation,
// circleci_github_app_repository and circleci_github_app_repositories had
// never actually been run against a real CircleCI installation.
//
// The github_app and github_hybrid fixtures both carry a real GitHub App
// installation; github_oauth and gitlab do not (confirmed [NET] against all
// four fixture organizations on 2026-08-21: GET
// .../github-app/organization/{id}/installation and .../repositories both
// answer 200 for the first two and 404 "Organization not found." for the
// other two). Each test below is gated to the side of that split it needs, so
// a full four-fixture run (one CIRCLECI_TEST_VCS_TYPE at a time, per
// TESTING.md) exercises both halves.

// testGitHubAppOrgWithInstallation and testGitHubAppOrgWithoutInstallation
// both resolve to testOrgID/testOrgName, gated to the VCS types known —
// [NET] — to have or lack an installation. Splitting the gate out like this
// means every test below states which side it needs at the top, rather than
// repeating the same testRequireVCSType call with a different comment each
// time.
func testGitHubAppOrgWithInstallation(t *testing.T) (orgID, orgName string) {
	t.Helper()
	testRequireVCSType(t, "github_app", "github_hybrid")

	return testOrgID(t), testOrgName(t)
}

// Returns only the id: every caller asserts on an absent installation, and none
// of them needs the organization's name. Returning a name nobody reads is how a
// helper drifts into looking like it proves more than it does.
func testGitHubAppOrgWithoutInstallation(t *testing.T) (orgID string) {
	t.Helper()
	testRequireVCSType(t, "github_oauth", "gitlab")

	return testOrgID(t)
}

func TestAccGitHubAppInstallationDataSourceNet_Installed(t *testing.T) {
	orgID, orgName := testGitHubAppOrgWithInstallation(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_github_app_installation" "net_test" {
  org_id = %[1]q
}
`, orgID),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_github_app_installation.net_test",
					tfjsonpath.New("target_type"),
					knownvalue.StringExact("Organization"),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_github_app_installation.net_test",
					tfjsonpath.New("login"),
					knownvalue.StringExact(orgName),
				),
			},
		}},
	})
}

// TestAccGitHubAppInstallationDataSourceNet_NotInstalled pins the diagnostic
// an OAuth-only or GitLab organization gets from this data source: an error
// naming the missing installation, not a confusing empty result. This is the
// case github_app_installation_data_source.go's schema description used to
// claim (wrongly) is reported as "an empty result" by the repositories data
// sources — it is not, it is this same named error, from all three.
func TestAccGitHubAppInstallationDataSourceNet_NotInstalled(t *testing.T) {
	orgID := testGitHubAppOrgWithoutInstallation(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_github_app_installation" "net_test" {
  org_id = %[1]q
}
`, orgID),
			ExpectError: regexp.MustCompile(`No GitHub App installation for organization`),
		}},
	})
}

func TestAccGitHubAppRepositoriesDataSourceNet_Installed(t *testing.T) {
	orgID, _ := testGitHubAppOrgWithInstallation(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_github_app_repositories" "net_test" {
  org_id = %[1]q
}
`, orgID),
			// Both fixture organizations with an installation (gh-app-cci-1,
			// gh-oauth-cci-2) carry at least one repository the app can see, so index
			// 0 existing is "at least one" without knownvalue needing an exact count;
			// an installation that legitimately saw none would need its own fixture
			// to test (see TestAccGitHubAppRepositoriesDataSource_empty for the
			// [FAKE] version of that case).
			Check: resource.TestCheckResourceAttrSet(
				"data.circleci_github_app_repositories.net_test", "repositories.0.external_id",
			),
		}},
	})
}

// TestAccGitHubAppRepositoriesDataSourceNet_NotInstalled is the real-API
// version of TestAccGitHubAppRepositoriesDataSource_notInstalled
// (github_app_repository_data_source_test.go), which is [FAKE]. This is what
// actually proves the fake's shape (404 "Organization not found.") matches
// production, rather than matching a belief nobody checked.
func TestAccGitHubAppRepositoriesDataSourceNet_NotInstalled(t *testing.T) {
	orgID := testGitHubAppOrgWithoutInstallation(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_github_app_repositories" "net_test" {
  org_id = %[1]q
}
`, orgID),
			ExpectError: regexp.MustCompile(`No GitHub App installation for organization`),
		}},
	})
}

// TestAccGitHubAppRepositoryDataSourceNet_Found resolves a repository this
// package does not otherwise know the name of: it asks the client directly
// (bypassing Terraform) for whichever repository the installation reports
// first, then feeds that name into the data source under test. This is
// deliberately not a hardcoded fixture repository name — the fixture
// organizations' repositories are not part of the documented, stable fixture
// contract (see TESTING.md's "Filling in the rest"), only whatever exists on
// them today, so asking the API is the only way to get a name guaranteed to
// resolve.
func TestAccGitHubAppRepositoryDataSourceNet_Found(t *testing.T) {
	testAccPreCheck(t)

	orgID, _ := testGitHubAppOrgWithInstallation(t)

	token := activeIntegrationToken(t)
	if token == "" {
		token = os.Getenv("CIRCLE_TOKEN")
	}
	client := circleci.New(circleci.Config{Token: token})

	repos, err := client.GitHubApp().ListRepositories(t.Context(), orgID)
	if err != nil {
		t.Fatalf("could not list GitHub App repositories for organization %s to find a fixture name: %v", orgID, err)
	}
	if len(repos) == 0 {
		t.Skipf("organization %s's GitHub App installation reports no repositories to resolve by name", orgID)
	}

	fullName := repos[0].FullName
	wantExternalID := repos[0].ExternalID()

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_github_app_repository" "net_test" {
  org_id    = %[1]q
  full_name = %[2]q
}
`, orgID, fullName),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_github_app_repository.net_test",
					tfjsonpath.New("external_id"),
					knownvalue.StringExact(wantExternalID),
				),
			},
		}},
	})
}

// TestAccGitHubAppRepositoryDataSourceNet_NotFound is a genuine miss against a
// real installation: the organization has a GitHub App installation, but no
// repository named this. It must report the "check the name or widen the
// installation" diagnostic, not the "no installation" one — the distinction
// TestAccGitHubAppRepositoryDataSourceNet_NotInstalled below pins on the other
// side.
func TestAccGitHubAppRepositoryDataSourceNet_NotFound(t *testing.T) {
	orgID, _ := testGitHubAppOrgWithInstallation(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_github_app_repository" "net_test" {
  org_id    = %[1]q
  full_name = "this-repository-does-not-exist/t2-breadth-probe"
}
`, orgID),
			ExpectError: regexp.MustCompile(`No GitHub App repository named`),
		}},
	})
}

// TestAccGitHubAppRepositoryDataSourceNet_NotInstalled is the real-API version
// of TestAccGitHubAppRepositoryDataSource_notInstalled, and the test that
// would have caught the bug fixed in github_app_repository_data_source.go: a
// lookup against an organization with no installation at all used to report
// the same "check the name or widen the installation" message as a genuine
// miss, which is wrong advice for an OAuth-only or GitLab organization that
// was never going to have an installation.
func TestAccGitHubAppRepositoryDataSourceNet_NotInstalled(t *testing.T) {
	orgID := testGitHubAppOrgWithoutInstallation(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_github_app_repository" "net_test" {
  org_id    = %[1]q
  full_name = "irrelevant/name"
}
`, orgID),
			ExpectError: regexp.MustCompile(`No GitHub App installation for organization`),
		}},
	})
}
