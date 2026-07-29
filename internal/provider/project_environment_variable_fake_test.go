// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeEnvVarAPI is an in-memory stand-in for the v2 project environment
// variable endpoints that circleci_project_environment_variable and its data
// source depend on.
//
// The field key is "created_at" (underscore), matching
// internal/circleci/environment_variable.go's ProjectEnvironmentVariable and
// the production shape confirmed against the API's env-var-public-view
// (the CircleCI API). An earlier revision of this fake sent the
// hyphenated "created-at" that the old vendored client library's own (wrongly
// tagged) environment-variable type decoded, which meant the real API's
// created_at was silently dropped whatever that library reported — that
// mismatch is now moot because this provider no longer goes through it at
// all.
type fakeEnvVarAPI struct {
	t *testing.T

	mu sync.Mutex

	vars     map[string]map[string]fakeEnvVar // slug -> name -> variable
	requests []string
	creates  []map[string]string // recorded create bodies (name, value), in order

	// missing makes a single-variable GET answer 404 for this exact
	// "slug/name" key, to simulate a variable deleted outside Terraform.
	missing map[string]bool

	// forceStatus, keyed by "slug/name", makes a single-variable GET answer
	// with this status instead of its normal response, for testing errors a
	// 404 would not exercise (the resource special-cases 404 as drift).
	forceStatus map[string]int
}

type fakeEnvVar struct {
	value     string
	createdAt string // RFC 3339, or "" to omit the field entirely
}

func newFakeEnvVarAPI(t *testing.T) (*fakeEnvVarAPI, string) {
	t.Helper()

	api := &fakeEnvVarAPI{
		t:           t,
		vars:        map[string]map[string]fakeEnvVar{},
		missing:     map[string]bool{},
		forceStatus: map[string]int{},
	}

	srv := httptest.NewServer(http.HandlerFunc(api.handle))
	t.Cleanup(srv.Close)

	return api, srv.URL
}

func (a *fakeEnvVarAPI) handle(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.requests = append(a.requests, r.Method+" "+r.URL.Path)
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	const prefix = "/api/v2/project/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Not Found: " + r.URL.Path})

		return
	}

	rest := strings.TrimPrefix(r.URL.Path, prefix)

	idx := strings.Index(rest, "/envvar")
	if idx == -1 {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Not Found: " + r.URL.Path})

		return
	}

	slug := rest[:idx]
	tail := strings.TrimPrefix(rest[idx+len("/envvar"):], "/")

	switch {
	case r.Method == http.MethodPost && tail == "":
		a.handleCreate(w, r, slug)
	case r.Method == http.MethodGet && tail != "":
		a.handleGet(w, slug, tail)
	case r.Method == http.MethodDelete && tail != "":
		a.handleDelete(w, slug, tail)
	default:
		a.write(w, http.StatusMethodNotAllowed, map[string]any{"message": "Method Not Allowed"})
	}
}

func (a *fakeEnvVarAPI) handleCreate(w http.ResponseWriter, r *http.Request, slug string) {
	var body struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "invalid JSON body"})

		return
	}

	a.mu.Lock()
	a.creates = append(a.creates, map[string]string{"name": body.Name, "value": body.Value})
	if a.vars[slug] == nil {
		a.vars[slug] = map[string]fakeEnvVar{}
	}
	a.vars[slug][body.Name] = fakeEnvVar{value: body.Value, createdAt: "2024-01-02T03:04:05.000Z"}
	a.mu.Unlock()

	// Matches create-env-var-response (the CircleCI API): even
	// the create response's value comes back through env-var-read-api, i.e.
	// already masked. No route ever discloses the literal value, not even the
	// one that just set it.
	a.write(w, http.StatusCreated, map[string]any{
		"name":       body.Name,
		"value":      maskEnvVarValue(body.Value),
		"created_at": "2024-01-02T03:04:05.000Z",
	})
}

func (a *fakeEnvVarAPI) handleGet(w http.ResponseWriter, slug, name string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if status := a.forceStatus[slug+"/"+name]; status != 0 {
		a.write(w, status, map[string]any{"message": "internal error"})

		return
	}

	if a.missing[slug+"/"+name] {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Environment variable not found"})

		return
	}

	v, ok := a.vars[slug][name]
	if !ok {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Environment variable not found"})

		return
	}

	body := map[string]any{"name": name, "value": maskEnvVarValue(v.value)}
	if v.createdAt != "" {
		body["created_at"] = v.createdAt
	}

	a.write(w, http.StatusOK, body)
}

func (a *fakeEnvVarAPI) handleDelete(w http.ResponseWriter, slug, name string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.vars[slug][name]; !ok {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Environment variable not found"})

		return
	}

	delete(a.vars[slug], name)
	a.write(w, http.StatusOK, map[string]any{"message": "ok"})
}

func (a *fakeEnvVarAPI) write(w http.ResponseWriter, status int, body any) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		a.t.Errorf("fake env var API could not encode a response: %v", err)
	}
}

// maskEnvVarValue mirrors the real API: four "x" characters followed by the
// last four characters of the real value (internal/circleci/environment_variable.go).
func maskEnvVarValue(value string) string {
	if len(value) <= 4 {
		return "xxxx"
	}

	return "xxxx" + value[len(value)-4:]
}

// seed adds an environment variable directly, bypassing Create, for tests
// that exercise Read, Update or Delete on their own.
func (a *fakeEnvVarAPI) seed(slug, name, value, createdAt string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.vars[slug] == nil {
		a.vars[slug] = map[string]fakeEnvVar{}
	}
	a.vars[slug][name] = fakeEnvVar{value: value, createdAt: createdAt}
}

// setMissing makes a single "slug/name" pair answer 404 on GET.
func (a *fakeEnvVarAPI) setMissing(slug, name string, missing bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.missing[slug+"/"+name] = missing
}

// setForceStatus makes a single "slug/name" pair answer with status on GET,
// for exercising an error path a 404 would not (404 is drift, not an error,
// to this resource).
func (a *fakeEnvVarAPI) setForceStatus(slug, name string, status int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.forceStatus[slug+"/"+name] = status
}

func (a *fakeEnvVarAPI) recordedRequests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.requests...)
}

func (a *fakeEnvVarAPI) recordedCreates() []map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]map[string]string(nil), a.creates...)
}
