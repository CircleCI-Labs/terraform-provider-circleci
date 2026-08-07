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

	// failSettingsStatus, when non-zero, makes every settings GET and PATCH
	// answer with that status instead of the normal behavior. It exists to
	// test what happens when CreateProject has already succeeded but the
	// settings call Create makes right after it fails — see
	// setFailSettingsStatus.
	failSettingsStatus int

	// forceSlug, when non-empty, overrides the slug Create responds with,
	// regardless of orgKind. It exists to test the provider's handling of a
	// malformed slug coming back from the API, which orgKind's normal shapes
	// cannot produce.
	forceSlug string

	// branchOverridesResponse, when non-nil, replaces whatever
	// pr_only_branch_overrides a settings PATCH would otherwise store and echo
	// back, regardless of what was sent. Every other response this fake gives
	// is derived from the request (or, for pr_only_branch_overrides, reordered
	// but otherwise unchanged by reorderedLikeTheAPI), so there is no other way
	// to make it answer with a list that genuinely differs in *content* from
	// the one a test sent — which is exactly what a caller must be able to do
	// to tell apart "state written from the request" from "state written from
	// the response".
	branchOverridesResponse *[]string

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

// defaultFakeProjectSettings mirrors the defaults a freshly followed private
// project has, matching fakeProjectSettingsAPI's defaults in
// project_settings_resource_test.go.
//
// These are not guesses. They are CircleCI's own defaults for these flags,
// confirmed against a live GET of a real project's settings:
// set_github_status and setup_workflows default to true (not false),
// forks_receive_secret_env_vars defaults to true on a private project, and
// pr_only_branch_overrides defaults to the project's default branch rather than an
// empty list. The fake used to default the first two to false, which hid the fact
// that circleci_project was forcing them off on every create.
func defaultFakeProjectSettings() map[string]any {
	return map[string]any{
		"autocancel_builds":             false,
		"build_fork_prs":                false,
		"build_prs_only":                false,
		"disable_ssh":                   false,
		"forks_receive_secret_env_vars": true,
		"oss":                           false,
		"set_github_status":             true,
		"setup_workflows":               true,
		"write_settings_requires_admin": false,
		"pr_only_branch_overrides":      []any{"main"},
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

	// 200, not 201. POST /api/v2/organization/{org}/project answers 200 — that is
	// what the route returns, and what its own published documentation says. The
	// slug-based sibling route (POST /api/v2/project/{provider}/{org}/{project}),
	// which this provider deliberately does not use, is the one that answers 201.
	// The client treats any 2xx as success either way, so this is fake fidelity
	// rather than a bug it was hiding.
	a.write(w, http.StatusOK, p.toJSON())
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
	a.write(w, http.StatusOK, map[string]any{"message": "Project deleted"})
}

func (a *fakeProjectAPI) handleGetSettings(w http.ResponseWriter, slug string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if status := a.failSettingsStatus; status != 0 {
		a.write(w, status, map[string]any{"message": "forced failure for test"})

		return
	}

	current, ok := a.settings[slug]
	if !ok {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Project not found"})

		return
	}

	a.write(w, http.StatusOK, map[string]any{"advanced": current})
}

func (a *fakeProjectAPI) handlePatchSettings(w http.ResponseWriter, r *http.Request, slug string) {
	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		a.write(w, http.StatusBadRequest, map[string]any{"message": "Invalid JSON body."})

		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if status := a.failSettingsStatus; status != 0 {
		a.write(w, status, map[string]any{"message": "forced failure for test"})

		return
	}

	current, ok := a.settings[slug]
	if !ok {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Project not found"})

		return
	}

	advanced, message, accepted := settingsPatchBody(raw)
	if advanced != nil {
		// Recorded even when rejected, so a test can assert on what was sent.
		a.patches = append(a.patches, advanced)
	}
	if !accepted {
		a.write(w, http.StatusBadRequest, map[string]any{"message": message})

		return
	}

	applySettingsPatch(current, advanced)

	if a.branchOverridesResponse != nil {
		current["pr_only_branch_overrides"] = *a.branchOverridesResponse
	}

	a.settings[slug] = current

	a.write(w, http.StatusOK, map[string]any{"advanced": current})
}

// settingsPatchBody validates a settings PATCH body the way the API does, and
// returns the "advanced" object to apply.
//
// Three behaviours, all established from what the API accepts rather than guessed:
//
//   - An empty body — `{}` — is rejected with "No JSON fields found." The check
//     is on the TOP LEVEL only.
//   - `{"advanced":{}}` is therefore accepted, and answers 200 with the current
//     settings. Both fakes here used to reject it, which made the client's own
//     comment ("the API rejects a body with no fields") look confirmed when it
//     was describing a different shape. Skipping a no-op PATCH is an
//     optimisation, not a way of avoiding a 400.
//   - Any unrecognised key is rejected naming the first one, at whatever depth:
//     "Unexpected field 'cheese'." at the top level and "Unexpected field
//     'advanced.oss'." inside. That is the same mechanism that makes oss
//     unwritable, so modelling it as one rule rather than as an oss special case
//     is what would catch the next unwritable field too.
func settingsPatchBody(raw map[string]any) (advanced map[string]any, message string, ok bool) {
	if len(raw) == 0 {
		return nil, "No JSON fields found.", false
	}

	for key := range raw {
		if key != "advanced" {
			return nil, fmt.Sprintf("Unexpected field '%s'.", key), false
		}
	}

	advanced, _ = raw["advanced"].(map[string]any)
	if advanced == nil {
		advanced = map[string]any{}
	}

	// oss is read-only on v2, and the API rejects the whole request when it is
	// present rather than ignoring the one field — verified against the live API:
	//
	//	PATCH /api/v2/project/{slug}/settings  {"advanced":{"oss":false}}
	//	→ 400  {"message":"Unexpected field 'advanced.oss'."}
	//
	// The fakes used to accept it, which is exactly why sending it survived a
	// passing test suite and broke every real create and update.
	for _, key := range []string{"oss"} {
		if _, present := advanced[key]; present {
			return advanced, fmt.Sprintf("Unexpected field 'advanced.%s'.", key), false
		}
	}

	return advanced, "", true
}

// applySettingsPatch merges an "advanced" PATCH body into the settings a fake
// holds, the way the real route applies a partial update — including the two
// things it does that a naive merge does not.
//
// Both matter, and neither was found by a test — they were established from how
// the route actually behaves:
//
//   - pr_only_branch_overrides is unordered on the API and comes back in an order
//     of its own; see reorderedLikeTheAPI in webhook_resource_fake_test.go.
//   - An EMPTY pr_only_branch_overrides array is accepted with HTTP 200 and then
//     ignored. The service fronting this route decodes the v2 body into a plain
//     []string and copies it into the v1.1 body it forwards, where the field
//     carries `omitempty`, so a zero-length slice is dropped before the write
//     that would have cleared the list ever happens. The PATCH response is a
//     fresh read, so the old branches come straight back. Both fakes used to
//     clear the list obligingly, which is why the provider could ship a "clear
//     the overrides" path that cannot work and still pass every test.
//
// See ProjectSettings.PROnlyBranchOverrides in internal/circleci for the caller
// side of the same finding.
func applySettingsPatch(current, advanced map[string]any) {
	for key, value := range advanced {
		if key == "pr_only_branch_overrides" {
			if list, isList := value.([]any); isList && len(list) == 0 {
				continue
			}

			value = reorderedLikeTheAPI(value)
		}

		current[key] = value
	}
}

// TestSettingsFakesIgnoreAnEmptyBranchOverrideList guards the guard, the same way
// TestFakeAPIsDoNotEchoCollectionOrder does for collection ordering.
//
// Requirement: both settings fakes must accept an empty pr_only_branch_overrides
// array and change nothing, because that is what the real route does. A fake that
// goes back to clearing the list obligingly makes
// TestProjectSettingsResourceCreateWithEmptyBranchOverrides pass by pretending a
// broken path works — which is the state the suite was in while
// `pr_only_branch_overrides = []` shipped as a documented way to remove every
// override.
//
// Both fakes are exercised through applySettingsPatch, which is the one place the
// behaviour lives; asserting on it directly is what keeps the two from drifting.
func TestSettingsFakesIgnoreAnEmptyBranchOverrideList(t *testing.T) {
	t.Parallel()

	t.Run("an empty list leaves the stored branches alone", func(t *testing.T) {
		t.Parallel()

		current := map[string]any{"pr_only_branch_overrides": []any{"main", "develop"}}
		applySettingsPatch(current, map[string]any{"pr_only_branch_overrides": []any{}})

		stored, ok := current["pr_only_branch_overrides"].([]any)
		if !ok || len(stored) != 2 {
			t.Errorf("an empty pr_only_branch_overrides array left %v; the real route drops it and keeps "+
				"the previous branches, so a fake that clears them cannot catch the defect that "+
				"clearing is impossible", current["pr_only_branch_overrides"])
		}
	})

	t.Run("a populated list still replaces them, reordered", func(t *testing.T) {
		t.Parallel()

		current := map[string]any{"pr_only_branch_overrides": []any{"main"}}
		applySettingsPatch(current, map[string]any{"pr_only_branch_overrides": []any{"alpha", "beta"}})

		stored, ok := current["pr_only_branch_overrides"].([]any)
		if !ok || len(stored) != 2 {
			t.Fatalf("a populated pr_only_branch_overrides array stored %v, want two branches",
				current["pr_only_branch_overrides"])
		}
		if stored[0] != "beta" {
			t.Errorf("stored %v in the submitted order; the API answers in an order of its own, so the "+
				"empty-list guard must not have cost us the ordering one", stored)
		}
	})

	t.Run("an empty advanced object is accepted, an empty body is not", func(t *testing.T) {
		t.Parallel()

		if _, message, ok := settingsPatchBody(map[string]any{"advanced": map[string]any{}}); !ok {
			t.Errorf(`{"advanced":{}} was rejected with %q; the API answers 200 for it, and its own `+
				`handler tests assert that`, message)
		}
		if _, message, ok := settingsPatchBody(map[string]any{}); ok {
			t.Error("an empty body was accepted; the request binder rejects it with " +
				`"No JSON fields found."`)
		} else if message != "No JSON fields found." {
			t.Errorf("an empty body was rejected with %q, want the message the API uses", message)
		}
		if _, message, ok := settingsPatchBody(map[string]any{"cheese": true}); ok {
			t.Error("an unrecognised top-level field was accepted; the binder rejects any unknown key")
		} else if message != "Unexpected field 'cheese'." {
			t.Errorf("an unrecognised top-level field was rejected with %q, want the message the API uses",
				message)
		}
	})
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

// setBranchOverridesResponse overrides the pr_only_branch_overrides a settings
// PATCH answers with. See branchOverridesResponse.
func (a *fakeProjectAPI) setBranchOverridesResponse(branches []string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.branchOverridesResponse = &branches
}

// setFailSettingsStatus makes every settings GET and PATCH answer with status,
// to test what happens when the settings call Create makes right after
// CreateProject has already succeeded fails. See failSettingsStatus.
func (a *fakeProjectAPI) setFailSettingsStatus(status int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.failSettingsStatus = status
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
