// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
)

// contextFakeAPI is an in-memory stand-in for the /api/v2/context endpoints,
// backing every circleci_context* resource and data source (all now on
// internal/circleci.Client; the name is kept only because every sibling
// _test.go file already refers to it).
//
// The wire shapes and status codes are copied from the real API's handlers
// and middleware, not from any Go client's structs, so a mock cannot merely
// agree with a client that disagrees with production:
//   - create response is only {id,name,created_at}
//   - read response also carries org_id, environment_variables and
//     restrictions
//   - list response is {items,next_page_token}, accepting either
//     owner-id+owner-type or owner-slug
//   - delete: {"message":"Context deleted."}
//   - restriction list response is {"items":[...]} with NO next_page_token
//     key at all, and project_id present only for "project" restrictions
//   - restriction create (201) response is
//     {context_id,id,restriction_type,restriction_value,project_id?} —
//     deliberately WITHOUT a "name" key. The API never reports a
//     restriction's name on create; only a subsequent list/read does. See
//     contextRestrictionResource.Create.
//   - restriction delete: {"message":"Context restriction deleted."}
//   - env var list response is {"items":[...],"next_page_token":...}, each
//     item carrying "truncated_value" rather than "value" (the raw value is
//     never returned by the API), paged at 100 with a token that is advertised
//     and never read back — see getEnvVars
//   - env var put reads exactly one request key, "value", and silently stores
//     an empty value for any other — see putEnvVar
//   - env var put response is {variable,context_id,created_at,updated_at}
//   - env var delete: {"message":"Environment variable deleted."}
//
// The most consequential shape modeled here is not a JSON field but a status
// code: every one of the routes above that addresses a context by id sits
// behind a middleware that resolves the id to its owning organization
// through a *separate* lookup and maps ANY failure of that lookup — a context
// that never existed, one that was deleted, one in another organization, or a
// token that cannot see it — to HTTP 403, before the route's own handler
// (which might otherwise 404) ever runs. resolveOrFail below is that
// middleware. Only the collection routes (list, create), which carry no
// context id, are exempt.
type contextFakeAPI struct {
	t  *testing.T
	mu sync.Mutex

	contexts map[string]*fakeContext // keyed by context id

	nextContextSeq     int
	nextRestrictionSeq int

	// missingContexts makes a lookup on these context ids answer 403, on every
	// route addressed by context id, simulating a context deleted outside
	// Terraform (or one this token can no longer see — the API does not
	// distinguish the two; see resolveOrFail below).
	missingContexts map[string]bool

	requests []string

	failStatus  int
	failMessage string

	// nextEnvVarUpdateSeq makes each PUT to an environment variable report a
	// distinct updated_at, so tests can tell a fresh upsert from a no-op one.
	nextEnvVarUpdateSeq int
}

type fakeContext struct {
	id        string
	name      string
	createdAt string
	orgID     string

	restrictions []*fakeContextRestriction
	envVars      map[string]*fakeContextEnvVar
}

type fakeContextRestriction struct {
	id               string
	name             string
	restrictionType  string
	restrictionValue string
}

type fakeContextEnvVar struct {
	value     string
	createdAt string
	updatedAt string
}

// newContextFakeAPI starts the stand-in API and returns it alongside its origin.
func newContextFakeAPI(t *testing.T) (*contextFakeAPI, string) {
	t.Helper()

	api := &contextFakeAPI{
		t:               t,
		contexts:        map[string]*fakeContext{},
		missingContexts: map[string]bool{},
	}

	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)

	return api, srv.URL
}

func (a *contextFakeAPI) handler() http.Handler {
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
func withRequestLog(a *contextFakeAPI, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.requests = append(a.requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		a.mu.Unlock()

		next.ServeHTTP(w, r)
	})
}

