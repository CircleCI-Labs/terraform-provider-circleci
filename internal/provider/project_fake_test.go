// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// fakeProjectAPI is an in-memory stand-in for the v2 project, v2 project
// settings, and v1.1 follow endpoints that circleci_project and the
// circleci_project data source depend on.
//
// It is shared between the project resource and data source unit tests: both
// exercise the same wire routes (internal/circleci/project.go and
// internal/circleci/project_settings.go), so one fake keeps the two test files
// from drifting on what "the API" means.
type fakeProjectAPI struct {
	t *testing.T

	mu sync.Mutex

	// orgKind decides the shape Create responds with.
	//
	//   - "classic" mimics a GitHub OAuth organization: addressed by name, and
	//     CircleCI does not follow the project as part of creating it, so a
	//     v1.1 follow call is expected afterwards.
	//   - "standalone" mimics a circleci-native organization: addressed by
	//     UUID, and the project is already followed by the create call, so no
	//     v1.1 follow call should ever be made.
	//
	// See internal/circleci/project.go's followProject for the condition this
	// mirrors.
	orgKind string

	projects map[string]*fakeProject   // keyed by slug
	settings map[string]map[string]any // keyed by slug
	patches  []map[string]any          // settings PATCH "advanced" bodies, in order
	requests []string                  // "METHOD path", in order
	followed []string                  // "vcsType/org/name" from every follow call

	// missingProject makes every GET for a single project answer 404, to
	// simulate a project deleted outside Terraform.
	missingProject bool

	// forceSlug, when non-empty, overrides the slug Create responds with,
	// regardless of orgKind. It exists to test the provider's handling of a
	// malformed slug coming back from the API, which orgKind's normal shapes
	// cannot produce.
	forceSlug string

	nextID int
}

// fakeProject is one project the fake API holds.
type fakeProject struct {
	id, name, slug                     string
	orgName, orgSlug, orgID            string
	vcsURL, vcsProvider, defaultBranch string
}

func (p *fakeProject) toJSON() map[string]any {
	return map[string]any{
		"id":                p.id,
		"name":              p.name,
		"slug":              p.slug,
		"organization_name": p.orgName,
		"organization_slug": p.orgSlug,
		"organization_id":   p.orgID,
		"vcs_info": map[string]any{
			"vcs_url":        p.vcsURL,
			"provider":       p.vcsProvider,
			"default_branch": p.defaultBranch,
		},
	}
}

// defaultFakeProjectSettings mirrors the defaults a freshly followed project
// has, matching fakeProjectSettingsAPI's defaults in project_settings_resource_test.go.
func defaultFakeProjectSettings() map[string]any {
	return map[string]any{
		"autocancel_builds":             false,
		"build_fork_prs":                false,
		"build_prs_only":                false,
		"disable_ssh":                   false,
		"forks_receive_secret_env_vars": true,
		"oss":                           false,
		"set_github_status":             false,
		"setup_workflows":               false,
		"write_settings_requires_admin": false,
		"pr_only_branch_overrides":      []any{},
	}
}

// newFakeProjectAPI starts the stand-in API and returns it alongside its
// origin, for tests that configure the provider by host.
func newFakeProjectAPI(t *testing.T, orgKind string) (*fakeProjectAPI, string) {
	t.Helper()

	api := &fakeProjectAPI{
		t:        t,
		orgKind:  orgKind,
		projects: map[string]*fakeProject{},
		settings: map[string]map[string]any{},
	}

	srv := httptest.NewServer(http.HandlerFunc(api.handle))
	t.Cleanup(srv.Close)

	return api, srv.URL
}

// newFakeProjectClient starts the stand-in API and returns it alongside a
// circleci.Client pointed at it, for tests that drive the resource's Go
// methods directly rather than through Terraform.
//
// It is always a "classic" organization: the standalone branch differs only in
// whether a v1.1 follow request is issued after create, which is a
// Terraform-level concern covered by TestProjectResourceUnit_CreateStandaloneOrgSkipsFollow
// against newFakeProjectAPI directly.
func newFakeProjectClient(t *testing.T) (*fakeProjectAPI, *circleci.Client) {
	t.Helper()

	api, host := newFakeProjectAPI(t, "classic")

	return api, circleci.New(circleci.Config{Host: host, Token: "fake"})
}

func (a *fakeProjectAPI) handle(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.requests = append(a.requests, r.Method+" "+r.URL.Path)
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodPost &&
		strings.HasPrefix(r.URL.Path, "/api/v2/organization/") &&
		strings.HasSuffix(r.URL.Path, "/project"):
		a.handleCreate(w, r)

	case r.Method == http.MethodPost &&
		strings.HasPrefix(r.URL.Path, "/api/v1.1/project/") &&
		strings.HasSuffix(r.URL.Path, "/follow"):
		a.handleFollow(w, r)

	case strings.HasPrefix(r.URL.Path, "/api/v2/project/"):
		a.handleProjectRoute(w, r)

	default:
		a.write(w, http.StatusNotFound, map[string]any{"message": "Not Found: " + r.Method + " " + r.URL.Path})
	}
}

