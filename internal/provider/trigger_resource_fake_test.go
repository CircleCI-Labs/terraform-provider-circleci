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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// This file backs `circleci_trigger` (trigger_resource.go) and its singular
// data source (trigger_data_source.go) with an in-process stand-in for the
// public API, so their CRUD paths run without TF_ACC or credentials.
//
// Routes and field names come from internal/circleci's trigger methods
// (internal/circleci/trigger.go), cross-checked against the API's schema and
// handler behaviour. Notably: Get/Update/Delete hit
// /projects/{project_id}/triggers/{trigger_id} (no pipeline-definitions
// segment), while only Create/List do.
//
// Before the SDK migration, trigger_resource.go had an
// isApiNotFoundError helper that decided "not found" by string-matching "404"
// (or "not found") anywhere in err.Error(), rather than inspecting an actual
// status code — so a 500 whose body happened to mention "404" was
// misclassified as "the trigger doesn't exist", silently dropping a live
// trigger from state. That helper is gone; Read now uses circleci.IsNotFound,
// which checks the real HTTP status. See
// TestTriggerResourceUnit_ServerErrorMentioning404DoesNotRemoveFromState below.

// --- fake trigger API ---

type fakeTriggerAPI struct {
	t *testing.T

	mu       sync.Mutex
	triggers map[string]map[string]any // "projectID/triggerID"
	nextID   int
	requests []fakeRecordedRequest

	// failStatus/failBody, when failStatus is non-zero, make every request
	// against an existing trigger answer with that response instead of the
	// normal behavior.
	failStatus int
	failBody   string
}

func newFakeTriggerAPI(t *testing.T) (*fakeTriggerAPI, string) {
	t.Helper()

	api := &fakeTriggerAPI{triggers: map[string]map[string]any{}, t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/projects/{projectID}/pipeline-definitions/{pipelineID}/triggers", api.create)
	mux.HandleFunc("GET /api/v2/projects/{projectID}/triggers/{triggerID}", api.get)
	mux.HandleFunc("PATCH /api/v2/projects/{projectID}/triggers/{triggerID}", api.update)
	mux.HandleFunc("DELETE /api/v2/projects/{projectID}/triggers/{triggerID}", api.delete)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"unrecognized route: `+r.Method+" "+r.URL.Path+`"}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return api, srv.URL
}

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()

	body := map[string]any{}
	raw, _ := io.ReadAll(r.Body)
	if len(raw) == 0 {
		return body
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Errorf("fake trigger API: request body is not JSON: %v (%s)", err, raw)
	}

	return body
}

// resolveActorID fakes the API's resolution of the "system"/"current" alias to
// a concrete actor id, deterministically.
func resolveActorID(alias string) string {
	switch alias {
	case "system":
		return "00000000-aaaa-aaaa-aaaa-000000000001"
	case "current":
		return "00000000-bbbb-bbbb-bbbb-000000000002"
	default:
		return ""
	}
}

func (a *fakeTriggerAPI) record(r *http.Request, body map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, fakeRecordedRequest{Method: r.Method, Path: r.URL.Path, Body: body})
}

// checkFail answers with the configured failure, if one is armed, and reports
// whether it did.
func (a *fakeTriggerAPI) checkFail(w http.ResponseWriter) bool {
	a.mu.Lock()
	status, body := a.failStatus, a.failBody
	a.mu.Unlock()

	if status == 0 {
		return false
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)

	return true
}

// The fields each write route accepts, exactly as the real request structs
// declare them — see the comment on rejectUnexpectedFields.
//
// createTriggerFields covers the create route's request body;
// updateTriggerFields covers the update route's. The difference that matters
// most is event_source.repo: create has it, update does not, so a trigger's
// event source repository cannot be changed. Sending it anyway is a 400, not
// a no-op.
var (
	createTriggerFields = map[string][]string{
		"":                     {"name", "description", "event_source", "event_preset", "checkout_ref", "config_ref", "parameters", "event_name", "disabled"},
		"event_source":         {"provider", "repo", "webhook", "schedule"},
		"event_source.repo":    {"external_id"},
		"event_source.webhook": {"sender"},
	}

	updateTriggerFields = map[string][]string{
		"":                     {"name", "description", "event_source", "event_preset", "checkout_ref", "config_ref", "parameters", "event_name", "disabled"},
		"event_source":         {"provider", "webhook", "schedule"},
		"event_source.webhook": {"sender"},
	}
)

