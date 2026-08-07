// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	sdkresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// testOrbAllowListOrg is a slug organization, deliberately containing a slash so
// the route building and the import id parsing are exercised on the harder case.
const testOrbAllowListOrg = "gh/acme"

// fakeOrbAllowListAPI is a stateful stand-in for the v2 URL orb allow list
// endpoints.
type fakeOrbAllowListAPI struct {
	mu      sync.Mutex
	entries []map[string]any
	nextID  int
	paths   []string
	// listStatus, when non-zero, makes the list route answer with that status
	// instead of the collection. It stands in for the 404 the real handler throws
	// both for an organization that does not exist and for one the token may not
	// view -- which is a different thing from an entry being absent.
	listStatus int
	// deleteStatus, when non-zero, makes the delete route answer with that status
	// instead of removing the entry. It stands in for the 404 the real delete
	// route throws when the organization cannot be resolved or viewed -- the real
	// route never 404s over a missing entry, so this is the only way a delete 404
	// happens.
	deleteStatus int
}

func newFakeOrbAllowListAPI(t *testing.T) (*httptest.Server, *fakeOrbAllowListAPI) {
	t.Helper()

	api := &fakeOrbAllowListAPI{nextID: 1}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()

		api.paths = append(api.paths, r.Method+" "+r.URL.Path)

		w.Header().Set("Content-Type", "application/json")

		const collection = "/url-orb-allow-list"

		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, collection):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding create body: %v", err)
			}

			id := fmt.Sprintf("00000000-0000-0000-0000-00000000000%d", api.nextID)
			api.nextID++

			entry := map[string]any{"id": id}
			for _, key := range []string{"name", "prefix", "auth"} {
				entry[key] = body[key]
			}
			api.entries = append(api.entries, entry)

			// The real API answers a create with 201 and only the id and a message.
			w.WriteHeader(http.StatusCreated)

			if err := json.NewEncoder(w).Encode(map[string]any{"id": id, "message": "Created."}); err != nil {
				t.Errorf("encoding create response: %v", err)
			}

		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, collection):
			if api.listStatus != 0 {
				w.WriteHeader(api.listStatus)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))

				return
			}

			items := append([]map[string]any(nil), api.entries...)
			if items == nil {
				items = []map[string]any{}
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"items": items}); err != nil {
				t.Errorf("encoding list response: %v", err)
			}

		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, collection+"/"):
			if api.deleteStatus != 0 {
				w.WriteHeader(api.deleteStatus)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))

				return
			}

			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]

			// The real route answers 200 whether or not id was present -- deleting an
			// entry that is already gone succeeds rather than 404ing. Mirror that here
			// rather than erroring on an unknown id, so this fake cannot mask the bug
			// where the provider mistook that 200-regardless-of-existence for "a 404
			// means the entry was already gone".
			kept := make([]map[string]any, 0, len(api.entries))
			for _, entry := range api.entries {
				if entry["id"] != id {
					kept = append(kept, entry)
				}
			}
			api.entries = kept

			if err := json.NewEncoder(w).Encode(map[string]any{"message": "Deleted."}); err != nil {
				t.Errorf("encoding delete response: %v", err)
			}

		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return srv, api
}

func (a *fakeOrbAllowListAPI) recordedPaths() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.paths...)
}

// setListStatus makes the list route fail with status, or restores it when status
// is zero.
func (a *fakeOrbAllowListAPI) setListStatus(status int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.listStatus = status
}

// setDeleteStatus makes the delete route fail with status instead of removing
// the entry, or restores normal behavior when status is zero.
func (a *fakeOrbAllowListAPI) setDeleteStatus(status int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.deleteStatus = status
}

func (a *fakeOrbAllowListAPI) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return len(a.entries)
}