func (a *fakeProjectAPI) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "invalid JSON body"})

		return
	}

	orgID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v2/organization/"), "/project")

	a.mu.Lock()
	a.nextID++
	id := fmt.Sprintf("proj-%d", a.nextID)

	var p *fakeProject
	switch a.orgKind {
	case "standalone":
		p = &fakeProject{
			id: id, name: body.Name,
			slug:          "circleci/" + orgID + "/" + id,
			orgName:       "Standalone Org Display Name",
			orgSlug:       "circleci/" + orgID,
			orgID:         orgID,
			vcsURL:        "//circleci.com/" + orgID + "/" + id,
			vcsProvider:   "CircleCI",
			defaultBranch: "main",
		}
	default: // "classic"
		p = &fakeProject{
			id: id, name: body.Name,
			slug:          "gh/AcmeOrg/" + body.Name,
			orgName:       "AcmeOrg",
			orgSlug:       "gh/AcmeOrg",
			orgID:         orgID,
			vcsURL:        "https://github.com/AcmeOrg/" + body.Name,
			vcsProvider:   "GitHub",
			defaultBranch: "main",
		}
	}

	if a.forceSlug != "" {
		p.slug = a.forceSlug
	}

	a.projects[p.slug] = p
	a.settings[p.slug] = defaultFakeProjectSettings()
	a.mu.Unlock()

	a.write(w, http.StatusCreated, p.toJSON())
}

func (a *fakeProjectAPI) handleFollow(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1.1/project/"), "/follow")

	a.mu.Lock()
	a.followed = append(a.followed, rest)
	a.mu.Unlock()

	w.WriteHeader(http.StatusOK)
}

func (a *fakeProjectAPI) handleProjectRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v2/project/")

	if slug, ok := strings.CutSuffix(rest, "/settings"); ok {
		switch r.Method {
		case http.MethodGet:
			a.handleGetSettings(w, slug)
		case http.MethodPatch:
			a.handlePatchSettings(w, r, slug)
		default:
			a.write(w, http.StatusMethodNotAllowed, map[string]any{"message": "Method Not Allowed"})
		}

		return
	}

	slug := rest
	switch r.Method {
	case http.MethodGet:
		a.handleGet(w, slug)
	case http.MethodDelete:
		a.handleDelete(w, slug)
	default:
		a.write(w, http.StatusMethodNotAllowed, map[string]any{"message": "Method Not Allowed"})
	}
}

func (a *fakeProjectAPI) handleGet(w http.ResponseWriter, slug string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.missingProject {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Project not found"})

		return
	}

	p, ok := a.projects[slug]
	if !ok {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Project not found"})

		return
	}

	a.write(w, http.StatusOK, p.toJSON())
}

func (a *fakeProjectAPI) handleDelete(w http.ResponseWriter, slug string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.projects[slug]; !ok {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Project not found"})

		return
	}

	delete(a.projects, slug)
	a.write(w, http.StatusOK, map[string]any{"message": "ok"})
}

func (a *fakeProjectAPI) handleGetSettings(w http.ResponseWriter, slug string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	current, ok := a.settings[slug]
	if !ok {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Project not found"})

		return
	}

	a.write(w, http.StatusOK, map[string]any{"advanced": current})
}

func (a *fakeProjectAPI) handlePatchSettings(w http.ResponseWriter, r *http.Request, slug string) {
	var body struct {
		Advanced map[string]any `json:"advanced"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "Invalid JSON body."})

		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	current, ok := a.settings[slug]
	if !ok {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Project not found"})

		return
	}

	if len(body.Advanced) == 0 {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "No JSON fields found."})

		return
	}

	a.patches = append(a.patches, body.Advanced)
	for key, value := range body.Advanced {
		current[key] = value
	}
	a.settings[slug] = current

	a.write(w, http.StatusOK, map[string]any{"advanced": current})
}

func (a *fakeProjectAPI) write(w http.ResponseWriter, status int, body any) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		a.t.Errorf("fake project API could not encode a response: %v", err)
	}
}

// addProject seeds a project directly, for tests that exercise Read, Update
// or Delete without going through Create.
func (a *fakeProjectAPI) addProject(p *fakeProject, settings map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.projects[p.slug] = p
	a.settings[p.slug] = settings
}

// setMissing makes every single-project GET answer 404.
func (a *fakeProjectAPI) setMissing(missing bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.missingProject = missing
}

// setForceSlug overrides the slug Create responds with. See forceSlug.
func (a *fakeProjectAPI) setForceSlug(slug string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.forceSlug = slug
}

func (a *fakeProjectAPI) recordedRequests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.requests...)
}

func (a *fakeProjectAPI) recordedPatches() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]map[string]any(nil), a.patches...)
}

func (a *fakeProjectAPI) followedCalls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.followed...)
}

// onlyPatch returns the single PATCH body received, failing when the provider
// sent a different number of settings updates.
func (a *fakeProjectAPI) onlyPatch(t *testing.T) map[string]any {
	t.Helper()

	patches := a.recordedPatches()
	if len(patches) != 1 {
		t.Fatalf("the provider sent %d settings PATCH requests, want exactly 1: %v", len(patches), patches)
	}

	return patches[0]
}
