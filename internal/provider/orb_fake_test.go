// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// This file stands up an in-memory fake of the v3 namespace, orb and orb version
// routes, so that the orb resources can be driven through real Terraform plans
// and applies without a CircleCI account.
//
// The fake keeps real state rather than replaying canned responses, because the
// interesting behaviour of these resources is stateful: publishing a version
// twice must fail, and destroying an orb version must not call the API at all.
//
// [FAKE], not [NET] — read this before trusting a "TestAcc*" name in this
// family. Every "TestAcc*" test in orb_namespace_resource_test.go,
// orb_resource_test.go, orb_version_resource_test.go and
// orb_data_sources_test.go runs resource.UnitTest against this fake, not
// resource.Test against a live account via testAccPreCheck. That is the
// opposite of what the prefix means everywhere else in this package (see
// acctest_test.go: "that prefix is reserved... for tests exercising a real
// acceptance-test resource"). It was the only way this family had ever been
// exercised before this investigation added a handful of real,
// testAccPreCheck-gated tests (search this package for "is [NET]" for the
// full list) — so treat any claim resting solely on a fake-backed TestAcc* in
// this family as UNVALIDATED against a real CircleCI installation, including
// ones this comment does not call out individually. Three claims that fake
// coverage got wrong before real testing caught them.
//
// First: namespace rename and delete used to look safe and successful here, but
// answer 403 Forbidden unconditionally on every live account tested — see
// Namespace's doc comment in internal/circleci/namespace.go. renameNamespace and
// deleteNamespace below are now fixed to match that finding rather than to model
// a route that succeeds: this fake has no code path left that lets either report
// success, so there is nothing left here for a "TestAcc*" name to overclaim.
//
// Second: the collection shape modelled here (orbSummary) undersells what a real
// listing returns, by omitting usage counts that are genuinely present; see
// orbPackageListWire's comment in internal/circleci/orb.go.
//
// Third, and the worst of them: createOrb used to accept and echo back any orb
// name at all, including a namespace-qualified one. That is exactly what let the
// real create route reject every single name this provider ever sent it. The fake
// could not have caught its own resource's core bug, because it did not model the
// one validation that mattered. See createOrb's own comment.
//
// That third gap is now covered, but not by teaching this fake to create a real
// orb — nothing here can, since the real route has no way to undo one. This
// family's real-API test files instead drive circleci_orb and
// circleci_orb_version against a real account along the one path that can
// never leave anything behind: a request guaranteed to be rejected (a
// duplicate orb name, a republish of an already-published version),
// asserting on the SPECIFIC rejection message that distinguishes
// "the API never even looked at whether this exists" from "the API looked,
// and it does." A regression that reintroduces the qualified-name bug changes
// which of those two messages comes back, which is exactly what those tests
// would catch.

// orbFakeRequest is one request the fake received.
type orbFakeRequest struct {
	Method string
	Path   string
	Query  url.Values
	Body   string
}

// orbFakeNamespace deliberately has no organization field: the real API's
// namespace representation does not report the owning organization either, which
// is why circleci_orb_namespace needs it in the import id.
type orbFakeNamespace struct {
	ID   string
	Name string
}

type orbFakeOrb struct {
	ID          string
	Name        string
	NamespaceID string
	IsPrivate   bool
	IsListed    bool
	CategoryIDs []string
	VersionIDs  []string
}

type orbFakeVersion struct {
	ID      string
	OrbID   string
	Version string
	Source  string
}

// orbFakeAPI is a fake of the CircleCI v3 namespace and orb API.
type orbFakeAPI struct {
	server *httptest.Server

	mu         sync.Mutex
	requests   []orbFakeRequest
	namespaces map[string]*orbFakeNamespace
	orbs       map[string]*orbFakeOrb
	versions   map[string]*orbFakeVersion
	nextID     int

	// failSetOrbListedStatus and failGetOrbSourceStatus, when non-zero, make
	// every request to the corresponding route answer with that status instead
	// of the normal behavior. They exist to test what happens when the create
	// call that establishes an orb, or an orb version, succeeds but a call
	// after it fails — see setFailSetOrbListedStatus and
	// setFailGetOrbSourceStatus.
	failSetOrbListedStatus int
	failGetOrbSourceStatus int
}

