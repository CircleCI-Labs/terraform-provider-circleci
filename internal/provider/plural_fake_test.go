// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The plural data sources read v2 collections that no fixture account can be
// relied on to contain, so these tests run against an in-process stand-in for the
// API rather than a real installation.
//
// The stand-in stores and serves raw JSON objects rather than the provider's own
// client structs, deliberately: a mock built from the structs under test cannot
// catch a field name that disagrees with production. The shapes here are copied
// from production, as noted on each seeder.

// Scope identifiers the fake serves collections for.
const (
	testPluralOrgID       = "00000000-1111-2222-3333-444444444444"
	testPluralProjectID   = "55555555-6666-7777-8888-999999999999"
	testPluralProjectSlug = "circleci/AbCdEfG/HiJkLmN"
	testPluralContextID   = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testPluralPipelineID  = "12121212-3434-5656-7878-909090909090"
)

// pluralAPI is an in-memory stand-in for the v2 collection endpoints the plural
// data sources read.
type pluralAPI struct {
	t *testing.T

	mu sync.Mutex

	// Each map is keyed by the scope its collection is listed under.
	contexts     map[string][]map[string]any // organization id
	restrictions map[string][]map[string]any // context id
	webhooks     map[string][]map[string]any // scope (project) id
	envVars      map[string][]map[string]any // project slug
	definitions  map[string][]map[string]any // project id
	triggers     map[string][]map[string]any // "project id/pipeline definition id"

	// pageSize splits the paginated responses into pages when positive, so that
	// the client's page draining is exercised rather than assumed.
	pageSize int

	// failStatus and failMessage make every request answer with a v2 error body
	// instead of a collection, for the diagnostic tests.
	failStatus  int
	failMessage string
}

// newPluralAPI starts the stand-in API and returns it alongside its origin.
func newPluralAPI(t *testing.T) (*pluralAPI, string) {
	t.Helper()

	api := &pluralAPI{
		t:            t,
		contexts:     map[string][]map[string]any{},
		restrictions: map[string][]map[string]any{},
		webhooks:     map[string][]map[string]any{},
		envVars:      map[string][]map[string]any{},
		definitions:  map[string][]map[string]any{},
		triggers:     map[string][]map[string]any{},
	}

	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)

	return api, srv.URL
}

// handler routes the endpoints the plural data sources call. Anything else is a
// 404 naming the path, so a client that builds the wrong route fails loudly
// rather than silently reading an empty collection.
func (a *pluralAPI) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v2/context", a.listContexts)
	mux.HandleFunc("GET /api/v2/context/{contextID}/restrictions", a.listRestrictions)
	mux.HandleFunc("GET /api/v2/webhook", a.listWebhooks)
	mux.HandleFunc("GET /api/v2/project/{vcs}/{org}/{repo}/envvar", a.listEnvVars)
	mux.HandleFunc("GET /api/v2/projects/{projectID}/pipeline-definitions", a.listDefinitions)
	mux.HandleFunc("GET /api/v2/projects/{projectID}/pipeline-definitions/{pipelineID}/triggers", a.listTriggers)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Not Found: " + r.Method + " " + r.URL.Path})
	})

	return mux
}

// fail makes every subsequent request answer with a v2 error body.
func (a *pluralAPI) fail(status int, message string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.failStatus = status
	a.failMessage = message
}

// --- seeders ---

// seedContext adds a context to an organization, shaped as the API returns it.
func (a *pluralAPI) seedContext(orgID, id, name, createdAt string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.contexts[orgID] = append(a.contexts[orgID], map[string]any{
		"name":       name,
		"id":         id,
		"created_at": createdAt,
	})
}

// seedRestriction adds a restriction to a context, shaped as the API returns
// it: project_id is only present for project restrictions.
func (a *pluralAPI) seedRestriction(contextID, id, name, restrictionType, value string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	restriction := map[string]any{
		"context_id":        contextID,
		"id":                id,
		"name":              name,
		"restriction_type":  restrictionType,
		"restriction_value": value,
	}
	if restrictionType == "project" {
		restriction["project_id"] = value
	}

	a.restrictions[contextID] = append(a.restrictions[contextID], restriction)
}

