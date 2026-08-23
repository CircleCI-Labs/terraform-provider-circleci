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
	"strconv"
	"strings"
	"sync"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// This file backs `circleci_pipeline` (pipeline_resource.go) and its singular
// data source (pipeline_data_source.go) with an in-process stand-in for the
// public API, so their CRUD paths run without TF_ACC or credentials.
//
// Both go through internal/circleci's pipeline definition methods
// (internal/circleci/pipeline_definition.go), whose wire shapes were
// cross-checked against the real API:
//   - the create body
//     (config_source{provider,repo{external_id},file_path},
//     checkout_source{provider,repo{external_id}})
//   - the update body
//     (config_source{file_path} ONLY — provider/repo are not updatable at all;
//     checkout_source{provider,repo{external_id}}, same as create)
//
// Before the SDK migration, circleci_pipeline had
// three characterized bugs, all fixed by that migration: project_id had no
// RequiresReplace (an in-place update sent the new project_id with the old
// pipeline id, 404ing); Read() treated a 404 as a permanent error rather than
// drift; and the resource could not be gated off CircleCI Server at all,
// because Configure only received *pipeline.PipelineService, which carried no
// deployment information. See the tests below with "ForcesReplacement",
// "Recreates" and "Gated" in their names.

// fakePipelineDefAPI is an in-memory stand-in for the pipeline-definitions
// routes, keyed by "projectID/pipelineID" so that a request against the wrong
// project for an otherwise-valid definition id is refused, exactly like the real
// API (definitions do not exist outside their project).
//
// The statuses below are the ones measured over the network against circleci.com
// on 2026-08-21, not the ones this fake used to return. It previously answered
// 404 for a missing definition on GET, PATCH and DELETE, which is wrong on all
// three counts and hid the bug the singular route actually has:
//
//	GET    a definition that is gone   400 {"message":"Failed to get pipeline definition."}
//	GET    a definition on GitLab      400, same body, even though it EXISTS
//	GET    a nonexistent project       404 {"message":"Pipeline definition not found."}
//	PATCH  a definition that is gone   400 {"message":"Failed to update pipeline definition."}
//	DELETE a definition that is gone   200 {"message":"Pipeline definition deleted."}  (idempotent)
//	LIST   a real project              200 {"items":[...]}, no next_page_token, on every integration
//	LIST   a nonexistent project       404 {"message":"Project not found."}
//
// See internal/circleci/pipeline_definition.go's pipelineDefinitionRoute for the
// full probe.
type fakePipelineDefAPI struct {
	t *testing.T

	mu          sync.Mutex
	definitions map[string]map[string]any
	requests    []fakeRecordedRequest
	nextID      int

	// missing forces the singular GET to answer as it does for a deleted
	// definition, simulating deletion outside Terraform. The definition is
	// removed from the plural list too, because the real API removes it from
	// both.
	missing map[string]bool

	// singularUnservable reproduces the GitLab case: the definition exists and
	// the plural list returns it, but the singular route answers the SAME 400 a
	// deleted definition gets. Keyed by project id, because it is a property of
	// the project's integration rather than of one definition.
	singularUnservable map[string]bool

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
		t:                  t,
		definitions:        map[string]map[string]any{},
		missing:            map[string]bool{},
		singularUnservable: map[string]bool{},
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
	case r.Method == http.MethodGet && pipelineID == "":
		a.list(w, projectID)
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

// circleciConfigSourceAllowedFilePathPrefix is the one file_path namespace the
// real create endpoint accepts for config_source.provider "circleci": [NET]
// confirmed by creating (and deleting) real definitions against two CircleCI
// Cloud organizations. A path under this prefix (with something after it)
// succeeds; every other path — a customer's ".circleci/config.yml", a bare
// "config.yml", a nested path outside the prefix, or the prefix alone with
// nothing after it — answers 400 "Invalid config file path.", and an empty
// file_path answers 400 "Unable to parse JSON body." This is CircleCI's own
// internal task-config namespace (visible on pre-existing definitions in
// CircleCI's own production projects), not a path any customer configuration
// would plausibly use — which is why circleci.PipelineConfigSourceProviders no
// longer offers "circleci" as something to create.
const circleciConfigSourceAllowedFilePathPrefix = "circleci-agents/"

// rejectInvalidPipelineDefinitionCreate answers HTTP 400 for the create bodies
// the real API refuses, and reports whether it did.
//
//   - a repo on the "circleci" branch of config_source. That branch is
//     `additionalProperties: false` over only provider and file_path, so a repo
//     there does not fall back to the VCS branch either — it fails the oneOf
//     outright, reported as a schema-validation error naming the offending path.
//     Enforced ahead of the file_path check below: a "circleci" body with both
//     a repo and a disallowed file_path still gets the oneOf error, because the
//     real API validates its schema before running any handler logic.
//   - a "circleci" config source whose file_path is not under
//     circleciConfigSourceAllowedFilePathPrefix.
//   - a repository external id that is not a number, for any provider that takes a
//     repository at all (config_source's github_app/github_server, and
//     checkout_source, which always takes one). Confirmed at the client layer by
//     circleci.TriggerRepoExternalIDIsValid, which the trigger endpoints already
//     use for the same rule.
func rejectInvalidPipelineDefinitionCreate(w http.ResponseWriter, body map[string]any) bool {
	fail := func(message string) bool {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":`+strconv.Quote(message)+`}`)

		return true
	}

	configSource, _ := body["config_source"].(map[string]any)
	configProvider, _ := configSource["provider"].(string)
	configRepo, hasConfigRepo := configSource["repo"].(map[string]any)

	if configProvider == "circleci" {
		if hasConfigRepo {
			return fail(`OpenAPI validation error: request body has an error: doesn't match schema ` +
				`./schemas.yaml#/createPipelineDefinitionRequest: Error at "/config_source/repo": ` +
				`unexpected property`)
		}

		filePath, _ := configSource["file_path"].(string)
		if filePath == "" {
			return fail("Unable to parse JSON body.")
		}
		if !strings.HasPrefix(filePath, circleciConfigSourceAllowedFilePathPrefix) ||
			filePath == circleciConfigSourceAllowedFilePathPrefix {
			return fail("Invalid config file path.")
		}
	} else if hasConfigRepo {
		if !isNumericExternalID(configRepo["external_id"]) {
			return fail("bad request")
		}
	}

	checkoutSource, _ := body["checkout_source"].(map[string]any)
	checkoutRepo, _ := checkoutSource["repo"].(map[string]any)
	if !isNumericExternalID(checkoutRepo["external_id"]) {
		return fail("bad request")
	}

	return false
}

// isNumericExternalID mirrors circleci.TriggerRepoExternalIDIsValid without
// importing the provider's own client package into a fake that stands in for the
// wire, not for this provider's code.
func isNumericExternalID(v any) bool {
	s, _ := v.(string)
	_, err := strconv.ParseInt(s, 10, 64)

	return err == nil
}

func (a *fakePipelineDefAPI) create(w http.ResponseWriter, projectID string, body map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if rejectInvalidPipelineDefinitionCreate(w, body) {
		return
	}

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

// lookupFailed400 is the exact response the real singular GET gives for a
// definition it will not serve — whether because the definition is gone or
// because the project is a GitLab one whose live definitions this route cannot
// serve at all. One status, one body, both conditions.
func lookupFailed400(w http.ResponseWriter) {
	w.WriteHeader(http.StatusBadRequest)
	_, _ = io.WriteString(w, `{"message":"Failed to get pipeline definition."}`)
}

// list is the plural route: the oracle the client falls back on. It returns
// every definition of the project in one body with no next_page_token, matching
// the measured response, and 404s only for a project with nothing stored at all.
func (a *fakePipelineDefAPI) list(w http.ResponseWriter, projectID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	prefix := projectID + "/"

	items := []map[string]any{}
	known := false

	for key, record := range a.definitions {
		if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
			continue
		}

		known = true

		// A definition deleted out of band is absent from the list as well as
		// from the singular route: that difference is the whole reason the list
		// can settle what the 400 cannot.
		if a.missing[key] {
			continue
		}

		items = append(items, record)
	}

	if !known {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Project not found."}`)

		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
}

func (a *fakePipelineDefAPI) get(w http.ResponseWriter, projectID, pipelineID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	key := projectID + "/" + pipelineID

	// The GitLab case: the definition is stored and the plural list returns it,
	// but this route refuses it with the very same 400 a deleted definition
	// gets.
	if a.singularUnservable[projectID] {
		lookupFailed400(w)

		return
	}

	if a.missing[key] {
		lookupFailed400(w)

		return
	}

	if _, ok := a.definitions[key]; !ok {
		// A definition id that never existed, or a real id under the wrong
		// project: measured, both give this same 400.
		lookupFailed400(w)

		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(a.definitions[key])
}

func (a *fakePipelineDefAPI) update(w http.ResponseWriter, projectID, pipelineID string, body map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	key := projectID + "/" + pipelineID
	record, ok := a.definitions[key]
	if !ok {
		// Exactly what happens today if project_id changes without forcing
		// replacement: the PATCH lands on the new project with the old
		// definition id, which does not exist there. Measured: PATCH against a
		// definition that is not there answers 400 with its own message, not a
		// 404 and not the GET message.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":"Failed to update pipeline definition."}`)

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

	// Measured: DELETE is idempotent. Deleting a definition that is already
	// gone answers 200 with the same body as deleting a live one, so there is no
	// absence case to report here at all.
	delete(a.definitions, projectID+"/"+pipelineID)

	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"message":"Pipeline definition deleted."}`)
}

// seed inserts a definition record directly, bypassing create() so a test can
// control its id and created_at exactly — the shape ImportState-refusal tests
// need, since create() always sets created_at and never generates a v5 id.
func (a *fakePipelineDefAPI) seed(projectID string, record map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	id, _ := record["id"].(string)
	a.definitions[projectID+"/"+id] = record
}

func (a *fakePipelineDefAPI) setMissing(projectID, pipelineID string, missing bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.missing[projectID+"/"+pipelineID] = missing
}

// setSingularUnservable makes the singular GET on projectID answer the
// ambiguous 400 while the plural list keeps returning the project's live
// definitions — the GitLab behaviour, measured over the network.
func (a *fakePipelineDefAPI) setSingularUnservable(projectID string, unservable bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.singularUnservable[projectID] = unservable
}

// seedImplicitDefinition inserts a definition directly, bypassing create(),
// with no "created_at" key at all — standing in for an IMPLICIT pipeline
// definition, the kind CircleCI creates automatically for an OAuth-backed
// project rather than one this resource ever POSTs.
//
// Before this method existed, create() was the fake's only way to populate a
// definition, and create() always writes created_at (correctly: measured over
// the network, an explicit definition always gets one). That made the fake
// unable to represent the one case this resource can encounter but never
// creates — an implicit definition reached only through `terraform import` —
// the same shape of gap TestFakePipelineDefAPIAnswersTheStatusesTheRealRouteAnswers
// closed for the singular route's statuses.
func (a *fakePipelineDefAPI) seedImplicitDefinition(projectID, id, provider, repoExternalID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.definitions[projectID+"/"+id] = map[string]any{
		"id":          id,
		"name":        "project-1",
		"description": "Implicit pipeline definition associated with an OAuth-based project.",
		// No "created_at" key — deliberately, see the method doc.
		"config_source": resolvedSource(map[string]any{
			"provider":  provider,
			"file_path": ".circleci/config.yml",
			"repo":      map[string]any{"external_id": repoExternalID},
		}, true),
		"checkout_source": resolvedSource(map[string]any{
			"provider": provider,
			"repo":     map[string]any{"external_id": repoExternalID},
		}, false),
	}
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
				Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "100001", "100002"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("name"), knownvalue.StringExact("pipe-1")),
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("description"), knownvalue.StringExact("original")),
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("config_source_repo_full_name"), knownvalue.StringExact(resolveFullName("100001"))),
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("checkout_source_repo_full_name"), knownvalue.StringExact(resolveFullName("100002"))),
				},
			},
			{
				// Update: change description and the checkout external id. This
				// must produce a PATCH carrying every updatable field.
				Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "updated", "100001", "100003"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("description"), knownvalue.StringExact("updated")),
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("checkout_source_repo_full_name"), knownvalue.StringExact(resolveFullName("100003"))),
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
	// sent, using the real API's field names.
	create := api.lastRequest(t, "POST", "/api/v2/projects/"+fakePipelineProjectID+"/pipeline-definitions")
	configSource, _ := create.Body["config_source"].(map[string]any)
	if configSource["provider"] != "github_app" {
		t.Errorf("create config_source.provider = %v, want github_app", configSource["provider"])
	}
	if configSource["file_path"] != "config.yml" {
		t.Errorf("create config_source.file_path = %v, want config.yml", configSource["file_path"])
	}
	repo, _ := configSource["repo"].(map[string]any)
	if repo["external_id"] != "100001" {
		t.Errorf("create config_source.repo.external_id = %v, want 100001", repo["external_id"])
	}

	// Pin the update body: config_source must carry ONLY file_path (the update
	// route's config_source has no provider/repo fields at all), while
	// checkout_source must carry both provider and repo.external_id, matching
	// the create body's checkout_source (the update route reuses it).
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
	if updateCheckoutRepo["external_id"] != "100003" {
		t.Errorf("update checkout_source.repo.external_id = %v, want 100003", updateCheckoutRepo["external_id"])
	}
}

// TestPipelineResourceUnit_RefusesToImportImplicitDefinition proves the fix for
// the trap the decision this pins is about: an OAuth-based project's implicit
// pipeline definition answers GET with 200 — it looks importable — but [NET]
// PATCH answers 400 and DELETE answers 500 and the definition survives, so
// importing one would build a resource `terraform destroy` can never actually
// destroy. The seeded record here has no created_at and a version-5 id, the
// shape circleci.PipelineDefinitionIsImplicit was measured against; see its doc
// comment.
func TestPipelineResourceUnit_RefusesToImportImplicitDefinition(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	const implicitID = "99999999-9999-5999-8999-999999999999"

	api.seed(fakePipelineProjectID, map[string]any{
		"id":          implicitID,
		"name":        "auto-runner",
		"description": "Implicit pipeline definition associated with an OAuth-based project.",
		// created_at deliberately absent.
		"config_source": map[string]any{
			"provider":  "github_oauth",
			"file_path": ".circleci/config.yml",
			"repo": map[string]any{
				"full_name": "acme-org/auto-runner", "external_id": "100001",
			},
		},
		"checkout_source": map[string]any{
			"provider": "github_oauth",
			"repo": map[string]any{
				"full_name": "acme-org/auto-runner", "external_id": "100001",
			},
		},
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// A real resource must exist at this address for Terraform to know
				// which resource type to import into; its own id is irrelevant to
				// the import attempt in the next step.
				Config: pipelineConfig(host, `
  config_source_provider           = "github_app"
  config_source_file_path          = ".circleci/config.yml"
  config_source_repo_external_id   = "100002"
  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = "100003"
`),
			},
			{
				ResourceName:  "circleci_pipeline_definition.test",
				ImportState:   true,
				ImportStateId: fakePipelineProjectID + "/" + implicitID,
				ExpectError: regexp.MustCompile(
					`(?s)Cannot Import Implicit Pipeline Definition.*OAuth-based.*cannot be managed`,
				),
			},
		},
	})

	// The step 1 resource is real and gets torn down by the test framework's own
	// cleanup regardless of what step 2 does, so only requests naming the
	// implicit definition's own path are relevant here.
	implicitPath := "/api/v2/projects/" + fakePipelineProjectID + "/pipeline-definitions/" + implicitID
	sawGet := false
	for _, req := range api.recorded() {
		if req.Path != implicitPath {
			continue
		}
		switch req.Method {
		case "GET":
			sawGet = true
		case "PATCH", "DELETE":
			t.Errorf("a refused import must not touch the implicit definition beyond GET, got %s %s", req.Method, req.Path)
		}
	}
	if !sawGet {
		t.Errorf("expected a GET for the implicit definition before refusing, requests: %+v", api.recorded())
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
// The API's update route accepts only `file_path` inside `config_source` — the
// provider and repo are not updatable at all (verified against the real API,
// and pinned by the CRUD test above). Before the SDK migration (issue
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
			{Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "100001", "100002")},
			{
				Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "100099", "100002"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_pipeline.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("config_source_repo_external_id"), knownvalue.StringExact("100099")),
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("config_source_repo_full_name"), knownvalue.StringExact(resolveFullName("100099"))),
				},
			},
		},
	})

	// A new definition was created carrying the new external id — no PATCH with
	// a missing repo is involved at all.
	create := api.lastRequest(t, "POST", "/api/v2/projects/"+fakePipelineProjectID+"/pipeline-definitions")
	configSource, _ := create.Body["config_source"].(map[string]any)
	repo, _ := configSource["repo"].(map[string]any)
	if repo["external_id"] != "100099" {
		t.Errorf("create config_source.repo.external_id = %v, want 100099", repo["external_id"])
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
			{Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "100001", "100002")},
			{
				Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-renamed", "original", "100001", "100002"),
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

	config := pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "100001", "100002")

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
// same id is refused, exactly as the real API refuses it — measured, with 400
// {"message":"Failed to update pipeline definition."} rather than a 404.
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
				Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "100001", "100002"),
			},
			{
				Config: pipelineFakeResourceConfig(host, fakePipelineOtherProjectID, "pipe-1", "original", "100001", "100002"),
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
// the API as a hard diagnostic, including the response a definition deleted
// outside Terraform produces. Unlike the checkout key resource (the established
// good pattern in checkout_key_resource_test.go), circleci_pipeline never called
// resp.State.RemoveResource, so drift never led to a clean "will be recreated"
// plan — it led to a permanent refresh error the practitioner could only escape
// by hand-editing state.
//
// The reason it stayed broken after RemoveResource was wired up is the status:
// the singular route answers 400, not 404, for a deleted definition — measured
// over the network by creating one, deleting it and fetching it back. This fake
// now answers that same 400, so this test fails outright unless the client
// resolves it (see circleci.GetPipelineDefinition). The next plan then proposes
// a create rather than erroring, exactly like
// TestAccCheckoutKeyResource_RemovedOutsideTerraform.
func TestPipelineResourceUnit_DriftRecreatesRatherThanHardError(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "100001", "100002"),
			},
			{
				PreConfig:          func() { api.setMissing(fakePipelineProjectID, "11111111-2222-3333-4444-000000000001", true) },
				Config:             pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "100001", "100002"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Restore the definition so the framework's own destroy step
				// (which reads state before issuing DELETE) succeeds.
				PreConfig: func() { api.setMissing(fakePipelineProjectID, "11111111-2222-3333-4444-000000000001", false) },
				Config:    pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "100001", "100002"),
			},
		},
	})
}

// TestPipelineResourceUnit_GitLabStyle400KeepsALiveDefinitionInState is the
// other half of the same bug, and the one that rules out the obvious fix.
//
// "Treat 400 like 404" would be wrong: measured over the network on two separate
// GitLab projects, the singular route answers 400 with the byte-identical body
// for definitions that DEMONSTRABLY EXIST — their ids were read out of a 200
// from the plural list moments earlier. A provider that read that 400 as "gone"
// would drop every GitLab-backed definition from state on the first refresh and
// then try to create a duplicate.
//
// So this test asserts the opposite outcome to
// TestPipelineResourceUnit_DriftRecreatesRatherThanHardError from a response
// that is identical on the wire: same status, same body, definition still
// listed. The refresh must succeed from the list entry and the plan must be
// empty. Anything else — an error, or a planned create — is the failure.
func TestPipelineResourceUnit_GitLabStyle400KeepsALiveDefinitionInState(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	config := pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "100001", "100002")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				// From here on the singular route refuses every read of this
				// project, while the definition stays alive and listed.
				PreConfig: func() { api.setSingularUnservable(fakePipelineProjectID, true) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_pipeline.test", plancheck.ResourceActionNoop),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("id"),
						knownvalue.StringExact("11111111-2222-3333-4444-000000000001")),
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("description"),
						knownvalue.StringExact("original")),
				},
			},
		},
	})

	// The refresh really did fall back to the plural route rather than getting
	// lucky with a cached read.
	api.lastRequest(t, "GET", "/api/v2/projects/"+fakePipelineProjectID+"/pipeline-definitions")
}

// TestPipelineResourceUnit_UnconfirmableAbsenceKeepsStateAndErrors covers the
// fail-safe path: the singular route gives the ambiguous 400 and the list that
// would settle it is unavailable too. Nothing is known, so the resource must
// stay in state and the practitioner must get an error that says why — not a
// silent removal.
//
// The recovery step is the assertion that matters: once the list works again,
// the plan must be a no-op. A create there would mean state had been dropped on
// an unconfirmed signal.
func TestPipelineResourceUnit_UnconfirmableAbsenceKeepsStateAndErrors(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	config := pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "original", "100001", "100002")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				// Every route answers 400 with the ambiguous body — including the
				// list probe, so the ambiguity cannot be resolved either way.
				PreConfig: func() {
					api.setFail(http.StatusBadRequest, `{"message":"Failed to get pipeline definition."}`)
				},
				Config:      config,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Unable to Read.*does not mean the definition is gone`),
			},
			{
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

// TestPipelineResourceUnit_ServerDeploymentIsGated documents the fix to a
// third bug: unlike circleci_pipeline_definitions (the plural data source, gated via
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
  config_source_repo_external_id    = "100001"
  checkout_source_provider          = "github_app"
  checkout_source_repo_external_id  = "100002"
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
				Config:      pipelineFakeResourceConfig(host, fakePipelineProjectID, "pipe-1", "d", "100001", "100002"),
				ExpectError: regexp.MustCompile(`(?s)Error creating CircleCI pipeline.*already exists`),
			},
		},
	})
}

