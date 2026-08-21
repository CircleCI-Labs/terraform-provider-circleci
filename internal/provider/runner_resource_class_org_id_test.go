// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccRunnerResourceClassResource_SendsOrganizationID is a regression test.
// circleci_runner_resource_class requires exactly one of organization_id/org_id
// in configuration (see orgIDConfigValidator), but the resource used to drop
// the value on the floor once it had it: Create built a ResourceClassInput
// without OrganizationID (so the org_id field went out empty) and Read passed
// "" as the org filter to ListResourceClasses. Practitioners therefore had to
// supply a value that never reached the API. Assert on the requests the API
// actually receives, not on resulting state, since state looked correct either
// way.
func TestAccRunnerResourceClassResource_SendsOrganizationID(t *testing.T) {
	const (
		organizationID  = "00000000-1111-2222-3333-444444444444"
		resourceClassID = "11111111-2222-3333-4444-555555555555"
		resourceClass   = "acc-ns/acc-rc"
		description     = "regression test resource class"
	)

	api := newRunnerFakeAPI(t)
	body := fmt.Sprintf(
		`{"id":%q,"resource_class":%q,"description":%q}`,
		resourceClassID, resourceClass, description,
	)
	api.respond("POST", "/api/v3/runner/resource", body)
	api.respond("GET", "/api/v3/runner/resource", fmt.Sprintf(`{"items":[%s]}`, body))

	config := runnerProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_runner_resource_class" "test" {
  organization_id = %q
  resource_class  = %q
  description     = %q
}
`, organizationID, resourceClass, description)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    []resource.TestStep{{Config: config}},
	})

	// Create must carry org_id in the request body.
	create := api.firstRequest(t, "POST", "/api/v3/runner/resource")

	var payload struct {
		OrganizationID string `json:"org_id"`
		ResourceClass  string `json:"resource_class"`
		Description    string `json:"description"`
	}
	if err := json.Unmarshal([]byte(create.Body), &payload); err != nil {
		t.Fatalf("could not decode the create request body %q: %s", create.Body, err)
	}

	if payload.OrganizationID != organizationID {
		t.Errorf("create request org_id = %q, want %q (body: %s)", payload.OrganizationID, organizationID, create.Body)
	}
	if payload.ResourceClass != resourceClass {
		t.Errorf("create request resource_class = %q, want %q", payload.ResourceClass, resourceClass)
	}
	if payload.Description != description {
		t.Errorf("create request description = %q, want %q", payload.Description, description)
	}

	// Read must scope the list by organization as well as namespace.
	read := api.firstRequest(t, "GET", "/api/v3/runner/resource")

	if got := read.Query.Get("org-id"); got != organizationID {
		t.Errorf("read request org-id = %q, want %q (query: %v)", got, organizationID, read.Query)
	}
	if got := read.Query.Get("namespace"); got != "acc-ns" {
		t.Errorf("read request namespace = %q, want %q", got, "acc-ns")
	}
}

// TestAccRunnerResourceClassResource_RejectsOrganizationSlug checks that the
// runner API's UUID-only organization_id is enforced at plan time rather than
// surfacing as an opaque HTTP 400.
func TestAccRunnerResourceClassResource_RejectsOrganizationSlug(t *testing.T) {
	api := newRunnerFakeAPI(t)

	config := runnerProviderConfig(api.URL()) + `
resource "circleci_runner_resource_class" "test" {
  organization_id = "circleci/my-org"
  resource_class  = "acc-ns/acc-rc"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`must be an organization UUID`),
		}},
	})

	if requests := api.allRequests(); len(requests) != 0 {
		t.Errorf("expected no requests to reach the runner API, got %v", requests)
	}
}

// TestAccRunnerResourceClassResource_MissingNamespaceSurfacesDiagnostic pins
// the wire shape confirmed live against the real runner API [NET]: a
// resource_class whose namespace half was never claimed (or is owned by
// someone else) answers HTTP 404 with a bare `{"message": "..."}` body —
// not the v3 `{"error": {...}}` envelope, and not the HTTP 400 "resource
// class not valid" a malformed name gets. See CreateResourceClass's doc
// comment in internal/circleci/runner.go for the exact response captured
// against the service.
//
// This asserts the provider surfaces that message to the practitioner rather
// than swallowing it or reporting a generic failure: circleci.Detail has no
// namespace-specific branch, so this is really a test of the v1/v2
// bare-message fallback in Detail, exercised through this resource's Create.
func TestAccRunnerResourceClassResource_MissingNamespaceSurfacesDiagnostic(t *testing.T) {
	api := newRunnerFakeAPI(t)
	api.respondStatus("POST", "/api/v3/runner/resource", http.StatusNotFound,
		`{"message":"not found with provided token: check permissions to view or admin self-hosted runners"}`)

	config := runnerProviderConfig(api.URL()) + `
resource "circleci_runner_resource_class" "test" {
  org_id         = "00000000-1111-2222-3333-444444444444"
  resource_class = "unclaimed-ns/acc-rc"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			// (?s) and \s+ in place of literal spaces: Terraform's diagnostic
			// renderer word-wraps the message, so the exact whitespace between
			// words is not stable to match on.
			ExpectError: regexp.MustCompile(
				`(?s)not found with provided token:\s+check permissions to view or admin\s+self-hosted\s+runners`,
			),
		}},
	})
}
