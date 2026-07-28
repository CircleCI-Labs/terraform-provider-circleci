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

const testEnvVarProjectSlug = "circleci/AbCdEfG/HiJkLmN"

func TestListProjectEnvironmentVariables(t *testing.T) {
	t.Parallel()

	// The shape mirrors the v2 API's env-var-public-view
	// (the CircleCI API): name, masked value, and a created_at that is
	// null for variables predating the timestamp.
	client, seen := pageListServer(t,
		`{"items":[{"name":"API_TOKEN","value":"xxxx1234","created_at":"2023-04-14T21:20:14.000Z"}],"next_page_token":"tok-2"}`,
		`{"items":[{"name":"ZONE","value":"xxxxst-1","created_at":null}],"next_page_token":null}`,
	)

	vars, err := client.ListProjectEnvironmentVariables(context.Background(), testEnvVarProjectSlug)
	if err != nil {
		t.Fatalf("ListProjectEnvironmentVariables returned error: %v", err)
	}

	if len(vars) != 2 {
		t.Fatalf("variable count = %d, want 2 (both pages drained)", len(vars))
	}
	if vars[0].Name != "API_TOKEN" || vars[0].Value != "xxxx1234" {
		t.Errorf("first variable = %+v, want API_TOKEN masked as xxxx1234", vars[0])
	}
	if vars[0].CreatedAt != "2023-04-14T21:20:14.000Z" {
		t.Errorf("first variable created_at = %q, want the timestamp verbatim", vars[0].CreatedAt)
	}
	// A null created_at must decode to "" rather than failing the whole list.
	if vars[1].CreatedAt != "" {
		t.Errorf("second variable created_at = %q, want \"\" for a null timestamp", vars[1].CreatedAt)
	}

	if len(*seen) != 2 {
		t.Fatalf("request count = %d, want 2", len(*seen))
	}

	// The slug's separators stay literal: percent-encoded separators do not match
	// the route on CircleCI Server.
	wantPath := "/api/v2/project/" + testEnvVarProjectSlug + "/envvar"
	if got := (*seen)[0]; got.method != http.MethodGet || got.rawURI != wantPath {
		t.Errorf("first request = %s %s, want GET %s", got.method, got.rawURI, wantPath)
	}
	if got := (*seen)[1].rawURI; got != wantPath+"?page-token=tok-2" {
		t.Errorf("second request URI = %q, want %q", got, wantPath+"?page-token=tok-2")
	}
}

func TestListProjectEnvironmentVariablesEmpty(t *testing.T) {
	t.Parallel()

	client, _ := pageListServer(t, `{"items":[],"next_page_token":null}`)

	vars, err := client.ListProjectEnvironmentVariables(context.Background(), testEnvVarProjectSlug)
	if err != nil {
		t.Fatalf("ListProjectEnvironmentVariables returned error: %v", err)
	}
	if len(vars) != 0 {
		t.Errorf("variable count = %d, want 0", len(vars))
	}
}

func TestListProjectEnvironmentVariablesRejectsMalformedSlug(t *testing.T) {
	t.Parallel()

	// A malformed slug is rejected before any request, because the resulting
	// request would otherwise fail with a confusing HTTP 404.
	for _, slug := range []string{"", "circleci", "circleci/org", "circleci//repo", "a/b/c/d"} {
		t.Run(slug, func(t *testing.T) {
			t.Parallel()

			client, seen := pageListServer(t, `{"items":[],"next_page_token":null}`)

			_, err := client.ListProjectEnvironmentVariables(context.Background(), slug)
			if err == nil {
				t.Fatalf("ListProjectEnvironmentVariables(%q) returned no error, want one", slug)
			}
			if !strings.Contains(err.Error(), "project slug") {
				t.Errorf("error = %v, want it to name the project slug", err)
			}
			if len(*seen) != 0 {
				t.Errorf("request count = %d, want 0: the slug must be rejected before any request", len(*seen))
			}
		})
	}
}

func TestListProjectEnvironmentVariablesEscapesSlugSegments(t *testing.T) {
	t.Parallel()

	// A reserved character inside one segment is escaped, while the separators
	// between segments stay literal.
	client, seen := pageListServer(t, `{"items":[],"next_page_token":null}`)

	if _, err := client.ListProjectEnvironmentVariables(context.Background(), "gh/acme/re po"); err != nil {
		t.Fatalf("ListProjectEnvironmentVariables returned error: %v", err)
	}

	wantURI := "/api/v2/project/gh/acme/re%20po/envvar"
	if got := (*seen)[0].rawURI; got != wantURI {
		t.Errorf("raw request URI = %q, want %q", got, wantURI)
	}
}

func TestListProjectEnvironmentVariablesNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListError(w, http.StatusNotFound, "Project not found.")
	})

	_, err := client.ListProjectEnvironmentVariables(context.Background(), testEnvVarProjectSlug)
	if !circleci.IsNotFound(err) {
		t.Errorf("ListProjectEnvironmentVariables error = %v, want a not found error", err)
	}
}