// TestFakePipelineDefAPIRefusesWhatTheRealAPIRefuses talks to the fake directly,
// with bodies the provider does not currently produce, so it can assert the fake
// would *catch* a wrong request rather than merely mirror what the client already
// sends correctly. Same rationale as
// TestFakeTriggerAPIRefusesWhatTheRealAPIRefuses in trigger_resource_fake_test.go.
func TestFakePipelineDefAPIRefusesWhatTheRealAPIRefuses(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	createPath := "/api/v2/projects/" + fakePipelineProjectID + "/pipeline-definitions"

	cases := map[string]struct {
		body        string
		wantMessage string
	}{
		"a repo alongside a circleci-hosted config source": {
			body: `{
				"name": "pipe-1", "description": "d",
				"config_source": {"provider": "circleci", "file_path": ".circleci/config.yml",
				                   "repo": {"external_id": "123456"}},
				"checkout_source": {"provider": "github_app", "repo": {"external_id": "123456"}}
			}`,
			wantMessage: `OpenAPI validation error: request body has an error: doesn't match schema ` +
				`./schemas.yaml#/createPipelineDefinitionRequest: Error at "/config_source/repo": ` +
				`unexpected property`,
		},
		"a repository name where the API parses a numeric config_source id": {
			body: `{
				"name": "pipe-1", "description": "d",
				"config_source": {"provider": "github_app", "file_path": ".circleci/config.yml",
				                   "repo": {"external_id": "acme-org/repo"}},
				"checkout_source": {"provider": "github_app", "repo": {"external_id": "123456"}}
			}`,
			wantMessage: "bad request",
		},
		"a repository name where the API parses a numeric checkout_source id": {
			body: `{
				"name": "pipe-1", "description": "d",
				"config_source": {"provider": "github_app", "file_path": ".circleci/config.yml",
				                   "repo": {"external_id": "123456"}},
				"checkout_source": {"provider": "github_app", "repo": {"external_id": "acme-org/repo"}}
			}`,
			wantMessage: "bad request",
		},
		// circleci.PipelineConfigSourceProviders no longer offers "circleci" for
		// this exact reason: the create endpoint refuses it for every
		// customer-plausible file_path, not only the one the resource's schema
		// used to let through with no repo.
		"a customer-plausible file path for a circleci-hosted config source": {
			body: `{
				"name": "pipe-1", "description": "d",
				"config_source": {"provider": "circleci", "file_path": ".circleci/config.yml"},
				"checkout_source": {"provider": "github_app", "repo": {"external_id": "123456"}}
			}`,
			wantMessage: "Invalid config file path.",
		},
		"a circleci-hosted config source with no file path": {
			body: `{
				"name": "pipe-1", "description": "d",
				"config_source": {"provider": "circleci"},
				"checkout_source": {"provider": "github_app", "repo": {"external_id": "123456"}}
			}`,
			wantMessage: "Unable to parse JSON body.",
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			got := postJSON(t, host+createPath, testCase.body)

			if got.status != http.StatusBadRequest {
				t.Fatalf("answered %d, want 400: %s", got.status, got.body)
			}
			if message, _ := got.decoded["message"].(string); message != testCase.wantMessage {
				t.Errorf("message = %q, want %q", message, testCase.wantMessage)
			}
		})
	}

	if stored := len(api.definitions); stored != 0 {
		t.Errorf("the fake stored %d definitions, want 0: a rejected request must not create one", stored)
	}
}

