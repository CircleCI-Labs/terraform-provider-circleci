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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// This file backs `circleci_pipeline` (pipeline_resource.go) and its singular
// data source (pipeline_data_source.go) with an in-process stand-in for the
// public API service, so their CRUD paths run without TF_ACC or credentials.
//
// Both go through internal/circleci's pipeline definition methods
// (internal/circleci/pipeline_definition.go), whose wire shapes were
// cross-checked against the CircleCI API:
//   - the CircleCI API for the create body
//     (config_source{provider,repo{external_id},file_path},
//     checkout_source{provider,repo{external_id}})
//   - the CircleCI API for the update body
//     (config_source{file_path} ONLY — provider/repo are not updatable at all;
//     checkout_source{provider,repo{external_id}}, same as create)
//
// Before the SDK migration (issue #26), circleci_pipeline had
// three characterized bugs, all fixed by that migration: project_id had no
// RequiresReplace (an in-place update sent the new project_id with the old
// pipeline id, 404ing); Read() treated a 404 as a permanent error rather than
// drift; and the resource could not be gated off CircleCI Server at all,
// because Configure only received *pipeline.PipelineService, which carried no
// deployment information. See the tests below with "ForcesReplacement",
// "Recreates" and "Gated" in their names.

// fakePipelineDefAPI is an in-memory stand-in for the pipeline-definitions
// routes, keyed by "projectID/pipelineID" so that a request against the wrong
// project for an otherwise-valid definition id gets a 404, exactly like the
// real API would (definitions do not exist outside their project).
type fakePipelineDefAPI struct {
	t *testing.T

	mu          sync.Mutex
	definitions map[string]map[string]any
	requests    []fakeRecordedRequest
	nextID      int

	// missing forces a 404 on GET for the given key, simulating deletion
	// outside Terraform.
	missing map[string]bool

	// failStatus/failBody, when failStatus is non-zero, make every GET answer
	// with that response instead of the stored definition. Used to reproduce
	// the string-matched-404 bug: a 500 whose body happens to contain "404".
	failStatus int
	failBody   string
}

// fakeRecordedRequest is one request the fake received, decoded generically so
// tests can assert exactly which keys were present — not just what the
// provider's own structs would produce.
type fakeRecordedRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

func newFakePipelineDefAPI(t *testing.T) (*fakePipelineDefAPI, string) {
	t.Helper()

	api := &fakePipelineDefAPI{
		t:           t,
		definitions: map[string]map[string]any{},
		missing:     map[string]bool{},
	}
	srv := httptest.NewServer(http.HandlerFunc(api.handle))
	t.Cleanup(srv.Close)

	return api, srv.URL
}