// recorded returns the request lines seen so far, trimmed of a trailing bare
// "?" when the request carried no query string.
func (a *contextFakeAPI) recorded() []string {
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

// fail makes every subsequent request answer with a v2 error body. This is a
// test-only escape hatch for exercising arbitrary status codes (4xx surfacing
// as a diagnostic, etc.); it takes priority over resolveOrFail below, the same
// way a real infrastructure failure would preempt any application-level
// middleware.
func (a *contextFakeAPI) fail(status int, message string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.failStatus = status
	a.failMessage = message
}

// setMissing makes a context id resolve to "missing" on every route addressed
// by context id (see resolveOrFail), simulating deletion outside Terraform.
func (a *contextFakeAPI) setMissing(contextID string, missing bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.missingContexts[contextID] = missing
}

// seedContext adds a context directly, bypassing the create flow, for tests
// that need a context to already exist (read, restriction, env var tests).
// Every caller in this package names it "build"; the name is fixed here rather
// than threaded through as a parameter that would never vary.
func (a *contextFakeAPI) seedContext(id, orgID, createdAt string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.contexts[id] = &fakeContext{
		id:        id,
		name:      "build",
		orgID:     orgID,
		createdAt: createdAt,
		envVars:   map[string]*fakeContextEnvVar{},
	}
}

// setContextOrg changes which organization a context reports as its owner,
// bypassing any route, so a test can simulate state that names the wrong
// organization — the situation the old unvalidated import produced. Only the
// single-context read reports org_id, so this is visible through that route
// alone.
func (a *contextFakeAPI) setContextOrg(contextID, orgID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	ctx, ok := a.contexts[contextID]
	if !ok {
		a.t.Fatalf("setContextOrg: no such context %q", contextID)
	}

	ctx.orgID = orgID
}

// seedEnvVar adds an environment variable directly to a seeded context,
// bypassing the PUT route, for data-source read tests.
func (a *contextFakeAPI) seedEnvVar(contextID, name, value, createdAt, updatedAt string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	ctx, ok := a.contexts[contextID]
	if !ok {
		a.t.Fatalf("seedEnvVar: no such context %q", contextID)
	}

	ctx.envVars[name] = &fakeContextEnvVar{value: value, createdAt: createdAt, updatedAt: updatedAt}
}

// removeEnvVar deletes an environment variable directly, bypassing the delete
// route, to simulate deletion outside Terraform for drift tests.
func (a *contextFakeAPI) removeEnvVar(contextID, name string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if ctx, ok := a.contexts[contextID]; ok {
		delete(ctx.envVars, name)
	}
}

// removeRestriction deletes a restriction directly, bypassing the delete
// route, to simulate deletion outside Terraform for drift tests.
func (a *contextFakeAPI) removeRestriction(contextID, id string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	ctx, ok := a.contexts[contextID]
	if !ok {
		return
	}

	for i, res := range ctx.restrictions {
		if res.id == id {
			ctx.restrictions = append(ctx.restrictions[:i], ctx.restrictions[i+1:]...)

			return
		}
	}
}

// seedRestriction adds a restriction directly to a seeded context.
func (a *contextFakeAPI) seedRestriction(contextID, id, name, restrictionType, value string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	ctx, ok := a.contexts[contextID]
	if !ok {
		a.t.Fatalf("seedRestriction: no such context %q", contextID)
	}

	ctx.restrictions = append(ctx.restrictions, &fakeContextRestriction{
		id:               id,
		name:             name,
		restrictionType:  restrictionType,
		restrictionValue: value,
	})
}

func (a *contextFakeAPI) failed(w http.ResponseWriter) bool {
	a.mu.Lock()
	status, message := a.failStatus, a.failMessage
	a.mu.Unlock()

	if status == 0 {
		return false
	}

	a.write(w, status, map[string]any{"message": message})

	return true
}

// resolveOrFail stands in for the middleware the real API puts in front of
// every context route: every route below that addresses a context
// by id calls this before doing anything else, and a context this token
// cannot resolve — deleted, never existed, or in another organization —
// answers 403 "Forbidden" here, before the route-specific handler runs at
// all. That is why a context_restriction or context_environment_variable
// route also 403s when its owning context is gone: they call this exactly
// the same way GetContext and DeleteContext do.
func (a *contextFakeAPI) resolveOrFail(w http.ResponseWriter, contextID string) (*fakeContext, bool) {
	a.mu.Lock()
	ctx, ok := a.contexts[contextID]
	missing := a.missingContexts[contextID]
	a.mu.Unlock()

	if !ok || missing {
		a.write(w, http.StatusForbidden, map[string]any{"message": "Forbidden"})

		return nil, false
	}

	return ctx, true
}

// --- handlers ---

func (a *contextFakeAPI) postContext(w http.ResponseWriter, r *http.Request) {
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
	ctx := &fakeContext{
		id:        id,
		name:      body.Name,
		orgID:     body.Owner.ID,
		createdAt: "2024-01-02T03:04:05.000Z",
		envVars:   map[string]*fakeContextEnvVar{},
	}
	a.contexts[id] = ctx
	a.mu.Unlock()

	// The create response only ever carries these three fields.
	a.write(w, http.StatusOK, map[string]any{
		"id":         ctx.id,
		"name":       ctx.name,
		"created_at": ctx.createdAt,
	})
}

func (a *contextFakeAPI) getContexts(w http.ResponseWriter, r *http.Request) {
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
	// Sorted by lower-cased name, which is the order the owning contexts service
	// imposes — not by id, and not insertion order. Nothing depends on it today;
	// answering in an order of the API's own choosing is the property that stopped
	// the events/pr_only_branch_overrides permanent diff being invisible here (see
	// reorderedLikeTheAPI), so it is worth getting right before something does.
	matching := make([]*fakeContext, 0, len(a.contexts))
	for _, ctx := range a.contexts {
		if ownerID != "" && ctx.orgID != ownerID {
			continue
		}
		matching = append(matching, ctx)
	}
	sort.Slice(matching, func(i, j int) bool {
		return strings.ToLower(matching[i].name) < strings.ToLower(matching[j].name)
	})

	items := make([]map[string]any, 0, len(matching))
	for _, ctx := range matching {
		items = append(items, map[string]any{
			"id":         ctx.id,
			"name":       ctx.name,
			"created_at": ctx.createdAt,
		})
	}
	a.mu.Unlock()

	a.write(w, http.StatusOK, map[string]any{"items": items, "next_page_token": nil})
}

func (a *contextFakeAPI) getContext(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	ctx, ok := a.resolveOrFail(w, r.PathValue("contextID"))
	if !ok {
		return
	}

	// This route reports org_id, and it is the ONLY context route that does —
	// create and list both omit it. org_id is load-bearing now rather than
	// present for fidelity alone: contextResource's Read and ImportState both
	// take the owning organization from here rather than trusting state or the
	// import id, so a fake that omitted it would make an unverifiable import
	// look verified.
	//
	// environment_variables and restrictions are reported inline too. They stay
	// empty here because nothing decodes them, but the keys are present because
	// the real route always sends them. Note that an inline variable object
	// carries no context_id, unlike the one the dedicated environment-variable
	// list route returns above — the two shapes differ.
	a.write(w, http.StatusOK, map[string]any{
		"id":                    ctx.id,
		"name":                  ctx.name,
		"created_at":            ctx.createdAt,
		"org_id":                ctx.orgID,
		"environment_variables": []map[string]any{},
		"restrictions":          []map[string]any{},
	})
}

func (a *contextFakeAPI) deleteContext(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	id := r.PathValue("contextID")

	if _, ok := a.resolveOrFail(w, id); !ok {
		return
	}

	a.mu.Lock()
	delete(a.contexts, id)
	a.mu.Unlock()

	a.write(w, http.StatusOK, map[string]any{"message": "Context deleted."})
}

func (a *contextFakeAPI) getRestrictions(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	ctx, ok := a.resolveOrFail(w, r.PathValue("contextID"))
	if !ok {
		return
	}

	a.mu.Lock()
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
	a.mu.Unlock()

	// Deliberately no "next_page_token" key: production's getContextRestrictions
	// returns the whole set in one body via newListResponse, not newListResponsePage.
	a.write(w, http.StatusOK, map[string]any{"items": items})
}

func (a *contextFakeAPI) postRestriction(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	ctx, ok := a.resolveOrFail(w, r.PathValue("contextID"))
	if !ok {
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
	res := &fakeContextRestriction{
		id:               fmt.Sprintf("rst-%d", a.nextRestrictionSeq),
		restrictionType:  body.RestrictionType,
		restrictionValue: body.RestrictionValue,
		// The name is deliberately left unset here: production learns a
		// restriction's human-readable name (e.g. a project's name) out of band
		// and only reports it on a later list/read, never on creation.
	}
	ctx.restrictions = append(ctx.restrictions, res)
	a.mu.Unlock()

	// The create response has no "name" field at all.
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

func (a *contextFakeAPI) deleteRestriction(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	ctx, ok := a.resolveOrFail(w, r.PathValue("contextID"))
	if !ok {
		return
	}

	restrictionID := r.PathValue("restrictionID")

	a.mu.Lock()
	var found bool
	for i, res := range ctx.restrictions {
		if res.id == restrictionID {
			ctx.restrictions = append(ctx.restrictions[:i], ctx.restrictions[i+1:]...)
			found = true

			break
		}
	}
	a.mu.Unlock()

	if !found {
		// The context resolved fine, but this particular restriction did not, so
		// the API answers a real 404 here — distinct from the context-level 403
		// above, which is decided before the route's own logic runs.
		a.write(w, http.StatusNotFound, map[string]any{"message": "restriction not found"})

		return
	}

	a.write(w, http.StatusOK, map[string]any{"message": "Context restriction deleted."})
}

// fakeContextEnvVarPageSize is the number of variables the real list route puts
// on a page, and the number this fake puts on one. It is fixed by the service:
// no request parameter changes it.
const fakeContextEnvVarPageSize = 100

// getEnvVars serves the list route, INCLUDING its pagination, which is the part
// this fake used to get wrong.
//
// It answered next_page_token: nil unconditionally, whatever the variable count.
// That is right for a context at or below the page size and wrong above it, and
// it made the truncation that a larger context really produces unreachable from
// every mocked test in this package: the plural data source could under-report by
// any number of variables and nothing here would notice. Measured against a real
// context filled past the cap, the route behaves as modelled below.
//
//   - Exactly fakeContextEnvVarPageSize items come back when more exist, sorted
//     by name.
//   - next_page_token is null at or below the page size and a real cursor above
//     it, derived from the last item on the page. Production's is base64 of a
//     transit-encoded ":after <name>"; the encoding is not reproduced literally
//     here, because a fake spelling out another service's internal cursor format
//     is a fake claiming to be that service. What IS reproduced is the property
//     the client depends on: the token names the page's last item, so it changes
//     whenever the tail of page one changes.
//   - The token is never read back. Whatever the request's query string holds,
//     the answer is page one and the same token — so the collection cannot be
//     paged, only detected as incomplete.
func (a *contextFakeAPI) getEnvVars(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	ctx, ok := a.resolveOrFail(w, r.PathValue("contextID"))
	if !ok {
		return
	}

	a.mu.Lock()
	names := make([]string, 0, len(ctx.envVars))
	for name := range ctx.envVars {
		names = append(names, name)
	}
	sort.Strings(names)

	// The page token is ignored, exactly as production ignores it: the page
	// served is always the first one.
	truncated := len(names) > fakeContextEnvVarPageSize
	if truncated {
		names = names[:fakeContextEnvVarPageSize]
	}

	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		ev := ctx.envVars[name]
		items = append(items, map[string]any{
			"variable":        name,
			"context_id":      ctx.id,
			"truncated_value": truncateContextEnvVarValue(ev.value),
			"created_at":      ev.createdAt,
			"updated_at":      ev.updatedAt,
		})
	}
	a.mu.Unlock()

	var nextPageToken any
	if truncated {
		nextPageToken = base64.StdEncoding.EncodeToString([]byte("after " + names[len(names)-1]))
	}

	a.write(w, http.StatusOK, map[string]any{"items": items, "next_page_token": nextPageToken})
}

func (a *contextFakeAPI) putEnvVar(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	ctx, ok := a.resolveOrFail(w, r.PathValue("contextID"))
	if !ok {
		return
	}

	name := r.PathValue("name")

	// Decoded as a map rather than into a struct so that a request key this
	// route does not read is visible rather than dropped on the floor.
	//
	// The real route reads exactly one key, "value", and ignores every other:
	// PUT {"val": "s3cr3t"} and PUT {"Value": "s3cr3t"} both answer 200 with a
	// normal-looking body and store an EMPTY value — measured against a real
	// context. An empty object is the one body it rejects, with 400 "Invalid
	// body.", so a wrong key cannot be told from a right one by status code.
	//
	// This fake reproduces the storing-empty part, because that is what
	// production does and a fake that quietly corrected it would hide the whole
	// class of defect. It also fails the test outright, because no test in this
	// package wants to exercise a provider that drops the secret it was given:
	// this is the same silent-drop shape as the webhook signing-secret defect,
	// where client and fake agreed on a key production never read.
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) == 0 {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "Invalid body."})

		return
	}

	var value string
	if raw, present := body["value"]; present {
		if err := json.Unmarshal(raw, &value); err != nil {
			a.write(w, http.StatusBadRequest, map[string]any{"message": "Invalid body."})

			return
		}
	} else {
		a.t.Errorf("PUT %s carried no \"value\" key (body keys: %v); the route reads only that key "+
			"and answers 200 while storing an empty value, so the variable would exist with no "+
			"secret in it", r.URL.Path, slices.Sorted(maps.Keys(body)))
	}

	a.mu.Lock()
	ev, exists := ctx.envVars[name]
	if !exists {
		ev = &fakeContextEnvVar{createdAt: "2024-01-02T03:04:05.000Z"}
		ctx.envVars[name] = ev
	}
	ev.value = value
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

