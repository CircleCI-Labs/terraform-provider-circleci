// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccRunnerResourceClassResource_SendsOrganizationID is a regression test.
// organization_id is Required on circleci_runner_resource_class, but the resource
// used to drop it on the floor: Create built a CreateResourceClassRequest without
// OrganizationID (so the org_id field went out empty) and Read passed "" as the
// org filter to ListResourceClasses. Practitioners therefore had to supply a value
// that never reached the API. Assert on the requests the API actually receives,
// not on resulting state, since state looked correct either way.
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

	resource.Test(t, resource.TestCase{
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

	resource.Test(t, resource.TestCase{
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
