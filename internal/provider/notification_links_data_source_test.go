// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// The fixture shape comes from the notifications service's own
// the CircleCI API handler: a v3 collection whose entities carry
// their fields under "attributes" and the owning user under
// "references.user.id", with NO id of their own.
func newNotificationLinksAPI(t *testing.T, body string, status int) (string, func() []url.Values) {
	t.Helper()

	var (
		mu      sync.Mutex
		queries []url.Values
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.Query())
		mu.Unlock()

		if got := r.URL.Path; got != "/api/v3/notification/links" {
			t.Errorf("request path = %q, want /api/v3/notification/links", got)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv.URL, func() []url.Values {
		mu.Lock()
		defer mu.Unlock()

		return append([]url.Values(nil), queries...)
	}
}

func notificationLinksConfig(host, deployment, extra string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host       = %q
  key        = "fake"
  deployment = %q
}

data "circleci_notification_links" "test" {
%s
}
`, host, deployment, extra)
}

func TestNotificationLinksDataSourceUnit_Read(t *testing.T) {
	host, queries := newNotificationLinksAPI(t, `{
		"data": [
			{
				"attributes": {
					"connection_type": "slack",
					"external_id": "U0123",
					"external_scope_id": "T0123",
					"display_name": "Ada Lovelace"
				},
				"references": {"user": {"id": "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"}}
			}
		],
		"page": {"next": null}
	}`, http.StatusOK)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: notificationLinksConfig(host, "cloud", ""),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_notification_links.test",
					tfjsonpath.New("links").AtSliceIndex(0).AtMapKey("connection_type"),
					knownvalue.StringExact("slack"),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_notification_links.test",
					tfjsonpath.New("links").AtSliceIndex(0).AtMapKey("external_id"),
					knownvalue.StringExact("U0123"),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_notification_links.test",
					tfjsonpath.New("links").AtSliceIndex(0).AtMapKey("external_scope_id"),
					knownvalue.StringExact("T0123"),
				),
				// The owning user comes from references, not attributes. Reading it
				// from attributes would leave it permanently empty — the same class of
				// bug as the checkout key snake_case tags.
				statecheck.ExpectKnownValue(
					"data.circleci_notification_links.test",
					tfjsonpath.New("links").AtSliceIndex(0).AtMapKey("user_id"),
					knownvalue.StringExact("9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"),
				),
			},
		}},
	})

	// filter[user_id] is required upstream, so the client must default it rather
	// than omitting it and getting a 400.
	sent := queries()
	if len(sent) == 0 {
		t.Fatal("no request recorded")
	}
	if got := sent[0].Get("filter[user_id]"); got != "me" {
		t.Errorf("filter[user_id] = %q, want %q by default", got, "me")
	}
	// Unset optional filters must not be sent as empty strings: an empty
	// filter[connection_type] would match nothing rather than everything.
	for _, absent := range []string{"filter[connection_type]", "filter[team_id]"} {
		if _, present := sent[0][absent]; present {
			t.Errorf("%s was sent as %q when unconfigured; it must be omitted entirely",
				absent, sent[0].Get(absent))
		}
	}
}

func TestNotificationLinksDataSourceUnit_Filters(t *testing.T) {
	host, queries := newNotificationLinksAPI(t, `{"data": [], "page": {"next": null}}`, http.StatusOK)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: notificationLinksConfig(host, "cloud", `
  connection_type = "slack"
  team_id         = "T0123"
`),
			ConfigStateChecks: []statecheck.StateCheck{
				// An empty result must be an empty list, not null: a configuration
				// iterating over it should get zero iterations, not an error.
				statecheck.ExpectKnownValue(
					"data.circleci_notification_links.test",
					tfjsonpath.New("links"),
					knownvalue.ListSizeExact(0),
				),
			},
		}},
	})

	sent := queries()
	if len(sent) == 0 {
		t.Fatal("no request recorded")
	}
	if got := sent[0].Get("filter[connection_type]"); got != "slack" {
		t.Errorf("filter[connection_type] = %q, want slack", got)
	}
	if got := sent[0].Get("filter[team_id]"); got != "T0123" {
		t.Errorf("filter[team_id] = %q, want T0123", got)
	}
}

// TestNotificationLinksDataSourceUnit_ForbiddenIsNotEmpty covers the privilege
// boundary. Asking for another user's links is refused with 403, and that must
// surface as an error rather than as an empty list — an empty list would read as
// "this user has connected nothing", which is a materially different claim.
func TestNotificationLinksDataSourceUnit_ForbiddenIsNotEmpty(t *testing.T) {
	host, _ := newNotificationLinksAPI(t,
		`{"message":"cannot read links for another user"}`, http.StatusForbidden)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      notificationLinksConfig(host, "cloud", `  user_id = "11111111-2222-3333-4444-555555555555"`),
			ExpectError: regexp.MustCompile(`(?s)Unable to list CircleCI notification links`),
		}},
	})
}

// TestNotificationLinksDataSourceUnit_ServerDeployment asserts the Cloud gate.
// The v3 API is not routed by a CircleCI Server installation, so this must fail
// with a clear diagnostic rather than a confusing 404.
func TestNotificationLinksDataSourceUnit_ServerDeployment(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      notificationLinksConfig("http://127.0.0.1:1", "server", ""),
			ExpectError: regexp.MustCompile(`(?s)circleci_notification_links requires CircleCI Cloud`),
		}},
	})
}
