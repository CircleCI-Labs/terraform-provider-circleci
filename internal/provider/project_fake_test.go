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

	// orgKind decides the shape Create responds with, and — because the two
	// classes are two different operations — what a create can do at all.
	//
	// For "classic" this fake models an organization in which a repository of
	// the requested name ALREADY EXISTS, because that is the only case where a
	// create succeeds there: on a classic organization the route adopts an
	// existing repository and never creates one. missingRepository models the
	// other case.
	//
	//   - "classic" mimics a GitHub OAuth organization: addressed by name, and
	//     CircleCI does not follow the project as part of creating it, so a
	//     v1.1 follow call is expected afterwards.
	//   - "standalone" mimics a circleci-native organization: addressed by two
	//     opaque base62 fragments, and the project is already followed by the
	//     create call, so no v1.1 follow call should ever be made.
	//
	// See internal/circleci/project.go's followProject for the condition this
	// mirrors, and fakeStandaloneOrgFragment below for why the standalone slug
	// looks the way it does.
	orgKind string

	projects map[string]*fakeProject   // keyed by slug
	settings map[string]map[string]any // keyed by slug
	patches  []map[string]any          // settings PATCH "advanced" bodies, in order
	requests []string                  // "METHOD path", in order
	followed []string                  // "vcsType/org/name" from every follow call

	// missingRepository makes a create on a CLASSIC organization answer
	// 404 {"message":"GitHub response: Not Found"} — what the API answers when
	// no repository of that name exists for it to adopt. Measured over the
	// network against two separate GitHub-backed organizations. It is ignored
	// for a standalone organization, where no repository is involved and a
	// create of any name succeeds (also measured, on an organization created
	// seconds earlier with no VCS connection at all).
	missingRepository bool

	// missingProject makes every GET for a single project answer 404, to
	// simulate a project deleted outside Terraform.
	missingProject bool

	// refuseDeleteAs404 makes every project DELETE answer
	// 404 {"message":"Project not found"} while leaving the project in place.
	//
	// This is not a hypothetical. Measured over the network with two personal
	// API tokens: a DELETE issued by a token that cannot see the project's
	// organization answers exactly that, and the project is still there
	// afterwards. See deletedProjectIsReallyGone in project_resource.go.
	refuseDeleteAs404 bool

	// hiddenOrganization makes GET /organization/{id} answer
	// 404 {"message":"Org not found."} — what CircleCI answers for an
	// organization the token cannot see, also measured. Paired with
	// refuseDeleteAs404 it reproduces a token that has lost access to the
	// organization; on its own it reproduces an organization that was deleted
	// while its project was still in state.
	hiddenOrganization bool

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

