// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestOrganizationIsStandalone(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"circleci":   true,
		"CircleCI":   true,
		" circleci ": true,
		"github":     false,
		"gh":         false,
		"bitbucket":  false,
		"bb":         false,
		"":           false,
	}

	for vcsType, want := range tests {
		if got := organizationIsStandalone(vcsType); got != want {
			t.Errorf("organizationIsStandalone(%q) = %v, want %v", vcsType, got, want)
		}
	}
}

// orgAPI records every request so a test can assert on what was *not* called.
type orgAPI struct {
	mu       sync.Mutex
	requests []string
	vcsType  string
}

func newOrgAPI(t *testing.T, vcsType string) (*orgAPI, string) {
	t.Helper()

	api := &orgAPI{vcsType: vcsType}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.requests = append(api.requests, r.Method+" "+r.URL.Path)
		api.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)

			return
		}

		_ = json.NewEncoder(w).Encode(map[string]string{
			"id":       "00000000-1111-2222-3333-444444444444",
			"name":     "acme",
			"slug":     api.slug(),
			"vcs_type": api.vcsType,
		})
	}))
	t.Cleanup(srv.Close)

	return api, srv.URL
}

func (a *orgAPI) slug() string {
	if a.vcsType == "circleci" {
		return "circleci/00000000-1111-2222-3333-444444444444"
	}

	return a.vcsType + "/acme"
}

func (a *orgAPI) deletes() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	n := 0
	for _, req := range a.requests {
		if strings.HasPrefix(req, http.MethodDelete) {
			n++
		}
	}

	return n
}

func orgConfig(host, vcsType string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
resource "circleci_organization" "test" {
  name     = "acme"
  vcs_type = %q
}
`, host, vcsType)
}

// TestAccOrganizationDestroyDoesNotDeleteAdoptedOrg is the regression test for a
// data-loss bug.
//
// For a VCS-backed organization, POST /api/v2/organization is a find-or-create:
// Create only ever adopts an organization that already exists. DELETE, however,
// tears down the organization's VCS connections and deletes it along with — per
// the API spec — "all projects including all build data".
//
// So `terraform destroy` used to irreversibly destroy an organization Terraform
// never created. Destroy must now release it from state instead.
func TestAccOrganizationDestroyDoesNotDeleteAdoptedOrg(t *testing.T) {
	for _, vcsType := range []string{"github", "bitbucket"} {
		t.Run(vcsType, func(t *testing.T) {
			api, host := newOrgAPI(t, vcsType)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{Config: orgConfig(host, vcsType)},
					// An empty config destroys the resource.
					{Config: providerOnlyConfig(host)},
				},
			})

			if got := api.deletes(); got != 0 {
				t.Errorf(
					"destroy issued %d DELETE request(s) for a %s organization, want 0.\n"+
						"Deleting an adopted organization also deletes every project in it and all "+
						"of their build history.",
					got, vcsType,
				)
			}
		})
	}
}

// TestAccOrganizationDestroyDeletesStandaloneOrg is the other half of the
// symmetry: a standalone organization genuinely is created by Create, so Destroy
// must genuinely delete it. Suppressing the delete for every vcs_type would
// silently leak organizations instead.
func TestAccOrganizationDestroyDeletesStandaloneOrg(t *testing.T) {
	api, host := newOrgAPI(t, "circleci")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: orgConfig(host, "circleci")},
			{Config: providerOnlyConfig(host)},
		},
	})

	if got := api.deletes(); got != 1 {
		t.Errorf("destroy issued %d DELETE request(s) for a standalone organization, want 1", got)
	}
}

func providerOnlyConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
`, host)
}
