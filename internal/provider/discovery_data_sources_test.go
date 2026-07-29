// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The read-only discovery data sources — GitHub App repositories, the execution
// catalog, users and Insights — are exercised against an in-process stand-in for
// the API rather than a real installation. Every one of them is a pure read with no
// writable counterpart, so there is nothing to create first, and the response
// bodies below are the only thing under test on the provider side.
//
// The bodies are the shapes production sends. They were taken from
// the API (the CircleCI API), the API
// (client/machineprovisioner) and the v2 API (the CircleCI API), and
// they are deliberately verbatim rather than convenient: a mock that matched the
// provider's assumptions instead of the API's would let a wrong field name pass.

// discoveryProviderFactories instantiates the provider for the discovery tests.
//
// It is deliberately an alias for the shared factory rather than a cut-down provider
// of its own. There used to be a `discoveryProvider` wrapper here that overrode
// DataSources() with a hardcoded list of eight, on the reasoning that these tests
// should not depend on the real registration. That turned out to be a footgun: it
// silently shadowed the real list, so a data source added later was simply absent and
// any test reusing this factory failed with "the provider does not support data
// source" — which reads like a broken data source rather than a stale test list.
// circleci_pipeline_values and circleci_github_app_installation both hit exactly that.
//
// TestEveryConstructorIsRegistered already guarantees the real list is complete, so
// depending on it is strictly better than duplicating it. This mirrors
// runnerProtoV6ProviderFactories in runner_fake_test.go.
var discoveryProviderFactories = testAccProtoV6ProviderFactories

// discoveryProviderConfig renders a provider block pointed at the mock API.
func discoveryProviderConfig(host, deployment string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host       = %[1]q
  key        = "fake-token"
  deployment = %[2]q
}
`, host, deployment)
}

const (
	// testDiscoveryOrgID is the organization the mock API serves.
	testDiscoveryOrgID = "b9291e0d-a11e-41fb-8517-c545388b5953"
	// testDiscoveryUserID is the user the mock API serves.
	testDiscoveryUserID = "a1b2c3d4-1111-2222-3333-444455556666"
	// testDiscoveryProjectSlug is a VCS-backed project slug.
	testDiscoveryProjectSlug = "gh/acme/api"
	// testDiscoveryStandaloneProjectSlug is the slug form a GitHub App, GitHub
	// Server or GitLab project takes: no VCS-side names to build one from, so both
	// trailing segments are UUIDs.
	testDiscoveryStandaloneProjectSlug = "circleci/11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222"
	// testDiscoveryOrgSlug is an organization slug — two segments, not three.
	testDiscoveryOrgSlug = "gh/acme"
)

// mockDiscoveryAPI is an in-memory stand-in for the routes the discovery data
// sources read.
type mockDiscoveryAPI struct {
	t *testing.T

	mu sync.Mutex
	// requests records the raw request line of every call, so a test can assert
	// which routes were hit and with which query parameters.
	requests []string

	// repositories is the GitHub App's repository list. repositoryPageSize splits
	// it into pages when positive, so the client's page/limit pagination is
	// exercised.
	repositories       []string
	repositoryPageSize int

	// userForbidden makes /me answer 403, as it does for a non-user token.
	userForbidden bool
	// insightsRateLimited makes the Insights routes answer 429.
	insightsRateLimited bool
	// flakyTestsEmpty serves a project with no recorded flakes.
	flakyTestsEmpty bool
}

// newMockDiscoveryAPI starts a mock API and returns it alongside its origin.
func newMockDiscoveryAPI(t *testing.T) (*mockDiscoveryAPI, string) {
	t.Helper()

	api := &mockDiscoveryAPI{t: t, repositories: defaultMockRepositories()}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	return api, srv.URL
}

// seenRequests returns a copy of the recorded request lines.
func (m *mockDiscoveryAPI) seenRequests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]string(nil), m.requests...)
}

// defaultMockRepositories is the GitHub App repository list the mock serves. Note
// repo_full_name and repo_name, which is how the API spells these — not full_name
// and name.
func defaultMockRepositories() []string {
	return []string{
		`{"id":123456789,"repo_full_name":"acme/api","repo_name":"api","owner":"acme","default_branch":"main","private":true}`,
		`{"id":987654321,"repo_full_name":"acme/Web-UI","repo_name":"Web-UI","owner":"acme","default_branch":"trunk","private":false}`,
		`{"id":555555555,"repo_full_name":"acme/docs","repo_name":"docs","owner":"acme","default_branch":"main","private":false}`,
	}
}

func (m *mockDiscoveryAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.requests = append(m.requests, r.Method+" "+r.RequestURI)
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	path := r.URL.Path

	switch {
	case path == "/api/v3/catalog/offerings":
		m.serveCatalog(w)
	case path == "/api/v2/me":
		m.serveCurrentUser(w)
	case path == "/api/v2/me/collaborations":
		m.serveCollaborations(w)
	case strings.HasPrefix(path, "/api/v2/user/"):
		m.serveUser(w, strings.TrimPrefix(path, "/api/v2/user/"))
	case strings.HasPrefix(path, "/api/v2/github-app/organization/") && strings.HasSuffix(path, "/repositories"):
		m.serveRepositories(w, r)
	case strings.HasPrefix(path, "/api/v2/insights/") && strings.HasSuffix(path, "/workflows"):
		m.serveInsights(w, m.workflowsBody())
	case strings.HasPrefix(path, "/api/v2/insights/") && strings.HasSuffix(path, "/flaky-tests"):
		m.serveInsights(w, m.flakyTestsBody())
	case strings.HasPrefix(path, "/api/v2/insights/") && strings.HasSuffix(path, "/summary"):
		m.serveInsights(w, m.summaryBody(r))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not found."}`))
	}
}