// orbFakeCategories is the fixed category set CircleCI publishes.
var orbFakeCategories = []struct{ ID, Name string }{
	{"99999999-0000-0000-0000-000000000001", "Build"},
	{"99999999-0000-0000-0000-000000000002", "Notifications"},
}

// newOrbFakeAPI starts a fake v3 API and stops it when the test ends.
func newOrbFakeAPI(t *testing.T) *orbFakeAPI {
	t.Helper()

	api := &orbFakeAPI{
		namespaces: map[string]*orbFakeNamespace{},
		orbs:       map[string]*orbFakeOrb{},
		versions:   map[string]*orbFakeVersion{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/namespaces", api.getNamespaceByName)
	mux.HandleFunc("POST /api/v3/namespaces", api.createNamespace)
	mux.HandleFunc("GET /api/v3/namespaces/{id}", api.getNamespace)
	mux.HandleFunc("DELETE /api/v3/namespaces/{id}", api.deleteNamespace)
	mux.HandleFunc("POST /api/v3/namespaces/{id}/rename", api.renameNamespace)
	mux.HandleFunc("GET /api/v3/orb/packages", api.listOrbs)
	mux.HandleFunc("POST /api/v3/orb/packages", api.createOrb)
	mux.HandleFunc("GET /api/v3/orb/packages/{id}", api.getOrb)
	mux.HandleFunc("POST /api/v3/orb/packages/{id}/set-listed", api.setOrbListed)
	mux.HandleFunc("POST /api/v3/orb/packages/{id}/add-category", api.addOrbCategory)
	mux.HandleFunc("POST /api/v3/orb/packages/{id}/remove-category", api.removeOrbCategory)
	mux.HandleFunc("GET /api/v3/orb/versions", api.listOrbVersions)
	mux.HandleFunc("POST /api/v3/orb/versions", api.publishOrbVersion)
	mux.HandleFunc("GET /api/v3/orb/versions/{id}", api.getOrbVersion)
	mux.HandleFunc("GET /api/v3/orb/versions/{id}/source", api.getOrbSource)
	mux.HandleFunc("GET /api/v3/orb/categories", api.listOrbCategories)

	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.record(r)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(api.server.Close)

	return api
}

// URL is the fake's origin, for the provider's host attribute.
func (a *orbFakeAPI) URL() string { return a.server.URL }

func (a *orbFakeAPI) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))

	a.mu.Lock()
	defer a.mu.Unlock()

	a.requests = append(a.requests, orbFakeRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Body:   string(body),
	})
}

// setFailSetOrbListedStatus makes every POST .../set-listed answer with status
// instead of setting the listed flag, so a test can force that one route to
// fail after CreateOrbPackage has already succeeded.
func (a *orbFakeAPI) setFailSetOrbListedStatus(status int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.failSetOrbListedStatus = status
}

// setFailGetOrbSourceStatus makes every GET .../source answer with status
// instead of the stored source, so a test can force that one route to fail
// after PublishOrbVersion has already succeeded.
func (a *orbFakeAPI) setFailGetOrbSourceStatus(status int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.failGetOrbSourceStatus = status
}

// requestsFor returns every recorded request whose method matches and whose path
// contains the given fragment.
func (a *orbFakeAPI) requestsFor(method, pathContains string) []orbFakeRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	var matching []orbFakeRequest
	for _, request := range a.requests {
		if request.Method == method && strings.Contains(request.Path, pathContains) {
			matching = append(matching, request)
		}
	}

	return matching
}

// allRequests returns a copy of every recorded request, for failure messages.
func (a *orbFakeAPI) allRequests() []orbFakeRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]orbFakeRequest(nil), a.requests...)
}

// seedNamespace adds a namespace directly, for tests that read one rather than
// create it.
func (a *orbFakeAPI) seedNamespace(name string) *orbFakeNamespace {
	a.mu.Lock()
	defer a.mu.Unlock()

	ns := &orbFakeNamespace{ID: a.mintID(), Name: name}
	a.namespaces[ns.ID] = ns

	return ns
}

// seedOrb adds a listed orb directly, for tests that read one rather than create
// it. Tests that need an unlisted orb create one with is_listed = false, so that
// the /set-listed call is exercised rather than bypassed.
func (a *orbFakeAPI) seedOrb(name, namespaceID string, categoryIDs ...string) *orbFakeOrb {
	a.mu.Lock()
	defer a.mu.Unlock()

	orb := &orbFakeOrb{ID: a.mintID(), Name: name, NamespaceID: namespaceID, IsListed: true, CategoryIDs: categoryIDs}
	a.orbs[orb.ID] = orb

	return orb
}