func TestAccURLOrbAllowListEntry_Lifecycle(t *testing.T) {
	srv, api := newFakeOrbAllowListAPI(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
resource "circleci_url_orb_allow_list_entry" "t" {
  organization = %q
  name         = "CircleCI-Public orbs"
  prefix       = "https://raw.githubusercontent.com/CircleCI-Public/orbs/refs/heads/main/"
  auth         = "github-app"
}
`, srv.URL, testOrbAllowListOrg)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config: cfg,
			Check: sdkresource.ComposeAggregateTestCheckFunc(
				sdkresource.TestCheckResourceAttrSet("circleci_url_orb_allow_list_entry.t", "id"),
				sdkresource.TestCheckResourceAttr("circleci_url_orb_allow_list_entry.t", "organization", testOrbAllowListOrg),
				sdkresource.TestCheckResourceAttr("circleci_url_orb_allow_list_entry.t", "name", "CircleCI-Public orbs"),
				sdkresource.TestCheckResourceAttr("circleci_url_orb_allow_list_entry.t", "auth", "github-app"),
			),
		}},
	})

	// The route must be v2 with the slug interpolated as two path segments.
	paths := api.recordedPaths()
	wantCreate := "POST /api/v2/organization/" + testOrbAllowListOrg + "/url-orb-allow-list"
	if len(paths) == 0 || paths[0] != wantCreate {
		t.Errorf("recorded requests = %v, want the first to be %q", paths, wantCreate)
	}

	// The test framework destroys at the end of the case, so the entry must be
	// gone from the fake API.
	if got := api.count(); got != 0 {
		t.Errorf("the fake API still holds %d entries after destroy, want 0", got)
	}
}

// TestAccURLOrbAllowListEntry_WorksOnServerDeployment is the counterpart to the
// organization settings test: this is a v2 API, so it must not be gated on
// CircleCI Cloud.
func TestAccURLOrbAllowListEntry_WorksOnServerDeployment(t *testing.T) {
	srv, api := newFakeOrbAllowListAPI(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host       = %q
  key        = "fake"
  deployment = "server"
}
resource "circleci_url_orb_allow_list_entry" "t" {
  organization = %q
  name          = "internal mirror"
  prefix        = "https://orbs.internal.example.com/"
  auth          = "none"
}
`, srv.URL, testOrbAllowListOrg)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config: cfg,
			Check: sdkresource.TestCheckResourceAttr(
				"circleci_url_orb_allow_list_entry.t", "auth", "none"),
		}},
	})

	if len(api.recordedPaths()) == 0 {
		t.Error("the provider made no requests on a server deployment, want the v2 route to be used")
	}
}

func TestAccURLOrbAllowListEntry_PrefixRequiresReplace(t *testing.T) {
	srv, _ := newFakeOrbAllowListAPI(t)

	config := func(prefix string) string {
		return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
resource "circleci_url_orb_allow_list_entry" "t" {
  organization = %q
  name         = "orbs"
  prefix       = %q
  auth         = "none"
}
`, srv.URL, testOrbAllowListOrg, prefix)
	}

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{
			{Config: config("https://orbs.example.com/a/")},
			{
				// There is no update route, so this must be a replacement rather
				// than reaching the Update method, which would error.
				Config: config("https://orbs.example.com/b/"),
				Check: sdkresource.TestCheckResourceAttr(
					"circleci_url_orb_allow_list_entry.t", "prefix", "https://orbs.example.com/b/"),
			},
		},
	})
}

func TestAccURLOrbAllowListEntry_RejectsUnknownAuth(t *testing.T) {
	cfg := fmt.Sprintf(`
provider "circleci" {
  host = "https://circleci.example.com"
  key  = "fake"
}
resource "circleci_url_orb_allow_list_entry" "t" {
  organization = %q
  name         = "orbs"
  prefix       = "https://orbs.example.com/"
  auth         = "gitlab-oauth"
}
`, testOrbAllowListOrg)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)auth.*github-app`),
		}},
	})
}

// TestAccURLOrbAllowListEntry_RejectsUnusablePrefix pins the two prefix rules the
// API enforces, both of which used to reach it unchecked.
//
// The enforcing handler parses the prefix as a URL and requires the https scheme
// and a path ending in "/". A prefix without the trailing slash also matches
// sibling paths that merely start with the same characters, which is why the API
// refuses it — so silently forwarding one and failing at apply time was the worst
// of both outcomes.
func TestAccURLOrbAllowListEntry_RejectsUnusablePrefix(t *testing.T) {
	cases := map[string]struct {
		prefix string
		want   *regexp.Regexp
	}{
		"no trailing slash": {
			prefix: "https://orbs.example.com/team",
			want:   regexp.MustCompile(`(?s)path to end in`),
		},
		"not https": {
			prefix: "http://orbs.example.com/",
			want:   regexp.MustCompile(`(?s)only https prefixes`),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := fmt.Sprintf(`
provider "circleci" {
  host = "https://circleci.example.com"
  key  = "fake"
}
resource "circleci_url_orb_allow_list_entry" "t" {
  organization = %q
  name         = "orbs"
  prefix       = %q
  auth         = "none"
}
`, testOrbAllowListOrg, tc.prefix)

			sdkresource.UnitTest(t, sdkresource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []sdkresource.TestStep{{
					Config:      cfg,
					ExpectError: tc.want,
				}},
			})
		})
	}
}