// rejectUnexpectedFields answers HTTP 400 for any key the real handler would not
// bind, and reports whether it did.
//
// This is the single most important thing this fake does, and it was missing.
// The API does not decode request bodies leniently: an unrecognised key
// answers "Unexpected field '<name>'." rather than being silently dropped —
// which is exactly the failure mode that let six client/fake pairs be wrong in
// the same way and still pass. A fake that ignores unknown keys cannot tell a
// correct field name from a wrong one.
//
// event_source.schedule is deliberately absent from the field maps: the real
// handler types it as map[string]any, so anything inside it binds.
func rejectUnexpectedFields(w http.ResponseWriter, body map[string]any, allowed map[string][]string) bool {
	var walk func(prefix string, object map[string]any) string

	walk = func(prefix string, object map[string]any) string {
		permitted, known := allowed[prefix]
		if !known {
			// No entry means the real struct holds a free-form map here, so every
			// key binds and nothing nested needs checking.
			return ""
		}

		for key, value := range object {
			if !slices.Contains(permitted, key) {
				if prefix == "" {
					return key
				}

				return prefix + "." + key
			}

			nested, isObject := value.(map[string]any)
			if !isObject {
				continue
			}

			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if unexpected := walk(path, nested); unexpected != "" {
				return unexpected
			}
		}

		return ""
	}

	unexpected := walk("", body)
	if unexpected == "" {
		return false
	}

	w.WriteHeader(http.StatusBadRequest)
	_, _ = io.WriteString(w, `{"message":"Unexpected field '`+unexpected+`'."}`)

	return true
}

// rejectInvalidCreate answers HTTP 400 for the create bodies the real API
// refuses, and reports whether it did.
//
// Every rule here is enforced by the real API, and every one of them was
// previously absent — so the suite asserted that requests the real API
// rejects are fine:
//
//   - a repository-backed event source with no repo at all. The API rejects
//     this for github_app, github_server and github_oauth alike with a bare
//     "bad request". github_oauth was the live case: the provider never sent
//     a repo for it.
//   - a repository external id that is not a number, which the API also
//     rejects.
//   - a webhook or schedule trigger with no checkout_ref or config_ref, which
//     the API rejects with a message naming the missing ref.
//
// The conditional GitHub rule is NOT modelled: for github_app and github_server a
// ref is required when the event source repository differs from the pipeline
// definition's and forbidden when it matches, which needs the definition's own
// repositories. The fake does not store definitions, so a github_app trigger with
// refs is accepted here and may be a 400 in production. That is a known gap, not
// an assertion that it is allowed.
func rejectInvalidCreate(w http.ResponseWriter, body map[string]any) bool {
	fail := func(message string) bool {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":`+strconv.Quote(message)+`}`)

		return true
	}

	eventSource, _ := body["event_source"].(map[string]any)
	provider, _ := eventSource["provider"].(string)

	if slices.Contains(circleci.TriggerRepoEventSourceProviders(), provider) {
		repo, ok := eventSource["repo"].(map[string]any)
		if !ok {
			return fail("bad request")
		}

		externalID, _ := repo["external_id"].(string)
		if _, err := strconv.ParseInt(externalID, 10, 64); err != nil {
			return fail("bad request")
		}
	}

	if circleci.TriggerProviderRequiresRefs(provider) {
		if asStringOrEmpty(body["checkout_ref"]) == "" {
			return fail("Checkout ref must be provided.")
		}
		if asStringOrEmpty(body["config_ref"]) == "" {
			return fail("Config ref must be provided.")
		}
	}

	return false
}

func (a *fakeTriggerAPI) create(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(a.t, r)
	a.record(r, body)

	w.Header().Set("Content-Type", "application/json")
	if a.checkFail(w) {
		return
	}

	if rejectUnexpectedFields(w, body, createTriggerFields) {
		return
	}
	if rejectInvalidCreate(w, body) {
		return
	}

	projectID := r.PathValue("projectID")

	a.mu.Lock()
	a.nextID++
	id := fmt.Sprintf("22222222-3333-4444-5555-%012d", a.nextID)
	record := a.buildRecord(id, body)
	a.triggers[projectID+"/"+id] = record
	a.mu.Unlock()

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(record)
}

// buildRecord turns a create/update request body into the stored
// TriggerResponse shape, resolving what the real API resolves (repo full
// names, webhook URLs, schedule attribution actors).
func (a *fakeTriggerAPI) buildRecord(id string, body map[string]any) map[string]any {
	eventSource, _ := body["event_source"].(map[string]any)
	provider, _ := eventSource["provider"].(string)

	outSource := map[string]any{"provider": provider}

	if repo, ok := eventSource["repo"].(map[string]any); ok {
		externalID, _ := repo["external_id"].(string)
		if externalID != "" {
			outSource["repo"] = map[string]any{
				"full_name":   resolveFullName(externalID),
				"external_id": externalID,
			}
		}
	}
	if webhook, ok := eventSource["webhook"].(map[string]any); ok {
		sender, _ := webhook["sender"].(string)
		outSource["webhook"] = map[string]any{
			"url":    "https://webhook.circleci.com/hooks/" + id + "?secret=fake-secret",
			"sender": sender,
		}
	}
	if schedule, ok := eventSource["schedule"].(map[string]any); ok {
		cron, _ := schedule["cron_expression"].(string)
		actorAlias, _ := schedule["attribution_actor"].(string)
		outSource["schedule"] = map[string]any{
			"cron_expression":   cron,
			"attribution_actor": map[string]any{"id": resolveActorID(actorAlias)},
		}
	}

	disabled, _ := body["disabled"].(bool)

	record := map[string]any{
		"id":           id,
		"created_at":   "2024-06-01T00:00:00.000Z",
		"checkout_ref": asStringOrEmpty(body["checkout_ref"]),
		"config_ref":   asStringOrEmpty(body["config_ref"]),
		"event_source": outSource,
		"event_name":   asStringOrEmpty(body["event_name"]),
		"event_preset": asStringOrEmpty(body["event_preset"]),
		"disabled":     disabled,
	}
	if params, ok := body["parameters"].(map[string]any); ok {
		record["parameters"] = params
	}

	return record
}

