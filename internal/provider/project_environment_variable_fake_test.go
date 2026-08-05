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
// the production shape. An earlier revision of this fake sent the
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

	// Even the create response's value comes back masked, the same as a
	// read. No route ever discloses the literal value, not even the one
	// that just set it.
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

	// created_at is always present and null when unrecorded, never absent. The
	// API builds the response with the key unconditionally, so "no timestamp"
	// is a null value rather than a missing key. This fake used to omit the
	// key instead, which decodes to the same "" in Go and so hid nothing —
	// but a fake that models a different shape from the service is one
	// refactor away from hiding something, and plural_fake_test.go's
	// seedEnvVar already had it right.
	body := map[string]any{"name": name, "value": maskEnvVarValue(v.value), "created_at": nil}
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
	a.write(w, http.StatusOK, map[string]any{"message": "Environment variable deleted."})
}

func (a *fakeEnvVarAPI) write(w http.ResponseWriter, status int, body any) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		a.t.Errorf("fake env var API could not encode a response: %v", err)
	}
}

// maskEnvVarValue mirrors how the API masks a value: four "x" characters
// followed by *up to* the last four characters of the real value.
//
// The tail is min(4, len/2) characters, rounded down — not a flat four. So a
// five-character value reveals two ("xxxxue" for "value") and a one-character
// value reveals none. This fake used to reveal four whenever the value was
// longer than four, which is right for anything eight characters or longer and
// wrong for the five-to-seven range. Nothing asserts on the mask for a value in
// that range today, so it hid nothing; it is corrected because a fake that
// invents its own masking rule is not evidence about the API's.
//
// Note that a context environment variable's truncated_value is a DIFFERENT
// shape — the same tail with no "xxxx" prefix, from a different service. See
// truncateContextEnvVarValue in context_fake_test.go.
func maskEnvVarValue(value string) string {
	revealed := min(4, len(value)/2)

	return "xxxx" + value[len(value)-revealed:]
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