func (a *fakePipelineDefAPI) handle(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	raw, _ := io.ReadAll(r.Body)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			a.t.Errorf("fake pipeline API: request body is not JSON: %v (%s)", err, raw)
		}
	}

	a.mu.Lock()
	a.requests = append(a.requests, fakeRecordedRequest{Method: r.Method, Path: r.URL.Path, Body: body})
	failStatus, failBody := a.failStatus, a.failBody
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	if failStatus != 0 {
		w.WriteHeader(failStatus)
		_, _ = io.WriteString(w, failBody)

		return
	}

	const prefix = "/api/v2/projects/"
	if len(r.URL.Path) <= len(prefix) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"not found"}`)

		return
	}

	rest := r.URL.Path[len(prefix):]
	// rest is one of: "{projectID}/pipeline-definitions" or
	// "{projectID}/pipeline-definitions/{pipelineID}".
	var projectID, pipelineID string
	const marker = "/pipeline-definitions"
	idx := indexOf(rest, marker)
	if idx < 0 {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"unrecognized route"}`)

		return
	}
	projectID = rest[:idx]
	tail := rest[idx+len(marker):]
	if len(tail) > 1 {
		pipelineID = tail[1:] // strip leading "/"
	}

	switch {
	case r.Method == http.MethodPost && pipelineID == "":
		a.create(w, projectID, body)
	case r.Method == http.MethodGet && pipelineID != "":
		a.get(w, projectID, pipelineID)
	case r.Method == http.MethodPatch && pipelineID != "":
		a.update(w, projectID, pipelineID, body)
	case r.Method == http.MethodDelete && pipelineID != "":
		a.delete(w, projectID, pipelineID)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"unrecognized route"}`)
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}

	return -1
}

// resolveFullName fakes VCS repo resolution: the real API fills in full_name
// from the external_id the caller supplies. The mapping just has to be stable
// across calls, not realistic.
func resolveFullName(externalID string) string {
	if externalID == "" {
		return ""
	}

	return "acme-org/repo-" + externalID
}

func (a *fakePipelineDefAPI) create(w http.ResponseWriter, projectID string, body map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.nextID++
	id := fmt.Sprintf("11111111-2222-3333-4444-%012d", a.nextID)

	configSource, _ := body["config_source"].(map[string]any)
	checkoutSource, _ := body["checkout_source"].(map[string]any)

	record := map[string]any{
		"id":              id,
		"name":            body["name"],
		"description":     body["description"],
		"created_at":      "2024-03-01T00:00:00.000Z",
		"config_source":   resolvedSource(configSource, true),
		"checkout_source": resolvedSource(checkoutSource, false),
	}

	a.definitions[projectID+"/"+id] = record

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(record)
}

// resolvedSource fills in repo.full_name from repo.external_id, the way the
// real API resolves it against the VCS provider. withFilePath keeps file_path
// when present (config_source only).
func resolvedSource(source map[string]any, withFilePath bool) map[string]any {
	provider, _ := source["provider"].(string)
	out := map[string]any{"provider": provider}
	if withFilePath {
		filePath, _ := source["file_path"].(string)
		out["file_path"] = filePath
	}

	repo, _ := source["repo"].(map[string]any)
	externalID, _ := repo["external_id"].(string)
	if externalID != "" {
		out["repo"] = map[string]any{
			"full_name":   resolveFullName(externalID),
			"external_id": externalID,
		}
	}

	return out
}

func (a *fakePipelineDefAPI) get(w http.ResponseWriter, projectID, pipelineID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	key := projectID + "/" + pipelineID

	if a.missing[key] {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Pipeline definition not found"}`)

		return
	}

	record, ok := a.definitions[key]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Pipeline definition not found"}`)

		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(record)
}

func (a *fakePipelineDefAPI) update(w http.ResponseWriter, projectID, pipelineID string, body map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	key := projectID + "/" + pipelineID
	record, ok := a.definitions[key]
	if !ok {
		// Exactly what happens today if project_id changes without forcing
		// replacement: the PATCH lands on the new project with the old
		// definition id, which does not exist there.
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Pipeline definition not found"}`)

		return
	}

	record["name"] = body["name"]
	record["description"] = body["description"]

	// The real update endpoint only accepts config_source.file_path: provider
	// and repo are immutable after creation. Merge file_path only, leaving the
	// stored provider/repo untouched.
	if existing, ok := record["config_source"].(map[string]any); ok {
		if newConfig, ok := body["config_source"].(map[string]any); ok {
			if fp, ok := newConfig["file_path"]; ok {
				existing["file_path"] = fp
			}
		}
	}

	// checkout_source.provider and .repo.external_id ARE updatable.
	if newCheckout, ok := body["checkout_source"].(map[string]any); ok {
		record["checkout_source"] = resolvedSource(newCheckout, false)
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(record)
}

func (a *fakePipelineDefAPI) delete(w http.ResponseWriter, projectID, pipelineID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	key := projectID + "/" + pipelineID
	if _, ok := a.definitions[key]; !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Pipeline definition not found"}`)

		return
	}

	delete(a.definitions, key)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"message":"ok"}`)
}

func (a *fakePipelineDefAPI) setMissing(projectID, pipelineID string, missing bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.missing[projectID+"/"+pipelineID] = missing
}

func (a *fakePipelineDefAPI) setFail(status int, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.failStatus = status
	a.failBody = body
}

func (a *fakePipelineDefAPI) clearFail() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.failStatus = 0
	a.failBody = ""
}

func (a *fakePipelineDefAPI) recorded() []fakeRecordedRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]fakeRecordedRequest(nil), a.requests...)
}

// lastRequest returns the most recent request matching method and path,
// failing the test when none matches.
func (a *fakePipelineDefAPI) lastRequest(t *testing.T, method, path string) fakeRecordedRequest {
	t.Helper()

	var last fakeRecordedRequest
	found := false
	for _, req := range a.recorded() {
		if req.Method == method && req.Path == path {
			last = req
			found = true
		}
	}
	if !found {
		t.Fatalf("no %s request to %s recorded (all: %+v)", method, path, a.recorded())
	}

	return last
}

func pipelineFakeProviderConfig(host, deployment string) string {
	if deployment == "" {
		deployment = "cloud"
	}

	return fmt.Sprintf(`
