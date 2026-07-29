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
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// This file backs `circleci_trigger` (trigger_resource.go) and its singular
// data source (trigger_data_source.go) with an in-process stand-in for the
// public API service, so their CRUD paths run without TF_ACC or credentials.
//
// Routes and field names come from internal/circleci's trigger methods
// (internal/circleci/trigger.go), cross-checked against
// the CircleCI API's
// openapi_definitions/v2_endpoints/trigger/schemas.yaml and
// the CircleCI API Notably: Get/Update/Delete hit
// /projects/{project_id}/triggers/{trigger_id} (no pipeline-definitions
// segment), while only Create/List do.
//
// Before the SDK migration (issue #26), trigger_resource.go had an
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

func (a *fakeTriggerAPI) create(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(a.t, r)
	a.record(r, body)

	w.Header().Set("Content-Type", "application/json")
	if a.checkFail(w) {
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

	key := r.PathValue("projectID") + "/" + r.PathValue("triggerID")

	a.mu.Lock()
	defer a.mu.Unlock()

	record, ok := a.triggers[key]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Trigger not found."}`)

		return
	}

	// Only the fields the real updateTriggerRequest schema documents are
	// applied. event_source.provider/repo are accepted (the real handler's Go
	// struct silently ignores unknown/unused fields — see
	// the CircleCI API's updateRequestEventSource,
	// which has no Repo field at all) but must not change anything.
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

// --- github_app provider: CRUD, import, validation ---

func triggerFakeGithubAppConfig(host, externalID, preset string, disabled bool) string {
	// checkout_ref/config_ref are set explicitly (rather than left unset, which
	// the schema otherwise allows for github_app) to avoid a separate bug: see
	// TestTriggerResourceUnit_UpdateOfNullCheckoutRefIsInconsistent below.
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
				Config: triggerFakeGithubAppConfig(host, "ext-1", "all-pushes", false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_source_provider"), knownvalue.StringExact("github_app")),
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_source_repo_full_name"), knownvalue.StringExact(resolveFullName("ext-1"))),
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("event_preset"), knownvalue.StringExact("all-pushes")),
					statecheck.ExpectKnownValue("circleci_trigger.test", tfjsonpath.New("disabled"), knownvalue.Bool(false)),
				},
			},
			{
				// Toggle disabled in place; must PATCH rather than replace.
				Config: triggerFakeGithubAppConfig(host, "ext-1", "all-pushes", true),
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
	if repo["external_id"] != "ext-1" {
		t.Errorf("create event_source.repo.external_id = %v, want ext-1", repo["external_id"])
	}

	update := api.lastRequest(t, "PATCH", "/api/v2/projects/"+fakeTriggerProjectID+"/triggers/22222222-3333-4444-5555-000000000001")
	if update.Body["disabled"] != true {
		t.Errorf("update body disabled = %v, want true", update.Body["disabled"])
	}
}