// TestFakePipelineDefAPIAcceptsCircleCIConfigSourceUnderReservedPrefix pins the
// one file_path shape the real create endpoint accepts for config_source.provider
// "circleci" — see circleciConfigSourceAllowedFilePathPrefix — so the fake does not
// drift into rejecting everything with that provider, which would be just as
// wrong as the old fake accepting everything with it. This is CircleCI's own
// internal namespace; nothing in circleci_pipeline_definition's schema can reach
// it, since circleci.PipelineConfigSourceProviders no longer offers "circleci".
func TestFakePipelineDefAPIAcceptsCircleCIConfigSourceUnderReservedPrefix(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	createPath := "/api/v2/projects/" + fakePipelineProjectID + "/pipeline-definitions"

	got := postJSON(t, host+createPath, `{
		"name": "internal-task", "description": "d",
		"config_source": {"provider": "circleci",
		                   "file_path": "circleci-agents/configs/example/config.yml"},
		"checkout_source": {"provider": "github_app", "repo": {"external_id": "123456"}}
	}`)

	if got.status != http.StatusOK {
		t.Fatalf("answered %d, want 200: %s", got.status, got.body)
	}

	configSource, _ := got.decoded["config_source"].(map[string]any)
	if configSource["provider"] != "circleci" {
		t.Errorf("config_source.provider = %v, want circleci", configSource["provider"])
	}
	if _, present := configSource["repo"]; present {
		t.Errorf("config_source carries repo = %v for a circleci-hosted config source, want it omitted", configSource["repo"])
	}

	if stored := len(api.definitions); stored != 1 {
		t.Errorf("the fake stored %d definitions, want 1", stored)
	}
}