// The slug fragments a standalone project is really addressed by, copied from a
// live create.
//
// This fake used to build a standalone slug as "circleci/<orgUUID>/<projectID>",
// with the project's own id as the last segment. The API produces neither: the
// organization segment is a 22-character base62 fragment (not its UUID) and the
// project segment is a 21-character one (not its UUID, and not its name). None of
// the three is interchangeable in every direction — measured over the network, a
// slug whose last segment is the project NAME is rejected with
// 400 "Invalid project slug …", while the organization's UUID in the first
// position does work. Modelling the slug as something derivable from the name is
// what would hide a caller that reconstructs it instead of using the one the API
// reported; see circleci.DeleteProject.
const (
	fakeStandaloneOrgSlug         = "circleci/TFtestOrgFragment01234"
	fakeStandaloneProjectFragment = "TFtestProjFragment012"
)

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

	case r.Method == http.MethodGet &&
		strings.HasPrefix(r.URL.Path, "/api/v2/organization/"):
		a.handleGetOrganization(w, r)

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
	missingRepository := a.missingRepository && a.orgKind != "standalone"
	a.mu.Unlock()

	if missingRepository {
		// The API's own message, verbatim. It never mentions the repository or
		// the fact that adoption is the only thing on offer, which is why
		// projectCreateFailureDetail adds that.
		a.write(w, http.StatusNotFound, map[string]any{"message": "GitHub response: Not Found"})

		return
	}

	a.mu.Lock()
	a.nextID++
	id := fmt.Sprintf("proj-%d", a.nextID)

	var p *fakeProject
	switch a.orgKind {
	case "standalone":
		p = &fakeProject{
			id: id, name: body.Name,
			slug:          fakeStandaloneOrgSlug + "/" + fakeStandaloneProjectFragment,
			orgName:       "Standalone Org Display Name",
			orgSlug:       fakeStandaloneOrgSlug,
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

	// A refused delete is answered exactly like a missing project, and the
	// project stays. That indistinguishability is the whole defect
	// deletedProjectIsReallyGone addresses, so the fake has to reproduce it
	// rather than making the two cases tell-apart-able by status code.
	if a.refuseDeleteAs404 {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Project not found"})

		return
	}

	if _, ok := a.projects[slug]; !ok {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Project not found"})

		return
	}

	delete(a.projects, slug)
	a.write(w, http.StatusOK, map[string]any{"message": "Project deleted"})
}

// handleGetOrganization serves GET /api/v2/organization/{org-slug-or-id}, which
// circleci_project's Delete uses to tell a deleted project apart from one this
// token cannot see.
//
// The 404 body is the API's own — "Org not found.", with the trailing full stop,
// which is a different string from the project route's "Project not found".
func (a *fakeProjectAPI) handleGetOrganization(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.hiddenOrganization {
		a.write(w, http.StatusNotFound, map[string]any{"message": "Org not found."})

		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/v2/organization/")

	a.write(w, http.StatusOK, map[string]any{
		"id":       id,
		"name":     "AcmeOrg",
		"slug":     "gh/AcmeOrg",
		"vcs_type": "github",
	})
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

	denied := applySettingsPatchForSlug(slugVCSType(slug), current, advanced)

	if a.branchOverridesResponse != nil {
		current["pr_only_branch_overrides"] = *a.branchOverridesResponse
	}

	a.settings[slug] = current

	if denied {
		// The other settings above are already written. See
		// applySettingsPatchForSlug.
		a.write(w, http.StatusForbidden, map[string]any{"message": "Permission denied."})

		return
	}

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

	// The branch-override list is capped, and the cap is enforced by the same
	// schema-validation layer as the unknown-field checks above, so it rejects the
	// whole request and writes nothing — verified over the network against the
	// live API:
	//
	//	PATCH … {"advanced":{"pr_only_branch_overrides":[ 100 branches ]}}  → 200
	//	PATCH … {"advanced":{"pr_only_branch_overrides":[ 101 branches ]}}
	//	→ 400  {"message":"Field 'pr_only_branch_overrides' only supports up to 100 branches."}
	//
	// The resource documents the limit but nothing enforced or tested it, so a
	// configuration over the cap failed only against a real installation.
	if list, isList := advanced["pr_only_branch_overrides"].([]any); isList && len(list) > settingsBranchOverrideLimit {
		return advanced, fmt.Sprintf(
			"Field 'pr_only_branch_overrides' only supports up to %d branches.", settingsBranchOverrideLimit,
		), false
	}

	return advanced, "", true
}

// settingsBranchOverrideLimit is the most branches pr_only_branch_overrides
// accepts. Network-measured: 100 is accepted, 101 is rejected.
const settingsBranchOverrideLimit = 100

// forkSecretsSettingKey is the one setting the route refuses to enable on a
// standalone organization's project.
const forkSecretsSettingKey = "forks_receive_secret_env_vars"

// standaloneForkSecretsDenied reports whether the settings route refuses this
// PATCH body with 403 "Permission denied." because it asks to enable
// forks_receive_secret_env_vars on a project in a standalone organization.
//
// Network-measured against the live API, on three separate standalone
// organizations (GitHub App backed, GitLab backed, and a repo-less one created
// by the token making the request):
//
//	PATCH /api/v2/project/circleci/<org>/<project>/settings
//	      {"advanced":{"forks_receive_secret_env_vars":true}}
//	→ 403  {"message":"Permission denied."}
//
// Refused even when the current value is already true, so it is the value that
// is rejected and not the transition. The same body against a classic
// organization ("gh/…" or "github/…") answers 200 — measured on two GitHub OAuth
// organizations. Both fakes used to accept it unconditionally, which is exactly
// the shape of wrong belief that let the oss field ship.
func standaloneForkSecretsDenied(vcsType string, advanced map[string]any) bool {
	if vcsType != circleci.StandaloneSlugVCSType {
		return false
	}

	enabled, isBool := advanced[forkSecretsSettingKey].(bool)

	return isBool && enabled
}

// applySettingsPatchForSlug applies a PATCH body the way the route does for a
// project in the organization class the slug's VCS segment names, and reports
// whether the route then answers 403 rather than 200.
//
// The order matters and is the finding: the 403 is NOT atomic. Every other field
// in the body is written and keeps its new value; only
// forks_receive_secret_env_vars is left alone. Measured — a body carrying
// {"forks_receive_secret_env_vars":true,"autocancel_builds":true} against a
// standalone project answered 403 and left autocancel_builds true. Modelling it
// as an ordinary rejection that writes nothing would hide the reason
// circleci.UpdateProjectSettings refuses to send the field at all.
func applySettingsPatchForSlug(vcsType string, current, advanced map[string]any) (denied bool) {
	denied = standaloneForkSecretsDenied(vcsType, advanced)

	if denied {
		// A copy, so the body a test recorded still shows what was actually sent.
		applied := make(map[string]any, len(advanced))
		for key, value := range advanced {
			if key == forkSecretsSettingKey {
				continue
			}
			applied[key] = value
		}
		advanced = applied
	}

	applySettingsPatch(current, advanced)

	return denied
}

// slugVCSType returns the VCS segment of a "vcs/org/project" slug.
func slugVCSType(slug string) string {
	vcsType, _, _ := strings.Cut(slug, "/")

	return vcsType
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

// setMissingRepository makes a create on a classic organization answer 404, as
// the API does when there is no repository of that name to adopt. See
// missingRepository.
func (a *fakeProjectAPI) setMissingRepository(missing bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.missingRepository = missing
}

// setRefuseDeleteAs404 makes every project DELETE answer 404 with the project
// left in place. See refuseDeleteAs404.
func (a *fakeProjectAPI) setRefuseDeleteAs404(refuse bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.refuseDeleteAs404 = refuse
}

// setOrganizationHidden makes GET /organization/{id} answer 404. See
// hiddenOrganization.
func (a *fakeProjectAPI) setOrganizationHidden(hidden bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.hiddenOrganization = hidden
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

// TestSettingsFakesRefuseEnablingForkSecretsOnStandalone guards the guard for the
// second unwritable field on this route, the way
// TestSettingsFakesIgnoreAnEmptyBranchOverrideList does for the branch list.
//
// Both fakes used to accept forks_receive_secret_env_vars: true from any project
// and store it, which is a wrong belief about the API: measured over the network,
// a standalone organization's project answers 403 "Permission denied." for that
// write and a classic organization's project answers 200. See
// standaloneForkSecretsDenied for the full measurement.
//
// The subtle half is the partial write, and it is the half a fake gets wrong by
// default: the route applies every OTHER field in the body and only then refuses.
// A fake that rejected the request wholesale would make
// circleci.UpdateProjectSettings's pre-flight guard look like an over-reaction
// instead of the only way to avoid leaving changes behind.
func TestSettingsFakesRefuseEnablingForkSecretsOnStandalone(t *testing.T) {
	t.Parallel()

	t.Run("standalone: refused, and every other field in the body is still written", func(t *testing.T) {
		t.Parallel()

		current := map[string]any{"forks_receive_secret_env_vars": false, "autocancel_builds": false}
		denied := applySettingsPatchForSlug("circleci", current, map[string]any{
			"forks_receive_secret_env_vars": true,
			"autocancel_builds":             true,
		})

		if !denied {
			t.Error("applySettingsPatchForSlug accepted forks_receive_secret_env_vars: true on a " +
				"standalone project; the real route answers 403 Permission denied.")
		}
		if current["forks_receive_secret_env_vars"] != false {
			t.Errorf("forks_receive_secret_env_vars = %v, want it left at false: the route refuses "+
				"the field and never writes it", current["forks_receive_secret_env_vars"])
		}
		if current["autocancel_builds"] != true {
			t.Errorf("autocancel_builds = %v, want true. The 403 is not atomic — measured: a body "+
				"carrying both fields answered 403 and left autocancel_builds true. A fake that "+
				"rolls the whole body back hides why the client refuses to send the request at all",
				current["autocancel_builds"])
		}
	})

	t.Run("standalone: turning it off is accepted", func(t *testing.T) {
		t.Parallel()

		current := map[string]any{"forks_receive_secret_env_vars": true}
		if denied := applySettingsPatchForSlug("circleci", current, map[string]any{
			"forks_receive_secret_env_vars": false,
		}); denied {
			t.Error("applySettingsPatchForSlug refused forks_receive_secret_env_vars: false on a " +
				"standalone project; measured, that write answers 200 and takes effect — which is " +
				"what makes the setting one-way rather than unwritable")
		}
		if current["forks_receive_secret_env_vars"] != false {
			t.Errorf("forks_receive_secret_env_vars = %v, want false", current["forks_receive_secret_env_vars"])
		}
	})

	for _, vcsType := range []string{"gh", "github", "bb", "bitbucket"} {
		t.Run("classic "+vcsType+": enabling it is accepted", func(t *testing.T) {
			t.Parallel()

			current := map[string]any{"forks_receive_secret_env_vars": false}
			if denied := applySettingsPatchForSlug(vcsType, current, map[string]any{
				"forks_receive_secret_env_vars": true,
			}); denied {
				t.Errorf("applySettingsPatchForSlug refused forks_receive_secret_env_vars: true on a "+
					"%s project; measured over the network, a classic organization answers 200", vcsType)
			}
			if current["forks_receive_secret_env_vars"] != true {
				t.Errorf("forks_receive_secret_env_vars = %v, want true", current["forks_receive_secret_env_vars"])
			}
		})
	}
}

// TestSettingsFakesEnforceTheBranchOverrideCap keeps both fakes honest about the
// one numeric limit this route has.
//
// Measured over the network: 100 branches answer 200 and 101 answer
// 400 "Field 'pr_only_branch_overrides' only supports up to 100 branches." The
// resource's documentation stated the limit and nothing enforced it, so a
// configuration over the cap passed every test and failed only against a real
// installation. Rejection is at the schema layer, alongside the unknown-field
// checks, so nothing in the request is written.
func TestSettingsFakesEnforceTheBranchOverrideCap(t *testing.T) {
	t.Parallel()

	branches := func(n int) []any {
		list := make([]any, 0, n)
		for i := range n {
			list = append(list, fmt.Sprintf("branch-%d", i))
		}

		return list
	}

	t.Run("100 branches are accepted", func(t *testing.T) {
		t.Parallel()

		if _, message, ok := settingsPatchBody(map[string]any{
			"advanced": map[string]any{"pr_only_branch_overrides": branches(settingsBranchOverrideLimit)},
		}); !ok {
			t.Errorf("settingsPatchBody rejected %d branches with %q; the live API accepts exactly that many",
				settingsBranchOverrideLimit, message)
		}
	})

	t.Run("101 branches are rejected, naming the limit", func(t *testing.T) {
		t.Parallel()

		_, message, ok := settingsPatchBody(map[string]any{
			"advanced": map[string]any{"pr_only_branch_overrides": branches(settingsBranchOverrideLimit + 1)},
		})
		if ok {
			t.Fatalf("settingsPatchBody accepted %d branches; the live API answers 400",
				settingsBranchOverrideLimit+1)
		}
		if !strings.Contains(message, "only supports up to 100 branches") {
			t.Errorf("rejection message = %q, want the API's own wording so a caller sees the limit", message)
		}
	})
}
