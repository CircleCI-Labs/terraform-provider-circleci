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

func TestAccRunnersDataSource(t *testing.T) {
	api := newRunnerFakeAPI(t)
	api.respond("GET", "/api/v3/runner", `{"items": [
	  {
	    "name": "acc-ns/acc-rc/agent-one",
	    "hostname": "runner-one",
	    "ip": "10.0.0.1",
	    "version": "3.1.2",
	    "status": "idle",
	    "resource_class": "acc-ns/acc-rc",
	    "first_connected": "2026-01-01T00:00:00Z",
	    "last_connected": "2026-01-02T00:00:00Z",
	    "last_used": "2026-01-03T00:00:00Z"
	  },
	  {
	    "name": "acc-ns/acc-rc/agent-two",
	    "hostname": "runner-two",
	    "ip": "10.0.0.2",
	    "version": "3.1.3",
	    "status": "running",
	    "resource_class": "acc-ns/acc-rc",
	    "last_used": null
	  }
	]}`)

	config := runnerProviderConfig(api.URL()) + `
data "circleci_runners" "test" {
  resource_class = "acc-ns/acc-rc"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_runners.test",
					tfjsonpath.New("runners"),
					knownvalue.ListSizeExact(2),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_runners.test",
					tfjsonpath.New("runners").AtSliceIndex(0),
					knownvalue.ObjectExact(map[string]knownvalue.Check{
						"name":            knownvalue.StringExact("acc-ns/acc-rc/agent-one"),
						"hostname":        knownvalue.StringExact("runner-one"),
						"ip":              knownvalue.StringExact("10.0.0.1"),
						"version":         knownvalue.StringExact("3.1.2"),
						"status":          knownvalue.StringExact("idle"),
						"resource_class":  knownvalue.StringExact("acc-ns/acc-rc"),
						"first_connected": knownvalue.StringExact("2026-01-01T00:00:00Z"),
						"last_connected":  knownvalue.StringExact("2026-01-02T00:00:00Z"),
						"last_used":       knownvalue.StringExact("2026-01-03T00:00:00Z"),
					}),
				),
				// last_used is null for an agent that has never claimed a task. The
				// API sends an explicit null here, and collapsing it to "" would
				// make "never used" indistinguishable from a missing value.
				statecheck.ExpectKnownValue(
					"data.circleci_runners.test",
					tfjsonpath.New("runners").AtSliceIndex(1).AtMapKey("last_used"),
					knownvalue.Null(),
				),
			},
		}},
	})

	request := api.firstRequest(t, "GET", "/api/v3/runner")
	if got := request.Query.Get("resource-class"); got != "acc-ns/acc-rc" {
		t.Errorf("resource-class = %q, want %q", got, "acc-ns/acc-rc")
	}
}

// TestAccRunnersDataSource_AllFilters checks that each configured filter reaches
// the API under the query parameter name the runner API expects.
func TestAccRunnersDataSource_AllFilters(t *testing.T) {
	api := newRunnerFakeAPI(t)
	api.respond("GET", "/api/v3/runner", `{"items": []}`)

	config := runnerProviderConfig(api.URL()) + `
data "circleci_runners" "test" {
  resource_class  = "acc-ns/acc-rc"
  namespace       = "acc-ns"
  organization_id = "00000000-1111-2222-3333-444444444444"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				// No runners is a valid answer and must stay an empty list.
				statecheck.ExpectKnownValue(
					"data.circleci_runners.test",
					tfjsonpath.New("runners"),
					knownvalue.ListSizeExact(0),
				),
			},
		}},
	})

	request := api.firstRequest(t, "GET", "/api/v3/runner")
	for parameter, want := range map[string]string{
		"resource-class": "acc-ns/acc-rc",
		"namespace":      "acc-ns",
		"org-id":         "00000000-1111-2222-3333-444444444444",
	} {
		if got := request.Query.Get(parameter); got != want {
			t.Errorf("%s = %q, want %q", parameter, got, want)
		}
	}
}

// TestAccRunnersDataSource_RequiresAFilter checks the at-least-one-of validator:
// the runner API rejects an unfiltered list, so it is refused at plan time.
func TestAccRunnersDataSource_RequiresAFilter(t *testing.T) {
	api := newRunnerFakeAPI(t)

	config := runnerProviderConfig(api.URL()) + `
data "circleci_runners" "test" {
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			// The organization counts under either of its two names, so both are
			// listed. See org_id_deprecation.go.
			ExpectError: regexp.MustCompile(
				`(?s)At least one of these attributes must be configured.*resource_class,namespace,organization_id,org_id`,
			),
		}},
	})

	if requests := api.allRequests(); len(requests) != 0 {
		t.Errorf("expected no requests to reach the runner API, got %v", requests)
	}
}
