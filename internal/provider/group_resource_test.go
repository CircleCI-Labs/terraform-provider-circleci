// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
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

// The groups API is served by v2 on both Cloud and Server, so these tests run
// against an in-process stand-in for it rather than a real installation. That
// keeps the create/read/delete and pagination behaviour under test without
// needing a provisioned organization.

// testGroupOrgID is the organization the mock API serves groups for.
const testGroupOrgID = "00000000-1111-2222-3333-444444444444"

// mockGroupAPI is an in-memory stand-in for /api/v2/organizations/{id}/groups.
type mockGroupAPI struct {
	t *testing.T

	mu     sync.Mutex
	groups map[string][]circleci.Group // organization id -> groups, in insertion order
	nextID int

	// pageSize splits list responses into pages when positive, so that the
	// client's pagination handling is exercised.
	pageSize int
}

// newMockGroupAPI starts a mock groups API and returns it alongside its origin.
func newMockGroupAPI(t *testing.T) (*mockGroupAPI, string) {
	t.Helper()

	api := &mockGroupAPI{t: t, groups: map[string][]circleci.Group{}}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	return api, srv.URL
}

// seed adds a group directly, for tests that read groups they did not create.
func (m *mockGroupAPI) seed(orgID, name, description string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.add(orgID, name, description).ID
}

// removeAll deletes every group in an organization behind Terraform's back, to
// test drift handling.
func (m *mockGroupAPI) removeAll(orgID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.groups, orgID)
}

// add records a new group. The caller must hold the lock.
func (m *mockGroupAPI) add(orgID, name, description string) circleci.Group {
	m.nextID++
	group := circleci.Group{
		ID:          fmt.Sprintf("00000000-0000-0000-0000-%012d", m.nextID),
		Name:        name,
		Description: description,
	}
	m.groups[orgID] = append(m.groups[orgID], group)

	return group
}

// drop removes a group and reports whether it existed. The caller must hold the
// lock.
func (m *mockGroupAPI) drop(orgID, groupID string) bool {
	for i, group := range m.groups[orgID] {
		if group.ID == groupID {
			m.groups[orgID] = append(m.groups[orgID][:i], m.groups[orgID][i+1:]...)

			return true
		}
	}

	return false
}

func (m *mockGroupAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// /api/v2/organizations/{org_id}/groups[/{group_id}]
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 5 || parts[0] != "api" || parts[1] != "v2" || parts[2] != "organizations" || parts[4] != "groups" {
		m.write(w, http.StatusNotFound, map[string]string{"message": "Not Found: " + r.URL.Path})

		return
	}

	orgID := parts[3]

	var groupID string
	if len(parts) > 5 {
		groupID = parts[5]
	}

	switch {
	case groupID == "" && r.Method == http.MethodPost:
		m.create(w, r, orgID)
	case groupID == "" && r.Method == http.MethodGet:
		m.list(w, r, orgID)
	case groupID != "" && r.Method == http.MethodGet:
		m.get(w, orgID, groupID)
	case groupID != "" && r.Method == http.MethodDelete:
		m.delete(w, orgID, groupID)
	default:
		m.write(w, http.StatusMethodNotAllowed, map[string]string{"message": "Method Not Allowed"})
	}
}

func (m *mockGroupAPI) create(w http.ResponseWriter, r *http.Request, orgID string) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.write(w, http.StatusBadRequest, map[string]string{"message": "invalid body"})

		return
	}
	if body.Name == "" {
		m.write(w, http.StatusBadRequest, map[string]string{"message": "name is required"})

		return
	}

	m.write(w, http.StatusCreated, m.add(orgID, body.Name, body.Description))
}

func (m *mockGroupAPI) list(w http.ResponseWriter, r *http.Request, orgID string) {
	groups := m.groups[orgID]

	start := 0
	if token := r.URL.Query().Get("page-token"); token != "" {
		parsed, err := strconv.Atoi(token)
		if err != nil {
			m.write(w, http.StatusBadRequest, map[string]string{"message": "invalid page-token"})

			return
		}
		start = parsed
	}
	if start > len(groups) {
		start = len(groups)
	}

	end := len(groups)
	if m.pageSize > 0 && start+m.pageSize < end {
		end = start + m.pageSize
	}

	page := struct {
		Items         []circleci.Group `json:"items"`
		NextPageToken *string          `json:"next_page_token"`
	}{Items: groups[start:end]}

	if end < len(groups) {
		token := strconv.Itoa(end)
		page.NextPageToken = &token
	}

	m.write(w, http.StatusOK, page)
}

func (m *mockGroupAPI) get(w http.ResponseWriter, orgID, groupID string) {
	for _, group := range m.groups[orgID] {
		if group.ID == groupID {
			m.write(w, http.StatusOK, group)

			return
		}
	}

	m.write(w, http.StatusForbidden, map[string]string{"message": "Permission denied."})
}

