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

	"terraform-provider-circleci/internal/circleci"
)

const (
	// testPGOrgID is the organization the mock API serves project groups for.
	testPGOrgID = "11111111-2222-3333-4444-555555555555"
	// testPGProjectID is the project groups are granted access to.
	testPGProjectID = "66666666-7777-8888-9999-000000000000"

	testPGGroupA = "a1a1a1a1-0000-0000-0000-000000000001"
	testPGGroupB = "b2b2b2b2-0000-0000-0000-000000000002"
)

// projectGroupCall is one mutating call the provider made.
type projectGroupCall struct {
	action   string // "assign" or "update-role"
	role     string
	groupIDs []string
}

// mockProjectGroupAPI is an in-memory stand-in for the project group routes.
//
// It deliberately serves no delete: the real API has no route for revoking a
// grant, and the resource must not depend on one.
type mockProjectGroupAPI struct {
	t *testing.T

	mu sync.Mutex
	// grants maps project id to group id to role.
	grants map[string]map[string]string
	// names maps group id to its display name.
	names map[string]string
	// missingProject makes the project answer 404.
	missingProject bool
	// recorded is every mutating call, in order.
	recorded []projectGroupCall
}

// newMockProjectGroupAPI starts a mock project group API and returns it alongside
// its origin.
func newMockProjectGroupAPI(t *testing.T) (*mockProjectGroupAPI, string) {
	t.Helper()

	api := &mockProjectGroupAPI{
		t:      t,
		grants: map[string]map[string]string{},
		names: map[string]string{
			testPGGroupA: "platform",
			testPGGroupB: "security",
		},
	}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	return api, srv.URL
}

// revoke removes a grant behind Terraform's back, as the web UI can.
func (m *mockProjectGroupAPI) revoke(projectID, groupID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.grants[projectID], groupID)
}

// setRole changes a grant's role behind Terraform's back.
func (m *mockProjectGroupAPI) setRole(projectID, groupID, role string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.grants[projectID] == nil {
		m.grants[projectID] = map[string]string{}
	}
	m.grants[projectID][groupID] = role
}

// role returns the role a group holds, or "" when it has no grant.
func (m *mockProjectGroupAPI) role(projectID, groupID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.grants[projectID][groupID]
}

// calls returns the mutating calls recorded so far.
func (m *mockProjectGroupAPI) calls() []projectGroupCall {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.Clone(m.recorded)
}

// resetCalls clears the recorded calls.
func (m *mockProjectGroupAPI) resetCalls() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.recorded = nil
}

func (m *mockProjectGroupAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// /api/v2/organizations/{org_id}/projects/{project_id}/groups
	// /api/v2/organizations/{org_id}/projects/{project_id}/groups/{group_id}/update-role
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 7 || parts[0] != "api" || parts[1] != "v2" ||
		parts[2] != "organizations" || parts[4] != "projects" || parts[6] != "groups" {
		m.write(w, http.StatusNotFound, map[string]string{"message": "Not Found: " + r.URL.Path})

		return
	}

	projectID := parts[5]

	if m.missingProject {
		m.write(w, http.StatusNotFound, map[string]string{"message": "Project not found"})

		return
	}

	switch {
	case len(parts) == 7 && r.Method == http.MethodGet:
		m.list(w, projectID)
	case len(parts) == 7 && r.Method == http.MethodPost:
		m.assign(w, r, projectID)
	case len(parts) == 9 && parts[8] == "update-role" && r.Method == http.MethodPost:
		m.updateRole(w, r, projectID, parts[7])
	default:
		// Notably this covers DELETE: the real API has no revoke route, so the
		// resource must never attempt one.
		m.write(w, http.StatusMethodNotAllowed, map[string]string{"message": "Method Not Allowed"})
	}
}

func (m *mockProjectGroupAPI) list(w http.ResponseWriter, projectID string) {
	type item struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Role string `json:"role"`
	}

	// Sort by group id so the list order is stable across runs.
	groupIDs := make([]string, 0, len(m.grants[projectID]))
	for groupID := range m.grants[projectID] {
		groupIDs = append(groupIDs, groupID)
	}
	slices.Sort(groupIDs)

	items := make([]item, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		items = append(items, item{
			ID:   groupID,
			Name: m.names[groupID],
			Role: m.grants[projectID][groupID],
		})
	}

	// The real response carries only items: the handler builds it without a
	// next_page_token.
	m.write(w, http.StatusOK, struct {
		Items []item `json:"items"`
	}{Items: items})
}

