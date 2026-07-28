// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
)

// contextLegacyAPI is an in-memory stand-in for the /api/v2/context endpoints.
//
// Two different clients read and write these routes: the provider's own
// internal/circleci.Client (used by contextDataSource for id/name lookup) and
// the legacy circleci-sdk-go context/envcontext services (used by
// contextResource, contextRestrictionResource,
// contextEnvironmentVariableResource and
// contextEnvironmentVariableDataSource). Both hit the exact same routes, so one
// fake serves every context_* resource and data source under test.
//
// The wire shapes are copied from the API's the CircleCI API not
// from the Go client structs, so a mock cannot merely agree with a client that
// disagrees with production:
//   - the API (postOrgContext): create response is only {id,name,created_at}
//   - the API (getOrgContext): read response also carries org_id,
//     environment_variables and restrictions
//   - the API (getContexts): list response is {items,next_page_token},
//     accepting either owner-id+owner-type or owner-slug
//   - the API: {"message":"Context deleted."}
//   - context_restrictions_get.go (getContextRestrictions): {"items":[...]} with
//     NO next_page_token key at all, and project_id present only for "project"
//     restrictions
//   - context_restriction_post.go (postContextRestrictions): 201 response is
//     {context_id,id,restriction_type,restriction_value,project_id?} — deliberately
//     WITHOUT a "name" key. The API never reports a restriction's name on
//     create; only a subsequent list/read does. See contextRestrictionResource.Create.
//   - context_restriction_delete.go: {"message":"Context restriction deleted."}
//   - context_env_vars_get.go (getContextEnvVars): {"items":[...],"next_page_token":null},
//     each item carrying "truncated_value" rather than "value" (the raw value is
//     never returned by the API)
//   - context_env_var_put.go (putContextEnvVar): {variable,context_id,created_at,updated_at}
//   - context_env_var_delete.go: {"message":"Environment variable deleted."}
type contextLegacyAPI struct {
	t  *testing.T
	mu sync.Mutex

	contexts map[string]*fakeLegacyContext // keyed by context id

	nextContextSeq     int
	nextRestrictionSeq int

	// missingContexts makes a GET or DELETE on these context ids answer 404,
	// simulating a context deleted outside Terraform.
	missingContexts map[string]bool

	requests []string

	failStatus  int
	failMessage string

	// nextEnvVarUpdateSeq makes each PUT to an environment variable report a
	// distinct updated_at, so tests can tell a fresh upsert from a no-op one.
	nextEnvVarUpdateSeq int
}

type fakeLegacyContext struct {
	id        string
	name      string
	createdAt string
	orgID     string

	restrictions []*fakeLegacyRestriction
	envVars      map[string]*fakeLegacyEnvVar
}

type fakeLegacyRestriction struct {
	id               string
	name             string
	restrictionType  string
	restrictionValue string
}

type fakeLegacyEnvVar struct {
	value     string
	createdAt string
	updatedAt string
}

// newContextLegacyAPI starts the stand-in API and returns it alongside its origin.
func newContextLegacyAPI(t *testing.T) (*contextLegacyAPI, string) {
	t.Helper()

	api := &contextLegacyAPI{
		t:               t,
		contexts:        map[string]*fakeLegacyContext{},
		missingContexts: map[string]bool{},
	}

	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)

	return api, srv.URL
}

func (a *contextLegacyAPI) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v2/context", a.postContext)
	mux.HandleFunc("GET /api/v2/context", a.getContexts)
	mux.HandleFunc("GET /api/v2/context/{contextID}", a.getContext)
	mux.HandleFunc("DELETE /api/v2/context/{contextID}", a.deleteContext)
	mux.HandleFunc("GET /api/v2/context/{contextID}/restrictions", a.getRestrictions)
	mux.HandleFunc("POST /api/v2/context/{contextID}/restrictions", a.postRestriction)
	mux.HandleFunc("DELETE /api/v2/context/{contextID}/restrictions/{restrictionID}", a.deleteRestriction)
	mux.HandleFunc("GET /api/v2/context/{contextID}/environment-variable", a.getEnvVars)
	mux.HandleFunc("PUT /api/v2/context/{contextID}/environment-variable/{name}", a.putEnvVar)
	mux.HandleFunc("DELETE /api/v2/context/{contextID}/environment-variable/{name}", a.deleteEnvVar)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		a.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		a.write(w, http.StatusNotFound, map[string]any{"message": "Not Found: " + r.Method + " " + r.URL.Path})
	})

	return withRequestLog(a, mux)
}