// seedWebhook adds a webhook to a project scope, shaped as the API returns it:
// the scope is nested and the signing secret is masked to "****" when set.
func (a *pluralAPI) seedWebhook(scopeID, id, name, url string, events []string, verifyTLS, hasSecret bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	secret := ""
	if hasSecret {
		secret = "****"
	}

	a.webhooks[scopeID] = append(a.webhooks[scopeID], map[string]any{
		"id":             id,
		"name":           name,
		"url":            url,
		"events":         events,
		"verify_tls":     verifyTLS,
		"signing_secret": secret,
		"scope":          map[string]any{"id": scopeID, "type": "project"},
		"created_at":     "2014-02-11T22:40:37Z",
		"updated_at":     "2015-02-11T22:40:37Z",
	})
}

// seedEnvVar adds an environment variable to a project, shaped as the API
// returns it: the value is masked and created_at may be null.
func (a *pluralAPI) seedEnvVar(projectSlug, name, maskedValue string, createdAt *string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	variable := map[string]any{"name": name, "value": maskedValue, "created_at": nil}
	if createdAt != nil {
		variable["created_at"] = *createdAt
	}

	a.envVars[projectSlug] = append(a.envVars[projectSlug], variable)
}

// seedDefinition adds a pipeline definition to a project, shaped as the API
// returns it. An empty repoFullName omits the repo object entirely, as the API
// does.
func (a *pluralAPI) seedDefinition(projectID, id, name, description, provider, filePath, repoFullName, repoExternalID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	configSource := map[string]any{"provider": provider, "file_path": filePath}
	checkoutSource := map[string]any{"provider": provider}
	if repoFullName != "" || repoExternalID != "" {
		repo := map[string]any{"full_name": repoFullName, "external_id": repoExternalID}
		configSource["repo"] = repo
		checkoutSource["repo"] = repo
	}

	definition := map[string]any{
		"id":              id,
		"name":            name,
		"config_source":   configSource,
		"checkout_source": checkoutSource,
	}
	// description and created_at are omitempty in production.
	if description != "" {
		definition["description"] = description
		definition["created_at"] = "2024-05-01T10:00:00Z"
	}

	a.definitions[projectID] = append(a.definitions[projectID], definition)
}

// seedRepoTrigger adds a repository-event trigger, shaped as the API returns
// it. disabled is omitted rather than sent as false, as the API does for an
// enabled trigger.
func (a *pluralAPI) seedRepoTrigger(projectID, pipelineID, id, name, eventName, preset, repoFullName, repoExternalID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	key := projectID + "/" + pipelineID
	a.triggers[key] = append(a.triggers[key], map[string]any{
		"id":           id,
		"name":         name,
		"event_name":   eventName,
		"description":  eventName,
		"created_at":   "2024-06-01T09:00:00Z",
		"checkout_ref": "main",
		"config_ref":   "main",
		"event_preset": preset,
		"event_source": map[string]any{
			"provider": "github_app",
			"repo":     map[string]any{"full_name": repoFullName, "external_id": repoExternalID},
		},
	})
}

// seedScheduledTrigger adds a scheduled trigger. attribution_actor is an object
// carrying an id, which is what a read returns even though a create accepts a
// bare string. parameters are passed through as stored JSON, so the values are
// deliberately not all strings.
func (a *pluralAPI) seedScheduledTrigger(projectID, pipelineID, id, name, cron, actorID string, parameters map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	trigger := map[string]any{
		"id":           id,
		"name":         name,
		"event_name":   name,
		"description":  name,
		"created_at":   "2024-06-02T09:00:00Z",
		"checkout_ref": "main",
		"config_ref":   "main",
		"disabled":     true,
		"event_source": map[string]any{
			"provider": "schedule",
			"schedule": map[string]any{
				"cron_expression":   cron,
				"attribution_actor": map[string]any{"id": actorID},
			},
		},
	}
	if parameters != nil {
		trigger["parameters"] = parameters
	}

	key := projectID + "/" + pipelineID
	a.triggers[key] = append(a.triggers[key], trigger)
}

