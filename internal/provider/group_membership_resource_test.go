// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// The membership routes hang off the group routes, which CircleCI Server exposes
// to the public API service, so these tests run against an in-process stand-in
// rather than a real installation.

const (
	// testMembershipOrgID is the organization the mock API serves groups for.
	testMembershipOrgID = "00000000-1111-2222-3333-444444444444"
	// testMembershipGroupID is the group whose membership is managed.
	testMembershipGroupID = "55555555-6666-7777-8888-999999999999"

	testUserA = "aaaaaaaa-0000-0000-0000-000000000001"
	testUserB = "bbbbbbbb-0000-0000-0000-000000000002"
	testUserC = "cccccccc-0000-0000-0000-000000000003"
	testUserD = "dddddddd-0000-0000-0000-000000000004"
)

// membershipCall is one mutating call the provider made, so that tests can assert
// on the add/remove delta rather than only on the end state.
type membershipCall struct {
	action  string // "add" or "remove"
	userIDs []string
}

// mockMembershipAPI is an in-memory stand-in for the group membership routes.
type mockMembershipAPI struct {
	t *testing.T

	mu sync.Mutex
	// members maps group id to the user ids in it, in insertion order.
	members map[string][]string
	// missing marks groups that answer 404, for drift tests.
	missing map[string]bool
	// recorded is every mutating call, in order.
	recorded []membershipCall
}

// newMockMembershipAPI starts a mock membership API and returns it alongside its
// origin.
func newMockMembershipAPI(t *testing.T) (*mockMembershipAPI, string) {
	t.Helper()

	api := &mockMembershipAPI{
		t:       t,
		members: map[string][]string{},
		missing: map[string]bool{},
	}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	return api, srv.URL
}

// seedMembers puts users into the managed group behind Terraform's back.
func (m *mockMembershipAPI) seedMembers(userIDs ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.members[testMembershipGroupID] = append(m.members[testMembershipGroupID], userIDs...)
}

// removeGroup makes the managed group answer 404, as if it had been deleted
// outside Terraform.
func (m *mockMembershipAPI) removeGroup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.missing[testMembershipGroupID] = true
}

// currentMembers returns the managed group's membership, sorted for comparison.
func (m *mockMembershipAPI) currentMembers() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	members := slices.Clone(m.members[testMembershipGroupID])
	slices.Sort(members)

	return members
}

// calls returns the mutating calls recorded so far.
func (m *mockMembershipAPI) calls() []membershipCall {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.Clone(m.recorded)
}

// resetCalls clears the recorded calls, so a later step can assert on only the
// calls that step made.
func (m *mockMembershipAPI) resetCalls() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.recorded = nil
}

func (m *mockMembershipAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// /api/v2/organizations/{org_id}/groups/{group_id}/{users|remove_users}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 7 || parts[0] != "api" || parts[1] != "v2" ||
		parts[2] != "organizations" || parts[4] != "groups" {
		m.write(w, http.StatusNotFound, map[string]string{"message": "Not Found: " + r.URL.Path})

		return
	}

	groupID, action := parts[5], parts[6]

	if m.missing[groupID] {
		m.write(w, http.StatusNotFound, map[string]string{"message": "Group not found"})

		return
	}

	switch {
	case action == "users" && r.Method == http.MethodGet:
		m.list(w, groupID)
	case action == "users" && r.Method == http.MethodPost:
		m.mutate(w, r, groupID, "add")
	case action == "remove_users" && r.Method == http.MethodPost:
		m.mutate(w, r, groupID, "remove")
	default:
		m.write(w, http.StatusMethodNotAllowed, map[string]string{"message": "Method Not Allowed"})
	}
}