// seedVersion adds a published version directly.
func (a *orbFakeAPI) seedVersion(orbID, version, source string) *orbFakeVersion {
	a.mu.Lock()
	defer a.mu.Unlock()

	v := &orbFakeVersion{ID: a.mintID(), OrbID: orbID, Version: version, Source: source}
	a.versions[v.ID] = v
	if orb := a.orbs[orbID]; orb != nil {
		orb.VersionIDs = append([]string{v.ID}, orb.VersionIDs...)
	}

	return v
}

// mintID returns a new UUID-shaped identifier. The shape matters: the resources
// tell a UUID from a name when parsing import ids.
func (a *orbFakeAPI) mintID() string {
	a.nextID++

	return fmt.Sprintf("%08d-1111-2222-3333-444444444444", a.nextID)
}

// orbFakeCreatedAt is a v3 timestamp in the exact form the API emits: UTC,
// RFC 3339, and always three decimal places, because the v3 response marshaller
// truncates every time.Time to milliseconds and formats it that way. A bare
// "...:05Z" is not a shape any v3 route can produce.
const orbFakeCreatedAt = "2026-01-02T03:04:05.000Z"

func orbFakeWriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func orbFakeWriteError(w http.ResponseWriter, status int, title, detail string) {
	orbFakeWriteJSON(w, status, map[string]any{
		"error": map[string]any{"title": title, "detail": detail},
	})
}

func orbFakeDecode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		orbFakeWriteError(w, http.StatusBadRequest, "Bad Request", err.Error())

		return false
	}

	return true
}

// --- namespaces ---

func (a *orbFakeAPI) namespaceEntity(ns *orbFakeNamespace) map[string]any {
	return map[string]any{"data": map[string]any{
		"id":         ns.ID,
		"attributes": map[string]any{"name": ns.Name},
	}}
}

func (a *orbFakeAPI) getNamespaceByName(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("filter[name]")

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, ns := range a.namespaces {
		if ns.Name == name {
			orbFakeWriteJSON(w, http.StatusOK, a.namespaceEntity(ns))

			return
		}
	}

	orbFakeWriteError(w, http.StatusNotFound, "Not Found", "namespace "+name+" not found")
}

func (a *orbFakeAPI) createNamespace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		OrgID string `json:"org_id"`
	}
	if !orbFakeDecode(w, r, &body) {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, ns := range a.namespaces {
		if ns.Name == body.Name {
			orbFakeWriteError(w, http.StatusBadRequest, "Bad Request", "namespace already exists")

			return
		}
	}

	// body.OrgID is accepted and then dropped, exactly as the real API does: the
	// namespace representation never reports its owning organization again.
	ns := &orbFakeNamespace{ID: a.mintID(), Name: body.Name}
	a.namespaces[ns.ID] = ns

	orbFakeWriteJSON(w, http.StatusCreated, a.namespaceEntity(ns))
}

func (a *orbFakeAPI) getNamespace(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	ns, ok := a.namespaces[r.PathValue("id")]
	if !ok {
		orbFakeWriteError(w, http.StatusNotFound, "Not Found", "namespace not found")

		return
	}

	orbFakeWriteJSON(w, http.StatusOK, a.namespaceEntity(ns))
}

// renameNamespace and deleteNamespace are unreachable from the provider: no
// client method calls either route any more (see internal/circleci/namespace.go
// and orb_namespace_resource.go's Update and Delete). They stay registered and
// modelled here — rather than being deleted along with the toggles that used
// to make their success conditional — so that a future regression which
// mistakenly wires a call back in hits the same wall [NET] measurement found
// on a live account: 403 Forbidden, unconditionally, on a namespace that
// exists, and 404 on one that does not. There is no code path left in this
// fake that lets either route report success.

func (a *orbFakeAPI) renameNamespace(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.namespaces[r.PathValue("id")]; !ok {
		orbFakeWriteError(w, http.StatusNotFound, "Not Found", "namespace not found")

		return
	}

	orbFakeWriteError(w, http.StatusForbidden, "Forbidden.", "")
}

