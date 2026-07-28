// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccRunnerResourceClassesDataSource(t *testing.T) {
	const organizationID = "00000000-1111-2222-3333-444444444444"

	api := newRunnerFakeAPI(t)
	api.respond("GET", "/api/v3/runner/resource", `{"items":[
	  {
	    "id": "11111111-2222-3333-4444-555555555555",
	    "resource_class": "acc-ns/linux",
	    "description": "linux runners"
	  },
	  {
	    "id": "66666666-7777-8888-9999-aaaaaaaaaaaa",
	    "resource_class": "acc-ns/macos"
	  }
	]}`)

	config := runnerProviderConfig(api.URL()) + `
data "circleci_runner_resource_classes" "test" {
  organization_id = "` + organizationID + `"
  namespace       = "acc-ns"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_runner_resource_classes.test",
					tfjsonpath.New("resource_classes"),
					knownvalue.ListSizeExact(2),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_runner_resource_classes.test",
					tfjsonpath.New("resource_classes").AtSliceIndex(0),
					knownvalue.ObjectExact(map[string]knownvalue.Check{
						"id":             knownvalue.StringExact("11111111-2222-3333-4444-555555555555"),
						"resource_class": knownvalue.StringExact("acc-ns/linux"),
						"description":    knownvalue.StringExact("linux runners"),
					}),
				),
				// A resource class with no description comes back as an empty string.
				statecheck.ExpectKnownValue(
					"data.circleci_runner_resource_classes.test",
					tfjsonpath.New("resource_classes").AtSliceIndex(1).AtMapKey("description"),
					knownvalue.StringExact(""),
				),
			},
		}},
	})

	request := api.firstRequest(t, "GET", "/api/v3/runner/resource")
	if got := request.Query.Get("org-id"); got != organizationID {
		t.Errorf("org-id = %q, want %q", got, organizationID)
	}
	if got := request.Query.Get("namespace"); got != "acc-ns" {
		t.Errorf("namespace = %q, want %q", got, "acc-ns")
	}
}

// TestAccRunnerResourceClassesDataSource_NamespaceOnly checks that either filter
// on its own is enough.
func TestAccRunnerResourceClassesDataSource_NamespaceOnly(t *testing.T) {
	api := newRunnerFakeAPI(t)
	api.respond("GET", "/api/v3/runner/resource", `{"items":[]}`)

	config := runnerProviderConfig(api.URL()) + `
data "circleci_runner_resource_classes" "test" {
  namespace = "acc-ns"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_runner_resource_classes.test",
					tfjsonpath.New("resource_classes"),
					knownvalue.ListSizeExact(0),
				),
			},
		}},
	})

	request := api.firstRequest(t, "GET", "/api/v3/runner/resource")
	if request.Query.Has("org-id") {
		t.Errorf("expected no org-id filter, got query %v", request.Query)
	}
}

// TestAccRunnerResourceClassesDataSource_RequiresAFilter checks the
// at-least-one-of validator: the runner API rejects an unfiltered list.
func TestAccRunnerResourceClassesDataSource_RequiresAFilter(t *testing.T) {
	api := newRunnerFakeAPI(t)

	config := runnerProviderConfig(api.URL()) + `
data "circleci_runner_resource_classes" "test" {
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`(?s)At least one of these attributes must be configured.*organization_id,namespace`),
		}},
	})

	if requests := api.allRequests(); len(requests) != 0 {
		t.Errorf("expected no requests to reach the runner API, got %v", requests)
	}
}
