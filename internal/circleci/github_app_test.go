// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const testGitHubAppOrgID = "b9291e0d-a11e-41fb-8517-c545388b5953"

// githubAppRequest is one request as the mock server saw it.
type githubAppRequest struct {
	method string
	path   string
	rawURI string
	query  string
}

// newGitHubAppServer serves the GitHub App routes from handler and records every
// request, so tests can assert on the exact path and query parameters sent.
func newGitHubAppServer(t *testing.T, handler http.HandlerFunc) (*circleci.Client, *[]githubAppRequest) {
	t.Helper()

	var seen []githubAppRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, githubAppRequest{
			method: r.Method,
			path:   r.URL.Path,
			rawURI: r.RequestURI,
			query:  r.URL.RawQuery,
		})

		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return circleci.New(circleci.Config{Host: srv.URL, Token: "tok"}), &seen
}

// The response body below is the shape production sends, taken from the
// repository schema in the API's
// openapi_definitions/v2_endpoints/github_app/schemas.yaml and the matching Go
// struct in the CircleCI API In particular the full name
// is repo_full_name, not full_name.
const testGitHubAppRepositoriesBody = `{
  "items": [
    {
      "id": 123456789,
      "repo_full_name": "acme/api",
      "repo_name": "api",
      "owner": "acme",
      "default_branch": "main",
      "private": true
    },
    {
      "id": 987654321,
      "repo_full_name": "acme/Web-UI",
      "repo_name": "Web-UI",
      "owner": "acme",
      "default_branch": "trunk",
      "private": false
    }
  ],
  "total_count": 2
}`

func TestGitHubAppListRepositories(t *testing.T) {
	t.Parallel()

	client, seen := newGitHubAppServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testGitHubAppRepositoriesBody))
	})

	repositories, err := client.GitHubApp().ListRepositories(context.Background(), testGitHubAppOrgID)
	if err != nil {
		t.Fatalf("ListRepositories returned error: %v", err)
	}

	if len(repositories) != 2 {
		t.Fatalf("repository count = %d, want 2", len(repositories))
	}

	got := repositories[0]
	if got.ID != 123456789 {
		t.Errorf("id = %d, want 123456789", got.ID)
	}
	// The whole point of the data source: the id must be usable as an external id
	// string without the caller converting it.
	if got.ExternalID() != "123456789" {
		t.Errorf("ExternalID() = %q, want %q", got.ExternalID(), "123456789")
	}
	if got.FullName != "acme/api" {
		t.Errorf("full name = %q, want %q (decoded from repo_full_name)", got.FullName, "acme/api")
	}
	if got.Name != "api" {
		t.Errorf("name = %q, want %q (decoded from repo_name)", got.Name, "api")
	}
	if got.Owner != "acme" {
		t.Errorf("owner = %q, want %q", got.Owner, "acme")
	}
	if got.DefaultBranch != "main" {
		t.Errorf("default branch = %q, want %q", got.DefaultBranch, "main")
	}
	if !got.Private {
		t.Error("private = false, want true")
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}

	wantPath := "/api/v2/github-app/organization/" + testGitHubAppOrgID + "/repositories"
	if req := (*seen)[0]; req.method != http.MethodGet || req.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", req.method, req.path, wantPath)
	}
	// page is 1-based and limit is the route's documented maximum.
	if query := (*seen)[0].query; query != "limit=100&page=1" {
		t.Errorf("query = %q, want %q", query, "limit=100&page=1")
	}
}

func TestGitHubAppListRepositoriesDrainsPages(t *testing.T) {
	t.Parallel()

	// The route paginates by page number, not by token, so a full page must be
	// followed by another request and a short page must end the drain. The first
	// page is exactly the request limit so that only a client that keeps going
	// sees the second.
	var calls int
	client, seen := newGitHubAppServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")

		if calls == 1 {
			items := make([]string, 0, 100)
			for i := range 100 {
				items = append(items, fmt.Sprintf(
					`{"id":%d,"repo_full_name":"acme/repo-%d","repo_name":"repo-%d","owner":"acme","default_branch":"main","private":false}`,
					i+1, i+1, i+1,
				))
			}
			_, _ = fmt.Fprintf(w, `{"items":[%s],"total_count":101}`, strings.Join(items, ","))

			return
		}

		_, _ = w.Write([]byte(
			`{"items":[{"id":101,"repo_full_name":"acme/last","repo_name":"last","owner":"acme","default_branch":"main","private":false}],"total_count":101}`,
		))
	})

	repositories, err := client.GitHubApp().ListRepositories(context.Background(), testGitHubAppOrgID)
	if err != nil {
		t.Fatalf("ListRepositories returned error: %v", err)
	}

	if len(repositories) != 101 {
		t.Fatalf("repository count = %d, want 101 (both pages drained)", len(repositories))
	}
	if repositories[100].FullName != "acme/last" {
		t.Errorf("last repository = %q, want %q", repositories[100].FullName, "acme/last")
	}

	if len(*seen) != 2 {
		t.Fatalf("request count = %d, want 2", len(*seen))
	}
	if query := (*seen)[1].query; query != "limit=100&page=2" {
		t.Errorf("second page query = %q, want %q", query, "limit=100&page=2")
	}
}