provider "circleci" {
  host       = %q
  key        = "fake-token"
  deployment = %q
}
`, host, deployment)
}

func pipelineFakeResourceConfig(host, projectID, name, description, configExternalID, checkoutExternalID string) string {
	return pipelineFakeProviderConfig(host, "cloud") + fmt.Sprintf(`
resource "circleci_pipeline" "test" {
  project_id                       = %[1]q
  name                              = %[2]q
  description                       = %[3]q
  config_source_provider            = "github_app"
  config_source_file_path           = "config.yml"
  config_source_repo_external_id    = %[4]q
  checkout_source_provider          = "github_app"
  checkout_source_repo_external_id  = %[5]q
}
`, projectID, name, description, configExternalID, checkoutExternalID)
}

const fakePipelineProjectID = "aaaaaaaa-1111-2222-3333-444444444444"
const fakePipelineOtherProjectID = "bbbbbbbb-1111-2222-3333-444444444444"

// TestPipelineResourceUnit_CRUD exercises create, read-back, update-in-place
// and delete against the fake, and pins the exact wire bodies sent for create
// and update.
func TestPipelineResourceUnit_CRUD(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "ext-1", "ext-2"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("name"), knownvalue.StringExact("pipe-1")),
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("description"), knownvalue.StringExact("original")),
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("config_source_repo_full_name"), knownvalue.StringExact(resolveFullName("ext-1"))),
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("checkout_source_repo_full_name"), knownvalue.StringExact(resolveFullName("ext-2"))),
				},
			},
			{
				// Update: change description and the checkout external id. This
				// must produce a PATCH carrying every updatable field.
				Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "updated", "ext-1", "ext-3"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("description"), knownvalue.StringExact("updated")),
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("checkout_source_repo_full_name"), knownvalue.StringExact(resolveFullName("ext-3"))),
				},
			},
			{
				ResourceName:      "circleci_pipeline.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: importStateIDFor("circleci_pipeline.test", "project_id"),
			},
		},
	})

	// Pin the create body: every field the schema exposes as settable must be
	// sent, using the real API's field names (verified against
	// the API's createRequestConfigSource/createRequestCheckoutSource).
	create := api.lastRequest(t, "POST", "/api/v2/projects/"+fakePipelineProjectID+"/pipeline-definitions")
	configSource, _ := create.Body["config_source"].(map[string]any)
	if configSource["provider"] != "github_app" {
		t.Errorf("create config_source.provider = %v, want github_app", configSource["provider"])
	}
	if configSource["file_path"] != "config.yml" {
		t.Errorf("create config_source.file_path = %v, want config.yml", configSource["file_path"])
	}
	repo, _ := configSource["repo"].(map[string]any)
	if repo["external_id"] != "ext-1" {
		t.Errorf("create config_source.repo.external_id = %v, want ext-1", repo["external_id"])
	}

	// Pin the update body: config_source must carry ONLY file_path (the real
	// API's updateRequestConfigSource has no provider/repo fields at all — see
	// the CircleCI API), while
	// checkout_source must carry both provider and repo.external_id, matching
	// createRequestCheckoutSource (the update handler reuses it).
	update := api.lastRequest(t, "PATCH", "/api/v2/projects/"+fakePipelineProjectID+"/pipeline-definitions/11111111-2222-3333-4444-000000000001")
	updateConfigSource, _ := update.Body["config_source"].(map[string]any)
	if len(updateConfigSource) != 1 {
		t.Errorf("update config_source has keys %v, want only file_path (provider/repo are not updatable)", keysOf(updateConfigSource))
	}
	if updateConfigSource["file_path"] != "config.yml" {
		t.Errorf("update config_source.file_path = %v, want config.yml", updateConfigSource["file_path"])
	}
	updateCheckoutSource, _ := update.Body["checkout_source"].(map[string]any)
	if updateCheckoutSource["provider"] != "github_app" {
		t.Errorf("update checkout_source.provider = %v, want github_app", updateCheckoutSource["provider"])
	}
	updateCheckoutRepo, _ := updateCheckoutSource["repo"].(map[string]any)
	if updateCheckoutRepo["external_id"] != "ext-3" {
		t.Errorf("update checkout_source.repo.external_id = %v, want ext-3", updateCheckoutRepo["external_id"])
	}
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	return keys
}

// importStateIDFor builds an ImportStateIdFunc for the "<scope>/id" import id
// format shared by circleci_pipeline, circleci_trigger and circleci_webhook.
//
// Only the scope attribute varies between them — project_id for a pipeline and a
// trigger, scope_id for a webhook — so the second half is always "id".
func importStateIDFor(resourceAddr, scopeAttr string) func(s *terraform.State) (string, error) {
	return func(s *terraform.State) (string, error) {
		res := s.RootModule().Resources[resourceAddr]
		if res == nil {
			return "", fmt.Errorf("resource %s not found in state", resourceAddr)
		}

		scope, ok := res.Primary.Attributes[scopeAttr]
		if !ok {
			return "", fmt.Errorf("attribute %s.%s not found", resourceAddr, scopeAttr)
		}
		id, ok := res.Primary.Attributes["id"]
		if !ok {
			return "", fmt.Errorf("attribute %s.id not found", resourceAddr)
		}

		return scope + "/" + id, nil
	}
}

// TestPipelineResourceUnit_ConfigRepoChangeForcesReplacement documents the fix
// to a fourth bug in this resource, of the same family as project_id's.
//
// The API's update handler accepts only `file_path` inside `config_source` — the
// provider and repo are not updatable at all (verified against
// the API's the CircleCI API and
// pinned by the CRUD test above). Before the SDK migration (issue
// #26), `config_source_repo_external_id` was a plain Required attribute with no
// RequiresReplace modifier: changing it planned an in-place update, the PATCH
// omitted it, and the apply reported success — the worst of the three outcomes
// available, since state recorded the new external id while the API kept the
// old one, and the next plan showed no drift either (Read populates the field
// from the same API that never received it).
//
// Now config_source_repo_external_id carries RequiresReplace, so a change plans
// a destroy/create instead: the old definition is deleted and a new one created
// with the new external id, which actually reaches the server.
func TestPipelineResourceUnit_ConfigRepoChangeForcesReplacement(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "ext-1", "ext-2")},
			{
				Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "ext-99", "ext-2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_pipeline.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("config_source_repo_external_id"), knownvalue.StringExact("ext-99")),
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("config_source_repo_full_name"), knownvalue.StringExact(resolveFullName("ext-99"))),
				},
			},
		},
	})

	// A new definition was created carrying the new external id — no PATCH with
	// a missing repo is involved at all.
	create := api.lastRequest(t, "POST", "/api/v2/projects/"+fakePipelineProjectID+"/pipeline-definitions")
	configSource, _ := create.Body["config_source"].(map[string]any)
	repo, _ := configSource["repo"].(map[string]any)
	if repo["external_id"] != "ext-99" {
		t.Errorf("create config_source.repo.external_id = %v, want ext-99", repo["external_id"])
	}
}

// TestPipelineResourceUnit_RenameForcesReplacement covers `name`, which the
// schema deliberately marks RequiresReplace ("Changing this value forces a new
// resource to be created" in its own MarkdownDescription) — unlike this test's
// predecessor assumed. That assumption was wrong independent of the SDK
// migration: the RequiresReplace modifier on `name` predates
// this migration (it is not one of the four characterized bugs), so a rename
// has always planned a destroy/create, never an in-place update. This test
// used to assert ResourceActionUpdate and a PATCH carrying the new name,
// neither of which ever happened; it now asserts what the schema actually
// does.
func TestPipelineResourceUnit_RenameForcesReplacement(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "ext-1", "ext-2")},
			{
				Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-renamed", "original", "ext-1", "ext-2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_pipeline.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_pipeline.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact("pipe-renamed"),
					),
				},
			},
		},
	})

	// The new definition is created with the new name — there is no PATCH to
	// inspect, because a rename never reaches Update at all.
	create := api.lastRequest(t, "POST", "/api/v2/projects/"+fakePipelineProjectID+"/pipeline-definitions")
	if create.Body["name"] != "pipe-renamed" {
		t.Errorf("create body name = %v, want pipe-renamed", create.Body["name"])
	}
}

// TestPipelineResourceUnit_ServerErrorMentioning404DoesNotDropState is the most
// important test in this file, because the failure it guards against is silent
// data loss rather than an error.
//
// A 5xx whose body happens to contain the string "404" must NOT be mistaken for
// "the resource is gone". Detecting absence with
// strings.Contains(err.Error(), "404") — which this provider genuinely used to do,
// see DESIGN.md "Errors are typed" — matches such a response and makes Terraform
// remove a live resource from state. The next apply then recreates a resource that
// already exists, or worse, the practitioner loses track of it entirely.
//
// The recovery step matters as much as the failure step: after the transient error
// clears, the resource must still be in state and plan clean. If the error had
// dropped it, the final plan would show a create.
func TestPipelineResourceUnit_ServerErrorMentioning404DoesNotDropState(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	config := pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "ext-1", "ext-2")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				// A 502 from a proxy, whose body mentions "404". Terraform must
				// surface this as an error, not treat the pipeline as deleted.
				PreConfig: func() {
					api.setFail(http.StatusBadGateway, `{"message":"upstream returned 404 page not found"}`)
				},
				Config:      config,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Unable to Read`),
			},
			{
				// Transient failure over: the resource must still be managed, and
				// the plan must be empty. A create here would mean state was dropped.
				PreConfig: func() { api.clearFail() },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_pipeline.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
}