// --- handlers ---

func (a *pluralAPI) listContexts(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	// The API requires an owner and only serves organizations, so a client
	// that omits either parameter must fail here rather than reading a collection.
	ownerID := r.URL.Query().Get("owner-id")
	if ownerID == "" {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "must specify either owner-slug or owner-id"})

		return
	}
	if ownerType := r.URL.Query().Get("owner-type"); ownerType != "organization" {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "Invalid owner-type"})

		return
	}

	a.mu.Lock()
	items := a.contexts[ownerID]
	a.mu.Unlock()

	a.writePage(w, r, items)
}

func (a *pluralAPI) listRestrictions(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	a.mu.Lock()
	items := a.restrictions[r.PathValue("contextID")]
	a.mu.Unlock()

	a.writeItems(w, items)
}

func (a *pluralAPI) listWebhooks(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	// The API rejects a request that does not name a project scope.
	scopeID := r.URL.Query().Get("scope-id")
	if scopeID == "" || r.URL.Query().Get("scope-type") != "project" {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "Invalid scope parameters"})

		return
	}

	a.mu.Lock()
	items := a.webhooks[scopeID]
	a.mu.Unlock()

	a.writePage(w, r, items)
}

func (a *pluralAPI) listEnvVars(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	slug := strings.Join([]string{r.PathValue("vcs"), r.PathValue("org"), r.PathValue("repo")}, "/")

	a.mu.Lock()
	items := a.envVars[slug]
	a.mu.Unlock()

	a.writePage(w, r, items)
}

func (a *pluralAPI) listDefinitions(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	a.mu.Lock()
	items := a.definitions[r.PathValue("projectID")]
	a.mu.Unlock()

	a.writeItems(w, items)
}

func (a *pluralAPI) listTriggers(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	a.mu.Lock()
	items := a.triggers[r.PathValue("projectID")+"/"+r.PathValue("pipelineID")]
	a.mu.Unlock()

	a.writeItems(w, items)
}

// failed answers with the configured error and reports whether it did.
func (a *pluralAPI) failed(w http.ResponseWriter) bool {
	a.mu.Lock()
	status, message := a.failStatus, a.failMessage
	a.mu.Unlock()

	if status == 0 {
		return false
	}

	a.write(w, status, map[string]any{"message": message})

	return true
}

// writeItems answers with the unpaginated v2 envelope, which carries no
// next_page_token at all.
func (a *pluralAPI) writeItems(w http.ResponseWriter, items []map[string]any) {
	if items == nil {
		items = []map[string]any{}
	}

	a.write(w, http.StatusOK, map[string]any{"items": items})
}

// writePage answers with the paginated v2 envelope, where next_page_token is a
// nullable string: it is present and null on the last page.
func (a *pluralAPI) writePage(w http.ResponseWriter, r *http.Request, items []map[string]any) {
	start := 0
	if token := r.URL.Query().Get("page-token"); token != "" {
		parsed, err := strconv.Atoi(token)
		if err != nil {
			a.write(w, http.StatusBadRequest, map[string]any{"message": "invalid page-token"})

			return
		}
		start = min(parsed, len(items))
	}

	end := len(items)
	if a.pageSize > 0 && start+a.pageSize < end {
		end = start + a.pageSize
	}

	page := items[start:end]
	if page == nil {
		page = []map[string]any{}
	}

	body := map[string]any{"items": page, "next_page_token": nil}
	if end < len(items) {
		body["next_page_token"] = strconv.Itoa(end)
	}

	a.write(w, http.StatusOK, body)
}

func (a *pluralAPI) write(w http.ResponseWriter, status int, body any) {
	a.t.Helper()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		a.t.Errorf("encoding stand-in response: %v", err)
	}
}

// pluralProviderConfig points the provider at the stand-in API. deployment is a
// parameter because most of these collections must work identically on Cloud and
// Server, and the two that cannot must say so.
func pluralProviderConfig(host, deployment string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host       = %[1]q
  key        = "fake-token"
  deployment = %[2]q
}
`, host, deployment)
}