func (m *mockMembershipAPI) list(w http.ResponseWriter, groupID string) {
	type member struct {
		UserID    string `json:"user_id"`
		Username  string `json:"username"`
		AvatarURL string `json:"avatar_url"`
		Email     string `json:"email"`
		GroupID   string `json:"group_id"`
	}

	items := make([]member, 0, len(m.members[groupID]))
	for _, id := range m.members[groupID] {
		items = append(items, member{
			UserID:    id,
			Username:  "user-" + id[:4],
			AvatarURL: "https://avatars.example/" + id[:4] + ".png",
			Email:     id[:4] + "@example.com",
			GroupID:   groupID,
		})
	}

	m.write(w, http.StatusOK, struct {
		Items         []member `json:"items"`
		NextPageToken *string  `json:"next_page_token"`
	}{Items: items})
}

func (m *mockMembershipAPI) mutate(w http.ResponseWriter, r *http.Request, groupID, action string) {
	var body struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.write(w, http.StatusBadRequest, map[string]string{"message": "invalid body"})

		return
	}
	// The real API rejects an empty list, so the provider must never send one.
	if len(body.UserIDs) == 0 {
		m.write(w, http.StatusBadRequest, map[string]string{"message": "No valid user_ids provided"})

		return
	}

	m.recorded = append(m.recorded, membershipCall{action: action, userIDs: slices.Clone(body.UserIDs)})

	for _, id := range body.UserIDs {
		switch action {
		case "add":
			if !slices.Contains(m.members[groupID], id) {
				m.members[groupID] = append(m.members[groupID], id)
			}
		case "remove":
			if i := slices.Index(m.members[groupID], id); i >= 0 {
				m.members[groupID] = slices.Delete(m.members[groupID], i, i+1)
			}
		}
	}

	m.write(w, http.StatusOK, map[string]string{"message": action + "ed user(s)"})
}

func (m *mockMembershipAPI) write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		m.t.Errorf("encoding mock response: %v", err)
	}
}

// assertCall checks that exactly one call with the given action was made, with
// want as its user ids in any order. Set ordering is not guaranteed, so the
// comparison is order-insensitive.
func assertCall(t *testing.T, calls []membershipCall, action string, want []string) {
	t.Helper()

	var matches []membershipCall
	for _, call := range calls {
		if call.action == action {
			matches = append(matches, call)
		}
	}

	if len(want) == 0 {
		if len(matches) != 0 {
			t.Errorf("got %d %q calls (%v), want none", len(matches), action, matches)
		}

		return
	}

	if len(matches) != 1 {
		t.Fatalf("got %d %q calls (%v), want exactly 1", len(matches), action, matches)
	}

	got := slices.Clone(matches[0].userIDs)
	slices.Sort(got)
	wantSorted := slices.Clone(want)
	slices.Sort(wantSorted)

	if !slices.Equal(got, wantSorted) {
		t.Errorf("%q call user_ids = %v, want %v", action, got, wantSorted)
	}
}

func testAccMembershipProviderConfig(host, deployment string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host       = %[1]q
  key        = "fake-token"
  deployment = %[2]q
}
`, host, deployment)
}

// testAccGroupMembershipConfig renders the resource with the given member ids.
func testAccGroupMembershipConfig(host, deployment string, userIDs ...string) string {
	quoted := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		quoted = append(quoted, fmt.Sprintf("%q", id))
	}

	return testAccMembershipProviderConfig(host, deployment) + fmt.Sprintf(`
