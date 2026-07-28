// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// The response body below is the shape production sends, taken from the
// "installation" schema in the API's
// openapi_definitions/v2_endpoints/github_app/schemas.yaml and confirmed
// against the CircleCI API's fixture.
const testGitHubAppInstallationBody = `{
  "id": 12345678,
  "target_type": "Organization",
  "login": "my-org",
  "repository_selection": "all"
}`

func TestGitHubAppGetInstallation(t *testing.T) {
	t.Parallel()

	client, seen := newGitHubAppServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testGitHubAppInstallationBody))
	})

	installation, err := client.GitHubApp().GetInstallation(context.Background(), testGitHubAppOrgID)
	if err != nil {
		t.Fatalf("GetInstallation returned error: %v", err)
	}

	if installation.ID != 12345678 {
		t.Errorf("id = %d, want 12345678", installation.ID)
	}
	if installation.TargetType != "Organization" {
		t.Errorf("target type = %q, want %q", installation.TargetType, "Organization")
	}
	if installation.Login != "my-org" {
		t.Errorf("login = %q, want %q", installation.Login, "my-org")
	}
	if installation.RepositorySelection != "all" {
		t.Errorf("repository selection = %q, want %q", installation.RepositorySelection, "all")
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}

	wantPath := "/api/v2/github-app/organization/" + testGitHubAppOrgID + "/installation"
	if req := (*seen)[0]; req.method != http.MethodGet || req.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", req.method, req.path, wantPath)
	}
	if query := (*seen)[0].query; query != "" {
		t.Errorf("query = %q, want empty (the route takes no query parameters)", query)
	}
}

// TestGitHubAppGetInstallationNotFound confirms a missing installation
// answers an ordinary 404 (IsNotFound), rather than the 403 anti-enumeration
// pattern circleci_group uses for a missing group. Confirmed against
// the API's "404 response when the GitHub App is not installed" case,
// which asserts ExpectedResponseCode: http.StatusNotFound.
func TestGitHubAppGetInstallationNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newGitHubAppServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Organization not found."}`))
	})

	_, err := client.GitHubApp().GetInstallation(context.Background(), testGitHubAppOrgID)
	if !circleci.IsNotFound(err) {
		t.Fatalf("GetInstallation error = %v, want a not found error", err)
	}
	if detail := circleci.Detail(err); !strings.Contains(detail, "Organization not found.") {
		t.Errorf("Detail() = %q, want it to carry the server message", detail)
	}
}

func TestGitHubAppGetInstallationEscapesOrgID(t *testing.T) {
	t.Parallel()

	client, seen := newGitHubAppServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testGitHubAppInstallationBody))
	})

	if _, err := client.GitHubApp().GetInstallation(context.Background(), "org/../evil"); err != nil {
		t.Fatalf("GetInstallation returned error: %v", err)
	}

	wantURI := "/api/v2/github-app/organization/org%2F..%2Fevil/installation"
	if got := (*seen)[0].rawURI; got != wantURI {
		t.Errorf("raw request URI = %q, want %q", got, wantURI)
	}
}