// TestPipelineResourceUnit_ProjectIDChangeForcesReplacement documents the fix
// to a real bug: project_id used to have no RequiresReplace plan modifier
// (pipeline_resource.go's "project_id" schema attribute), and Update used the
// NEW project_id together with the OLD pipeline id as path parameters.
// Terraform therefore used to plan an in-place update instead of a
// destroy/create, and the PATCH landed on the wrong project — this fake
// reproduces that faithfully: the definition is created under
// fakePipelineProjectID, so a PATCH against fakePipelineOtherProjectID with the
// same id would 404, exactly as the real API would.
//
// Now project_id carries RequiresReplace, so changing it plans a destroy/create
// instead, and the new definition is created under the new project — no PATCH
// against the wrong project is ever attempted.
func TestPipelineResourceUnit_ProjectIDChangeForcesReplacement(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "ext-1", "ext-2"),
			},
			{
				Config: pipelineFakeResourceConfig(host, fakePipelineOtherProjectID, "pipe-1", "original", "ext-1", "ext-2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_pipeline.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("project_id"), knownvalue.StringExact(fakePipelineOtherProjectID)),
				},
			},
		},
	})

	// The new definition was created under the new project, and the old one
	// under the original project was deleted — never a PATCH against the wrong
	// project.
	create := api.lastRequest(t, "POST", "/api/v2/projects/"+fakePipelineOtherProjectID+"/pipeline-definitions")
	if create.Method == "" {
		t.Fatal("expected a create against the new project")
	}
	del := api.lastRequest(t, "DELETE", "/api/v2/projects/"+fakePipelineProjectID+"/pipeline-definitions/11111111-2222-3333-4444-000000000001")
	if del.Method == "" {
		t.Fatal("expected the old definition under the original project to be deleted")
	}
}