resource "circleci_group_membership" "test" {
  organization_id = %[1]q
  group_id        = %[2]q
  user_ids        = [%[3]s]
}
`, testMembershipOrgID, testMembershipGroupID, strings.Join(quoted, ", "))
}

func TestGroupMembershipResourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwresource.SchemaResponse{}
	NewGroupMembershipResource().Schema(ctx, fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// The two identifiers address the membership, so a change to either must
	// replace the resource rather than silently re-point it at another group.
	for _, name := range []string{"organization_id", "group_id"} {
		attribute, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("schema is missing the %q attribute", name)
		}

		stringAttribute, ok := attribute.(rschema.StringAttribute)
		if !ok {
			t.Fatalf("attribute %q is %T, want rschema.StringAttribute", name, attribute)
		}

		var forcesReplacement bool
		for _, modifier := range stringAttribute.PlanModifiers {
			if strings.Contains(strings.ToLower(modifier.Description(ctx)), "destroy and recreate") {
				forcesReplacement = true
			}
		}
		if !forcesReplacement {
			t.Errorf("attribute %q has no plan modifier forcing replacement", name)
		}
	}

	// user_ids must be required: the resource owns the complete membership, and an
	// optional list would make "no members" indistinguishable from "unmanaged".
	userIDs, ok := resp.Schema.Attributes["user_ids"]
	if !ok {
		t.Fatal("schema is missing the \"user_ids\" attribute")
	}
	if !userIDs.IsRequired() {
		t.Error("attribute \"user_ids\" is not required, but the resource owns the full member list")
	}
}

func TestGroupMembershipResourceImportStateRejectsMalformedID(t *testing.T) {
	t.Parallel()

	// The import ID must carry both ids, because a group id is only meaningful
	// within its organization.
	for _, id := range []string{
		"",
		"only-a-group-id",
		"/group-id",
		"organization-id/",
	} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			r, ok := NewGroupMembershipResource().(*groupMembershipResource)
			if !ok {
				t.Fatal("NewGroupMembershipResource did not return a *groupMembershipResource")
			}

			resp := &fwresource.ImportStateResponse{}
			r.ImportState(t.Context(), fwresource.ImportStateRequest{ID: id}, resp)

			if !resp.Diagnostics.HasError() {
				t.Errorf("import of %q produced no error, want one", id)
			}
		})
	}
}

func TestAccGroupMembershipResource(t *testing.T) {
	api, host := newMockMembershipAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create: an empty group gets exactly one add call.
			{
				Config: testAccGroupMembershipConfig(host, "cloud", testUserA, testUserB),
				Check: func(*terraform.State) error {
					assertCall(t, api.calls(), "add", []string{testUserA, testUserB})
					assertCall(t, api.calls(), "remove", nil)

					if got := api.currentMembers(); !slices.Equal(got, []string{testUserA, testUserB}) {
						t.Errorf("members = %v, want [%s %s]", got, testUserA, testUserB)
					}

					return nil
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_group_membership.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact(testMembershipOrgID+"/"+testMembershipGroupID),
					),
					statecheck.ExpectKnownValue(
						"circleci_group_membership.test",
						tfjsonpath.New("user_ids"),
						knownvalue.SetExact([]knownvalue.Check{
							knownvalue.StringExact(testUserA),
							knownvalue.StringExact(testUserB),
						}),
					),
				},
			},
			// Import, using the "organization_id/group_id" form.
			{
				ResourceName:      "circleci_group_membership.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					rs, ok := state.RootModule().Resources["circleci_group_membership.test"]
					if !ok {
						return "", fmt.Errorf("circleci_group_membership.test not found in state")
					}

					return rs.Primary.Attributes["organization_id"] + "/" + rs.Primary.Attributes["group_id"], nil
				},
			},
			// The delta: {a,b} -> {a,c,d} must remove only b and add only c and d.
			// Re-adding a, or removing and re-adding it, would be wrong.
			{
				PreConfig: api.resetCalls,
				Config:    testAccGroupMembershipConfig(host, "cloud", testUserA, testUserC, testUserD),
				Check: func(*terraform.State) error {
					calls := api.calls()
					assertCall(t, calls, "remove", []string{testUserB})
					assertCall(t, calls, "add", []string{testUserC, testUserD})

					want := []string{testUserA, testUserC, testUserD}
					slices.Sort(want)
					if got := api.currentMembers(); !slices.Equal(got, want) {
						t.Errorf("members = %v, want %v", got, want)
					}

					return nil
				},
			},
			// A no-op change must make no calls at all.
			{
				PreConfig: api.resetCalls,
				Config:    testAccGroupMembershipConfig(host, "cloud", testUserD, testUserA, testUserC),
				Check: func(*testing.T) func(*terraform.State) error {
					return func(*terraform.State) error {
						if calls := api.calls(); len(calls) != 0 {
							t.Errorf("reordering the same members made %d calls (%v), want none", len(calls), calls)
						}

						return nil
					}
				}(t),
			},
		},
	})
}

func TestAccGroupMembershipResource_emptySetEmptiesTheGroup(t *testing.T) {
	api, host := newMockMembershipAPI(t)
	api.seedMembers(testUserA, testUserB)

	// An empty set is a valid desired state: it means "this group has no members".
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupMembershipConfig(host, "cloud"),
				Check: func(*terraform.State) error {
					assertCall(t, api.calls(), "remove", []string{testUserA, testUserB})
					// Nothing to add, so no add call may be made: the real API
					// rejects an empty user_ids array.
					assertCall(t, api.calls(), "add", nil)

					if got := api.currentMembers(); len(got) != 0 {
						t.Errorf("members = %v, want none", got)
					}

					return nil
				},
			},
		},
	})
}

func TestAccGroupMembershipResource_takesOverExistingMembers(t *testing.T) {
	api, host := newMockMembershipAPI(t)
	// The group already has members that the configuration does not list. The
	// resource claims exclusive ownership, so they must be removed on create
	// rather than merged with.
	api.seedMembers(testUserA, testUserB)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupMembershipConfig(host, "cloud", testUserA, testUserC),
				Check: func(*terraform.State) error {
					assertCall(t, api.calls(), "remove", []string{testUserB})
					assertCall(t, api.calls(), "add", []string{testUserC})

					want := []string{testUserA, testUserC}
					slices.Sort(want)
					if got := api.currentMembers(); !slices.Equal(got, want) {
						t.Errorf("members = %v, want %v", got, want)
					}

					return nil
				},
			},
		},
	})
}

func TestAccGroupMembershipResource_correctsDrift(t *testing.T) {
	api, host := newMockMembershipAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupMembershipConfig(host, "cloud", testUserA),
			},
			// A member added in the web UI shows up as drift, because this
			// resource owns the whole list.
			{
				PreConfig:          func() { api.seedMembers(testUserB) },
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
			// The next apply removes it again.
			{
				PreConfig: api.resetCalls,
				Config:    testAccGroupMembershipConfig(host, "cloud", testUserA),
				Check: func(*terraform.State) error {
					assertCall(t, api.calls(), "remove", []string{testUserB})

					if got := api.currentMembers(); !slices.Equal(got, []string{testUserA}) {
						t.Errorf("members = %v, want [%s]", got, testUserA)
					}

					return nil
				},
			},
		},
	})
}

func TestAccGroupMembershipResource_deletedGroupLeavesState(t *testing.T) {
	api, host := newMockMembershipAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupMembershipConfig(host, "cloud", testUserA),
			},
			// A group deleted outside Terraform takes its membership with it, so
			// the refresh must drop the resource rather than fail.
			{
				PreConfig:          api.removeGroup,
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAccGroupMembershipResource_serverDeployment(t *testing.T) {
	_, host := newMockMembershipAPI(t)

	// Groups need a `circleci` type (standalone) organization. A CircleCI Server
	// installation is always a `github` type organization, so deployment =
	// "server" must be rejected with an explanatory error rather than attempting
	// a request the API would refuse.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccGroupMembershipConfig(host, "server", testUserA),
			ExpectError: regexp.MustCompile(`circleci_group_membership requires a standalone CircleCI organization`),
		}},
	})
}

func TestAccGroupMembershipResource_importRejectsMalformedID(t *testing.T) {
	_, host := newMockMembershipAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupMembershipConfig(host, "cloud", testUserA),
			},
			{
				ResourceName:  "circleci_group_membership.test",
				ImportState:   true,
				ImportStateId: "missing-the-organization",
				ExpectError:   regexp.MustCompile(`Invalid import ID for circleci_group_membership`),
			},
		},
	})
}