func asStringOrEmpty(v any) string {
	s, _ := v.(string)

	return s
}

func (a *fakeTriggerAPI) get(w http.ResponseWriter, r *http.Request) {
	a.record(r, nil)

	w.Header().Set("Content-Type", "application/json")
	if a.checkFail(w) {
		return
	}

	key := r.PathValue("projectID") + "/" + r.PathValue("triggerID")

	a.mu.Lock()
	record, ok := a.triggers[key]
	a.mu.Unlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Trigger not found."}`)

		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(record)
}

func (a *fakeTriggerAPI) update(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(a.t, r)
	a.record(r, body)

	w.Header().Set("Content-Type", "application/json")
	if a.checkFail(w) {
		return
	}

	if rejectUnexpectedFields(w, body, updateTriggerFields) {
		return
	}

	key := r.PathValue("projectID") + "/" + r.PathValue("triggerID")

	a.mu.Lock()
	defer a.mu.Unlock()

	record, ok := a.triggers[key]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Trigger not found."}`)

		return
	}

	// Only the fields the real update route accepts are applied.
	// event_source.provider is one of them and changes nothing;
	// event_source.repo is NOT — the update route has no repo field, and the
	// API answers 400 "Unexpected field 'event_source.repo'." for any key it
	// cannot bind, so sending it is an error rather than a silent no-op. This
	// comment previously said the opposite, and the fake behaved the way the
	// comment described; rejectUnexpectedFields above is what makes the two
	// agree with production.
	if v, ok := body["checkout_ref"]; ok {
		record["checkout_ref"] = v
	}
	if v, ok := body["config_ref"]; ok {
		record["config_ref"] = v
	}
	if v, ok := body["event_name"]; ok {
		record["event_name"] = v
	}
	if v, ok := body["event_preset"]; ok {
		record["event_preset"] = v
	}
	if v, ok := body["disabled"]; ok {
		record["disabled"] = v
	}
	if v, ok := body["parameters"]; ok {
		record["parameters"] = v
	}
	if eventSource, ok := body["event_source"].(map[string]any); ok {
		outSource, _ := record["event_source"].(map[string]any)
		if webhook, ok := eventSource["webhook"].(map[string]any); ok {
			sender, _ := webhook["sender"].(string)
			existingWebhook, _ := outSource["webhook"].(map[string]any)
			if existingWebhook == nil {
				existingWebhook = map[string]any{}
			}
			existingWebhook["sender"] = sender
			outSource["webhook"] = existingWebhook
		}
		if schedule, ok := eventSource["schedule"].(map[string]any); ok {
			existingSchedule, _ := outSource["schedule"].(map[string]any)
			if existingSchedule == nil {
				existingSchedule = map[string]any{}
			}
			if cron, ok := schedule["cron_expression"].(string); ok {
				existingSchedule["cron_expression"] = cron
			}
			if actorAlias, ok := schedule["attribution_actor"].(string); ok {
				existingSchedule["attribution_actor"] = map[string]any{"id": resolveActorID(actorAlias)}
			}
			outSource["schedule"] = existingSchedule
		}
		record["event_source"] = outSource
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(record)
}

func (a *fakeTriggerAPI) delete(w http.ResponseWriter, r *http.Request) {
	a.record(r, nil)

	w.Header().Set("Content-Type", "application/json")
	if a.checkFail(w) {
		return
	}

	key := r.PathValue("projectID") + "/" + r.PathValue("triggerID")

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.triggers[key]; !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Trigger not found."}`)

		return
	}

	delete(a.triggers, key)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"message":"ok"}`)
}

func (a *fakeTriggerAPI) setFail(status int, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.failStatus = status
	a.failBody = body
}

func (a *fakeTriggerAPI) clearFail() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.failStatus = 0
	a.failBody = ""
}

func (a *fakeTriggerAPI) recorded() []fakeRecordedRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]fakeRecordedRequest(nil), a.requests...)
}

func (a *fakeTriggerAPI) lastRequest(t *testing.T, method, path string) fakeRecordedRequest {
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

const (
	fakeTriggerProjectID  = "cccccccc-1111-2222-3333-444444444444"
	fakeTriggerPipelineID = "dddddddd-1111-2222-3333-444444444444"
)

func triggerFakeProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host       = %q
  key        = "fake-token"
  deployment = "cloud"
}
`, host)
}

