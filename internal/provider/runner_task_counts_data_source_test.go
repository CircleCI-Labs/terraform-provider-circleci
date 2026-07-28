// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccRunnerTaskCountsDataSource(t *testing.T) {
	api := newRunnerFakeAPI(t)
	api.respond("GET", "/api/v3/runner/tasks", `{"unclaimed_task_count":7}`)
	api.respond("GET", "/api/v3/runner/tasks/running", `{"running_runner_tasks":3}`)

	config := runnerProviderConfig(api.URL()) + `
data "circleci_runner_task_counts" "test" {
  resource_class = "acc-ns/acc-rc"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_runner_task_counts.test",
					tfjsonpath.New("unclaimed_task_count"),
					knownvalue.Int64Exact(7),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_runner_task_counts.test",
					tfjsonpath.New("running_task_count"),
					knownvalue.Int64Exact(3),
				),
			},
		}},
	})

	// Both counts come from one data source, so both endpoints are hit with the
	// same resource class in the same refresh.
	for _, path := range []string{"/api/v3/runner/tasks", "/api/v3/runner/tasks/running"} {
		request := api.firstRequest(t, "GET", path)
		if got := request.Query.Get("resource-class"); got != "acc-ns/acc-rc" {
			t.Errorf("%s resource-class = %q, want %q", path, got, "acc-ns/acc-rc")
		}
	}
}

// TestAccRunnerTaskCountsDataSource_ZeroCounts checks that an idle resource class
// reports zeroes rather than nulls.
func TestAccRunnerTaskCountsDataSource_ZeroCounts(t *testing.T) {
	api := newRunnerFakeAPI(t)
	api.respond("GET", "/api/v3/runner/tasks", `{"unclaimed_task_count":0}`)
	api.respond("GET", "/api/v3/runner/tasks/running", `{"running_runner_tasks":0}`)

	config := runnerProviderConfig(api.URL()) + `
data "circleci_runner_task_counts" "test" {
  resource_class = "acc-ns/acc-rc"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_runner_task_counts.test",
					tfjsonpath.New("unclaimed_task_count"),
					knownvalue.Int64Exact(0),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_runner_task_counts.test",
					tfjsonpath.New("running_task_count"),
					knownvalue.Int64Exact(0),
				),
			},
		}},
	})
}