// TestPipelineResourceUnit_DriftRecreatesRatherThanHardError documents the fix
// to a second bug: pipeline_resource.go's Read() used to treat every error from
// the API, including a 404 for a definition deleted outside Terraform, as a
// hard diagnostic. Unlike the checkout key resource (the established good
// pattern in checkout_key_resource_test.go), circleci_pipeline never called
// resp.State.RemoveResource, so drift never led to a clean "will be recreated"
// plan — it led to a permanent refresh error.
//
// Now a 404 on Read calls resp.State.RemoveResource, so the next plan proposes
// a create rather than erroring, exactly like TestAccCheckoutKeyResource_RemovedOutsideTerraform.
func TestPipelineResourceUnit_DriftRecreatesRatherThanHardError(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "ext-1", "ext-2"),
			},
			{
				PreConfig:          func() { api.setMissing(fakePipelineProjectID, "11111111-2222-3333-4444-000000000001", true) },
				Config:             pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "ext-1", "ext-2"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Restore the definition so the framework's own destroy step
				// (which reads state before issuing DELETE) succeeds.
				PreConfig: func() { api.setMissing(fakePipelineProjectID, "11111111-2222-3333-4444-000000000001", false) },
				Config:    pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "ext-1", "ext-2"),
			},
		},
	})
}