// TestAccURLOrbAllowListEntry_AcceptsAPrefixWithAQueryString keeps the plan-time
// check from being stricter than the API. The rule is about the URL's *path*, so a
// prefix whose path ends in "/" is accepted even when the string does not.
func TestAccURLOrbAllowListEntry_AcceptsAPrefixWithAQueryString(t *testing.T) {
	srv, _ := newFakeOrbAllowListAPI(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
resource "circleci_url_orb_allow_list_entry" "t" {
  organization = %q
  name         = "orbs"
  prefix       = "https://orbs.example.com/team/?ref=main"
  auth         = "none"
}
`, srv.URL, testOrbAllowListOrg)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config: cfg,
			Check: sdkresource.TestCheckResourceAttr(
				"circleci_url_orb_allow_list_entry.t", "prefix",
				"https://orbs.example.com/team/?ref=main",
			),
		}},
	})
}

func TestAccURLOrbAllowListEntry_Import(t *testing.T) {
	srv, _ := newFakeOrbAllowListAPI(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
resource "circleci_url_orb_allow_list_entry" "t" {
  organization = %q
  name         = "orbs"
  prefix       = "https://orbs.example.com/"
  auth         = "none"
}
`, srv.URL, testOrbAllowListOrg)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{
			{Config: cfg},
			{
				ResourceName: "circleci_url_orb_allow_list_entry.t",
				ImportState:  true,
				// The organization is part of the import id because reading an
				// entry means listing its organization's allow list. The slug
				// itself contains a slash, so only the last one separates the id.
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					res, ok := state.RootModule().Resources["circleci_url_orb_allow_list_entry.t"]
					if !ok {
						return "", fmt.Errorf("resource not found in state")
					}

					return testOrbAllowListOrg + "/" + res.Primary.Attributes["id"], nil
				},
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccURLOrbAllowListEntry_RejectsMalformedImportID(t *testing.T) {
	srv, _ := newFakeOrbAllowListAPI(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
resource "circleci_url_orb_allow_list_entry" "t" {
  organization = %q
  name         = "orbs"
  prefix       = "https://orbs.example.com/"
  auth         = "none"
}
`, srv.URL, testOrbAllowListOrg)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{
			{Config: cfg},
			{
				ResourceName:  "circleci_url_orb_allow_list_entry.t",
				ImportState:   true,
				ImportStateId: "no-slash-here",
				ExpectError:   regexp.MustCompile(`(?s)Invalid Import ID Format`),
			},
		},
	})
}

func TestAccURLOrbAllowListDataSource_ListsEveryEntry(t *testing.T) {
	srv, api := newFakeOrbAllowListAPI(t)

	api.mu.Lock()
	api.entries = []map[string]any{
		{"id": "aaaa", "name": "first", "prefix": "https://orbs.example.com/a/", "auth": "none"},
		{"id": "bbbb", "name": "second", "prefix": "https://orbs.example.com/b/", "auth": "github-app"},
	}
	api.mu.Unlock()

	cfg := fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
data "circleci_url_orb_allow_list" "t" {
  organization = %q
}
`, srv.URL, testOrbAllowListOrg)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config: cfg,
			Check: sdkresource.ComposeAggregateTestCheckFunc(
				sdkresource.TestCheckResourceAttr("data.circleci_url_orb_allow_list.t", "entries.#", "2"),
				sdkresource.TestCheckResourceAttr("data.circleci_url_orb_allow_list.t", "entries.0.id", "aaaa"),
				sdkresource.TestCheckResourceAttr("data.circleci_url_orb_allow_list.t", "entries.0.name", "first"),
				sdkresource.TestCheckResourceAttr("data.circleci_url_orb_allow_list.t", "entries.1.auth", "github-app"),
				sdkresource.TestCheckResourceAttr("data.circleci_url_orb_allow_list.t", "entries.1.prefix", "https://orbs.example.com/b/"),
			),
		}},
	})

	want := "GET /api/v2/organization/" + testOrbAllowListOrg + "/url-orb-allow-list"
	for _, got := range api.recordedPaths() {
		if got != want {
			t.Errorf("request = %q, want %q: a data source must never write", got, want)
		}
	}
}