// TestFakePipelineDefAPIAnswersTheStatusesTheRealRouteAnswers pins the fake's
// singular-route statuses to what was measured over the network against
// circleci.com on 2026-08-21, one probe per row.
//
// This test exists because the fake was wrong here in the same direction as the
// code and the tests: it answered 404 for a missing definition on GET, PATCH and
// DELETE, so the suite was green while the provider was permanently broken
// against the real 400. A fake that agrees with a mistaken belief cannot catch
// it, so the belief itself is the assertion.
func TestFakePipelineDefAPIAnswersTheStatusesTheRealRouteAnswers(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	base := host + "/api/v2/projects/" + fakePipelineProjectID + "/pipeline-definitions"
	const liveID = "11111111-2222-3333-4444-000000000001"

	created := postJSON(t, base, `{
		"name": "pipe-1", "description": "d",
		"config_source": {"provider": "github_app", "file_path": ".circleci/config.yml",
		                   "repo": {"external_id": "100001"}},
		"checkout_source": {"provider": "github_app", "repo": {"external_id": "100002"}}
	}`)
	if created.status != http.StatusOK {
		t.Fatalf("create answered %d, want 200: %s", created.status, created.body)
	}

	const updateBody = `{"name":"pipe-1","description":"d",
		"config_source":{"file_path":".circleci/config.yml"},
		"checkout_source":{"provider":"github_app","repo":{"external_id":"100002"}}}`

	cases := []struct {
		name        string
		method      string
		url         string
		body        string
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "GET a definition that never existed, under a real project",
			method:      http.MethodGet,
			url:         base + "/99999999-9999-9999-9999-999999999999",
			wantStatus:  http.StatusBadRequest,
			wantMessage: "Failed to get pipeline definition.",
		},
		{
			name:        "GET a real definition id under the wrong project",
			method:      http.MethodGet,
			url:         host + "/api/v2/projects/" + fakePipelineOtherProjectID + "/pipeline-definitions/" + liveID,
			wantStatus:  http.StatusBadRequest,
			wantMessage: "Failed to get pipeline definition.",
		},
		{
			name:        "PATCH a definition that is not there",
			method:      http.MethodPatch,
			url:         base + "/99999999-9999-9999-9999-999999999999",
			body:        updateBody,
			wantStatus:  http.StatusBadRequest,
			wantMessage: "Failed to update pipeline definition.",
		},
		{
			name:        "DELETE a definition that is not there is idempotent, not an absence error",
			method:      http.MethodDelete,
			url:         base + "/99999999-9999-9999-9999-999999999999",
			wantStatus:  http.StatusOK,
			wantMessage: "Pipeline definition deleted.",
		},
		{
			name:       "LIST a real project returns 200",
			method:     http.MethodGet,
			url:        base,
			wantStatus: http.StatusOK,
		},
		{
			name:        "LIST a project the API has never heard of returns 404",
			method:      http.MethodGet,
			url:         host + "/api/v2/projects/" + fakePipelineOtherProjectID + "/pipeline-definitions",
			wantStatus:  http.StatusNotFound,
			wantMessage: "Project not found.",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := sendJSON(t, testCase.method, testCase.url, testCase.body)

			if got.status != testCase.wantStatus {
				t.Errorf("answered %d, want %d: %s", got.status, testCase.wantStatus, got.body)
			}
			if testCase.wantMessage == "" {
				return
			}
			if message, _ := got.decoded["message"].(string); message != testCase.wantMessage {
				t.Errorf("message = %q, want %q", message, testCase.wantMessage)
			}
		})
	}

	// The list must not carry a next_page_token: measured, this route returns
	// every definition in one body on every integration, which is what makes it
	// usable as the oracle for "is this definition still there".
	list := sendJSON(t, http.MethodGet, base, "")
	if _, paginated := list.decoded["next_page_token"]; paginated {
		t.Errorf("the list body carries next_page_token (%s); a paginated list could omit a live "+
			"definition and make the client report it as gone", list.body)
	}

	// The GitLab case: a live, listed definition that the singular route still
	// refuses. Both halves have to hold, or the fake is not reproducing it.
	api.setSingularUnservable(fakePipelineProjectID, true)

	single := sendJSON(t, http.MethodGet, base+"/"+liveID, "")
	if single.status != http.StatusBadRequest {
		t.Errorf("singular GET answered %d, want 400 for an unservable project: %s", single.status, single.body)
	}
	if message, _ := single.decoded["message"].(string); message != "Failed to get pipeline definition." {
		t.Errorf("singular GET message = %q, want the same body a deleted definition gets — the "+
			"indistinguishability is the point", message)
	}

	stillListed := sendJSON(t, http.MethodGet, base, "")
	if !strings.Contains(stillListed.body, liveID) {
		t.Errorf("the list no longer carries %s (%s); the GitLab case is a definition that EXISTS while "+
			"the singular route refuses it", liveID, stillListed.body)
	}
}