// serveRepositories answers one page of the GitHub App repository list. The route
// paginates with 1-based page and limit query parameters and reports a
// total_count, not a next_page_token.
func (m *mockDiscoveryAPI) serveRepositories(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	all := append([]string(nil), m.repositories...)
	pageSize := m.repositoryPageSize
	m.mu.Unlock()

	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"limit must be a positive integer."}`))

		return
	}
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"page must be a positive integer."}`))

		return
	}

	// A test-controlled page size stands in for a server that serves smaller pages
	// than the client asked for, which is how the drain gets exercised without
	// seeding a hundred repositories.
	if pageSize > 0 && pageSize < limit {
		limit = pageSize
	}

	start := min((page-1)*limit, len(all))
	end := min(start+limit, len(all))

	_, _ = fmt.Fprintf(w, `{"items":[%s],"total_count":%d}`, strings.Join(all[start:end], ","), len(all))
}

// serveCatalog answers the v3 catalog. The envelope carries attributes but no id
// and no references, because the catalog is a singleton rather than an addressable
// entity.
func (m *mockDiscoveryAPI) serveCatalog(w http.ResponseWriter) {
	_, _ = w.Write([]byte(`{
  "data": {
    "attributes": {
      "linux": {
        "medium": ["ubuntu-2404:current", "ubuntu-2204:current"],
        "large": ["ubuntu-2404:current"]
      },
      "windows": {
        "windows.medium": ["windows-server-2022-gui:current"]
      },
      "macos": {},
      "deprecated": {
        "ubuntu-2004": ["2024.10.1"]
      }
    }
  }
}`))
}

// serveCurrentUser answers /me. It is a flat object with exactly four keys and no
// envelope.
func (m *mockDiscoveryAPI) serveCurrentUser(w http.ResponseWriter) {
	m.mu.Lock()
	forbidden := m.userForbidden
	m.mu.Unlock()

	if forbidden {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Forbidden."}`))

		return
	}

	_, _ = fmt.Fprintf(w, `{"id":%q,"login":"octocat","name":"Mona Lisa Octocat","avatar_url":"https://avatars.example.com/u/1"}`,
		testDiscoveryUserID)
}

// serveUser answers /user/{id}.
func (m *mockDiscoveryAPI) serveUser(w http.ResponseWriter, userID string) {
	if userID != testDiscoveryUserID {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not found."}`))

		return
	}

	_, _ = fmt.Fprintf(w, `{"id":%q,"login":"other-user","name":"Other User","avatar_url":"https://avatars.example.com/u/9"}`,
		userID)
}

// serveCollaborations answers /me/collaborations with a bare JSON array — not the
// items/next_page_token envelope, and not paginated. The second entry has a null
// id, which is what an organization CircleCI does not know yet looks like.
func (m *mockDiscoveryAPI) serveCollaborations(w http.ResponseWriter) {
	_, _ = w.Write([]byte(`[
  {
    "id": "11111111-1111-1111-1111-111111111111",
    "vcs_type": "circleci",
    "name": "acme",
    "slug": "circleci/11111111-1111-1111-1111-111111111111",
    "avatar_url": "https://avatars.example.com/u/2"
  },
  {
    "id": null,
    "vcs_type": "github",
    "name": "not-onboarded",
    "slug": "gh/not-onboarded",
    "avatar_url": "https://avatars.example.com/u/3"
  }
]`))
}

