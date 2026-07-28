// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// projectCall is one request the fake project API received.
type projectCall struct {
	method     string
	path       string
	requestURI string
	body       []byte
}

// newProjectAPI serves the project routes, recording every call.
//
// createdSlug shapes the create response, and it is what decides whether a follow
// request is issued: the slug's middle segment is the organization name for a
// classic organization and a UUID for a standalone one.
func newProjectAPI(t *testing.T, createdSlug, provider string) (*httptest.Server, func() []projectCall) {
	const orgName = "acme"

	t.Helper()

	var (
		mu    sync.Mutex
		calls []projectCall
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		calls = append(calls, projectCall{
			method: r.Method, path: r.URL.Path, requestURI: r.RequestURI, body: body,
		})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		// The v1.1 follow route answers with an empty body.
		if strings.HasSuffix(r.URL.Path, "/follow") {
			w.WriteHeader(http.StatusOK)

			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":                "00000000-1111-2222-3333-444444444444",
			"name":              "repo",
			"slug":              createdSlug,
			"organization_name": orgName,
			"organization_slug": "gh/acme",
			"organization_id":   "55555555-6666-7777-8888-999999999999",
			"vcs_info": map[string]string{
				"vcs_url":        "https://github.com/acme/repo",
				"provider":       provider,
				"default_branch": "main",
			},
		})
	}))
	t.Cleanup(srv.Close)

	return srv, func() []projectCall {
		mu.Lock()
		defer mu.Unlock()

		return append([]projectCall(nil), calls...)
	}
}

// TestCreateProjectFollowsAgainstTheConfiguredHost is the regression test for the
// CircleCI Server hole.
//
// circleci-sdk-go sent this follow request to a hardcoded https://circleci.com,
// ignoring the configured host, so creating a project against a Server
// installation either failed or silently followed a project on Cloud. The SDK
// carries a live TODO acknowledging it.
func TestCreateProjectFollowsAgainstTheConfiguredHost(t *testing.T) {
	t.Parallel()

	srv, recorded := newProjectAPI(t, "gh/acme/repo", "GitHub")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	project, err := c.CreateProject(context.Background(), "55555555-6666-7777-8888-999999999999", "repo")
	if err != nil {
		t.Fatalf("CreateProject returned error: %v", err)
	}
	if project.Slug != "gh/acme/repo" {
		t.Errorf("Slug = %q, want %q", project.Slug, "gh/acme/repo")
	}

	calls := recorded()
	if len(calls) != 2 {
		t.Fatalf("made %d requests, want 2 (create then follow): %+v", len(calls), calls)
	}

	if got, want := calls[0].path, "/api/v2/organization/55555555-6666-7777-8888-999999999999/project"; got != want {
		t.Errorf("create path = %q, want %q", got, want)
	}
	if !strings.Contains(string(calls[0].body), `"name":"repo"`) {
		t.Errorf("create body = %s, want it to carry the project name", calls[0].body)
	}

	// The whole point: this reached the test server, so it used the configured
	// host rather than circleci.com.
	if got, want := calls[1].path, "/api/v1.1/project/github/acme/repo/follow"; got != want {
		t.Errorf("follow path = %q, want %q", got, want)
	}
}

// TestCreateProjectSkipsFollowForStandaloneOrgs covers the other branch.
//
// A standalone organization follows the project as part of creating it, and its
// slug's middle segment is a UUID rather than the organization name. Issuing a
// second follow there would be a pointless request against a route that does not
// apply.
func TestCreateProjectSkipsFollowForStandaloneOrgs(t *testing.T) {
	t.Parallel()

	const standaloneSlug = "circleci/aaaaaaaa-1111-2222-3333-444444444444/repo"

	srv, recorded := newProjectAPI(t, standaloneSlug, "GitHub")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if _, err := c.CreateProject(context.Background(), "55555555-6666-7777-8888-999999999999", "repo"); err != nil {
		t.Fatalf("CreateProject returned error: %v", err)
	}

	calls := recorded()
	if len(calls) != 1 {
		t.Fatalf("made %d requests, want 1 (create only, no follow): %+v", len(calls), calls)
	}
	for _, call := range calls {
		if strings.Contains(call.path, "/follow") {
			t.Errorf("issued a follow request for a standalone organization: %s", call.path)
		}
	}
}

func TestGetProjectEscapesSlugSegments(t *testing.T) {
	t.Parallel()

	srv, recorded := newProjectAPI(t, "gh/acme/repo", "GitHub")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	// A project name may contain characters that must be escaped, but the slug's
	// own separators must stay literal or the route will not match.
	if _, err := c.GetProject(context.Background(), "circleci/my org/my+repo"); err != nil {
		t.Fatalf("GetProject returned error: %v", err)
	}

	calls := recorded()
	if len(calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(calls))
	}

	// Assert on the raw request line: r.URL.Path is already percent-decoded and
	// would pass whether or not the segments were escaped.
	if want := "/api/v2/project/circleci/my%20org/my+repo"; calls[0].requestURI != want {
		t.Errorf("raw request URI = %q, want %q", calls[0].requestURI, want)
	}
}

func TestProjectSlugValidation(t *testing.T) {
	t.Parallel()

	c := circleci.New(circleci.Config{Host: "http://127.0.0.1:1", Token: "tok"})

	for _, slug := range []string{"", "repo", "gh/acme", "gh/acme/repo/extra", "gh//repo", "gh/acme/"} {
		if _, err := c.GetProject(context.Background(), slug); err == nil {
			t.Errorf("GetProject(%q) returned no error, want one", slug)
		}
		if err := c.DeleteProject(context.Background(), slug); err == nil {
			t.Errorf("DeleteProject(%q) returned no error, want one", slug)
		}
	}
}

func TestDeleteProjectPath(t *testing.T) {
	t.Parallel()

	srv, recorded := newProjectAPI(t, "gh/acme/repo", "GitHub")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if err := c.DeleteProject(context.Background(), "gh/acme/repo"); err != nil {
		t.Fatalf("DeleteProject returned error: %v", err)
	}

	calls := recorded()
	if len(calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(calls))
	}
	if calls[0].method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", calls[0].method)
	}
	if want := "/api/v2/project/gh/acme/repo"; calls[0].path != want {
		t.Errorf("path = %q, want %q", calls[0].path, want)
	}
}

// TestCreateProjectFollowLowercasesTheProvider covers the other VCS.
//
// The follow route takes the provider in lowercase, while the project response
// reports it capitalized ("GitHub", "Bitbucket"). Only exercising GitHub left the
// lowercasing untested for every other VCS.
func TestCreateProjectFollowLowercasesTheProvider(t *testing.T) {
	t.Parallel()

	srv, recorded := newProjectAPI(t, "bb/acme/repo", "Bitbucket")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if _, err := c.CreateProject(context.Background(), "55555555-6666-7777-8888-999999999999", "repo"); err != nil {
		t.Fatalf("CreateProject returned error: %v", err)
	}

	calls := recorded()
	if len(calls) != 2 {
		t.Fatalf("made %d requests, want 2 (create then follow): %+v", len(calls), calls)
	}
	if got, want := calls[1].path, "/api/v1.1/project/bitbucket/acme/repo/follow"; got != want {
		t.Errorf("follow path = %q, want %q", got, want)
	}
}