// --- github_app provider: CRUD and import ---

func triggerFakeGithubAppConfig(host, externalID, preset string, disabled bool) string {
	// checkout_ref/config_ref are set explicitly here. Leaving them unset is valid
	// for github_app, and Update now maps the API's "" back to null so that case
	// round-trips; this fixture pins the set-explicitly path instead.
	return triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id                        = %[1]q
  pipeline_id                       = %[2]q
  event_source_provider             = "github_app"
  event_source_repo_external_id     = %[3]q
  event_preset                      = %[4]q
  disabled                          = %[5]t
  checkout_ref                      = "main"
  config_ref                        = "main"
}
`, fakeTriggerProjectID, fakeTriggerPipelineID, externalID, preset, disabled)
}

func TestTriggerResourceUnit_GithubAppCRUD(t *testing.T) {
	api, host := newFakeTriggerAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: triggerFakeGithubAppConfig(host, "1234", "all-pushes", false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_source_provider"), knownvalue.StringExact("github_app")),
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_source_repo_full_name"), knownvalue.StringExact(resolveFullName("1234"))),
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_preset"), knownvalue.StringExact("all-pushes")),
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("disabled"), knownvalue.Bool(false)),
				},
			},
			{
				// Toggle disabled in place; must PATCH rather than replace.
				Config: triggerFakeGithubAppConfig(host, "1234", "all-pushes", true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("disabled"), knownvalue.Bool(true)),
				},
			},
			{
				ResourceName: "circleci_trigger.test",
				ImportState:  true,
				// The import id carries the pipeline definition id as its middle
				// segment, because the API never returns it: a trigger is created
				// under a definition but read under the project, and the response has
				// no reference back. pipeline_id therefore round-trips only because
				// the practitioner supplies it here.
				ImportStateVerify: true,
				ImportStateIdFunc: triggerImportID(),
			},
		},
	})

	create := api.lastRequest(t, "POST", "/api/v2/projects/"+fakeTriggerProjectID+"/pipeline-definitions/"+fakeTriggerPipelineID+"/triggers")
	eventSource, _ := create.Body["event_source"].(map[string]any)
	if eventSource["provider"] != "github_app" {
		t.Errorf("create event_source.provider = %v, want github_app", eventSource["provider"])
	}
	repo, _ := eventSource["repo"].(map[string]any)
	if repo["external_id"] != "1234" {
		t.Errorf("create event_source.repo.external_id = %v, want 1234", repo["external_id"])
	}

	update := api.lastRequest(t, "PATCH", "/api/v2/projects/"+fakeTriggerProjectID+"/triggers/22222222-3333-4444-5555-000000000001")
	if update.Body["disabled"] != true {
		t.Errorf("update body disabled = %v, want true", update.Body["disabled"])
	}
}

// TestTriggerResourceUnit_CreateDoesNotNeedASecondReadToPopulateState is a
// regression test for issue #6.
//
// Create used to follow a successful CreateTrigger with a GetTrigger call
// made for no reason but to pick up CreatedAt — even though CreateTrigger
// "returns it as stored" (see that method's doc comment in
// internal/circleci/trigger.go): the create response is already a full
// Trigger, the same shape GetTrigger returns, so CreatedAt was sitting right
// there unused. Because that extra call happened *before* resp.State.Set,
// its failure returned early and left a trigger the API had already created
// with no record in Terraform state at all — the next apply would try to
// create it again. The fix trusts the create response directly and makes no
// such call.
//
// This is checked by counting requests rather than by failing a follow-up
// read, because after the fix there is no follow-up read left to fail. A
// single create-and-verify test step should produce exactly one POST (the
// create) and exactly one GET (the plugin-testing framework's own
// post-apply refresh, which confirms the plan comes back empty) — not two
// GETs, which is what the extra internal GetTrigger call used to add.
func TestTriggerResourceUnit_CreateDoesNotNeedASecondReadToPopulateState(t *testing.T) {
	api, host := newFakeTriggerAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: triggerFakeGithubAppConfig(host, "1234", "all-pushes", false),
				ConfigStateChecks: []statecheck.StateCheck{
					// The value the fake's create response carries (see buildRecord),
					// proving created_at came from the create response and not from a
					// second call.
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("created_at"), knownvalue.StringExact("2024-06-01T00:00:00.000Z")),
				},
			},
		},
	})

	var creates, gets int
	for _, req := range api.recorded() {
		switch req.Method {
		case http.MethodPost:
			creates++
		case http.MethodGet:
			gets++
		}
	}
	if creates != 1 {
		t.Errorf("saw %d create requests, want exactly 1: %+v", creates, api.recorded())
	}
	if gets != 1 {
		t.Errorf("saw %d GET requests for one create-and-verify step, want exactly 1 (the "+
			"framework's own post-apply refresh); Create must not make its own follow-up "+
			"read to populate state: %+v", gets, api.recorded())
	}
}

// fakeTriggerOtherProjectID is a second project id, used only by
// TestTriggerResourceUnit_ProjectIDChangeForcesReplacement.
const fakeTriggerOtherProjectID = "cccccccc-9999-8888-7777-444444444444"

// TestTriggerResourceUnit_ProjectIDChangeForcesReplacement is a regression test
// for a defect this review found: project_id had no RequiresReplace, unlike
// circleci_pipeline_definition's identically-shaped project_id (see that
// schema's comment). Both UpdateTrigger and DeleteTrigger address
// a trigger as /projects/{project_id}/triggers/{trigger_id} — see
// triggerRoute in internal/circleci/trigger.go — so a changed project_id used
// to plan an in-place Update whose PATCH would land on the *new* project
// carrying the *old* (and, there, nonexistent) trigger id.
func TestTriggerResourceUnit_ProjectIDChangeForcesReplacement(t *testing.T) {
	api, host := newFakeTriggerAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: triggerFakeGithubAppConfig(host, "1234", "all-pushes", false)},
			{
				Config: triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id                    = %[1]q
  pipeline_id                   = %[2]q
  event_source_provider         = "github_app"
  event_source_repo_external_id = "1234"
  event_preset                  = "all-pushes"
  checkout_ref                  = "main"
  config_ref                    = "main"
}
`, fakeTriggerOtherProjectID, fakeTriggerPipelineID),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_trigger.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("project_id"), knownvalue.StringExact(fakeTriggerOtherProjectID)),
				},
			},
		},
	})

	// The new trigger was created under the new project, and the old one under
	// the original project was deleted — never a PATCH against the wrong
	// project.
	create := api.lastRequest(t, "POST", "/api/v2/projects/"+fakeTriggerOtherProjectID+"/pipeline-definitions/"+fakeTriggerPipelineID+"/triggers")
	if create.Method == "" {
		t.Fatal("expected a create against the new project")
	}
	del := api.lastRequest(t, "DELETE", "/api/v2/projects/"+fakeTriggerProjectID+"/triggers/22222222-3333-4444-5555-000000000001")
	if del.Method == "" {
		t.Fatal("expected the trigger under the original project to be deleted")
	}
}