func (a *contextFakeAPI) deleteEnvVar(w http.ResponseWriter, r *http.Request) {
	if a.failed(w) {
		return
	}

	ctx, ok := a.resolveOrFail(w, r.PathValue("contextID"))
	if !ok {
		return
	}

	a.mu.Lock()
	delete(ctx.envVars, r.PathValue("name"))
	a.mu.Unlock()

	a.write(w, http.StatusOK, map[string]any{"message": "Environment variable deleted."})
}

// truncateContextEnvVarValue reproduces what the owning contexts service puts in
// a context environment variable's truncated_value.
//
// It is the tail of the value ON ITS OWN, with no mask prefix: the last four
// characters, or the last floor(len/2) when the value is eight characters or
// shorter. The service documents "FOO" → "O", "FOOBAR" → "BAR" and "FOOBARBAZ"
// → "RBAZ", and its own tests pin "hgfedcba" → "dcba" and "" → "".
//
// This fake used to answer "xxxx" plus the last four characters, borrowing the
// *project* environment variable convention (which does prefix the tail with
// "xxxx"). The two look alike and are not, and the client's own doc comment
// carried the same mistake — a shape agreed on by the client and the mock and by
// neither service, which is the exact failure mode the fakes here exist to
// avoid. Nothing surfaces truncated_value to a practitioner today, so this cost
// nothing; the point is that it would have if anything ever had.
func truncateContextEnvVarValue(value string) string {
	revealed := min(4, len(value)/2)

	return value[len(value)-revealed:]
}

func (a *contextFakeAPI) write(w http.ResponseWriter, status int, body any) {
	a.t.Helper()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		a.t.Errorf("encoding stand-in response: %v", err)
	}
}

// contextFakeProviderConfig points the provider at the stand-in API.
func contextFakeProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake-token"
}
`, host)
}