func (m *mockGroupAPI) delete(w http.ResponseWriter, orgID, groupID string) {
	if !m.drop(orgID, groupID) {
		m.write(w, http.StatusForbidden, map[string]string{"message": "Permission denied."})

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (m *mockGroupAPI) write(w http.ResponseWriter, status int, body any) {
	m.t.Helper()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		m.t.Errorf("encoding mock response: %v", err)
	}
}

// testAccGroupProviderConfig points the provider at the mock API. deployment is
// a parameter because groups must work identically on cloud and server.
func testAccGroupProviderConfig(host, deployment string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host       = %[1]q
  key        = "fake-token"
  deployment = %[2]q
}
`, host, deployment)
}

func testAccGroupResourceConfig(host, deployment, name, description string) string {
	return testAccGroupProviderConfig(host, deployment) + fmt.Sprintf(`
resource "circleci_group" "test" {
  organization_id = %[1]q
  name            = %[2]q
  description     = %[3]q
}
`, testGroupOrgID, name, description)
}

func testAccGroupResourceConfigNoDescription(host, deployment, name string) string {
	return testAccGroupProviderConfig(host, deployment) + fmt.Sprintf(`
resource "circleci_group" "test" {
  organization_id = %[1]q
  name            = %[2]q
}
`, testGroupOrgID, name)
}

func TestGroupResourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwresource.SchemaResponse{}
	NewGroupResource().Schema(ctx, fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// The API has no update endpoint, so every writable attribute must force
	// replacement rather than silently doing nothing on change.
	for _, name := range []string{"organization_id", "name", "description"} {
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
			t.Errorf("attribute %q has no plan modifier forcing replacement, but the API cannot update groups", name)
		}
	}
}

func TestGroupResourceImportStateRejectsMalformedID(t *testing.T) {
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

			r, ok := NewGroupResource().(*groupResource)
			if !ok {
				t.Fatal("NewGroupResource did not return a *groupResource")
			}

			resp := &fwresource.ImportStateResponse{}
			r.ImportState(t.Context(), fwresource.ImportStateRequest{ID: id}, resp)

			if !resp.Diagnostics.HasError() {
				t.Errorf("import of %q produced no error, want one", id)
			}
		})
	}
}

func TestAccGroupResource(t *testing.T) {
	api, host := newMockGroupAPI(t)

	idRegex := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read.
			{
				Config: testAccGroupResourceConfig(host, "cloud", "platform", "Platform team"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_group.test",
						tfjsonpath.New("id"),
						knownvalue.StringRegexp(idRegex),
					),
					statecheck.ExpectKnownValue(
						"circleci_group.test",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(testGroupOrgID),
					),
					statecheck.ExpectKnownValue(
						"circleci_group.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact("platform"),
					),
					statecheck.ExpectKnownValue(
						"circleci_group.test",
						tfjsonpath.New("description"),
						knownvalue.StringExact("Platform team"),
					),
				},
			},
			// Import, using the "organization_id/group_id" form.
			{
				ResourceName:      "circleci_group.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					rs, ok := state.RootModule().Resources["circleci_group.test"]
					if !ok {
						return "", fmt.Errorf("circleci_group.test not found in state")
					}

					return rs.Primary.Attributes["organization_id"] + "/" + rs.Primary.ID, nil
				},
			},
			// A changed name replaces the group, since there is no update
			// endpoint. The new group gets a new id.
			{
				Config: testAccGroupResourceConfig(host, "cloud", "platform-renamed", "Platform team"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_group.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact("platform-renamed"),
					),
				},
			},
			// A group deleted outside Terraform cannot be silently dropped from
			// state. The API answers 403 "Permission denied." for a group that does
			// not exist, and gives the same answer for a group in another
			// organization and for a token without access — the conflation is
			// deliberate, to avoid confirming whether an id exists.
			//
			// Treating 403 as "gone" would mean a token that loses permission makes
			// Terraform recreate live groups, so refresh reports an error naming all
			// three possibilities and how to proceed.
			{
				PreConfig:    func() { api.removeAll(testGroupOrgID) },
				RefreshState: true,
				ExpectError:  regexp.MustCompile(`(?s)Unable to read CircleCI group`),
			},
		},
	})
}

func TestAccGroupResource_serverDeployment(t *testing.T) {
	_, host := newMockGroupAPI(t)

	// Groups need a `circleci` type (standalone) organization. A CircleCI Server
	// installation is always a `github` type organization, so deployment =
	// "server" must be rejected with an explanatory error rather than attempting
	// a request the API would refuse.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccGroupResourceConfig(host, "server", "server-group", "On Server"),
			ExpectError: regexp.MustCompile(`circleci_group requires a standalone CircleCI organization`),
		}},
	})
}

func TestAccGroupResource_withoutDescription(t *testing.T) {
	_, host := newMockGroupAPI(t)

	// description is optional and computed: with none configured, the value the
	// server returns is recorded instead of leaving the attribute unknown.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupResourceConfigNoDescription(host, "cloud", "no-description"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_group.test",
						tfjsonpath.New("description"),
						knownvalue.StringExact(""),
					),
				},
			},
		},
	})
}

func TestAccGroupResource_importRejectsMalformedID(t *testing.T) {
	_, host := newMockGroupAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupResourceConfig(host, "cloud", "platform", "Platform team"),
			},
			{
				ResourceName:  "circleci_group.test",
				ImportState:   true,
				ImportStateId: "missing-the-organization",
				ExpectError:   regexp.MustCompile(`Invalid import ID for circleci_group`),
			},
		},
	})
}