// withRequestLog records every request line before delegating to next, so
// tests can assert on the exact method and path the provider sent.
func withRequestLog(a *contextLegacyAPI, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.requests = append(a.requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		a.mu.Unlock()

		next.ServeHTTP(w, r)
	})
}

// recorded returns the request lines seen so far, trimmed of a trailing bare
// "?" when the request carried no query string.
func (a *contextLegacyAPI) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]string, len(a.requests))
	for i, req := range a.requests {
		out[i] = trimBareQuery(req)
	}

	return out
}

func trimBareQuery(req string) string {
	if len(req) > 0 && req[len(req)-1] == '?' {
		return req[:len(req)-1]
	}

	return req
}

// fail makes every subsequent request answer with a v2 error body.
func (a *contextLegacyAPI) fail(status int, message string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.failStatus = status
	a.failMessage = message
}

// setMissing makes a context id answer 404 on GET, simulating deletion outside
// Terraform.
func (a *contextLegacyAPI) setMissing(contextID string, missing bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.missingContexts[contextID] = missing
}

// seedContext adds a context directly, bypassing the create flow, for tests
// that need a context to already exist (read, restriction, env var tests).
// Every caller in this package names it "build"; the name is fixed here rather
// than threaded through as a parameter that would never vary.
func (a *contextLegacyAPI) seedContext(id, orgID, createdAt string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.contexts[id] = &fakeLegacyContext{
		id:        id,
		name:      "build",
		orgID:     orgID,
		createdAt: createdAt,
		envVars:   map[string]*fakeLegacyEnvVar{},
	}
}

// seedEnvVar adds an environment variable directly to a seeded context,
// bypassing the PUT route, for data-source read tests.
func (a *contextLegacyAPI) seedEnvVar(contextID, name, value, createdAt, updatedAt string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	ctx, ok := a.contexts[contextID]
	if !ok {
		a.t.Fatalf("seedEnvVar: no such context %q", contextID)
	}

	ctx.envVars[name] = &fakeLegacyEnvVar{value: value, createdAt: createdAt, updatedAt: updatedAt}
}

// removeEnvVar deletes an environment variable directly, bypassing the delete
// route, to simulate deletion outside Terraform for drift tests.
func (a *contextLegacyAPI) removeEnvVar(contextID, name string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if ctx, ok := a.contexts[contextID]; ok {
		delete(ctx.envVars, name)
	}
}

// seedRestriction adds a restriction directly to a seeded context.
func (a *contextLegacyAPI) seedRestriction(contextID, id, name, restrictionType, value string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	ctx, ok := a.contexts[contextID]
	if !ok {
		a.t.Fatalf("seedRestriction: no such context %q", contextID)
	}

	ctx.restrictions = append(ctx.restrictions, &fakeLegacyRestriction{
		id:               id,
		name:             name,
		restrictionType:  restrictionType,
		restrictionValue: value,
	})
}

func (a *contextLegacyAPI) failed(w http.ResponseWriter) bool {
	a.mu.Lock()
	status, message := a.failStatus, a.failMessage
	a.mu.Unlock()

	if status == 0 {
		return false
	}

	a.write(w, status, map[string]any{"message": message})

	return true
}

// --- handlers ---

func (a *contextLegacyAPI) postContext(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	var body struct {
		Name  string `json:"name"`
		Owner struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"owner"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "Invalid body."})

		return
	}

	if body.Owner.Type != "" && body.Owner.Type != "organization" {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "Invalid owner type - only organization is supported at present"})

		return
	}

	a.mu.Lock()
	a.nextContextSeq++
	id := fmt.Sprintf("ctx-%d", a.nextContextSeq)
	ctx := &fakeLegacyContext{
		id:        id,
		name:      body.Name,
		orgID:     body.Owner.ID,
		createdAt: "2024-01-02T03:04:05.000Z",
		envVars:   map[string]*fakeLegacyEnvVar{},
	}
	a.contexts[id] = ctx
	a.mu.Unlock()

	// the API's response only ever carries these three fields.
	a.write(w, http.StatusOK, map[string]any{
		"id":         ctx.id,
		"name":       ctx.name,
		"created_at": ctx.createdAt,
	})
}

