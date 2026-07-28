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

func TestAccRunnerTokensDataSource(t *testing.T) {
	api := newRunnerFakeAPI(t)
	api.respond("GET", "/api/v3/runner/token", `{"items":[
	  {
	    "id": "11111111-2222-3333-4444-555555555555",
	    "nickname": "first",
	    "resource_class": "acc-ns/acc-rc",
	    "created_at": "2026-01-01T00:00:00Z"
	  },
	  {
	    "id": "66666666-7777-8888-9999-aaaaaaaaaaaa",
	    "nickname": "second",
	    "resource_class": "acc-ns/acc-rc",
	    "created_at": "2026-02-02T00:00:00Z"
	  }
	]}`)

	config := runnerProviderConfig(api.URL()) + `
data "circleci_runner_tokens" "test" {
  resource_class = "acc-ns/acc-rc"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_runner_tokens.test",
					tfjsonpath.New("tokens"),
					knownvalue.ListSizeExact(2),
				),
				// ObjectExact fails if the object grows an attribute, so this also
				// pins down that no "token" attribute is exposed: the runner API
				// returns a token's value only when it is created, so a data source
				// could never populate it.
				statecheck.ExpectKnownValue(
					"data.circleci_runner_tokens.test",
					tfjsonpath.New("tokens").AtSliceIndex(0),
					knownvalue.ObjectExact(map[string]knownvalue.Check{
						"id":             knownvalue.StringExact("11111111-2222-3333-4444-555555555555"),
						"nickname":       knownvalue.StringExact("first"),
						"resource_class": knownvalue.StringExact("acc-ns/acc-rc"),
						"created_at":     knownvalue.StringExact("2026-01-01T00:00:00Z"),
					}),
				),
			},
		}},
	})

	request := api.firstRequest(t, "GET", "/api/v3/runner/token")
	if got := request.Query.Get("resource-class"); got != "acc-ns/acc-rc" {
		t.Errorf("resource-class = %q, want %q", got, "acc-ns/acc-rc")
	}
}

// TestAccRunnerTokensDataSource_NoTokens checks that a resource class with no
// tokens yields an empty list rather than null, so configurations can iterate.
func TestAccRunnerTokensDataSource_NoTokens(t *testing.T) {
	api := newRunnerFakeAPI(t)
	api.respond("GET", "/api/v3/runner/token", `{"items":[]}`)

	config := runnerProviderConfig(api.URL()) + `
data "circleci_runner_tokens" "test" {
  resource_class = "acc-ns/acc-rc"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_runner_tokens.test",
					tfjsonpath.New("tokens"),
					knownvalue.ListSizeExact(0),
				),
			},
		}},
	})
}

// TestAccRunnerTokensDataSource_RejectsBareResourceClass checks that a resource
// class missing its namespace is caught at plan time.
func TestAccRunnerTokensDataSource_RejectsBareResourceClass(t *testing.T) {
	api := newRunnerFakeAPI(t)

	config := runnerProviderConfig(api.URL()) + `
data "circleci_runner_tokens" "test" {
  resource_class = "acc-rc"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`must be in the format 'namespace/name'`),
		}},
	})

	if requests := api.allRequests(); len(requests) != 0 {
		t.Errorf("expected no requests to reach the runner API, got %v", requests)
	}
}