// TestTriggerResourceUnit_EventSourceProviderChangeForcesReplacement is a
// regression test for a defect this review found: event_source_provider had
// no RequiresReplace, unlike circleci_pipeline_definition's analogous
// config_source_provider. UpdateTriggerEventSourceInput (see
// internal/circleci/trigger.go) carries no repo field at all — matching the
// update route exactly — so an event source's provider, and the repo it
// carries, is immutable after creation. Before the
// fix, switching from github_app to github_server planned an in-place Update
// whose PATCH could not carry a repo at all, silently leaving the trigger's
// actual event source (provider and repository) untouched on the server.
func TestTriggerResourceUnit_EventSourceProviderChangeForcesReplacement(t *testing.T) {
	api, host := newFakeTriggerAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: triggerFakeGithubAppConfig(host, "1234", "only-build-prs", false)},
			{
				Config: triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id                    = %[1]q
  pipeline_id                   = %[2]q
  event_source_provider         = "github_server"
  event_source_repo_external_id = "1234"
  event_preset                  = "only-build-prs"
  checkout_ref                  = "main"
  config_ref                    = "main"
}
`, fakeTriggerProjectID, fakeTriggerPipelineID),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_trigger.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_source_provider"), knownvalue.StringExact("github_server")),
				},
			},
		},
	})

	del := api.lastRequest(t, "DELETE", "/api/v2/projects/"+fakeTriggerProjectID+"/triggers/22222222-3333-4444-5555-000000000001")
	if del.Method == "" {
		t.Fatal("expected the github_app trigger to be deleted rather than PATCHed to github_server")
	}
}

// TestTriggerResourceUnit_EventSourceRepoExternalIdChangeForcesReplacement is a
// regression test for a defect this review found: event_source_repo_external_id
// had no RequiresReplace, but a trigger's event source repository is immutable
// after creation (see event_source_provider's schema comment, and
// UpdateTriggerEventSourceInput in internal/circleci/trigger.go, which has no
// repo field). Before the fix, changing the repository's external id planned
// an in-place Update whose PATCH could not carry a repo at all, so the change
// was silently discarded — the resource's plan promised the new id, but
// nothing on the server, and nothing this provider sent, could ever make that
// true, which the plugin framework's own consistency check turns into a
// confusing "Provider produced inconsistent result after apply" rather than a
// clean plan-time replacement.
func TestTriggerResourceUnit_EventSourceRepoExternalIdChangeForcesReplacement(t *testing.T) {
	api, host := newFakeTriggerAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: triggerFakeGithubAppConfig(host, "1234", "all-pushes", false)},
			{
				Config: triggerFakeGithubAppConfig(host, "5678", "all-pushes", false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_trigger.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_source_repo_external_id"), knownvalue.StringExact("5678")),
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_source_repo_full_name"), knownvalue.StringExact(resolveFullName("5678"))),
				},
			},
		},
	})

	create := api.lastRequest(t, "POST", "/api/v2/projects/"+fakeTriggerProjectID+"/pipeline-definitions/"+fakeTriggerPipelineID+"/triggers")
	eventSource, _ := create.Body["event_source"].(map[string]any)
	repo, _ := eventSource["repo"].(map[string]any)
	if repo["external_id"] != "5678" {
		t.Errorf("replacement create event_source.repo.external_id = %v, want 5678", repo["external_id"])
	}
}

// The per-provider validation tests that used to sit here — a github_app trigger
// with no repository id, one with an unrecognized event_preset, and an
// unrecognized event_source_provider — are now in trigger_validation_test.go's
// table. They moved rather than multiplied: the rules run at plan time now (issue
// #30), and that table asserts *when* each one fires and that no request reaches
// the API, which these could not.

// --- webhook provider: CRUD ---

func triggerFakeWebhookConfig(host, eventName, sender string) string {
	return triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id                     = %[1]q
  pipeline_id                     = %[2]q
  event_source_provider          = "webhook"
  event_name                     = %[3]q
  event_source_web_hook_sender   = %[4]q
  # Both refs are required for a webhook event source: an inbound POST carries no
  # ref, so there is nothing to inherit and the API rejects a create
  # that omits either.
  checkout_ref                   = "main"
  config_ref                     = "main"
}
`, fakeTriggerProjectID, fakeTriggerPipelineID, eventName, sender)
}