func (a *contextLegacyAPI) getContexts(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	slug := r.URL.Query().Get("owner-slug")
	ownerID := r.URL.Query().Get("owner-id")
	if slug == "" && ownerID == "" {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "must specify either owner-slug or owner-id"})

		return
	}

	a.mu.Lock()
	var items []map[string]any
	ids := make([]string, 0, len(a.contexts))
	for id := range a.contexts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ctx := a.contexts[id]
		if ownerID != "" && ctx.orgID != ownerID {
			continue
		}
		items = append(items, map[string]any{
			"id":         ctx.id,
			"name":       ctx.name,
			"created_at": ctx.createdAt,
		})
	}
	a.mu.Unlock()

	if items == nil {
		items = []map[string]any{}
	}

	a.write(w, http.StatusOK, map[string]any{"items": items, "next_page_token": nil})
}

func (a *contextLegacyAPI) getContext(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	id := r.PathValue("contextID")

	a.mu.Lock()
	ctx, ok := a.contexts[id]
	missing := a.missingContexts[id]
	a.mu.Unlock()

	if !ok || missing {
		a.write(w, http.StatusNotFound, map[string]any{"message": "context not found"})

		return
	}

	// getOrgContext also reports org_id, environment_variables and restrictions;
	// included here for wire fidelity even though the clients under test only
	// read id/name/created_at from this route.
	a.write(w, http.StatusOK, map[string]any{
		"id":                    ctx.id,
		"name":                  ctx.name,
		"created_at":            ctx.createdAt,
		"org_id":                ctx.orgID,
		"environment_variables": []map[string]any{},
		"restrictions":          []map[string]any{},
	})
}

func (a *contextLegacyAPI) deleteContext(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	id := r.PathValue("contextID")

	a.mu.Lock()
	_, ok := a.contexts[id]
	if ok {
		delete(a.contexts, id)
	}
	a.mu.Unlock()

	if !ok {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "context not found"})

		return
	}

	a.write(w, http.StatusOK, map[string]any{"message": "Context deleted."})
}

func (a *contextLegacyAPI) getRestrictions(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	id := r.PathValue("contextID")

	a.mu.Lock()
	ctx, ok := a.contexts[id]
	a.mu.Unlock()

	if !ok {
		a.write(w, http.StatusNotFound, map[string]any{"message": "context not found"})

		return
	}

	items := make([]map[string]any, 0, len(ctx.restrictions))
	for _, res := range ctx.restrictions {
		item := map[string]any{
			"context_id":        ctx.id,
			"id":                res.id,
			"name":              res.name,
			"restriction_type":  res.restrictionType,
			"restriction_value": res.restrictionValue,
		}
		if res.restrictionType == "project" {
			item["project_id"] = res.restrictionValue
		}
		items = append(items, item)
	}

	// Deliberately no "next_page_token" key: production's getContextRestrictions
	// returns the whole set in one body via newListResponse, not newListResponsePage.
	a.write(w, http.StatusOK, map[string]any{"items": items})
}

