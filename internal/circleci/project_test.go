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

// Identifiers a real standalone project is addressed by, copied from a live
// create so the fake's shapes are the API's shapes rather than plausible-looking
// stand-ins.
//
// standaloneCreatedSlug has the shape POST /organization/{uuid}/project answered
// with: two opaque base62 fragments of 21 or 22 characters each — both lengths
// were observed in both positions — and neither one a UUID or a name. The
// identifiers themselves are stand-ins; the shape is what was measured. The fake
// used to model a
// standalone slug as "circleci/<uuid>/repo" — organization UUID, project NAME —
// which is a shape the API never produces and which would hide any caller that
// addresses a standalone project by name. Measured: the name-addressed form is
// rejected outright, with 400 "Invalid project slug …", not 404. See
// circleci.DeleteProject's comment for all three measurements.
const (
	standaloneOrgUUID     = "11111111-2222-3333-4444-555555555555"
	standaloneProjectUUID = "66666666-7777-8888-9999-aaaaaaaaaaaa"
	standaloneCreatedSlug = "circleci/TFtestOrgFragment01234/TFtestProjFragment012"
)

// newProjectAPI serves the project routes, recording every call.
//
// createdSlug shapes the create response, and it is what decides whether a follow
// request is issued: the slug's middle segment is the organization name for a
// classic organization, and an opaque identifier that is never the organization
// name for a standalone one.
//
// The rest of the response body is DERIVED from createdSlug rather than
// hardcoded, because the two classes do not differ in the slug alone. Measured
// over the network: a classic project reports organization_slug "gh/<org>",
// provider "GitHub" and a real https vcs_url, while a standalone project reports
// organization_slug "circleci/<fragment>", provider "CircleCI" and a vcs_url of
// "//circleci.com/<orgUUID>/<projectUUID>". The fake used to answer with the
// classic shape for both, so every standalone test was asserting against a body
// the API cannot return.
func newProjectAPI(t *testing.T, createdSlug, provider string) (*httptest.Server, func() []projectCall) {
	const orgName = "acme"

	t.Helper()

	orgSlug, vcsURL := projectBodyShape(createdSlug, provider, orgName)

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
			"id":                standaloneProjectUUID,
			"name":              "repo",
			"slug":              createdSlug,
			"organization_name": orgName,
			"organization_slug": orgSlug,
			"organization_id":   standaloneOrgUUID,
			"vcs_info": map[string]string{
				"vcs_url":        vcsURL,
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

// projectBodyShape derives the organization_slug and vcs_info.vcs_url that go
// with a given project slug, the way the API does. See newProjectAPI.
func projectBodyShape(createdSlug, provider, orgName string) (orgSlug, vcsURL string) {
	segments := strings.Split(createdSlug, "/")
	if len(segments) != 3 {
		// A deliberately malformed slug: leave the rest of the body classic, since
		// the test using it is about the malformed slug and nothing else.
		return "gh/" + orgName, "https://github.com/" + orgName + "/repo"
	}

	orgSlug = segments[0] + "/" + segments[1]

	if segments[0] == "circleci" {
		// Standalone: no repository behind the project, so no VCS URL either —
		// CircleCI reports its own scheme-relative one.
		return orgSlug, "//circleci.com/" + standaloneOrgUUID + "/" + standaloneProjectUUID
	}

	host := "github.com"
	if strings.EqualFold(provider, "Bitbucket") {
		host = "bitbucket.org"
	}

	return orgSlug, "https://" + host + "/" + segments[1] + "/" + segments[2]
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
// A standalone organization follows the project as part of creating it, so a
// second follow would be a pointless request against a route that does not
// apply. The slug this uses is one a live create actually returned
// (standaloneCreatedSlug) rather than the "circleci/<uuid>/repo" the fake used to
// invent, and the create response is the standalone one: provider "CircleCI" and
// a "//circleci.com/…" vcs_url.
func TestCreateProjectSkipsFollowForStandaloneOrgs(t *testing.T) {
	t.Parallel()

	srv, recorded := newProjectAPI(t, standaloneCreatedSlug, "CircleCI")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	project, err := c.CreateProject(context.Background(), standaloneOrgUUID, "repo")
	if err != nil {
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

	if got, want := project.VCSInfo.Provider, "CircleCI"; got != want {
		t.Errorf("vcs_info.provider = %q, want %q for a standalone project", got, want)
	}
	if got := project.VCSInfo.VCSURL; !strings.HasPrefix(got, "//circleci.com/") {
		t.Errorf("vcs_info.vcs_url = %q, want a //circleci.com/… URL for a repository-less project", got)
	}
}

// TestDeleteProjectUsesTheSlugTheAPIReported is the BUG P6 investigation, pinned.
//
// The concern was that DeleteProject builds a name-addressed slug, which for a
// standalone project the API rejects — measured, over the network, on a freshly
// created standalone project:
//
//	DELETE /project/circleci/TFtestOrgFragment01234/TFtestProjFragment012 → 200 "Project deleted"
//	DELETE /project/circleci/TFtestOrgFragment01234/my-project            → 400 "Invalid project slug …"
//	DELETE /project/circleci/<orgUUID>/my-project                         → 400, same
//
// It does not build one: it sends the slug it is given, and circleci_project's
// Delete gives it the slug create or the last read reported. So destroy works on
// a standalone project, which a live apply-then-destroy confirmed. This test
// holds that property in place, because the failure it would guard against is
// invisible against a fake whose standalone slug ends in the project name — which
// is exactly the fake this file had.
func TestDeleteProjectUsesTheSlugTheAPIReported(t *testing.T) {
	t.Parallel()

	srv, recorded := newProjectAPI(t, standaloneCreatedSlug, "CircleCI")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	project, err := c.CreateProject(context.Background(), standaloneOrgUUID, "repo")
	if err != nil {
		t.Fatalf("CreateProject returned error: %v", err)
	}

	if err := c.DeleteProject(context.Background(), project.Slug); err != nil {
		t.Fatalf("DeleteProject returned error: %v", err)
	}

	calls := recorded()
	deleted := ""
	for _, call := range calls {
		if call.method == http.MethodDelete {
			deleted = call.path
		}
	}

	if want := "/api/v2/project/" + standaloneCreatedSlug; deleted != want {
		t.Errorf("delete path = %q, want %q — the slug the API reported, verbatim", deleted, want)
	}
	if strings.HasSuffix(deleted, "/repo") {
		t.Errorf("delete path = %q ends in the project NAME; the API answers 400 "+
			"\"Invalid project slug\" for that form on a standalone project", deleted)
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

// TestProjectSlugRejectsDotSegments is the regression test for issue #7: a slug
// whose segment is exactly "." or ".." survives url.PathEscape unchanged (it
// doesn't touch dots) and url.Parse doesn't clean dot-segments out of a path
// either, so either one would put a literal "./" or "../" into the outbound
// request path and retarget it at a different route. This is defence in depth,
// not a fix for a reachable bug: a slug comes from Terraform configuration or
// from CircleCI's own API responses, never from a third party.
//
// It also checks the legitimate cases still parse, including a segment that
// merely contains a dot (a repository named "my.repo"), which must remain
// valid — rejecting that would break real configurations.
func TestProjectSlugRejectsDotSegments(t *testing.T) {
	t.Parallel()

	srv, recorded := newProjectAPI(t, "gh/acme/repo", "GitHub")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	tests := []struct {
		name    string
		slug    string
		wantErr bool
	}{
		{name: "dot in first segment", slug: "./acme/repo", wantErr: true},
		{name: "dot-dot in first segment", slug: "../acme/repo", wantErr: true},
		{name: "dot in middle segment", slug: "gh/./repo", wantErr: true},
		{name: "dot-dot in middle segment", slug: "gh/../repo", wantErr: true},
		{name: "dot in last segment", slug: "gh/acme/.", wantErr: true},
		{name: "dot-dot in last segment", slug: "gh/acme/..", wantErr: true},
		{name: "ordinary slug", slug: "gh/acme/repo", wantErr: false},
		{name: "segment merely containing a dot", slug: "gh/acme/my.repo", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.GetProject(context.Background(), tt.slug)
			if tt.wantErr && err == nil {
				t.Errorf("GetProject(%q) returned no error, want one", tt.slug)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("GetProject(%q) returned error: %v, want none", tt.slug, err)
			}

			err = c.DeleteProject(context.Background(), tt.slug)
			if tt.wantErr && err == nil {
				t.Errorf("DeleteProject(%q) returned no error, want one", tt.slug)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("DeleteProject(%q) returned error: %v, want none", tt.slug, err)
			}
		})
	}

	// Sanity check the legitimate requests actually reached the server, so a
	// mistake that also broke the happy path wouldn't be masked by an early
	// error-only assertion.
	calls := recorded()
	if len(calls) != 4 {
		t.Fatalf("made %d requests, want 4 (get+delete for each of the two legitimate slugs): %+v", len(calls), calls)
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

// TestCreateProjectReturnsTheProjectWhenOnlyFollowFails is BUG P8's regression
// test: a v2 create that succeeds, followed by a v1.1 follow that does not,
// used to make CreateProject discard the created project entirely and return
// nil — indistinguishable, to a caller, from the v2 create itself having
// failed and nothing existing at all. It is not nothing: CircleCI is tracking
// this project, under this exact slug, whether or not the follow call ever
// succeeds — measured live by adopting a repository with no commits on its
// default branch, where the v2 create still answers 200 and the v1.1 follow
// right after it answers 400 {"message":"Branch not found"} (see
// followProject's own doc comment).
//
// The caller this matters most to is internal/provider/project_resource.go's
// Create, by way of projectCreateFailureDetail: it names the project's slug in
// the diagnostic and points at `terraform import`, which it can only do with
// the project CreateProject returns here.
func TestCreateProjectReturnsTheProjectWhenOnlyFollowFails(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.HasSuffix(r.URL.Path, "/follow") {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Branch not found"})

			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":                "c7806d88-6921-4d9d-8810-7c3da450ae19",
			"name":              "repo",
			"slug":              "gh/acme/repo",
			"organization_name": "acme",
			"organization_slug": "gh/acme",
			"organization_id":   standaloneOrgUUID,
			"vcs_info": map[string]string{
				"vcs_url":        "https://github.com/acme/repo",
				"provider":       "GitHub",
				"default_branch": "main",
			},
		})
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	project, err := c.CreateProject(context.Background(), standaloneOrgUUID, "repo")
	if err == nil {
		t.Fatal("CreateProject succeeded; the fake was asked to fail the follow call")
	}
	if !circleci.HasStatus(err, http.StatusBadRequest) {
		t.Errorf("CreateProject error = %v, want one carrying the follow call's 400", err)
	}

	if project == nil {
		t.Fatal("CreateProject returned a nil project alongside the error; the v2 create it already " +
			"made succeeded, so the caller has no way to find what CircleCI is now tracking")
	}
	if project.Slug != "gh/acme/repo" {
		t.Errorf("project.Slug = %q, want %q (the v2 create's own response)", project.Slug, "gh/acme/repo")
	}
}