func TestTriggerResourceUnit_WebhookCRUD(t *testing.T) {
	api, host := newFakeTriggerAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: triggerFakeWebhookConfig(host, "deploy-hook", "datadog"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_source_provider"), knownvalue.StringExact("webhook")),
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_name"), knownvalue.StringExact("deploy-hook")),
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_source_web_hook_sender"), knownvalue.StringExact("datadog")),
				},
			},
			{
				ResourceName:      "circleci_trigger.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: triggerImportID(),
			},
		},
	})

	create := api.lastRequest(t, "POST", "/api/v2/projects/"+fakeTriggerProjectID+"/pipeline-definitions/"+fakeTriggerPipelineID+"/triggers")
	if create.Body["event_name"] != "deploy-hook" {
		t.Errorf("create event_name = %v, want deploy-hook", create.Body["event_name"])
	}
	eventSource, _ := create.Body["event_source"].(map[string]any)
	webhook, _ := eventSource["webhook"].(map[string]any)
	if webhook["sender"] != "datadog" {
		t.Errorf("create event_source.webhook.sender = %v, want datadog", webhook["sender"])
	}
}

// --- schedule provider: CRUD and parameters ---

func triggerFakeScheduleConfig(host, eventName, cron, actor string, parameters map[string]string) string {
	paramsBlock := ""
	if parameters != nil {
		paramsBlock = "  parameters = {\n"
		for k, v := range parameters {
			paramsBlock += fmt.Sprintf("    %s = %q\n", k, v)
		}
		paramsBlock += "  }\n"
	}

	return triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id                                = %[1]q
  pipeline_id                                = %[2]q
  event_source_provider                      = "schedule"
  event_name                                 = %[3]q
  checkout_ref                               = "main"
  config_ref                                 = "main"
  event_source_schedule_cron_expression      = %[4]q
  event_source_schedule_attribution_actor    = %[5]q
%[6]s}
`, fakeTriggerProjectID, fakeTriggerPipelineID, eventName, cron, actor, paramsBlock)
}

func TestTriggerResourceUnit_ScheduleCRUD(t *testing.T) {
	api, host := newFakeTriggerAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: triggerFakeScheduleConfig(host, "nightly", "0 0 * * *", "system", map[string]string{"deploy_env": "staging"}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_source_provider"), knownvalue.StringExact("schedule")),
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_source_schedule_cron_expression"), knownvalue.StringExact("0 0 * * *")),
					// The alias must be preserved verbatim, not the resolved actor
					// UUID the fake's response carries — otherwise every refresh
					// would show a spurious diff.
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_source_schedule_attribution_actor"), knownvalue.StringExact("system")),
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("parameters"), knownvalue.MapExact(map[string]knownvalue.Check{
						"deploy_env": knownvalue.StringExact("staging"),
					})),
				},
			},
			{
				// Update the cron expression and parameters in place.
				Config: triggerFakeScheduleConfig(host, "nightly", "0 6 * * *", "system", map[string]string{"deploy_env": "production"}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_source_schedule_cron_expression"), knownvalue.StringExact("0 6 * * *")),
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("parameters"), knownvalue.MapExact(map[string]knownvalue.Check{
						"deploy_env": knownvalue.StringExact("production"),
					})),
				},
			},
			{
				ResourceName: "circleci_trigger.test",
				ImportState:  true,
				// BUG, still live: Read()'s guard for preserving the
				// attribution_actor alias only checks IsNull()/IsUnknown(), which is
				// true right after import (trigger_resource.go's ImportState does not
				// set this attribute — it cannot; the import id carries no actor
				// alias), so the first post-import Read resolves it to the actor UUID
				// instead of restoring the "system"/"current" alias the original
				// configuration used. Confirmed by mutation: deleting this ignore
				// fails with
				//   - "event_source_schedule_attribution_actor": "system"
				//   + "event_source_schedule_attribution_actor": "00000000-aaaa-aaaa-aaaa-000000000001"
				// This is a genuine round-trip gap, not a masked secret: the
				// resource page should tell practitioners that an imported
				// schedule trigger's attribution_actor will read as a UUID rather
				// than the alias, and that re-applying the original alias in
				// configuration is a safe, idempotent no-op server-side.
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"event_source_schedule_attribution_actor",
				},
				ImportStateIdFunc: triggerImportID(),
			},
		},
	})

	create := api.lastRequest(t, "POST", "/api/v2/projects/"+fakeTriggerProjectID+"/pipeline-definitions/"+fakeTriggerPipelineID+"/triggers")
	eventSource, _ := create.Body["event_source"].(map[string]any)
	schedule, _ := eventSource["schedule"].(map[string]any)
	if schedule["attribution_actor"] != "system" {
		t.Errorf("create event_source.schedule.attribution_actor = %v, want bare string \"system\"", schedule["attribution_actor"])
	}
	if create.Body["parameters"] == nil {
		t.Error("create body has no parameters, want deploy_env")
	}
}

// --- the critical bug: a 5xx whose body mentions "404" must not look like a 404 ---

// TestTriggerResourceUnit_ServerErrorMentioning404DoesNotRemoveFromState is the
// trigger-side companion to
// TestPipelineResourceUnit_ServerErrorMentioning404DoesNotDropState.
//
// Before the SDK migration, trigger_resource.go's Read() decided
// "not found" with an isApiNotFoundError helper that string-matched "404" (or
// "not found") anywhere in err.Error(). A 500 whose body happened to mention
// "404" — e.g. while describing an upstream failure — was misclassified as
// "the trigger doesn't exist", so Read silently called RemoveResource on a
// trigger that was very much still there: the live trigger was never actually
// deleted from the API, only from Terraform's state, so the next plan proposed
// recreating it — a real-world duplicate trigger.
//
// Read now uses circleci.IsNotFound, which inspects the actual HTTP status, so
// a 500 must surface as an error diagnostic and the trigger must stay in
// state.
func TestTriggerResourceUnit_ServerErrorMentioning404DoesNotRemoveFromState(t *testing.T) {
	api, host := newFakeTriggerAPI(t)

	cfg := triggerFakeGithubAppConfig(host, "1234", "all-pushes", false)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
			},
			{
				PreConfig: func() {
					api.setFail(http.StatusInternalServerError,
						`{"message":"upstream gateway 404 timeout while resolving actor"}`)
				},
				Config:      cfg,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Error Reading Trigger`),
			},
			{
				// Transient failure over: the trigger must still be managed, and the
				// plan must be empty. A create here would mean state was dropped.
				PreConfig: func() { api.clearFail() },
				Config:    cfg,
			},
		},
	})
}