func (a *contextLegacyAPI) postRestriction(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	id := r.PathValue("contextID")

	a.mu.Lock()
	ctx, ok := a.contexts[id]
	a.mu.Unlock()

	if !ok {
		a.write(w, http.StatusNotFound, map[string]any{"message": "context not found"})

		return
	}

	var body struct {
		RestrictionType  string `json:"restriction_type"`
		RestrictionValue string `json:"restriction_value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RestrictionValue == "" {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "Invalid restriction."})

		return
	}
	switch body.RestrictionType {
	case "project", "expression", "group":
	default:
		a.write(w, http.StatusBadRequest, map[string]any{"message": "Invalid restriction."})

		return
	}

	a.mu.Lock()
	a.nextRestrictionSeq++
	res := &fakeLegacyRestriction{
		id:               fmt.Sprintf("rst-%d", a.nextRestrictionSeq),
		restrictionType:  body.RestrictionType,
		restrictionValue: body.RestrictionValue,
		// The name is deliberately left unset here: production learns a
		// restriction's human-readable name (e.g. a project's name) out of band
		// and only reports it on a later list/read, never on creation.
	}
	ctx.restrictions = append(ctx.restrictions, res)
	a.mu.Unlock()

	// context_restriction_post.go's response struct has no "name" field at all.
	resp := map[string]any{
		"context_id":        ctx.id,
		"id":                res.id,
		"restriction_type":  res.restrictionType,
		"restriction_value": res.restrictionValue,
	}
	if res.restrictionType == "project" {
		resp["project_id"] = res.restrictionValue
	}

	a.write(w, http.StatusCreated, resp)
}

func (a *contextLegacyAPI) deleteRestriction(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	contextID := r.PathValue("contextID")
	restrictionID := r.PathValue("restrictionID")

	a.mu.Lock()
	ctx, ok := a.contexts[contextID]
	var found bool
	if ok {
		for i, res := range ctx.restrictions {
			if res.id == restrictionID {
				ctx.restrictions = append(ctx.restrictions[:i], ctx.restrictions[i+1:]...)
				found = true

				break
			}
		}
	}
	a.mu.Unlock()

	if !ok || !found {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "restriction not found"})

		return
	}

	a.write(w, http.StatusOK, map[string]any{"message": "Context restriction deleted."})
}

func (a *contextLegacyAPI) getEnvVars(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	id := r.PathValue("contextID")

	a.mu.Lock()
	ctx, ok := a.contexts[id]
	a.mu.Unlock()

	if !ok {
		a.write(w, http.StatusNotFound, map[string]any{"message": "context not found"})

		return
	}

	names := make([]string, 0, len(ctx.envVars))
	for name := range ctx.envVars {
		names = append(names, name)
	}
	sort.Strings(names)

	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		ev := ctx.envVars[name]
		items = append(items, map[string]any{
			"variable":        name,
			"context_id":      ctx.id,
			"truncated_value": "xxxx" + ev.value[max(0, len(ev.value)-4):],
			"created_at":      ev.createdAt,
			"updated_at":      ev.updatedAt,
		})
	}

	a.write(w, http.StatusOK, map[string]any{"items": items, "next_page_token": nil})
}

func (a *contextLegacyAPI) putEnvVar(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	id := r.PathValue("contextID")
	name := r.PathValue("name")

	a.mu.Lock()
	ctx, ok := a.contexts[id]
	a.mu.Unlock()

	if !ok {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "context not found"})

		return
	}

	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "Invalid body."})

		return
	}

	a.mu.Lock()
	ev, exists := ctx.envVars[name]
	if !exists {
		ev = &fakeLegacyEnvVar{createdAt: "2024-01-02T03:04:05.000Z"}
		ctx.envVars[name] = ev
	}
	ev.value = body.Value
	// updated_at advances on every upsert; created_at is set once, matching an
	// atomic "overwrite in place" update semantic.
	a.nextEnvVarUpdateSeq++
	ev.updatedAt = fmt.Sprintf("2024-06-%02dT00:00:00.000Z", a.nextEnvVarUpdateSeq)
	a.mu.Unlock()

	a.write(w, http.StatusOK, map[string]any{
		"variable":   name,
		"context_id": ctx.id,
		"created_at": ev.createdAt,
		"updated_at": ev.updatedAt,
	})
}

func (a *contextLegacyAPI) deleteEnvVar(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	id := r.PathValue("contextID")
	name := r.PathValue("name")

	a.mu.Lock()
	ctx, ok := a.contexts[id]
	if ok {
		delete(ctx.envVars, name)
	}
	a.mu.Unlock()

	if !ok {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "context not found"})

		return
	}

	a.write(w, http.StatusOK, map[string]any{"message": "Environment variable deleted."})
}

func (a *contextLegacyAPI) write(w http.ResponseWriter, status int, body any) {
	a.t.Helper()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		a.t.Errorf("encoding stand-in response: %v", err)
	}
}

// legacyContextProviderConfig points the provider at the stand-in API.
func legacyContextProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake-token"
}
`, host)
}
