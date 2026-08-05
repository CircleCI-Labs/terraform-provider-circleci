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

	// Organization only. Configuring a namespace alongside it is now refused at
	// plan time, because the API checks org-id first and
	// never looks at the namespace — so the old two-filter config in this test
	// asked for one namespace and would have received the whole organization. See
	// TestAccRunnerResourceClassesDataSource_RequiresExactlyOneFilter.
	config := runnerProviderConfig(api.URL()) + `
data "circleci_runner_resource_classes" "test" {
  organization_id = "` + organizationID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
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
	// The namespace parameter must be absent, not empty-and-present: org-id wins
	// over it server-side, so sending both would make the request lie about its
	// scope.
	if request.Query.Has("namespace") {
		t.Errorf("expected no namespace filter, got query %v", request.Query)
	}
}

// TestAccRunnerResourceClassesDataSource_RequiresExactlyOneFilter checks the
// exactly-one-of validator.
//
// An unfiltered list is HTTP 400. An organization plus a namespace is not an
// error at all, which is worse: the API switches on
// org-id first and returns every resource class the organization owns, ignoring
// the namespace, so the configuration's stated scope and the result silently
// disagree. Both are now plan-time errors, and neither reaches the API.
func TestAccRunnerResourceClassesDataSource_RequiresExactlyOneFilter(t *testing.T) {
	tests := []struct {
		name       string
		attributes string
	}{
		{name: "no filter", attributes: ""},
		{
			name:       "organization and namespace",
			attributes: "organization_id = \"00000000-1111-2222-3333-444444444444\"\n  namespace = \"acc-ns\"",
		},
		{
			name:       "org id and namespace",
			attributes: "org_id = \"00000000-1111-2222-3333-444444444444\"\n  namespace = \"acc-ns\"",
		},
		{
			name:       "both organization spellings",
			attributes: "org_id = \"00000000-1111-2222-3333-444444444444\"\n  organization_id = \"00000000-1111-2222-3333-444444444444\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newRunnerFakeAPI(t)

			config := fmt.Sprintf("%s\ndata \"circleci_runner_resource_classes\" \"test\" {\n  %s\n}\n",
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

	resource.UnitTest(t, resource.TestCase{
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

// TestAccRunnerResourceClassesDataSource_RejectsAnInvalidNamespace checks that a
// namespace the service would refuse is caught at plan time.
//
// The API rejects any namespace value containing "." or "/"
// and then requires `^[a-z0-9_-]+$`, so an upper-case namespace — the shape a
// practitioner reaches for, since an organization's display name is usually
// capitalised — came back as HTTP 400 "invalid namespace" from a refresh.
func TestAccRunnerResourceClassesDataSource_RejectsAnInvalidNamespace(t *testing.T) {
	for _, namespace := range []string{"Acc-NS", "acc.ns", "acc/ns"} {
		t.Run(namespace, func(t *testing.T) {
			api := newRunnerFakeAPI(t)

			config := fmt.Sprintf("%s\ndata \"circleci_runner_resource_classes\" \"test\" {\n  namespace = %q\n}\n",
				runnerProviderConfig(api.URL()), namespace)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      config,
					ExpectError: regexp.MustCompile(`must be a runner namespace`),
				}},
			})

			if requests := api.allRequests(); len(requests) != 0 {
				t.Errorf("expected no requests to reach the runner API, got %v", requests)
			}
		})
	}
}