func (a *orbFakeAPI) deleteNamespace(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.namespaces[r.PathValue("id")]; !ok {
		orbFakeWriteError(w, http.StatusNotFound, "Not Found", "namespace not found")

		return
	}

	orbFakeWriteError(w, http.StatusForbidden, "Forbidden.", "")
}

// --- orbs ---

// orbDetail renders the by-id shape, whose namespace reference carries a name.
//
// reverseCategories controls the order orb_categories renders in — see
// orbCategoryRefs. getOrb (Read) and setOrbListed pass true, standing in for a
// route that answers with a snapshot of the package's current state rather than
// echoing back the effect of a category mutation just performed: createOrb,
// addOrbCategory and removeOrbCategory pass false. That split is what lets an
// is_listed-only update — which never touches category membership — show the
// registry reporting categories in a different order than the one Create last
// wrote to state, exactly as the real, unordered API could.
func (a *orbFakeAPI) orbDetail(orb *orbFakeOrb, reverseCategories bool) map[string]any {
	namespace := map[string]any{"id": orb.NamespaceID, "attributes": map[string]any{"name": ""}}
	if ns, ok := a.namespaces[orb.NamespaceID]; ok {
		namespace["attributes"] = map[string]any{"name": ns.Name}
	}

	return map[string]any{
		"id": orb.ID,
		"attributes": map[string]any{
			"name":                       orb.Name,
			"is_private":                 orb.IsPrivate,
			"is_listed":                  orb.IsListed,
			"created_at":                 orbFakeCreatedAt,
			"home_url":                   "",
			"last_30_days_build_count":   0,
			"last_30_days_project_count": 0,
			"last_30_days_org_count":     0,
		},
		"references": map[string]any{
			"namespace":      namespace,
			"orb_versions":   a.orbVersionRefs(orb),
			"orb_categories": a.orbCategoryRefs(orb, reverseCategories),
		},
	}
}

// orbSummary renders the collection shape, whose namespace reference is only an
// id. Keeping the fake faithful here is what lets the tests prove the resources
// do not depend on data the collection never sends.
func (a *orbFakeAPI) orbSummary(orb *orbFakeOrb) map[string]any {
	return map[string]any{
		"id": orb.ID,
		"attributes": map[string]any{
			"name":       orb.Name,
			"is_private": orb.IsPrivate,
			"is_listed":  orb.IsListed,
		},
		"references": map[string]any{
			"namespace":      map[string]any{"id": orb.NamespaceID},
			"orb_versions":   a.orbVersionRefs(orb),
			"orb_categories": a.orbCategoryRefs(orb, false),
		},
	}
}

func (a *orbFakeAPI) orbVersionRefs(orb *orbFakeOrb) []map[string]any {
	refs := make([]map[string]any, 0, len(orb.VersionIDs))
	for _, id := range orb.VersionIDs {
		v, ok := a.versions[id]
		if !ok {
			continue
		}
		refs = append(refs, map[string]any{
			"id":         v.ID,
			"attributes": map[string]any{"version": v.Version, "created_at": orbFakeCreatedAt},
		})
	}

	return refs
}

// orbCategoryRefs renders an orb's categories.
//
// The real registry gives no ordering guarantee at all for an orb's categories,
// and this fake previously always echoed orb.CategoryIDs in insertion order —
// which a test suite that only ever adds one category cannot tell apart from a
// genuinely stable order. reversed renders them back to front instead, which is
// what lets a test with two or more categories exercise the same failure a real
// reordering API would cause: `categories` is a Computed ListNestedAttribute, so
// two responses disagreeing about element order look like a change with
// nothing to apply. orbCategoriesToList (orb_resource.go) sorts the result so
// this cannot happen. See orbDetail for which routes pass true.
func (a *orbFakeAPI) orbCategoryRefs(orb *orbFakeOrb, reversed bool) []map[string]any {
	ids := orb.CategoryIDs
	if reversed {
		ids = make([]string, len(orb.CategoryIDs))
		for i, id := range orb.CategoryIDs {
			ids[len(orb.CategoryIDs)-1-i] = id
		}
	}

	refs := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		for _, category := range orbFakeCategories {
			if category.ID == id {
				refs = append(refs, map[string]any{
					"id":         category.ID,
					"attributes": map[string]any{"name": category.Name},
				})
			}
		}
	}

	return refs
}

