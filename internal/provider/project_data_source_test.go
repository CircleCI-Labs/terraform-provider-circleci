// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// TestAccProjectDataSource reads a pre-existing project and checks every
// attribute the data source reports.
//
// THE VCS_INFO SHAPE DEPENDS ON THE ORGANIZATION'S CLASS, and this test used to
// hardcode one of the two with no gate of any kind. It asserted provider
// "CircleCI" and a vcs_url of "//circleci.com/<orgID>/<projectID>", which is the
// STANDALONE shape, so against a classic organization it failed three assertions
// at once. That is BUG P5. Measured over the network, the same GET on one project
// of each class:
//
//	circleci/TFtestOrgFragment01234/TFtestProjFragment012
//	  → provider "CircleCI", vcs_url "//circleci.com/<orgUUID>/<projectUUID>"
//	gh/example-org/example-repo
//	  → provider "GitHub",   vcs_url "https://github.com/example-org/example-repo"
//
// The expectation is now derived from the project slug rather than assumed, by
// expectedProjectVCSInfo. Deriving it from the slug rather than from
// CIRCLECI_TEST_VCS_TYPE is deliberate: the class is a property of the
// organization, not of the integration — a GitHub organization can be either —
// so a VCS-type list would be wrong for whichever half of an integration's
// organizations it did not name.
func TestAccProjectDataSource(t *testing.T) {
	organizationID := testOrgID(t)
	organizationName := testOrgName(t)
	organizationSlug := testOrgSlug(t)
	projectID := testStaticProjectID(t)
	projectName := testStaticProjectName(t)
	projectSlug := testStaticProjectSlug(t)

	wantProvider, wantVcsURL, ok := expectedProjectVCSInfo(projectSlug, organizationID, projectID)
	if !ok {
		t.Skipf("the vcs_info shape a project slug beginning %q reports has not been measured, so "+
			"this test has no expectation to assert; see expectedProjectVCSInfo",
			strings.SplitN(projectSlug, "/", 2)[0])
	}

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
					// Not asserted exactly, and deliberately. The default branch is a
					// property of whichever repository the fixture points at, not of
					// this provider: the old hardcoded "main" was simply wrong for the
					// classic fixture measured above, whose default branch is "labs".
					// NotNull still proves the field is read and mapped, which is the
					// only part this data source is responsible for.
					statecheck.ExpectKnownValue(
						"data.circleci_project.test_project",
						tfjsonpath.New("vcs_info").AtMapKey("default_branch"),
						knownvalue.NotNull(),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project.test_project",
						tfjsonpath.New("vcs_info").AtMapKey("provider"),
						knownvalue.StringExact(wantProvider),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project.test_project",
						tfjsonpath.New("vcs_info").AtMapKey("vcs_url"),
						knownvalue.StringExact(wantVcsURL),
					),
				},
			},
		},
	})
}

// expectedProjectVCSInfo returns the vcs_info.provider and vcs_info.vcs_url that
// CircleCI reports for a project, derived from its slug.
//
// Every shape below was read off a live GET; nothing here is inferred from the
// route's documentation:
//
//	circleci/…  provider "CircleCI",  vcs_url "//circleci.com/<orgUUID>/<projectUUID>"
//	            Confirmed on a GitHub App organization and on a GitLab one, so the
//	            standalone shape is the same whatever the organization is connected
//	            to — including nothing at all.
//	gh/…        provider "GitHub",    vcs_url "https://github.com/<org>/<repo>"
//
// bb/… is included as "Bitbucket" and bitbucket.org by symmetry with gh/…, and is
// marked as such: no Bitbucket fixture was available to read it from.
//
// Anything else — a GitHub Server organization, say — reports false rather than a
// guess, so the caller skips instead of asserting something unmeasured. A wrong
// expectation here would fail exactly like a provider regression, which is the
// failure mode this whole function exists to avoid.
func expectedProjectVCSInfo(projectSlug, organizationID, projectID string) (provider, vcsURL string, ok bool) {
	segments := strings.Split(projectSlug, "/")
	if len(segments) != 3 {
		return "", "", false
	}

	switch segments[0] {
	case "circleci":
		return "CircleCI", fmt.Sprintf("//circleci.com/%s/%s", organizationID, projectID), true
	case "gh", "github":
		return "GitHub", fmt.Sprintf("https://github.com/%s/%s", segments[1], segments[2]), true
	case "bb", "bitbucket":
		// Not measured — no Bitbucket fixture. Symmetric with the GitHub case.
		return "Bitbucket", fmt.Sprintf("https://bitbucket.org/%s/%s", segments[1], segments[2]), true
	default:
		return "", "", false
	}
}

// TestExpectedProjectVCSInfo unit-tests the derivation above with no account and
// no environment variable, so a mistake in it is caught by `go test` rather than
// by an acceptance run that reads like a provider regression. Both measured rows
// are here verbatim.
func TestExpectedProjectVCSInfo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		slug         string
		orgID        string
		projectID    string
		wantProvider string
		wantURL      string
		wantOK       bool
	}{
		{
			name:         "standalone",
			slug:         "circleci/TFtestOrgFragment01234/TFtestProjFragment012",
			orgID:        "11111111-2222-3333-4444-555555555555",
			projectID:    "66666666-7777-8888-9999-aaaaaaaaaaaa",
			wantProvider: "CircleCI",
			wantURL:      "//circleci.com/11111111-2222-3333-4444-555555555555/66666666-7777-8888-9999-aaaaaaaaaaaa",
			wantOK:       true,
		},
		{
			name:         "classic github",
			slug:         "gh/example-org/example-repo",
			wantProvider: "GitHub",
			wantURL:      "https://github.com/example-org/example-repo",
			wantOK:       true,
		},
		{name: "unmeasured vcs type", slug: "ghe/acme/repo"},
		{name: "malformed slug", slug: "nonsense"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			provider, url, ok := expectedProjectVCSInfo(c.slug, c.orgID, c.projectID)
			if ok != c.wantOK {
				t.Fatalf("expectedProjectVCSInfo(%q) ok = %v, want %v", c.slug, ok, c.wantOK)
			}
			if provider != c.wantProvider || url != c.wantURL {
				t.Errorf("expectedProjectVCSInfo(%q) = (%q, %q), want (%q, %q)",
					c.slug, provider, url, c.wantProvider, c.wantURL)
			}
		})
	}
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