func TestGitHubAppListRepositoriesStopsOnEmptyPage(t *testing.T) {
	t.Parallel()

	// A server that ignores the page parameter and always answers with an empty
	// page must not spin: an empty page is a short page and ends the drain.
	var calls int
	client, _ := newGitHubAppServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"total_count":5000}`))
	})

	repositories, err := client.GitHubApp().ListRepositories(context.Background(), testGitHubAppOrgID)
	if err != nil {
		t.Fatalf("ListRepositories returned error: %v", err)
	}
	if len(repositories) != 0 {
		t.Errorf("repository count = %d, want 0", len(repositories))
	}
	if calls != 1 {
		t.Errorf("request count = %d, want 1", calls)
	}
}

func TestGitHubAppFindRepository(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fullName string
		wantID   int64
	}{
		{name: "exact match", fullName: "acme/api", wantID: 123456789},
		// GitHub treats owner and repository names case-insensitively, and
		// repo_full_name preserves whatever casing the repository was created
		// with, so a configuration that spells it differently must still resolve.
		{name: "different case", fullName: "ACME/web-ui", wantID: 987654321},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, _ := newGitHubAppServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(testGitHubAppRepositoriesBody))
			})

			repository, err := client.GitHubApp().FindRepository(context.Background(), testGitHubAppOrgID, tt.fullName)
			if err != nil {
				t.Fatalf("FindRepository returned error: %v", err)
			}
			if repository.ID != tt.wantID {
				t.Errorf("id = %d, want %d", repository.ID, tt.wantID)
			}
		})
	}
}

func TestGitHubAppFindRepositoryNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newGitHubAppServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testGitHubAppRepositoriesBody))
	})

	_, err := client.GitHubApp().FindRepository(context.Background(), testGitHubAppOrgID, "acme/absent")
	if !circleci.IsNotFound(err) {
		t.Errorf("FindRepository error = %v, want a not found error", err)
	}
}

func TestGitHubAppListRepositoriesNotFound(t *testing.T) {
	t.Parallel()

	// The route answers the v2 error envelope, a bare "message" string, so the
	// diagnostic detail must pick that up rather than reporting only a status.
	client, _ := newGitHubAppServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Organization not found."}`))
	})

	_, err := client.GitHubApp().ListRepositories(context.Background(), testGitHubAppOrgID)
	if !circleci.IsNotFound(err) {
		t.Fatalf("ListRepositories error = %v, want a not found error", err)
	}
	if detail := circleci.Detail(err); !strings.Contains(detail, "Organization not found.") {
		t.Errorf("Detail() = %q, want it to carry the server message", detail)
	}
}

func TestGitHubAppListRepositoriesEscapesOrgID(t *testing.T) {
	t.Parallel()

	// The organization id arrives from configuration, so anything path-significant
	// in it must be escaped rather than changing which route is called.
	client, seen := newGitHubAppServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"total_count":0}`))
	})

	if _, err := client.GitHubApp().ListRepositories(context.Background(), "org/../evil"); err != nil {
		t.Fatalf("ListRepositories returned error: %v", err)
	}

	// Assert on the raw request line: r.URL.Path is already percent-decoded, so it
	// would look identical whether or not the value was escaped.
	wantURI := "/api/v2/github-app/organization/org%2F..%2Fevil/repositories?limit=100&page=1"
	if got := (*seen)[0].rawURI; got != wantURI {
		t.Errorf("raw request URI = %q, want %q", got, wantURI)
	}
}
