// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccRunnerResourceClassResource(t *testing.T) {
	organizationId := testOrgID(t)
	resourceClass := fmt.Sprintf("%s/acc-test-runner", testRunnerNamespace(t))
	description := "Acceptance test runner resource class"
	uuidRegex := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccRunnerResourceClassConfig(organizationId, resourceClass, description, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_runner_resource_class.test",
						tfjsonpath.New("resource_class"),
						knownvalue.StringExact(resourceClass),
					),
					statecheck.ExpectKnownValue(
						"circleci_runner_resource_class.test",
						tfjsonpath.New("description"),
						knownvalue.StringExact(description),
					),
					statecheck.ExpectKnownValue(
						"circleci_runner_resource_class.test",
						tfjsonpath.New("id"),
						knownvalue.StringRegexp(uuidRegex),
					),
				},
			},
			// ImportState testing
			{
				ResourceName:      "circleci_runner_resource_class.test",
				ImportState:       true,
				ImportStateVerify: true,
				// force_delete is not stored by the API, and neither organization
				// attribute name is importable: the import ID is the resource class
				// string alone, and the API reports no organization on a resource
				// class to recover one from.
				ImportStateVerifyIgnore: []string{"force_delete", "organization_id", "org_id"},
				ImportStateId:           resourceClass,
			},
			// Update after import testing
			{
				Config: testAccRunnerResourceClassConfig(organizationId, resourceClass, description, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_runner_resource_class.test",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(organizationId),
					),
				},
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

func TestAccRunnerResourceClassForceDelete(t *testing.T) {
	organizationId := testOrgID(t)
	resourceClass := fmt.Sprintf("%s/acc-test-runner-force", testRunnerNamespace(t))
	description := "Acceptance test runner resource class with force delete"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create with force_delete = true
			{
				Config: testAccRunnerResourceClassConfig(organizationId, resourceClass, description, true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_runner_resource_class.test",
						tfjsonpath.New("resource_class"),
						knownvalue.StringExact(resourceClass),
					),
					statecheck.ExpectKnownValue(
						"circleci_runner_resource_class.test",
						tfjsonpath.New("description"),
						knownvalue.StringExact(description),
					),
					statecheck.ExpectKnownValue(
						"circleci_runner_resource_class.test",
						tfjsonpath.New("force_delete"),
						knownvalue.Bool(true),
					),
				},
			},
			// Delete testing automatically occurs in TestCase (exercises force-delete path)
		},
	})
}

// TestRunnerResourceClassImport is the fake-backed counterpart to
// TestAccRunnerResourceClassResource's import step, and the reason a
// fake-backed one is worth having even though that acceptance test already
// covers the same ground correctly: TestAccRunnerResourceClassResource needs
// TF_ACC and real credentials, so it never runs for a developer running
// `go test ./...` (see TestCredentialFreeTestsUseUnitTest), while this does.
//
// It also makes explicit, with a plan check rather than a hand-read of the
// code, something the resource documentation gets wrong today: "organization_id
// is resolved from the resource class during the subsequent read" is not
// true. ResourceClass (see internal/circleci/runner.go) carries no organization
// field at all, so Read cannot recover it -- neither organization_id nor org_id
// is set after import, and the first plan against a configuration naming an
// organization is a genuine Update, not a no-op.
func TestRunnerResourceClassImport(t *testing.T) {
	api := newRunnerFakeAPI(t)
	api.respond("GET", "/api/v3/runner/resource", `{"items":[
	  {"id": "11111111-2222-3333-4444-555555555555", "resource_class": "acc-ns/linux", "description": "linux runners"}
	]}`)

	const orgID = "00000000-1111-2222-3333-444444444444"

	config := runnerProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_runner_resource_class" "test" {
  organization_id = %q
  resource_class  = "acc-ns/linux"
  description     = "linux runners"
}
`, orgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "circleci_runner_resource_class.test",
				ImportState:        true,
				ImportStateId:      "acc-ns/linux",
				ImportStatePersist: true,
				Config:             config,
			},
			{
				// organization_id has no RequiresReplace on this resource (see the
				// Schema method's comment on why), so filling it in from
				// configuration is an Update -- never a Replace -- and Update
				// persists the plan verbatim with no API call. Still not a Noop: the
				// value is genuinely absent from the imported state.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_runner_resource_class.test", plancheck.ResourceActionUpdate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_runner_resource_class.test",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(orgID),
					),
				},
			},
			// A third, identical plan is checked automatically as part of the
			// second step's apply (terraform-plugin-testing fails a step whose
			// post-apply plan is non-empty), proving organization_id having
			// settled really does make this Noop from here on.
		},
	})
}

func testAccRunnerResourceClassConfig(organizationId, resourceClass, description string, forceDelete bool) string {
	return fmt.Sprintf(`
