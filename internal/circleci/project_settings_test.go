// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
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
	srv, calls := newProjectSettingsServer(t, http.StatusOK, projectSettingsResponse)
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

	srv, calls := newProjectSettingsServer(t, http.StatusOK, projectSettingsResponse)
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

// TestUpdateProjectSettingsClearsBranchOverrides covers the one case a plain
// slice could not express: an empty list must be sent, because sending [] is how
// every override is removed.
func TestUpdateProjectSettingsClearsBranchOverrides(t *testing.T) {
	t.Parallel()

	srv, calls := newProjectSettingsServer(t, http.StatusOK, projectSettingsResponse)
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

// newProjectSettingsServer serves a fixed status and body, recording every call.
func newProjectSettingsServer(t *testing.T, status int, body string) (*httptest.Server, *[]projectSettingsCall) {
	t.Helper()

	calls := new([]projectSettingsCall)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}

		*calls = append(*calls, projectSettingsCall{method: r.Method, path: r.URL.Path, body: raw})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
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
// Reporting non-empty would send {"advanced":{}} and earn a different 400
// ("No JSON fields found.").
func TestProjectSettingsIsEmptyIgnoresOSS(t *testing.T) {
	t.Parallel()

	yes := true

	if !(circleci.ProjectSettings{OSS: &yes}).IsEmpty() {
		t.Error("settings holding only OSS report non-empty; OSS is never sent, so the " +
			"request body would be {\"advanced\":{}} and the API would reject it")
	}

	if (circleci.ProjectSettings{OSS: &yes, SetGithubStatus: &yes}).IsEmpty() {
		t.Error("settings with a sendable field report empty")
	}
}
