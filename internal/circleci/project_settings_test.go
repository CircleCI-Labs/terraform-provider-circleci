// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// projectSettingsResponse is what the v2 API returns for both the read and the
// update: the full advanced settings, nested under a single "advanced" key.
const projectSettingsResponse = `{
  "advanced": {
    "autocancel_builds": true,
    "build_fork_prs": false,
    "build_prs_only": true,
    "disable_ssh": true,
    "forks_receive_secret_env_vars": false,
    "oss": false,
    "set_github_status": true,
    "setup_workflows": false,
    "write_settings_requires_admin": true,
    "pr_only_branch_overrides": ["main", "develop"]
  }
}`

func TestGetProjectSettingsRouteAndEnvelope(t *testing.T) {
	t.Parallel()

	// newSettingsServer is shared with the organization settings tests in this
	// package.
	srv, calls := newProjectSettingsServer(t, projectSettingsResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	settings, err := c.GetProjectSettings(context.Background(), "github", "acme", "my repo")
	if err != nil {
		t.Fatalf("GetProjectSettings returned error: %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("made %d calls, want 1", len(*calls))
	}

	call := (*calls)[0]
	if call.method != http.MethodGet {
		t.Errorf("method = %q, want GET", call.method)
	}

	// Each slug segment is escaped on its own, so a name containing a space
	// still resolves and the separators stay literal. The recorded path is the
	// decoded one: reaching this route at all means the space travelled as %20,
	// since an unescaped space would not have produced a valid request line.
	if want := "/api/v2/project/github/acme/my repo/settings"; call.path != want {
		t.Errorf("path = %q, want %q", call.path, want)
	}

	if settings.BuildPrsOnly == nil || !*settings.BuildPrsOnly {
		t.Error("build_prs_only was not decoded as true; the SDK struct cannot carry this field, which is why this one exists")
	}
	if settings.PROnlyBranchOverrides == nil || len(*settings.PROnlyBranchOverrides) != 2 {
		t.Errorf("pr_only_branch_overrides = %v, want two branches", settings.PROnlyBranchOverrides)
	}
}

// TestUpdateProjectSettingsSendsOnlySetFields is the client-side half of the
// partial-update property: a nil field must not reach the wire at all, because
// the API writes every field the body carries.
func TestUpdateProjectSettingsSendsOnlySetFields(t *testing.T) {
	t.Parallel()

	srv, calls := newProjectSettingsServer(t, projectSettingsResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	enabled := true
	_, err := c.UpdateProjectSettings(context.Background(), "gh", "acme", "repo", circleci.ProjectSettings{
		AutocancelBuilds: &enabled,
	})
	if err != nil {
		t.Fatalf("UpdateProjectSettings returned error: %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("made %d calls, want 1", len(*calls))
	}

	call := (*calls)[0]
	if call.method != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", call.method)
	}

	var body struct {
		Advanced map[string]any `json:"advanced"`
	}
	if err := json.Unmarshal(call.body, &body); err != nil {
		t.Fatalf("request body %q is not the expected envelope: %v", call.body, err)
	}

	keys := make([]string, 0, len(body.Advanced))
	for key := range body.Advanced {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	if got := strings.Join(keys, ","); got != "autocancel_builds" {
		t.Errorf("request body carried %q, want only autocancel_builds", got)
	}
}

// TestUpdateProjectSettingsSendsAnEmptyBranchOverrideList covers the one case a
// plain slice could not express: an empty list reaches the wire as [] rather than
// being omitted.
//
// Sending it is correct even though the API ignores it — see
// TestUpdateProjectSettingsRejectsAnIgnoredBranchOverrideClear, which is where the
// consequence of that is pinned down. The server here answers as though the clear
// worked, which is what isolates "the bytes are right" from "the API honours
// them".
func TestUpdateProjectSettingsSendsAnEmptyBranchOverrideList(t *testing.T) {
	t.Parallel()

	srv, calls := newProjectSettingsServer(t,
		`{"advanced":{"pr_only_branch_overrides":[]}}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	none := []string{}
	_, err := c.UpdateProjectSettings(context.Background(), "gh", "acme", "repo", circleci.ProjectSettings{
		PROnlyBranchOverrides: &none,
	})
	if err != nil {
		t.Fatalf("UpdateProjectSettings returned error: %v", err)
	}

	if want := `{"advanced":{"pr_only_branch_overrides":[]}}`; strings.TrimSpace(string((*calls)[0].body)) != want {
		t.Errorf("request body = %s, want %s", (*calls)[0].body, want)
	}
}

func TestProjectSettingsIsEmpty(t *testing.T) {
	t.Parallel()

	value := false
	none := []string{}

	tests := []struct {
		name     string
		settings circleci.ProjectSettings
		want     bool
	}{
		{name: "nothing set", settings: circleci.ProjectSettings{}, want: true},
		// OSS deliberately cannot be the example here: it is never marshalled, so a
		// settings object holding only OSS genuinely has nothing to send. See
		// TestProjectSettingsIsEmptyIgnoresOSS.
		{name: "a false toggle counts as set", settings: circleci.ProjectSettings{BuildForkPrs: &value}},
		{name: "build_prs_only counts as set", settings: circleci.ProjectSettings{BuildPrsOnly: &value}},
		{name: "an empty override list counts as set", settings: circleci.ProjectSettings{PROnlyBranchOverrides: &none}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.settings.IsEmpty(); got != tt.want {
				t.Errorf("IsEmpty() = %t, want %t", got, tt.want)
			}
		})
	}
}

// projectSettingsCall records one request the fake API received.
//
// This is intentionally local rather than shared with the organization settings
// tests: a helper spanning two unrelated entities couples them, and the two suites
// have no reason to move together.
type projectSettingsCall struct {
	method string
	path   string
	body   []byte
}

// newProjectSettingsServer serves a fixed 200 and body, recording every call.
func newProjectSettingsServer(t *testing.T, body string) (*httptest.Server, *[]projectSettingsCall) {
	t.Helper()

	calls := new([]projectSettingsCall)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}

		*calls = append(*calls, projectSettingsCall{method: r.Method, path: r.URL.Path, body: raw})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv, calls
}

// TestProjectSettingsNeverSendsOSS is the regression test for a bug that every
// mocked test in this repository passed while the real API rejected every write.
//
// oss is returned by GET /project/{slug}/settings, and CircleCI's published API
// reference lists it in the PATCH request body, so it looks writable. It is not.
// Verified against the live API:
//
//	PATCH .../settings  {"advanced":{"oss":false}}
//	→ 400 {"message":"Unexpected field 'advanced.oss'."}
//
// and the identical request without oss answers 200. Because the field is
// rejected at the envelope level the whole request fails, so one unwritable field
// broke project creation and every settings update.
//
// The fake accepted oss, which is exactly why 894 passing tests could not see it.
// This test asserts on the marshalled bytes instead of on a fake's behaviour.
func TestProjectSettingsNeverSendsOSS(t *testing.T) {
	t.Parallel()

	yes := true

	settings := circleci.ProjectSettings{
		OSS:             &yes,
		SetGithubStatus: &yes,
	}

	body, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshalling settings: %v", err)
	}

	if strings.Contains(string(body), "oss") {
		t.Errorf("marshalled settings contain oss: %s\n\n"+
			"The API answers 400 \"Unexpected field 'advanced.oss'.\" and rejects the whole "+
			"request, so this breaks every project create and settings update. oss is "+
			"read-only on v2.", body)
	}

	// The rest of the object must still be sent, or dropping oss would have broken
	// writes a different way.
	if !strings.Contains(string(body), `"set_github_status":true`) {
		t.Errorf("marshalled settings lost set_github_status: %s", body)
	}
}

// TestProjectSettingsIsEmptyIgnoresOSS covers the consequence of the above: a
// settings object holding only oss has nothing to send, so it must report empty.
// Reporting non-empty would send a PATCH that cannot change anything.
func TestProjectSettingsIsEmptyIgnoresOSS(t *testing.T) {
	t.Parallel()

	yes := true

	if !(circleci.ProjectSettings{OSS: &yes}).IsEmpty() {
		t.Error("settings holding only OSS report non-empty; OSS is never sent, so the " +
			"request body would be {\"advanced\":{}} and the request would be pointless")
	}

	if (circleci.ProjectSettings{OSS: &yes, SetGithubStatus: &yes}).IsEmpty() {
		t.Error("settings with a sendable field report empty")
	}
}

// TestUpdateProjectSettingsRejectsAnIgnoredBranchOverrideClear is the regression
// test for a defect no fake in this repository could previously express.
//
// Clearing pr_only_branch_overrides is impossible on this route. The service
// fronting it decodes the v2 body into a plain []string and copies it into the
// v1.1 body it forwards, where the field carries `omitempty` — so a zero-length
// slice is dropped before the write that would have cleared the list is made.
// The PATCH answers 200 with a *fresh read*, which therefore still reports the
// old branches.
//
// Every fake in the suite used to clear the list obligingly, so a "clear the
// overrides" path passed everywhere and could never work anywhere. Now the client
// compares what it asked for against what came back and says so, because the
// alternative is handing a caller a value that contradicts its own request — which
// a Terraform resource turns into either state that lies or an opaque "Provider
// produced inconsistent result after apply" naming no attribute and no cause.
//
// The empty list is still sent (asserted below): the guard stops firing by itself
// if the route is ever fixed.
func TestUpdateProjectSettingsRejectsAnIgnoredBranchOverrideClear(t *testing.T) {
	t.Parallel()

	// The response a real PATCH gives: 200, and the branches still there.
	srv, calls := newProjectSettingsServer(t, projectSettingsResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	none := []string{}
	_, err := c.UpdateProjectSettings(context.Background(), "gh", "acme", "repo", circleci.ProjectSettings{
		PROnlyBranchOverrides: &none,
	})

	if err == nil {
		t.Fatal("UpdateProjectSettings succeeded after asking to clear pr_only_branch_overrides and " +
			"being answered with the branches still set — a caller that trusts this stores a value " +
			"CircleCI never accepted")
	}
	if !errors.Is(err, circleci.ErrCannotClearBranchOverrides) {
		t.Errorf("error = %v, want it to wrap ErrCannotClearBranchOverrides so a caller can "+
			"recognise this case without matching on the message", err)
	}
	for _, want := range []string{"main", "develop"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name the branch %q that is still in force", err, want)
		}
	}

	// The empty list must still have been sent. Suppressing the request instead
	// would hide the defect rather than report it, and would keep working after a
	// fix only by accident.
	if len(*calls) != 1 {
		t.Fatalf("made %d calls, want 1", len(*calls))
	}

	var body struct {
		Advanced map[string]json.RawMessage `json:"advanced"`
	}
	if err := json.Unmarshal((*calls)[0].body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if got := string(body.Advanced["pr_only_branch_overrides"]); got != "[]" {
		t.Errorf("pr_only_branch_overrides sent as %q, want []", got)
	}
}

// TestUpdateProjectSettingsAcceptsAClearThatHadNothingToClear is the other half
// of the guard above: asking for no overrides on a project that already has none
// is a no-op, not an error. The end state is the one that was asked for.
func TestUpdateProjectSettingsAcceptsAClearThatHadNothingToClear(t *testing.T) {
	t.Parallel()

	const noOverrides = `{"advanced":{"build_prs_only":true,"pr_only_branch_overrides":[]}}`

	srv, _ := newProjectSettingsServer(t, noOverrides)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	none := []string{}
	updated, err := c.UpdateProjectSettings(context.Background(), "gh", "acme", "repo", circleci.ProjectSettings{
		PROnlyBranchOverrides: &none,
	})
	if err != nil {
		t.Fatalf("UpdateProjectSettings returned error: %v", err)
	}

	if updated.PROnlyBranchOverrides == nil || len(*updated.PROnlyBranchOverrides) != 0 {
		t.Errorf("pr_only_branch_overrides = %v, want empty", updated.PROnlyBranchOverrides)
	}
}

// TestProjectSettingsRejectsDotSegments is the regression test for
// checkProjectSettingsPathSegments: a vcsType, orgName or projectName that is
// exactly "." or ".." survives url.PathEscape unchanged (it doesn't touch
// dots), and RouteParams's escaping is exactly that — per-value PathEscape —
// so either one would put a literal "./" or "../" into the outbound request
// path and retarget it at a different route. This is defence in depth, not a
// fix for a reachable bug: these arguments come from Terraform configuration
// or from a slug split at the provider layer, never from a third party.
//
// This site matters more than the other three because
// internal/provider/project_settings_data_source.go builds these three
// arguments by splitting a configuration-supplied project_slug on "/" and
// validating nothing beyond the segment count — so a slug like "gh/./repo"
// reaches this function unchanged.
//
// It also checks the legitimate cases still parse, including an argument that
// merely contains a dot (a repository named "my.repo"), which must remain
// valid — rejecting that would break real configurations.
func TestProjectSettingsRejectsDotSegments(t *testing.T) {
	t.Parallel()

	srv, calls := newProjectSettingsServer(t, projectSettingsResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	tests := []struct {
		name                       string
		vcsType, orgName, projName string
		wantErr                    bool
	}{
		{name: "dot vcsType", vcsType: ".", orgName: "acme", projName: "repo", wantErr: true},
		{name: "dot-dot vcsType", vcsType: "..", orgName: "acme", projName: "repo", wantErr: true},
		{name: "dot orgName", vcsType: "github", orgName: ".", projName: "repo", wantErr: true},
		{name: "dot-dot orgName", vcsType: "github", orgName: "..", projName: "repo", wantErr: true},
		{name: "dot projectName", vcsType: "github", orgName: "acme", projName: ".", wantErr: true},
		{name: "dot-dot projectName", vcsType: "github", orgName: "acme", projName: "..", wantErr: true},
		{name: "ordinary arguments", vcsType: "github", orgName: "acme", projName: "repo", wantErr: false},
		{name: "argument merely containing a dot", vcsType: "github", orgName: "acme", projName: "my.repo", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.GetProjectSettings(context.Background(), tt.vcsType, tt.orgName, tt.projName)
			if tt.wantErr && err == nil {
				t.Errorf("GetProjectSettings(%q, %q, %q) returned no error, want one", tt.vcsType, tt.orgName, tt.projName)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("GetProjectSettings(%q, %q, %q) returned error: %v, want none", tt.vcsType, tt.orgName, tt.projName, err)
			}

			_, err = c.UpdateProjectSettings(context.Background(), tt.vcsType, tt.orgName, tt.projName, circleci.ProjectSettings{})
			if tt.wantErr && err == nil {
				t.Errorf("UpdateProjectSettings(%q, %q, %q) returned no error, want one", tt.vcsType, tt.orgName, tt.projName)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("UpdateProjectSettings(%q, %q, %q) returned error: %v, want none", tt.vcsType, tt.orgName, tt.projName, err)
			}
		})
	}

	// Sanity check the legitimate requests actually reached the server, so a
	// mistake that also broke the happy path wouldn't be masked by an
	// error-only assertion.
	if len(*calls) != 4 {
		t.Fatalf("made %d calls, want 4 (get+update for each of the two legitimate argument sets): %+v", len(*calls), *calls)
	}
}