resource "circleci_runner_resource_class" "test" {
  organization_id = %[1]q
  resource_class  = %[2]q
  description     = %[3]q
  force_delete    = %[4]t
}
`, organizationId, resourceClass, description, forceDelete)
}

// statefulRunnerResourceClassAPI is a stateful stand-in for the runner admin
// API's resource-class routes, unlike runnerFakeAPI (runner_fake_test.go),
// which answers every request for a given method and path with the same
// fixed body regardless of what was sent. That is fine for the tests
// runnerFakeAPI already backs, but proving a replacement here needs the
// second create's response to actually carry the *new* description: with a
// fixed response, the second create would echo the first create's
// description back, and Terraform would reject that as "Provider produced
// inconsistent result after apply" for the wrong reason (a broken fake, not
// the defect under test).
//
// Confirmed against internal/circleci/runner.go: the create body has
// resource_class and description (org_id is sent but the real handler
// ignores it — see ResourceClassInput's doc comment, reproduced here by not
// reading it either), and the response mirrors ResourceClass — id,
// resource_class, description.
type statefulRunnerResourceClassAPI struct {
	server *httptest.Server

	mu      sync.Mutex
	records map[string]map[string]any
	nextID  int
	creates []map[string]any
	deletes []string
}

func newStatefulRunnerResourceClassAPI(t *testing.T) *statefulRunnerResourceClassAPI {
	t.Helper()

	api := &statefulRunnerResourceClassAPI{records: map[string]map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/runner/resource", api.create)
	mux.HandleFunc("GET /api/v3/runner/resource", api.list)
	mux.HandleFunc("DELETE /api/v3/runner/resource/{id}", api.delete)

	api.server = httptest.NewServer(mux)
	t.Cleanup(api.server.Close)

	return api
}

func (a *statefulRunnerResourceClassAPI) URL() string { return a.server.URL }

func (a *statefulRunnerResourceClassAPI) create(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)

	a.mu.Lock()
	a.nextID++
	id := fmt.Sprintf("rc-%d", a.nextID)
	record := map[string]any{
		"id":             id,
		"resource_class": body["resource_class"],
		"description":    body["description"],
	}
	a.records[id] = record
	a.creates = append(a.creates, record)
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(record)
}

func (a *statefulRunnerResourceClassAPI) list(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	items := make([]map[string]any, 0, len(a.records))
	for _, record := range a.records {
		items = append(items, record)
	}
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
}

func (a *statefulRunnerResourceClassAPI) delete(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	id := r.PathValue("id")
	delete(a.records, id)
	a.deletes = append(a.deletes, id)
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{}`)
}

// TestRunnerResourceClassResourceUnit_DescriptionChangeForcesReplacement is a
// regression test for a defect this review found: description had no
// RequiresReplace, but CreateResourceClass and DeleteResourceClass
// (internal/circleci/runner.go) are the only two write routes for a resource
// class — there is no update route at all. An edited description therefore
// used to plan an in-place Update, whose implementation (see the Schema
// method's Update, before the fix) had no API call to make and simply
// persisted the plan into state, reporting apply success while the resource
// class on the server kept its original description. This asserts the fixed
// behaviour: changing description plans a replacement, and the replacement
// really does carry the new description to the server rather than leaving it
// unapplied.
func TestRunnerResourceClassResourceUnit_DescriptionChangeForcesReplacement(t *testing.T) {
	api := newStatefulRunnerResourceClassAPI(t)

	const orgID = "00000000-1111-2222-3333-444444444444"

	config := func(description string) string {
		return runnerProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_runner_resource_class" "test" {
  org_id         = %q
  resource_class = "acc-ns/linux"
  description    = %q
}
`, orgID, description)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("original"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_runner_resource_class.test", tfjsonpath.New("description"),
						knownvalue.StringExact("original"),
					),
				},
			},
			{
				Config: config("changed"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_runner_resource_class.test", plancheck.ResourceActionDestroyBeforeCreate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_runner_resource_class.test", tfjsonpath.New("description"),
						knownvalue.StringExact("changed"),
					),
				},
			},
		},
	})

	// Two creates and (at least) one delete: the second create carried the new
	// description to the server — there is no PATCH to inspect, because a
	// description change never reaches Update at all. Checked against the
	// fake's request log rather than its live record set, because
	// resource.UnitTest destroys the resource as part of tearing the test
	// case down, which would otherwise make this assertion race the test
	// framework's own cleanup.
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.creates) != 2 {
		t.Fatalf("expected exactly two creates (initial + replacement), got %d: %v", len(api.creates), api.creates)
	}
	if got := api.creates[1]["description"]; got != "changed" {
		t.Errorf("replacement create body description = %v, want %q", got, "changed")
	}
	if len(api.deletes) == 0 {
		t.Fatalf("expected the original resource class to be deleted as part of the replacement, got no deletes")
	}
}