// --- the fake's own contract: it must refuse what production refuses ---

// TestFakeTriggerAPIRefusesWhatTheRealAPIRefuses pins the two rules that make
// this fake a stand-in for the API rather than a mirror of the client.
//
// It talks to the fake directly, with bodies the provider does not currently
// produce, because that is the only way to assert the fake would *catch* a wrong
// field name. Six real bugs got through a 1400-test suite by being wrong in the
// client and the fake at once; a fake that accepts anything cannot distinguish a
// correct key from a typo, and every test built on it passes either way.
//
// The expectations come from the real API:
//   - it answers 400 "Unexpected field '<name>'." for any key the request
//     struct cannot bind. There is no lenient mode.
//   - the update route has no repo field, so event_source.repo is such a key
//     on PATCH even though it is required on POST.
func TestFakeTriggerAPIRefusesWhatTheRealAPIRefuses(t *testing.T) {
	api, host := newFakeTriggerAPI(t)

	createPath := "/api/v2/projects/" + fakeTriggerProjectID +
		"/pipeline-definitions/" + fakeTriggerPipelineID + "/triggers"
	triggerPath := func(id string) string {
		return "/api/v2/projects/" + fakeTriggerProjectID + "/triggers/" + id
	}

	// A valid create first, to have something to PATCH.
	created := postJSON(t, host+createPath, `{
		"event_source": {"provider": "github_app", "repo": {"external_id": "1234"}},
		"event_preset": "all-pushes"
	}`)
	if created.status != http.StatusOK {
		t.Fatalf("valid create answered %d: %s", created.status, created.body)
	}
	id, _ := created.decoded["id"].(string)
	if id == "" {
		t.Fatalf("valid create returned no id: %s", created.body)
	}

	cases := map[string]struct {
		method, url, body string
		wantMessage       string
	}{
		"a misspelled top-level field on create": {
			method: http.MethodPost, url: host + createPath,
			body: `{
				"event_source": {"provider": "github_app", "repo": {"external_id": "1234"}},
				"checkout-ref": "main"
			}`,
			wantMessage: "Unexpected field 'checkout-ref'.",
		},
		"full_name on a create's repo, which is resolved by the server not supplied": {
			method: http.MethodPost, url: host + createPath,
			body: `{
				"event_source": {
					"provider": "github_app",
					"repo": {"external_id": "1234", "full_name": "acme-org/repo"}
				}
			}`,
			wantMessage: "Unexpected field 'event_source.repo.full_name'.",
		},
		"a repository on update, where the event source is immutable": {
			method: http.MethodPatch, url: host + triggerPath(id),
			body:        `{"event_source": {"provider": "github_app", "repo": {"external_id": "5678"}}}`,
			wantMessage: "Unexpected field 'event_source.repo'.",
		},
		"a webhook trigger with no refs to fall back to": {
			method: http.MethodPost, url: host + createPath,
			body: `{
				"event_source": {"provider": "webhook", "webhook": {"sender": "datadog"}},
				"event_name": "deploy-hook"
			}`,
			wantMessage: "Checkout ref must be provided.",
		},
		"a repository-backed event source with no repository": {
			method: http.MethodPost, url: host + createPath,
			body:        `{"event_source": {"provider": "github_oauth"}, "event_preset": "all-pushes"}`,
			wantMessage: "bad request",
		},
		"a repository name where the API parses a numeric id": {
			method: http.MethodPost, url: host + createPath,
			body: `{
				"event_source": {"provider": "github_app", "repo": {"external_id": "acme-org/repo"}}
			}`,
			wantMessage: "bad request",
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			got := sendJSON(t, testCase.method, testCase.url, testCase.body)

			if got.status != http.StatusBadRequest {
				t.Fatalf("answered %d, want 400: %s", got.status, got.body)
			}
			if message, _ := got.decoded["message"].(string); message != testCase.wantMessage {
				t.Errorf("message = %q, want %q", message, testCase.wantMessage)
			}
		})
	}

	// The valid create is the only request that should have produced a trigger.
	api.mu.Lock()
	stored := len(api.triggers)
	api.mu.Unlock()
	if stored != 1 {
		t.Errorf("the fake stored %d triggers, want 1: a rejected request must not create one", stored)
	}
}