// listOrbs implements the filter precedence the real handler has, which is not
// the additive one the shape of the query string suggests.
//
// filter[name] is a separate by-name lookup that runs first and returns
// immediately, so it ignores every other filter — including filter[visibility],
// and it finds a private orb without being asked to. filter[visibility] is read
// only on the namespace-scoped branch, where it selects public-only or
// private-only rather than widening the set. A filter[visibility] with no
// filter[namespace_id] does nothing at all.
//
// The fake used to apply visibility to every listing, which made the client's
// retry with filter[visibility]=private look load-bearing when against
// production it was a duplicate of the request that had just been made.
func (a *orbFakeAPI) listOrbs(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	name := r.URL.Query().Get("filter[name]")
	namespaceID := r.URL.Query().Get("filter[namespace_id]")
	visibility := r.URL.Query().Get("filter[visibility]")

	data := make([]map[string]any, 0, len(a.orbs))
	for _, orb := range a.orbs {
		switch {
		case name != "":
			if orb.Name != name {
				continue
			}
		case namespaceID != "":
			if orb.NamespaceID != namespaceID {
				continue
			}
			if orb.IsPrivate != (visibility == "private") {
				continue
			}
		}
		data = append(data, a.orbSummary(orb))
	}

	// A single-page v3 collection omits "page" entirely rather than sending null
	// cursors: the envelope's page object is only emitted when a cursor exists.
	orbFakeWriteJSON(w, http.StatusOK, map[string]any{"data": data})
}

// createOrb models POST /orb/packages.
//
// [NET]: this route wants the BARE orb name and rejects anything containing
// "/" — including the fully qualified form every read route reports — with a
// generic 400 that reads exactly like a name-format or quota problem
// regardless of what was actually sent; see CreateOrbPackage's doc comment in
// internal/circleci/orb.go for how that was tracked down. This fake used to
// accept whatever name it was given verbatim and echo it straight back,
// which is what let orb_resource.go send the qualified "<namespace>/<orb>"
// form for as long as it did without a single test noticing: the fake never
// modeled the one behavior that would have caught it.
func (a *orbFakeAPI) createOrb(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Data struct {
			Attributes struct {
				Name      string `json:"name"`
				IsPrivate bool   `json:"is_private"`
			} `json:"attributes"`
			References struct {
				Namespace struct {
					ID string `json:"id"`
				} `json:"namespace"`
			} `json:"references"`
		} `json:"data"`
	}
	if !orbFakeDecode(w, r, &body) {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	name := body.Data.Attributes.Name
	if strings.Contains(name, "/") {
		orbFakeWriteError(w, http.StatusBadRequest, "Bad Request",
			fmt.Sprintf("Cannot create an Orb named '%s': this name is invalid. See the "+
				"documentation for more information about the restrictions on Orb names.", name))

		return
	}

	ns, ok := a.namespaces[body.Data.References.Namespace.ID]
	if !ok {
		orbFakeWriteError(w, http.StatusNotFound, "Not Found.", "")

		return
	}

	// The stored/reported name is namespace-qualified, matching every read
	// route, even though the request that created it was not.
	qualified := ns.Name + "/" + name
	for _, orb := range a.orbs {
		if orb.Name == qualified {
			orbFakeWriteError(w, http.StatusBadRequest, "Bad Request", "orb already exists")

			return
		}
	}

	orb := &orbFakeOrb{
		ID:          a.mintID(),
		Name:        qualified,
		NamespaceID: body.Data.References.Namespace.ID,
		IsPrivate:   body.Data.Attributes.IsPrivate,
		// CircleCI lists a new public orb and hides a private one.
		IsListed: !body.Data.Attributes.IsPrivate,
	}
	a.orbs[orb.ID] = orb

	orbFakeWriteJSON(w, http.StatusCreated, map[string]any{"data": a.orbDetail(orb, false)})
}

func (a *orbFakeAPI) lockedOrb(w http.ResponseWriter, r *http.Request) (*orbFakeOrb, bool) {
	orb, ok := a.orbs[r.PathValue("id")]
	if !ok {
		orbFakeWriteError(w, http.StatusNotFound, "Not Found", "orb not found")

		return nil, false
	}

	return orb, true
}

func (a *orbFakeAPI) getOrb(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	orb, ok := a.lockedOrb(w, r)
	if !ok {
		return
	}

	orbFakeWriteJSON(w, http.StatusOK, map[string]any{"data": a.orbDetail(orb, true)})
}