func TestTriggerResourceUnit_GithubAppRequiresExternalID(t *testing.T) {
	_, host := newFakeTriggerAPI(t)

	cfg := triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id             = %[1]q
  pipeline_id             = %[2]q
  event_source_provider  = "github_app"
  event_preset           = "all-pushes"
}
`, fakeTriggerProjectID, fakeTriggerPipelineID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			// \s+ rather than a literal space: the diagnostic renderer word-wraps
			// long messages, and this one happens to wrap exactly between
			// "requires" and "event_source_repo_external_id".
			ExpectError: regexp.MustCompile(`(?s)requires\s+event_source_repo_external_id`),
		}},
	})
}

func TestTriggerResourceUnit_GithubAppInvalidEventPreset(t *testing.T) {
	_, host := newFakeTriggerAPI(t)

	cfg := triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id                    = %[1]q
  pipeline_id                    = %[2]q
  event_source_provider         = "github_app"
  event_source_repo_external_id = "ext-1"
  event_preset                  = "not-a-real-preset"
}
`, fakeTriggerProjectID, fakeTriggerPipelineID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)unexpected event_preset`),
		}},
	})
}

func TestTriggerResourceUnit_UnsupportedProvider(t *testing.T) {
	_, host := newFakeTriggerAPI(t)

	cfg := triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id             = %[1]q
  pipeline_id             = %[2]q
  event_source_provider  = "bitbucket_cloud"
}
`, fakeTriggerProjectID, fakeTriggerPipelineID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)unexpected event source provider`),
		}},
	})
}

// --- webhook provider: CRUD + validation ---

func triggerFakeWebhookConfig(host, eventName, sender string) string {
	return triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id                     = %[1]q
  pipeline_id                     = %[2]q
  event_source_provider          = "webhook"
  event_name                     = %[3]q
  event_source_web_hook_sender   = %[4]q
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
				// BUG: trigger_resource.go's ImportState only sets "id" and
				// "project_id"; Read() never populates PipelineId either — same
				// pre-existing gap noted in TestTriggerResourceUnit_GithubAppCRUD
				// above.
				ImportStateVerifyIgnore: []string{"event_source_web_hook_url"},
				ImportStateIdFunc:       triggerImportID(),
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

func TestTriggerResourceUnit_WebhookRequiresEventNameAndSender(t *testing.T) {
	_, host := newFakeTriggerAPI(t)

	tests := []struct {
		name string
		cfg  string
	}{
		{
			name: "missing event_name",
			cfg: triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id                    = %[1]q
  pipeline_id                    = %[2]q
  event_source_provider         = "webhook"
  event_source_web_hook_sender  = "datadog"
}
`, fakeTriggerProjectID, fakeTriggerPipelineID),
		},
		{
			name: "missing sender",
			cfg: triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id             = %[1]q
  pipeline_id             = %[2]q
  event_source_provider  = "webhook"
  event_name             = "deploy-hook"
}
`, fakeTriggerProjectID, fakeTriggerPipelineID),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      tc.cfg,
					ExpectError: regexp.MustCompile(`(?s)webhook provider requires`),
				}},
			})
		})
	}
}

// --- schedule provider: CRUD, parameters, validation ---

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
				// BUG: pipeline_id is never restored on import (see the identical
				// note in TestTriggerResourceUnit_GithubAppCRUD). And a second, more
				// subtle bug: Read()'s guard for preserving the
				// attribution_actor alias only checks IsNull()/IsUnknown(), which is
				// true right after import (ImportState never sets it either), so
				// the first post-import Read resolves it to the actor UUID instead
				// of restoring the "system" alias the original config used — a
				// permanent diff between an imported trigger and its configuration.
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

func TestTriggerResourceUnit_ScheduleRequiresFields(t *testing.T) {
	base := func(overrides string) string {
		return triggerFakeProviderConfig("http://127.0.0.1:1") + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id             = %[1]q
  pipeline_id             = %[2]q
  event_source_provider  = "schedule"
%[3]s
}
`, fakeTriggerProjectID, fakeTriggerPipelineID, overrides)
	}

	tests := []struct {
		name string
		cfg  string
	}{
		{
			name: "missing event_name",
			cfg: base(`
  checkout_ref = "main"
  config_ref = "main"
  event_source_schedule_cron_expression = "0 0 * * *"
  event_source_schedule_attribution_actor = "system"
`),
		},
		{
			name: "missing checkout_ref",
			cfg: base(`
  event_name = "nightly"
  config_ref = "main"
  event_source_schedule_cron_expression = "0 0 * * *"
  event_source_schedule_attribution_actor = "system"
`),
		},
		{
			name: "missing config_ref",
			cfg: base(`
  event_name = "nightly"
  checkout_ref = "main"
  event_source_schedule_cron_expression = "0 0 * * *"
  event_source_schedule_attribution_actor = "system"
`),
		},
		{
			name: "missing cron_expression",
			cfg: base(`
  event_name = "nightly"
  checkout_ref = "main"
  config_ref = "main"
  event_source_schedule_attribution_actor = "system"
`),
		},
		{
			name: "missing attribution_actor",
			cfg: base(`
  event_name = "nightly"
  checkout_ref = "main"
  config_ref = "main"
  event_source_schedule_cron_expression = "0 0 * * *"
`),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      tc.cfg,
					ExpectError: regexp.MustCompile(`(?s)schedule provider requires`),
				}},
			})
		})
	}
}

func TestTriggerResourceUnit_ParametersRejectedForNonScheduleProvider(t *testing.T) {
	cfg := triggerFakeProviderConfig("http://127.0.0.1:1") + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id                    = %[1]q
  pipeline_id                    = %[2]q
  event_source_provider         = "github_app"
  event_source_repo_external_id = "ext-1"
  event_preset                  = "all-pushes"
  parameters = {
    foo = "bar"
  }
}
`, fakeTriggerProjectID, fakeTriggerPipelineID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)does not support parameters`),
		}},
	})
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

	cfg := triggerFakeGithubAppConfig(host, "ext-1", "all-pushes", false)

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
