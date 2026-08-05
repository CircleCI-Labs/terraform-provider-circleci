// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const (
	lookupOrgID     = "00000000-1111-2222-3333-444444444444"
	lookupOrgSlug   = "gh/acme"
	lookupContextID = "55555555-6666-7777-8888-999999999999"
)

// lookupAPI serves the organization and context routes needed to resolve an
// identifier, recording every path so a test can prove which lookup was used.
type lookupAPI struct {
	mu    sync.Mutex
	paths []string
}

func newLookupAPI(t *testing.T) (*lookupAPI, string) {
	t.Helper()

	api := &lookupAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.paths = append(api.paths, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		api.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		switch {
		// The organization route accepts a slug or an id in the same segment.
		case strings.HasPrefix(r.URL.Path, "/api/v2/organization/"):
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id": lookupOrgID, "name": "acme", "slug": lookupOrgSlug, "vcs_type": "github",
			})

		// Contexts have no lookup-by-name route, so the provider lists and filters.
		case r.URL.Path == "/api/v2/context":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]string{
					{"id": "11111111-0000-0000-0000-000000000001", "name": "other", "created_at": "2026-01-01T00:00:00Z"},
					{"id": lookupContextID, "name": "build", "created_at": "2026-01-02T00:00:00Z"},
				},
				"next_page_token": nil,
			})

		case strings.HasSuffix(r.URL.Path, "/restrictions"):
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})

		case strings.HasPrefix(r.URL.Path, "/api/v2/context/"):
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id": lookupContextID, "name": "build", "created_at": "2026-01-02T00:00:00Z",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found."}`))
		}
	}))
	t.Cleanup(srv.Close)

	return api, srv.URL
}

func (a *lookupAPI) requested() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.paths...)
}

func lookupProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
`, host)
}

// TestAccOrganizationDataSourceBySlug covers the chicken-and-egg problem.
//
// Every other resource in this provider is keyed by organization ID, which is
// otherwise visible only in the web application — so an id-only data source gave a
// practitioner no way in. The route segment is documented as "org-slug-or-id", so
// one request serves both lookups.
func TestAccOrganizationDataSourceBySlug(t *testing.T) {
	api, host := newLookupAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: lookupProviderConfig(host) + fmt.Sprintf(`
data "circleci_organization" "test" {
  slug = %q
}
`, lookupOrgSlug),
			ConfigStateChecks: []statecheck.StateCheck{
				// The id is what the caller actually wanted.
				statecheck.ExpectKnownValue("data.circleci_organization.test",
					tfjsonpath.New("id"), knownvalue.StringExact(lookupOrgID)),
				statecheck.ExpectKnownValue("data.circleci_organization.test",
					tfjsonpath.New("name"), knownvalue.StringExact("acme")),
			},
		}},
	})

	// The slug must reach the API in the identifier segment.
	var sawSlugLookup bool
	for _, path := range api.requested() {
		if strings.Contains(path, "/api/v2/organization/gh/acme") {
			sawSlugLookup = true
		}
	}
	if !sawSlugLookup {
		t.Errorf("no request used the slug as the identifier: %v", api.requested())
	}
}

func TestAccOrganizationDataSourceRequiresExactlyOneIdentifier(t *testing.T) {
	t.Parallel()

	for name, config := range map[string]string{
		"neither": `data "circleci_organization" "test" {}`,
		"both": fmt.Sprintf(`data "circleci_organization" "test" {
  id   = %q
  slug = %q
}`, lookupOrgID, lookupOrgSlug),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      lookupProviderConfig("http://127.0.0.1:1") + config,
					ExpectError: regexp.MustCompile(`(?s)Invalid Attribute Combination|Missing Attribute Configuration|exactly one`),
				}},
			})
		})
	}
}

// TestAccContextDataSourceByName covers
// CircleCI-Public/terraform-provider-circleci#118.
//
// The API has no lookup-by-name route, so the provider lists the organization's
// contexts and matches exactly. Names are unique within an organization, which is
// what makes that unambiguous.
func TestAccContextDataSourceByName(t *testing.T) {
	api, host := newLookupAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: lookupProviderConfig(host) + fmt.Sprintf(`
data "circleci_context" "test" {
  name            = "build"
  organization_id = %q
}
`, lookupOrgID),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("data.circleci_context.test",
					tfjsonpath.New("id"), knownvalue.StringExact(lookupContextID)),
				statecheck.ExpectKnownValue("data.circleci_context.test",
					tfjsonpath.New("name"), knownvalue.StringExact("build")),
				// The organization the caller supplied is echoed back: the API does
				// not report a context's owner, so dropping it would leave the
				// configuration and the state inconsistent.
				statecheck.ExpectKnownValue("data.circleci_context.test",
					tfjsonpath.New("organization_id"), knownvalue.StringExact(lookupOrgID)),
			},
		}},
	})

	var sawList bool
	for _, path := range api.requested() {
		if strings.HasPrefix(path, "GET /api/v2/context?") {
			sawList = true
		}
	}
	if !sawList {
		t.Errorf("lookup by name did not list contexts: %v", api.requested())
	}
}

func TestAccContextDataSourceByNameNotFound(t *testing.T) {
	_, host := newLookupAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: lookupProviderConfig(host) + fmt.Sprintf(`
data "circleci_context" "test" {
  name            = "does-not-exist"
  organization_id = %q
}
`, lookupOrgID),
			ExpectError: regexp.MustCompile(`(?s)No CircleCI context named`),
		}},
	})
}

func TestAccContextDataSourceNameRequiresOrganization(t *testing.T) {
	t.Parallel()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: lookupProviderConfig("http://127.0.0.1:1") + `
data "circleci_context" "test" {
  name = "build"
}
`,
			ExpectError: regexp.MustCompile(`(?s)organization_id|Invalid Attribute Combination`),
		}},
	})
}
