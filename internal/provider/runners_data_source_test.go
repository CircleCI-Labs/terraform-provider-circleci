// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccRunnersDataSource(t *testing.T) {
	// Scoped by organization, because that is the only listing the API
	// populates `status` on — see the fixture's comment below and the status
	// attribute in runners_data_source.go.
	const organizationID = "00000000-1111-2222-3333-444444444444"

	api := newRunnerFakeAPI(t)
	// `busy` and `idle` are the only two values the API assigns to `status`.
	// The fixture used to serve `running`, which the
	// service never sends and which the schema then documented back — a fake and a
	// description agreeing with each other and with nothing else.
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
	    "status": "busy",
	    "resource_class": "acc-ns/acc-rc",
	    "last_used": null
	  }
	]}`)

	config := runnerProviderConfig(api.URL()) + `
data "circleci_runners" "test" {
  org_id = "` + organizationID + `"
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
	if got := request.Query.Get("org-id"); got != organizationID {
		t.Errorf("org-id = %q, want %q", got, organizationID)
	}
}

// TestAccRunnersDataSource_EachFilterReachesTheAPI checks that whichever single
// filter is configured reaches the API under the query parameter name the runner
// API expects, and — just as importantly — that nothing else is sent alongside it.
//
// It replaces a test that configured all three filters at once and asserted all
// three parameters were sent. That passed against the fake and was wrong about
// production twice over: the API honours exactly one
// scope, so two of the three were being discarded, and the same combination
// without an organization is an outright HTTP 400. The fake answered 200 to all
// of it, which is precisely why the suite could not see the problem.
func TestAccRunnersDataSource_EachFilterReachesTheAPI(t *testing.T) {
	tests := []struct {
		name      string
		attribute string
		value     string
		parameter string
	}{
		{name: "resource class", attribute: "resource_class", value: "acc-ns/acc-rc", parameter: "resource-class"},
		{name: "namespace", attribute: "namespace", value: "acc-ns", parameter: "namespace"},
		{name: "org id", attribute: "org_id", value: "00000000-1111-2222-3333-444444444444", parameter: "org-id"},
		{
			name:      "deprecated organization id",
			attribute: "organization_id",
			value:     "00000000-1111-2222-3333-444444444444",
			parameter: "org-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newRunnerFakeAPI(t)
			api.respond("GET", "/api/v3/runner", `{"items": []}`)

			config := fmt.Sprintf("%s\ndata \"circleci_runners\" \"test\" {\n  %s = %q\n}\n",
				runnerProviderConfig(api.URL()), tt.attribute, tt.value)

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
			if got := request.Query.Get(tt.parameter); got != tt.value {
				t.Errorf("%s = %q, want %q", tt.parameter, got, tt.value)
			}

			// One scope in, one scope out: an unconfigured filter must not appear at
			// all, since the API would honour the wrong one.
			for _, parameter := range []string{"resource-class", "namespace", "org-id"} {
				if parameter == tt.parameter {
					continue
				}
				if request.Query.Has(parameter) {
					t.Errorf("query carries %s=%q, want it absent (query: %v)",
						parameter, request.Query.Get(parameter), request.Query)
				}
			}
		})
	}
}

// TestAccRunnersDataSource_StatusIsEmptyOutsideAnOrgListing pins down the shape
// of a resource-class-scoped listing.
//
// The API maps runners through different code paths depending on scope: a
// resource-class or namespace scope never populates `status`, so the key is
// absent from the response entirely (it is
// `json:"status,omitempty"` over an unset string) and the attribute is "". The
// data source must still produce a known empty string rather than null or an
// error, because the attribute is Computed and practitioners interpolate it.
func TestAccRunnersDataSource_StatusIsEmptyOutsideAnOrgListing(t *testing.T) {
	api := newRunnerFakeAPI(t)
	// No "status" key at all — that is what the service sends for this scope.
	api.respond("GET", "/api/v3/runner", `{"items": [
	  {
	    "name": "acc-ns/acc-rc/agent-one",
	    "hostname": "runner-one",
	    "ip": "10.0.0.1",
	    "version": "3.1.2",
	    "resource_class": "acc-ns/acc-rc",
	    "first_connected": "2026-01-01T00:00:00Z",
	    "last_connected": "2026-01-02T00:00:00Z",
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
					tfjsonpath.New("runners").AtSliceIndex(0).AtMapKey("status"),
					knownvalue.StringExact(""),
				),
			},
		}},
	})
}

// TestAccRunnersDataSource_RequiresExactlyOneFilter checks the exactly-one-of
// validator from both directions.
//
// No filter at all is an HTTP 400 from the API. Two filters is worse than an
// error: the API honours one scope in a fixed priority order and discards
// the rest, so `org_id` plus `resource_class` silently lists the whole
// organization. Both are refused at plan time, and neither reaches the API.
func TestAccRunnersDataSource_RequiresExactlyOneFilter(t *testing.T) {
	tests := []struct {
		name       string
		attributes string
	}{
		{name: "no filter", attributes: ""},
		{
			// The combination the API answers with a 400.
			name:       "resource class and namespace",
			attributes: "resource_class = \"acc-ns/acc-rc\"\n  namespace = \"acc-ns\"",
		},
		{
			// The combination the API answers 200 to, having ignored resource_class.
			name:       "org id and resource class",
			attributes: "org_id = \"00000000-1111-2222-3333-444444444444\"\n  resource_class = \"acc-ns/acc-rc\"",
		},
		{
			name:       "both organization spellings",
			attributes: "org_id = \"00000000-1111-2222-3333-444444444444\"\n  organization_id = \"00000000-1111-2222-3333-444444444444\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newRunnerFakeAPI(t)

			config := fmt.Sprintf("%s\ndata \"circleci_runners\" \"test\" {\n  %s\n}\n",
				runnerProviderConfig(api.URL()), tt.attributes)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: config,
					// Terraform titles the diagnostic "Missing Attribute
					// Configuration" for none and "Invalid Attribute Combination" for
					// too many; the sentence below is common to both.
					ExpectError: regexp.MustCompile(`Exactly one of these attributes must be configured`),
				}},
			})

			if requests := api.allRequests(); len(requests) != 0 {
				t.Errorf("expected no requests to reach the runner API, got %v", requests)
			}
		})
	}
}