func TestAccURLOrbAllowListEntry_RemovedFromStateWhenGoneUpstream(t *testing.T) {
	srv, api := newFakeOrbAllowListAPI(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
resource "circleci_url_orb_allow_list_entry" "t" {
  organization = %q
  name         = "orbs"
  prefix       = "https://orbs.example.com/"
  auth         = "none"
}
`, srv.URL, testOrbAllowListOrg)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{
			{Config: cfg},
			{
				// Delete the entry behind Terraform's back. There is no read-one
				// route, so the refresh sees a listing that no longer contains the
				// id, which must be treated as "gone" and produce a plan to
				// recreate rather than an error.
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()

					api.entries = nil
				},
				Config:             cfg,
				ExpectNonEmptyPlan: true,
				PlanOnly:           true,
			},
		},
	})
}

// TestAccURLOrbAllowListEntry_KeepsStateWhenTheOrganizationAnswers404 is the
// counterpart to the test above, for the 404 that means something else entirely.
//
// An entry is read by listing the organization's allow list, and that handler
// throws not-found both when the organization cannot be resolved and when the
// token may not view it. circleci.IsNotFound says yes to that 404 just as it does
// to the sentinel for an absent entry, so treating the two alike would drop a live
// entry from state and add a duplicate on the next apply — against a limit of five
// per organization, and with a permission change as the trigger. Read has to report
// it and leave state where it is.
func TestAccURLOrbAllowListEntry_KeepsStateWhenTheOrganizationAnswers404(t *testing.T) {
	srv, api := newFakeOrbAllowListAPI(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
resource "circleci_url_orb_allow_list_entry" "t" {
  organization = %q
  name         = "orbs"
  prefix       = "https://orbs.example.com/"
  auth         = "none"
}
`, srv.URL, testOrbAllowListOrg)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{
			{Config: cfg},
			{
				PreConfig:    func() { api.setListStatus(http.StatusNotFound) },
				RefreshState: true,
				// Terraform wraps the detail, so match a short phrase rather than a
				// sentence. This is the branch that reports the 404 instead of
				// silently removing the resource.
				ExpectError: regexp.MustCompile(`(?s)404 for organization`),
			},
			// Undo the failure so the framework's own destroy step can run.
			{
				PreConfig: func() { api.setListStatus(0) },
				Config:    cfg,
			},
		},
	})

	// Exactly one create across the whole run. Had the 404 removed the resource from
	// state, the step that follows it would have created a second entry alongside
	// the first — which is the damage this branch exists to prevent. (The entry
	// count itself is not the assertion: the framework's own destroy step runs
	// last.)
	var creates int

	for _, path := range api.recordedPaths() {
		if strings.HasPrefix(path, http.MethodPost+" ") {
			creates++
		}
	}

	if creates != 1 {
		t.Errorf("%d create requests, want 1: a 404 on the organization must not cause a "+
			"recreate, got %v", creates, api.recordedPaths())
	}
}

// TestAccURLOrbAllowListEntry_DestroyFailsWhenTheOrganizationAnswers404 is the
// regression test for the issue where `terraform destroy` reported success
// without deleting anything.
//
// DeleteURLOrbAllowListEntry documents that the delete route answers 200
// whether or not the entry existed, so a 404 from that call is never "already
// gone" -- it means the organization itself could not be resolved or viewed, a
// renamed org or a token that lost access being the likely causes. Delete used
// to treat that 404 the same as "already gone" (circleci.IsNotFound), which
// made Terraform drop the resource from state while the entry stayed live
// upstream, permanently occupying one of the organization's five allow-list
// slots with nothing left tracking it -- and silently, since destroy reported
// success. Delete must instead report an error naming the organization and
// leave the resource in state, matching the distinction Read already draws for
// the same 404.
func TestAccURLOrbAllowListEntry_DestroyFailsWhenTheOrganizationAnswers404(t *testing.T) {
	srv, api := newFakeOrbAllowListAPI(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
resource "circleci_url_orb_allow_list_entry" "t" {
  organization = %q
  name         = "orbs"
  prefix       = "https://orbs.example.com/"
  auth         = "none"
}
`, srv.URL, testOrbAllowListOrg)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() { api.setDeleteStatus(http.StatusNotFound) },
				Config:    cfg,
				Destroy:   true,
				// Terraform wraps the diagnostic detail, so match a short phrase
				// naming the organization rather than a full sentence.
				ExpectError: regexp.MustCompile(`(?s)organization`),
			},
			{
				// The failed destroy above must not have dropped the resource from
				// state: undo the failure and confirm a plan against the same config
				// is empty, a refresh rather than a recreate. Had the bug survived,
				// this step would see a diff to create a duplicate entry.
				PreConfig: func() {
					if got := api.count(); got != 1 {
						t.Errorf("fake API holds %d entries after the failed destroy, want 1: "+
							"the failed delete must not have removed the live entry", got)
					}

					api.setDeleteStatus(0)
				},
				Config:   cfg,
				PlanOnly: true,
			},
		},
	})

	// The framework's own destroy step runs last, once deleteStatus is back to
	// zero, so by now the entry is gone for real. Exactly one delete request
	// should have succeeded: the one from that final step. The failed attempt
	// above must have reached the API (recorded) without removing anything.
	var deletes int

	for _, path := range api.recordedPaths() {
		if strings.HasPrefix(path, http.MethodDelete+" ") {
			deletes++
		}
	}

	if deletes != 2 {
		t.Errorf("%d delete requests, want 2 (the failed attempt plus the framework's "+
			"final destroy), got %v", deletes, api.recordedPaths())
	}

	if got := api.count(); got != 0 {
		t.Errorf("the fake API still holds %d entries after the test, want 0", got)
	}
}