type fakeAPIResponse struct {
	status  int
	body    string
	decoded map[string]any
}

func postJSON(t *testing.T, url, body string) fakeAPIResponse {
	t.Helper()

	return sendJSON(t, http.MethodPost, url, body)
}

func sendJSON(t *testing.T, method, url, body string) fakeAPIResponse {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("building %s %s: %v", method, url, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response to %s %s: %v", method, url, err)
	}

	decoded := map[string]any{}
	_ = json.Unmarshal(raw, &decoded)

	return fakeAPIResponse{status: resp.StatusCode, body: string(raw), decoded: decoded}
}

// triggerImportID builds the three-segment import id a trigger needs:
// "project_id/pipeline_definition_id/trigger_id". The middle segment cannot be
// recovered from the API, which is why it is part of the address rather than something
// Read fills in.
func triggerImportID() func(s *terraform.State) (string, error) {
	const resourceAddr = "circleci_trigger.test"

	return func(s *terraform.State) (string, error) {
		res := s.RootModule().Resources[resourceAddr]
		if res == nil {
			return "", fmt.Errorf("resource %s not found in state", resourceAddr)
		}

		for _, attr := range []string{"project_id", "pipeline_definition_id", "id"} {
			if _, ok := res.Primary.Attributes[attr]; !ok {
				return "", fmt.Errorf("attribute %s.%s not found", resourceAddr, attr)
			}
		}

		return res.Primary.Attributes["project_id"] + "/" +
			res.Primary.Attributes["pipeline_definition_id"] + "/" +
			res.Primary.Attributes["id"], nil
	}
}