func (m *mockProjectGroupAPI) assign(w http.ResponseWriter, r *http.Request, projectID string) {
	var body struct {
		Role     string   `json:"role"`
		GroupIDs []string `json:"group_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.write(w, http.StatusBadRequest, map[string]string{"message": "invalid body"})

		return
	}
	if len(body.GroupIDs) == 0 {
		m.write(w, http.StatusBadRequest, map[string]string{"message": "No valid group_ids provided"})

		return
	}
	if !slices.Contains(circleci.ProjectRoles(), body.Role) {
		m.write(w, http.StatusBadRequest, map[string]string{"message": "invalid role " + body.Role})

		return
	}

	m.recorded = append(m.recorded, projectGroupCall{
		action:   "assign",
		role:     body.Role,
		groupIDs: slices.Clone(body.GroupIDs),
	})

	if m.grants[projectID] == nil {
		m.grants[projectID] = map[string]string{}
	}
	for _, groupID := range body.GroupIDs {
		m.grants[projectID][groupID] = body.Role
	}

	m.write(w, http.StatusOK, map[string]string{"message": "Project groups updated."})
}

func (m *mockProjectGroupAPI) updateRole(w http.ResponseWriter, r *http.Request, projectID, groupID string) {
	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.write(w, http.StatusBadRequest, map[string]string{"message": "invalid body"})

		return
	}
	if !slices.Contains(circleci.ProjectRoles(), body.Role) {
		m.write(w, http.StatusBadRequest, map[string]string{"message": "invalid role " + body.Role})

		return
	}
	if _, ok := m.grants[projectID][groupID]; !ok {
		m.write(w, http.StatusNotFound, map[string]string{"message": "Group has no grant on project"})

		return
	}

	m.recorded = append(m.recorded, projectGroupCall{
		action:   "update-role",
		role:     body.Role,
		groupIDs: []string{groupID},
	})

	m.grants[projectID][groupID] = body.Role

	m.write(w, http.StatusOK, map[string]string{"message": "Project group role updated."})
}

func (m *mockProjectGroupAPI) write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		m.t.Errorf("encoding mock response: %v", err)
	}
}

// testAccProjectGroupConfig renders the resource. The deployment is always cloud:
// the project group routes are not reachable on CircleCI Server.
func testAccProjectGroupConfig(host, role string) string {
	return testAccMembershipProviderConfig(host, "cloud") + fmt.Sprintf(`
resource "circleci_project_group" "test" {
  organization_id = %[1]q
  project_id      = %[2]q
  group_id        = %[3]q
  role            = %[4]q
}
`, testPGOrgID, testPGProjectID, testPGGroupA, role)
}

func TestProjectGroupResourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwresource.SchemaResponse{}
	NewProjectGroupResource().Schema(ctx, fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// The three identifiers address the grant, so a change to any of them must
	// replace the resource. role must not, because /update-role can change it in
	// place.
	for _, name := range []string{"organization_id", "project_id", "group_id"} {
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

	role, ok := resp.Schema.Attributes["role"].(rschema.StringAttribute)
	if !ok {
		t.Fatal("schema is missing the \"role\" attribute")
	}
	for _, modifier := range role.PlanModifiers {
		if strings.Contains(strings.ToLower(modifier.Description(ctx)), "destroy and recreate") {
			t.Error("attribute \"role\" forces replacement, but /update-role can change it in place")
		}
	}
	if len(role.Validators) == 0 {
		t.Error("attribute \"role\" has no validators, but only three role values are accepted")
	}
}

func TestProjectGroupResourceImportStateRejectsMalformedID(t *testing.T) {
	t.Parallel()

	// All three ids are needed: the group and project are only meaningful within
	// their organization.
	for _, id := range []string{
		"",
		"only-a-group-id",
		"org/project",
		"org/project/",
		"/project/group",
		"org//group",
		"org/project/group/extra",
	} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			r, ok := NewProjectGroupResource().(*projectGroupResource)
			if !ok {
				t.Fatal("NewProjectGroupResource did not return a *projectGroupResource")
			}

			resp := &fwresource.ImportStateResponse{}
			r.ImportState(t.Context(), fwresource.ImportStateRequest{ID: id}, resp)

			if !resp.Diagnostics.HasError() {
				t.Errorf("import of %q produced no error, want one", id)
			}
		})
	}
}

func TestAccProjectGroupResource(t *testing.T) {
	api, host := newMockProjectGroupAPI(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create: one assign call carrying the role and the single group id.
			{
				Config: testAccProjectGroupConfig(host, circleci.ProjectRoleViewer),
				Check: func(*terraform.State) error {
					calls := api.calls()
					if len(calls) != 1 {
						t.Fatalf("got %d calls (%v), want 1", len(calls), calls)
					}
					if calls[0].action != "assign" || calls[0].role != circleci.ProjectRoleViewer {
						t.Errorf("call = %+v, want an assign with role %q", calls[0], circleci.ProjectRoleViewer)
					}
					if !slices.Equal(calls[0].groupIDs, []string{testPGGroupA}) {
						t.Errorf("call group_ids = %v, want [%s]", calls[0].groupIDs, testPGGroupA)
					}

					return nil
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project_group.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact(testPGOrgID+"/"+testPGProjectID+"/"+testPGGroupA),
					),
					statecheck.ExpectKnownValue(
						"circleci_project_group.test",
						tfjsonpath.New("role"),
						knownvalue.StringExact(circleci.ProjectRoleViewer),
					),
					// name is computed from the read-back, proving the grant was
					// re-read rather than assumed.
					statecheck.ExpectKnownValue(
						"circleci_project_group.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact("platform"),
					),
				},
			},
			// Import, using the "organization_id/project_id/group_id" form.
			{
				ResourceName:      "circleci_project_group.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					rs, ok := state.RootModule().Resources["circleci_project_group.test"]
					if !ok {
						return "", fmt.Errorf("circleci_project_group.test not found in state")
					}

					return rs.Primary.Attributes["organization_id"] + "/" +
						rs.Primary.Attributes["project_id"] + "/" +
						rs.Primary.Attributes["group_id"], nil
				},
			},
			// A role change is applied in place through /update-role, not by
			// replacing the grant.
			{
				PreConfig: api.resetCalls,
				Config:    testAccProjectGroupConfig(host, circleci.ProjectRoleAdmin),
				Check: func(*terraform.State) error {
					calls := api.calls()
					if len(calls) != 1 {
						t.Fatalf("got %d calls (%v), want 1", len(calls), calls)
					}
					if calls[0].action != "update-role" {
						t.Errorf("call action = %q, want \"update-role\"", calls[0].action)
					}
					if calls[0].role != circleci.ProjectRoleAdmin {
						t.Errorf("call role = %q, want %q", calls[0].role, circleci.ProjectRoleAdmin)
					}

					if got := api.role(testPGProjectID, testPGGroupA); got != circleci.ProjectRoleAdmin {
						t.Errorf("stored role = %q, want %q", got, circleci.ProjectRoleAdmin)
					}

					return nil
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project_group.test",
						tfjsonpath.New("role"),
						knownvalue.StringExact(circleci.ProjectRoleAdmin),
					),
				},
			},
		},
	})
}

func TestAccProjectGroupResource_rejectsOrgRole(t *testing.T) {
	_, host := newMockProjectGroupAPI(t)

	// The organization-level roles are not valid on a project grant. The
	// validator must reject one at plan time rather than letting the API 400.
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccProjectGroupConfig(host, "org-viewer"),
				ExpectError: regexp.MustCompile(`Attribute role value must be one of`),
			},
		},
	})
}

func TestAccProjectGroupResource_correctsRoleDrift(t *testing.T) {
	api, host := newMockProjectGroupAPI(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectGroupConfig(host, circleci.ProjectRoleViewer),
			},
			// A role changed in the web UI is drift.
			{
				PreConfig: func() {
					api.setRole(testPGProjectID, testPGGroupA, circleci.ProjectRoleAdmin)
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
			// The next apply puts it back.
			{
				Config: testAccProjectGroupConfig(host, circleci.ProjectRoleViewer),
				Check: func(*terraform.State) error {
					if got := api.role(testPGProjectID, testPGGroupA); got != circleci.ProjectRoleViewer {
						t.Errorf("stored role = %q, want %q", got, circleci.ProjectRoleViewer)
					}

					return nil
				},
			},
		},
	})
}

func TestAccProjectGroupResource_revokedGrantLeavesState(t *testing.T) {
	api, host := newMockProjectGroupAPI(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectGroupConfig(host, circleci.ProjectRoleViewer),
			},
			// A grant revoked in the web UI must be detected through the list,
			// since there is no route for reading a single grant.
			{
				PreConfig:          func() { api.revoke(testPGProjectID, testPGGroupA) },
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAccProjectGroupResource_destroyWarnsAndDoesNotRevoke(t *testing.T) {
	api, host := newMockProjectGroupAPI(t)

	// The API has no revoke route. Destroy must therefore succeed without
	// attempting one (a DELETE would 405 against the mock) and leave the grant in
	// place, which is what the resource documentation promises.
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectGroupConfig(host, circleci.ProjectRoleViewer),
			},
			{
				Config:  testAccMembershipProviderConfig(host, "cloud"),
				Destroy: false,
				Check: func(*terraform.State) error {
					// The grant survives the removal of the resource, since it can
					// only be revoked in the web UI.
					if got := api.role(testPGProjectID, testPGGroupA); got != circleci.ProjectRoleViewer {
						t.Errorf("stored role after destroy = %q, want the grant to survive as %q",
							got, circleci.ProjectRoleViewer)
					}

					return nil
				},
			},
		},
	})
}

func TestAccProjectGroupResource_importRejectsMalformedID(t *testing.T) {
	_, host := newMockProjectGroupAPI(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectGroupConfig(host, circleci.ProjectRoleViewer),
			},
			{
				ResourceName:  "circleci_project_group.test",
				ImportState:   true,
				ImportStateId: "missing-the-project",
				ExpectError:   regexp.MustCompile(`Invalid import ID for circleci_project_group`),
			},
		},
	})
}
