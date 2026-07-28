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

// These tests back the circleci_project data source (project_data_source.go)
// with the same fake used for the resource (project_fake_test.go), so they run
// without TF_ACC or credentials. Previously this data source had only a
// resource.Test acceptance test gated on CIRCLE_TOKEN (TestAccProjectDataSource)
// plus a schema-only unit test with no client at all.

func projectDataSourceProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
`, host)
}

func projectDataSourceConfigForSlug(host, slug string) string {
	return projectDataSourceProviderConfig(host) + fmt.Sprintf(`
data "circleci_project" "test" {
  slug = %q
}
`, slug)
}

// TestProjectDataSourceUnit_Read covers a normal read, checking that every
// field the API returns (including the nested vcs_info object) reaches state.
func TestProjectDataSourceUnit_Read(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")
	api.addProject(&fakeProject{
		id: "proj-1", name: "my-repo", slug: "gh/AcmeOrg/my-repo",
		orgName: "AcmeOrg", orgSlug: "gh/AcmeOrg", orgID: "org-1",
		vcsURL: "https://github.com/AcmeOrg/my-repo", vcsProvider: "GitHub", defaultBranch: "main",
	}, defaultFakeProjectSettings())

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectDataSourceConfigForSlug(host, "gh/AcmeOrg/my-repo"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.circleci_project.test", tfjsonpath.New("id"), knownvalue.StringExact("proj-1")),
					statecheck.ExpectKnownValue("data.circleci_project.test", tfjsonpath.New("name"), knownvalue.StringExact("my-repo")),
					statecheck.ExpectKnownValue("data.circleci_project.test", tfjsonpath.New("organization_id"), knownvalue.StringExact("org-1")),
					statecheck.ExpectKnownValue("data.circleci_project.test", tfjsonpath.New("organization_name"), knownvalue.StringExact("AcmeOrg")),
					statecheck.ExpectKnownValue("data.circleci_project.test", tfjsonpath.New("organization_slug"), knownvalue.StringExact("gh/AcmeOrg")),
					statecheck.ExpectKnownValue(
						"data.circleci_project.test",
						tfjsonpath.New("vcs_info").AtMapKey("default_branch"),
						knownvalue.StringExact("main"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project.test",
						tfjsonpath.New("vcs_info").AtMapKey("provider"),
						knownvalue.StringExact("GitHub"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project.test",
						tfjsonpath.New("vcs_info").AtMapKey("vcs_url"),
						knownvalue.StringExact("https://github.com/AcmeOrg/my-repo"),
					),
				},
			},
		},
	})
}

// TestProjectDataSourceUnit_MissingProjectErrorsCleanly checks that a 404
// surfaces as a diagnostic. A data source has no state to drop a resource
// from — RemoveResource is a resource concept — so an error here is the
// correct behaviour, unlike the resource's drift handling gap.
func TestProjectDataSourceUnit_MissingProjectErrorsCleanly(t *testing.T) {
	_, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      projectDataSourceConfigForSlug(host, "gh/Nobody/nothing"),
			ExpectError: regexp.MustCompile(`(?s)Client Error.*Unable to read CircleCI project`),
		}},
	})
}

// TestProjectDataSourceUnit_MalformedSlugPassesValidatorButFailsCleanly
// documents a defense-in-depth gap in the schema validator, not a bug: the
// "slug" attribute is validated with the regex `^.+/.+/.+$`
// (project_data_source.go), which — because `.+` is greedy — also matches a
// slug with more than three segments (it can absorb extra "/" characters into
// the first or middle group). A four-segment slug therefore reaches Read
// rather than being rejected at plan time.
//
// It still fails cleanly rather than panicking, because
// circleci.GetProject -> projectSlugPath independently enforces exactly three
// segments and returns a plain error (bug history item 2's guard, reused
// here). So the outcome required by this task ("malformed project slugs
// produce a clean diagnostic, not a panic") holds, just via a different layer
// than the one that looks like it should have caught it.
func TestProjectDataSourceUnit_MalformedSlugPassesValidatorButFailsCleanly(t *testing.T) {
	_, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      projectDataSourceConfigForSlug(host, "gh/Acme/repo/extra"),
			ExpectError: regexp.MustCompile(`expected three segments`),
		}},
	})
}

// TestProjectDataSourceUnit_SlugValidatorRejectsObviouslyBadSlug checks the
// case the regex validator does catch, at plan time, before any request.
func TestProjectDataSourceUnit_SlugValidatorRejectsObviouslyBadSlug(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      projectDataSourceConfigForSlug(host, "just-an-org"),
			ExpectError: regexp.MustCompile(`vcs-type/org-name/repo-name`),
		}},
	})

	if requests := api.recordedRequests(); len(requests) != 0 {
		t.Errorf("the provider made %v, want no request for a slug the validator should reject", requests)
	}
}