// serveInsights writes an Insights body, or the rate limit response when the test
// asked for it.
func (m *mockDiscoveryAPI) serveInsights(w http.ResponseWriter, body string) {
	m.mu.Lock()
	rateLimited := m.insightsRateLimited
	m.mu.Unlock()

	if rateLimited {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"Rate Limit Exceeded"}`))

		return
	}

	_, _ = w.Write([]byte(body))
}

// workflowsBody is the per-workflow metrics response. The second workflow has
// never failed, so its mttr, credits, recoveries and every duration statistic are
// null rather than zero.
func (m *mockDiscoveryAPI) workflowsBody() string {
	return `{
  "items": [
    {
      "name": "build-and-test",
      "project_id": "22222222-2222-2222-2222-222222222222",
      "window_start": "2026-04-28T00:00:00Z",
      "window_end": "2026-07-27T00:00:00Z",
      "metrics": {
        "total_runs": 420,
        "successful_runs": 399,
        "failed_runs": 21,
        "success_rate": 0.95,
        "throughput": 4.615,
        "mttr": 1834,
        "total_credits_used": 91234,
        "total_recoveries": 19,
        "duration_metrics": {
          "min": 121, "mean": 384, "median": 355, "p95": 701, "max": 1288,
          "standard_deviation": 142.5
        }
      }
    },
    {
      "name": "nightly",
      "project_id": "22222222-2222-2222-2222-222222222222",
      "window_start": "2026-04-28T00:00:00Z",
      "window_end": "2026-07-27T00:00:00Z",
      "metrics": {
        "total_runs": 3,
        "successful_runs": 3,
        "failed_runs": 0,
        "success_rate": 1.0,
        "throughput": 0.03,
        "mttr": null,
        "total_credits_used": null,
        "total_recoveries": null,
        "duration_metrics": {
          "min": null, "mean": null, "median": null, "p95": null, "max": null,
          "standard_deviation": null
        }
      }
    }
  ],
  "next_page_token": null
}`
}

// flakyTestsBody is the flaky tests response. total_flaky_tests counts unique
// tests, so it is deliberately smaller than the number of entries: the same test
// appears twice.
func (m *mockDiscoveryAPI) flakyTestsBody() string {
	m.mu.Lock()
	empty := m.flakyTestsEmpty
	m.mu.Unlock()

	if empty {
		return `{"flaky_tests":[],"total_flaky_tests":0}`
	}

	return `{
  "flaky_tests": [
    {
      "test_name": "test_checkout_retries",
      "classname": "CheckoutTest",
      "file": "spec/checkout_spec.rb",
      "source": "junit",
      "times_flaked": 7,
      "job_name": "rspec",
      "job_number": 1234,
      "pipeline_number": 10027,
      "workflow_id": "966b80cf-909b-4e79-b228-9d59dfd0c3ff",
      "workflow_name": "build-and-test",
      "workflow_created_at": "2026-07-26T23:59:55.667Z",
      "time_wasted": 480
    },
    {
      "test_name": "test_checkout_retries",
      "classname": "CheckoutTest",
      "file": null,
      "source": null,
      "times_flaked": 7,
      "job_name": "rspec",
      "job_number": 1240,
      "pipeline_number": 10031,
      "workflow_id": "0f2c9e51-7b0f-45c3-9d4a-6dd6c0e1a2b3",
      "workflow_name": "build-and-test",
      "workflow_created_at": "2026-07-27T04:12:00.000Z"
    }
  ],
  "total_flaky_tests": 1
}`
}

// summaryBody is the organization summary. org_project_data is only populated when
// project-names was passed, which mirrors the API: it does not report every
// project by default.
func (m *mockDiscoveryAPI) summaryBody(r *http.Request) string {
	projectData := "[]"
	if names := r.URL.Query()["project-names"]; len(names) > 0 {
		entries := make([]string, 0, len(names))
		for _, name := range names {
			// throughput is absent from a project block: the API does not compute it
			// per project.
			entries = append(entries, fmt.Sprintf(`{
      "project_name": %q,
      "metrics": {"total_runs":3000,"total_duration_secs":1000000,"total_credits_used":1500000,"success_rate":0.94},
      "trends": {"total_runs":0.2,"total_duration_secs":0.1,"total_credits_used":0.15,"success_rate":-0.02}
    }`, name))
		}
		projectData = "[" + strings.Join(entries, ",") + "]"
	}

	return `{
  "org_data": {
    "metrics": {
      "total_runs": 5000,
      "total_duration_secs": 1800000,
      "total_credits_used": 2500000,
      "success_rate": 0.92,
      "throughput": 55.5
    },
    "trends": {
      "total_runs": 0.12,
      "total_duration_secs": -0.05,
      "total_credits_used": 0.08,
      "success_rate": 0.01,
      "throughput": 0.12
    }
  },
  "org_project_data": ` + projectData + `,
  "all_projects": ["api", "web-ui", "docs"]
}`
}