func (a *orbFakeAPI) setOrbListed(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IsListed bool `json:"is_listed"`
	}
	if !orbFakeDecode(w, r, &body) {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if status := a.failSetOrbListedStatus; status != 0 {
		orbFakeWriteError(w, status, "Internal Server Error", "forced failure for test")

		return
	}

	orb, ok := a.lockedOrb(w, r)
	if !ok {
		return
	}

	orb.IsListed = body.IsListed

	orbFakeWriteJSON(w, http.StatusOK, map[string]any{"data": a.orbDetail(orb, true)})
}

func (a *orbFakeAPI) addOrbCategory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CategoryID string `json:"category_id"`
	}
	if !orbFakeDecode(w, r, &body) {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	orb, ok := a.lockedOrb(w, r)
	if !ok {
		return
	}

	for _, id := range orb.CategoryIDs {
		if id == body.CategoryID {
			orbFakeWriteJSON(w, http.StatusOK, map[string]any{"data": a.orbDetail(orb, false)})

			return
		}
	}
	orb.CategoryIDs = append(orb.CategoryIDs, body.CategoryID)

	orbFakeWriteJSON(w, http.StatusOK, map[string]any{"data": a.orbDetail(orb, false)})
}

func (a *orbFakeAPI) removeOrbCategory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CategoryID string `json:"category_id"`
	}
	if !orbFakeDecode(w, r, &body) {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	orb, ok := a.lockedOrb(w, r)
	if !ok {
		return
	}

	remaining := make([]string, 0, len(orb.CategoryIDs))
	for _, id := range orb.CategoryIDs {
		if id != body.CategoryID {
			remaining = append(remaining, id)
		}
	}
	orb.CategoryIDs = remaining

	orbFakeWriteJSON(w, http.StatusOK, map[string]any{"data": a.orbDetail(orb, false)})
}

func (a *orbFakeAPI) listOrbCategories(w http.ResponseWriter, _ *http.Request) {
	data := make([]map[string]any, 0, len(orbFakeCategories))
	for _, category := range orbFakeCategories {
		data = append(data, map[string]any{
			"id":         category.ID,
			"attributes": map[string]any{"name": category.Name},
		})
	}

	orbFakeWriteJSON(w, http.StatusOK, map[string]any{"data": data})
}

// --- orb versions ---

// versionEntity renders an orb version.
//
// references.orb_package carries an id and nothing else. That is not a shortcut:
// the API's orb reference renders its attributes object only when it has an orb
// name to put in it, and the version records it renders from never carry one, so
// no orb version route ever sends references.orb_package.attributes.
//
// The fake used to send the name, which made the whole test suite endorse a
// field production omits: every assertion on orb_name passed while the attribute
// was permanently empty against the real API. The client now resolves the name
// from the orb package instead, and this fake is what proves it has to.
//
// attributes.source is likewise absent. The by-id route includes it only when
// asked with ?include=source, which the client does not do — it reads
// /orb/versions/{id}/source instead, so that one code path serves every route.
func (a *orbFakeAPI) versionEntity(v *orbFakeVersion) map[string]any {
	return map[string]any{
		"id": v.ID,
		"attributes": map[string]any{
			"version":    v.Version,
			"created_at": orbFakeCreatedAt,
		},
		"references": map[string]any{
			"orb_package": map[string]any{"id": v.OrbID},
		},
	}
}