// TestPipelineResourceUnit_ServerDeploymentIsGated documents the fix to a
// third bug: unlike circleci_pipelines (the plural data source, gated via
// requireCloud in pipelines_data_source.go) and the v3-only resources gated in
// cloud_only.go, circleci_pipeline used to implement neither
// ResourceWithModifyPlan nor any requireCloud check — it could not, because its
// Configure only received *pipeline.PipelineService, which carried no
// deployment information at all. Setting deployment = "server" therefore did
// not fail at plan time with a clear diagnostic; it planned a normal create,
// and apply failed with whatever raw error the wire happened to produce.
//
// Now Configure receives *circleci.Client, which knows its own deployment, so
// ModifyPlan (see cloud_only.go) rejects CircleCI Server before any request is
// ever sent — the create never reaches the wire at all.
func TestPipelineResourceUnit_ServerDeploymentIsGated(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)
	// CircleCI Server does not route this endpoint at all; simulate that with
	// a bare 404 rather than a stored definition, so a regression back to the
	// old behaviour would still be caught as an error rather than a false
	// success.
	api.setFail(http.StatusNotFound, `{"message":"404 page not found"}`)

	cfg := pipelineFakeProviderConfig(host, "server") + fmt.Sprintf(`
resource "circleci_pipeline" "test" {
  project_id                       = %[1]q
  name                              = "pipe-1"
  description                       = "d"
  config_source_provider            = "github_app"
  config_source_file_path           = "config.yml"
  config_source_repo_external_id    = "ext-1"
  checkout_source_provider          = "github_app"
  checkout_source_repo_external_id  = "ext-2"
}
`, fakePipelineProjectID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)requires CircleCI Cloud`),
			},
		},
	})

	if requests := api.recorded(); len(requests) != 0 {
		t.Errorf("recorded requests = %+v, want none — the plan-time gate must block before any request is sent", requests)
	}
}

// TestPipelineResourceUnit_Create4xxIsADiagnosticNotAPanic guards against a
// regression where a 4xx from the create call (e.g. a duplicate name) is
// mis-handled.
func TestPipelineResourceUnit_Create4xxIsADiagnosticNotAPanic(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)
	api.setFail(http.StatusBadRequest, `{"message":"a pipeline definition with this name already exists"}`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "d", "ext-1", "ext-2"),
				ExpectError: regexp.MustCompile(`(?s)Error creating CircleCI pipeline.*already exists`),
			},
		},
	})
}