// TestPipelineResourceSchema_CreatedAtDescribesImplicitAbsence pins the fix to
// created_at's documentation: it used to unconditionally promise "The
// timestamp when the pipeline was created", which is simply false for an
// implicit pipeline definition — one CircleCI creates automatically for an
// OAuth-backed project — since the API never assigns one a created_at at all
// (see circleci.PipelineDefinition.CreatedAt, and
// TestPipelineResourceUnit_ImplicitDefinitionImport below for the behaviour
// this describes). This resource never creates an implicit definition itself,
// but `terraform import` can still bring one under management, because the
// singular pipeline-definition route serves it.
//
// This test fails without that documentation fix and passes with it; it does
// not (and should not) require changing how created_at is decoded, because an
// empty Computed string is an acceptable representation of "absent" here —
// see the test below for why: no diff and no inconsistent-result error
// follows from it.
func TestPipelineResourceSchema_CreatedAtDescribesImplicitAbsence(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwresource.SchemaResponse{}
	(&pipelineResource{}).Schema(ctx, fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}

	attr, ok := resp.Schema.Attributes["created_at"]
	if !ok {
		t.Fatal("schema is missing the created_at attribute")
	}

	description := attr.GetMarkdownDescription()
	if !strings.Contains(description, "implicit") {
		t.Errorf("created_at description = %q, does not say anything about implicit pipeline "+
			"definitions, which never have one", description)
	}
	if !strings.Contains(strings.ToLower(description), "import") {
		t.Errorf("created_at description = %q, does not say how a practitioner can end up managing "+
			"an implicit definition (terraform import) despite this resource never creating one", description)
	}
}

