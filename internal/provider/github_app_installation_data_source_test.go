// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// newMockGitHubAppInstallationAPI serves
// GET /api/v2/github-app/organization/{org_id}/installation. installed
// controls whether it answers 200 with the fixture below or 404, so tests can
// exercise both of the API's "installed"/"not installed" cases.
func newMockGitHubAppInstallationAPI(t *testing.T, installed bool) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v2/github-app/organization/" + testDiscoveryOrgID + "/installation"
		if r.URL.Path != wantPath {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, `{"message":"unexpected path %s"}`, r.URL.Path)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		if !installed {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Organization not found."}`))

			return
		}

		_, _ = w.Write([]byte(`{
  "id": 12345678,
  "target_type": "Organization",
  "login": "my-org",
  "repository_selection": "all"
}`))
	}))
	t.Cleanup(srv.Close)

	return srv.URL
}

func testAccGitHubAppInstallationConfig(host string) string {
	return discoveryProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_github_app_installation" "test" {
  organization_id = %[1]q
}
`, testDiscoveryOrgID)
}

func TestGitHubAppInstallationDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewGitHubAppInstallationDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// The organization is required to look the installation up, but it is accepted
	// under two names while `organization_id` is deprecated, so both are Optional
	// and orgIDDataSourceConfigValidator requires exactly one. See
	// org_id_deprecation.go.
	for _, name := range []string{"organization_id", "org_id"} {
		attribute, ok := resp.Schema.Attributes[name]
		if !ok || !attribute.IsOptional() {
			t.Errorf("attribute %q must be present and optional", name)
		}
	}

	for _, name := range []string{"id", "target_type", "login", "repository_selection"} {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("schema is missing the %s attribute", name)
		}
		if !attr.IsComputed() {
			t.Errorf("%s is not computed, but it is entirely API-derived", name)
		}
	}

	if !strings.Contains(resp.Schema.MarkdownDescription, "unpublished CircleCI API") {
		t.Error("schema description does not warn that the underlying API is unpublished")
	}
}

func TestAccGitHubAppInstallationDataSource(t *testing.T) {
	host := newMockGitHubAppInstallationAPI(t, true)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGitHubAppInstallationConfig(host),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_installation.test",
						tfjsonpath.New("id"),
						knownvalue.Int64Exact(12345678),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_installation.test",
						tfjsonpath.New("target_type"),
						knownvalue.StringExact("Organization"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_installation.test",
						tfjsonpath.New("login"),
						knownvalue.StringExact("my-org"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_github_app_installation.test",
						tfjsonpath.New("repository_selection"),
						knownvalue.StringExact("all"),
					),
				},
			},
		},
	})
}

func TestAccGitHubAppInstallationDataSource_notInstalled(t *testing.T) {
	host := newMockGitHubAppInstallationAPI(t, false)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccGitHubAppInstallationConfig(host),
			ExpectError: regexp.MustCompile(`No GitHub App installation for organization`),
		}},
	})
}

func TestAccGitHubAppInstallationDataSource_serverDeployment(t *testing.T) {
	host := newMockGitHubAppInstallationAPI(t, true)

	// A CircleCI Server installation is always a `github` type organization, so
	// it can never have a GitHub App installation to report.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config: discoveryProviderConfig(host, "server") + fmt.Sprintf(`
data "circleci_github_app_installation" "test" {
  organization_id = %[1]q
}
`, testDiscoveryOrgID),
			ExpectError: regexp.MustCompile(
				`circleci_github_app_installation requires a standalone CircleCI organization`,
			),
		}},
	})
}
