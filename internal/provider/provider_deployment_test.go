// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// orgServer serves the v2 organization endpoint and records every path it is
// asked for, so tests can assert on the exact route the provider built.
func orgServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()

	var (
		mu    sync.Mutex
		paths []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":"00000000-1111-2222-3333-444444444444","name":"acme","slug":"gh/acme","vcs_type":"github"}`)
	}))
	t.Cleanup(srv.Close)

	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()

		return append([]string(nil), paths...)
	}
}

// TestAccProvider_LegacyHostWithAPIVersionSuffix guards backward compatibility.
//
// Releases up to v0.4.0 documented host = "https://circleci.com/api/v2". The
// provider now expects a bare origin and adds the version per request, so a
// carried-over host must be normalized rather than producing /api/v2/api/v2/...
func TestAccProvider_LegacyHostWithAPIVersionSuffix(t *testing.T) {
	srv, recorded := orgServer(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
data "circleci_organization" "t" {
  id = "00000000-1111-2222-3333-444444444444"
}
`, srv.URL+"/api/v2")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    []resource.TestStep{{Config: cfg}},
	})

	paths := recorded()
	if len(paths) == 0 {
		t.Fatal("the provider made no requests")
	}
	for _, got := range paths {
		if want := "/api/v2/organization/00000000-1111-2222-3333-444444444444"; got != want {
			t.Errorf("request path = %q, want %q (the /api/v2 suffix in host must not be doubled)", got, want)
		}
	}
}

// TestAccProvider_BareHost is the same check for the now-canonical bare origin.
func TestAccProvider_BareHost(t *testing.T) {
	srv, recorded := orgServer(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
data "circleci_organization" "t" {
  id = "00000000-1111-2222-3333-444444444444"
}
`, srv.URL)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    []resource.TestStep{{Config: cfg}},
	})

	paths := recorded()
	if len(paths) == 0 {
		t.Fatal("the provider made no requests")
	}
	for _, got := range paths {
		if want := "/api/v2/organization/00000000-1111-2222-3333-444444444444"; got != want {
			t.Errorf("request path = %q, want %q", got, want)
		}
	}
}

func TestAccProvider_DeploymentRejectsUnknownValue(t *testing.T) {
	cfg := `
provider "circleci" {
  host       = "https://circleci.example.com"
  key        = "fake"
  deployment = "on-prem"
}
data "circleci_organization" "t" {
  id = "00000000-1111-2222-3333-444444444444"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)deployment.*(cloud|server)`),
		}},
	})
}

func TestAccProvider_DeploymentAcceptsServer(t *testing.T) {
	srv, _ := orgServer(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host       = %q
  key        = "fake"
  deployment = "server"
}
data "circleci_organization" "t" {
  id = "00000000-1111-2222-3333-444444444444"
}
`, srv.URL)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    []resource.TestStep{{Config: cfg}},
	})
}

func TestAccProvider_DeploymentAcceptsCloud(t *testing.T) {
	srv, _ := orgServer(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host       = %q
  key        = "fake"
  deployment = "cloud"
}
data "circleci_organization" "t" {
  id = "00000000-1111-2222-3333-444444444444"
}
`, srv.URL)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    []resource.TestStep{{Config: cfg}},
	})
}