func (a *orbFakeAPI) publishOrbVersion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Data struct {
			Attributes struct {
				OrbID   string `json:"orb_id"`
				YAML    string `json:"yaml"`
				Version string `json:"version"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if !orbFakeDecode(w, r, &body) {
		return
	}

	attrs := body.Data.Attributes

	a.mu.Lock()
	defer a.mu.Unlock()

	orb, ok := a.orbs[attrs.OrbID]
	if !ok {
		orbFakeWriteError(w, http.StatusNotFound, "Not Found", "orb not found")

		return
	}

	// Publishing the same stable version twice is rejected: a published version is
	// immutable. Dev versions may be republished.
	isDev := strings.HasPrefix(attrs.Version, "dev:")
	for _, id := range orb.VersionIDs {
		existing := a.versions[id]
		if existing == nil || existing.Version != attrs.Version {
			continue
		}
		if !isDev {
			orbFakeWriteError(w, http.StatusBadRequest, "Bad Request",
				"version "+attrs.Version+" already exists")

			return
		}

		existing.Source = attrs.YAML
		orbFakeWriteJSON(w, http.StatusCreated, map[string]any{"data": a.versionEntity(existing)})

		return
	}

	v := &orbFakeVersion{ID: a.mintID(), OrbID: orb.ID, Version: attrs.Version, Source: attrs.YAML}
	a.versions[v.ID] = v
	orb.VersionIDs = append([]string{v.ID}, orb.VersionIDs...)

	orbFakeWriteJSON(w, http.StatusCreated, map[string]any{"data": a.versionEntity(v)})
}

func (a *orbFakeAPI) getOrbVersion(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	v, ok := a.versions[r.PathValue("id")]
	if !ok {
		orbFakeWriteError(w, http.StatusNotFound, "Not Found", "orb version not found")

		return
	}

	orbFakeWriteJSON(w, http.StatusOK, map[string]any{"data": a.versionEntity(v)})
}

func (a *orbFakeAPI) listOrbVersions(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	orbID := r.URL.Query().Get("filter[orb_id]")
	ref := r.URL.Query().Get("filter[ref]")

	// filter[ref] is a fully-qualified "namespace/orb@version" reference, and the
	// server resolves through it while ignoring filter[orb_id] entirely. The fake
	// previously accepted a bare version and honoured orb_id alongside ref, which
	// meant the test suite endorsed a contract production does not implement.
	if ref != "" {
		orbFakeWriteJSON(w, http.StatusOK, map[string]any{"data": a.versionsMatchingRef(ref)})

		return
	}

	if orbID == "" {
		orbFakeWriteJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{
				"type":   "validation_error",
				"id":     "trace",
				"title":  "Missing Required Filter",
				"detail": "Query parameter 'filter[orb_id]' is required.",
			},
		})

		return
	}

	data := make([]map[string]any, 0, len(a.versions))
	if orb, ok := a.orbs[orbID]; ok {
		for _, id := range orb.VersionIDs {
			if v := a.versions[id]; v != nil {
				data = append(data, a.versionEntity(v))
			}
		}
	}

	// A single-page v3 collection omits "page" entirely rather than sending null
	// cursors.
	orbFakeWriteJSON(w, http.StatusOK, map[string]any{"data": data})
}

// versionsMatchingRef resolves a qualified "namespace/orb@version" reference,
// including the "volatile" alias, which selects the most recent version.
func (a *orbFakeAPI) versionsMatchingRef(ref string) []map[string]any {
	at := strings.LastIndex(ref, "@")
	if at < 0 {
		return []map[string]any{}
	}

	qualifiedOrb, wanted := ref[:at], ref[at+1:]

	for _, orb := range a.orbs {
		// The API reports attributes.name in the qualified "namespace/orb" form,
		// which is what filter[ref] carries before the "@version" suffix.
		if orb.Name != qualifiedOrb {
			continue
		}

		for _, id := range orb.VersionIDs {
			v := a.versions[id]
			if v == nil {
				continue
			}
			if wanted == "volatile" || v.Version == wanted {
				return []map[string]any{a.versionEntity(v)}
			}
		}
	}

	return []map[string]any{}
}

func (a *orbFakeAPI) getOrbSource(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if status := a.failGetOrbSourceStatus; status != 0 {
		orbFakeWriteError(w, status, "Internal Server Error", "forced failure for test")

		return
	}

	v, ok := a.versions[r.PathValue("id")]
	if !ok {
		orbFakeWriteError(w, http.StatusNotFound, "Not Found", "orb version not found")

		return
	}

	// The real route answers text/plain, which is why the client needs a raw
	// decoder for it.
	w.Header().Set("Content-Type", "text/plain")
	_, _ = io.WriteString(w, v.Source)
}

// --- provider wiring ---

// orbProviderConfig renders a provider block pointing at the fake.
func orbProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
`, host)
}

// orbServerProviderConfig renders a provider block that claims to be CircleCI
// Server, for the tests that assert the orb types refuse to run there.
func orbServerProviderConfig() string {
	return `
provider "circleci" {
  host       = "https://circleci.example.com"
  key        = "fake"
  deployment = "server"
}
`
}