// TestPipelineResourceUnit_ImplicitDefinitionImport is the behavioural half of
// the created_at fix: it imports a definition the fake seeds directly (never
// created through this resource, exactly like an implicit pipeline
// definition — see fakePipelineDefAPI.seedImplicitDefinition), and asserts
// what happens to created_at across that import and the plan right after it.
//
// Measured over the network (pipeline_definition.go's PipelineDefinition.CreatedAt
// doc): an implicit definition's created_at is permanently absent, on every
// read, forever — not absent-then-later-populated. So the empty string this
// decodes to is stable rather than drifting, and the two things that WOULD make
// an empty string wrong here — a permanent diff, or a "provider produced
// inconsistent result after apply" error — do not occur. Reverting the schema
// description fix does not make this test fail (the description is not
// checked here, see the test above for that); this test exists to pin the
// behaviour the description now truthfully describes, on a path
// (import-an-implicit-definition) no other test in this file reaches.
func TestPipelineResourceUnit_ImplicitDefinitionImport(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	const implicitID = "99999999-8888-7777-6666-555555555555"
	api.seedImplicitDefinition(fakePipelineProjectID, implicitID, "github_app", "100001")

	// Matches exactly what seedImplicitDefinition stored — including
	// config_source_file_path, which pipelineFakeResourceConfig hardcodes to a
	// different value ("config.yml") and so cannot be reused here without
	// producing an unrelated diff on that field.
	config := pipelineFakeProviderConfig(host, "cloud") + fmt.Sprintf(`
resource "circleci_pipeline" "test" {
  project_id                       = %[1]q
  name                              = "project-1"
  description                       = "Implicit pipeline definition associated with an OAuth-based project."
  config_source_provider            = "github_app"
  config_source_file_path           = ".circleci/config.yml"
  config_source_repo_external_id    = "100001"
  checkout_source_provider          = "github_app"
  checkout_source_repo_external_id  = "100001"
}
`, fakePipelineProjectID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Bring the never-created-by-Terraform definition under
				// management. ImportStatePersist is required to carry the
				// imported state into the next step — see
				// TestAccIOSSigningCertificateResource_ImportForcesReplacement's
				// comment in ios_signing_certificate_resource_test.go.
				ResourceName:       "circleci_pipeline.test",
				ImportState:        true,
				ImportStateId:      fakePipelineProjectID + "/" + implicitID,
				ImportStatePersist: true,
				Config:             config,
				ConfigStateChecks: []statecheck.StateCheck{
					// Absent from the wire decodes to an empty string, not an
					// error and not some other placeholder.
					statecheck.ExpectKnownValue("circleci_pipeline.test", tfjsonpath.New("created_at"), knownvalue.StringExact("")),
				},
			},
			{
				// Nothing about the definition changed, so a plan immediately
				// after import must be empty. A diff here would mean the
				// empty created_at is not the stable value the doc now
				// describes it as.
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}
