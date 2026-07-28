// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

func TestNormalizeHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		in           string
		want         string
		wantSuffixed bool
	}{
		{"bare origin", "https://circleci.com", "https://circleci.com", false},
		{"trailing slash", "https://circleci.com/", "https://circleci.com", false},
		// The provider used to document host = "https://circleci.com/api/v2",
		// so existing configurations must keep working.
		{"legacy v2 suffix", "https://circleci.com/api/v2", "https://circleci.com", true},
		{"legacy v2 suffix with slash", "https://circleci.com/api/v2/", "https://circleci.com", true},
		{"v3 suffix", "https://circleci.com/api/v3", "https://circleci.com", true},
		{"v1.1 suffix", "https://circleci.com/api/v1.1", "https://circleci.com", true},
		{"server host", "https://circleci.example.com/api/v2", "https://circleci.example.com", true},
		{"server bare", "https://circleci.example.com", "https://circleci.example.com", false},
		{"empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, gotSuffixed := circleci.NormalizeHost(tt.in)
			if got != tt.want {
				t.Errorf("NormalizeHost(%q) host = %q, want %q", tt.in, got, tt.want)
			}
			if gotSuffixed != tt.wantSuffixed {
				t.Errorf("NormalizeHost(%q) hadVersionSuffix = %v, want %v", tt.in, gotSuffixed, tt.wantSuffixed)
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	t.Parallel()

	c := circleci.New(circleci.Config{Token: "tok"})

	if got := c.Host(); got != circleci.DefaultHost {
		t.Errorf("Host() = %q, want %q", got, circleci.DefaultHost)
	}
	if got := c.Deployment(); got != circleci.DeploymentCloud {
		t.Errorf("Deployment() = %q, want %q", got, circleci.DeploymentCloud)
	}
	if !c.IsCloud() {
		t.Error("IsCloud() = false, want true for the default deployment")
	}
	if !c.UseV3() {
		t.Error("UseV3() = false, want true on cloud")
	}
}

func TestUseV3ByDeployment(t *testing.T) {
	t.Parallel()

	// Server does not route /api/v3 to the public API service, so entities that
	// exist on both versions must take the v2 path there.
	server := circleci.New(circleci.Config{Token: "tok", Deployment: circleci.DeploymentServer})
	if server.UseV3() {
		t.Error("UseV3() = true on server, want false")
	}
	if server.IsCloud() {
		t.Error("IsCloud() = true on server, want false")
	}

	cloud := circleci.New(circleci.Config{Token: "tok", Deployment: circleci.DeploymentCloud})
	if !cloud.UseV3() {
		t.Error("UseV3() = false on cloud, want true")
	}
}

// recordingServer captures the path, query and headers of the last request.
type recordedRequest struct {
	path       string
	requestURI string
	query      string
	header     http.Header
}

func newRecordingServer(t *testing.T, status int, body string) (*httptest.Server, *recordedRequest) {
	t.Helper()

	var rec recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.path = r.URL.Path
		rec.requestURI = r.RequestURI
		rec.query = r.URL.RawQuery
		rec.header = r.Header.Clone()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv, &rec
}

func TestVersionPrefixes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		call     func(context.Context, *circleci.Client) error
		wantPath string
	}{
		{
			name: "GetV2 prefixes /api/v2",
			call: func(ctx context.Context, c *circleci.Client) error {
				var out map[string]any
				return c.GetV2(ctx, "/context/abc", &out)
			},
			wantPath: "/api/v2/context/abc",
		},
		{
			name: "GetV3 prefixes /api/v3",
			call: func(ctx context.Context, c *circleci.Client) error {
				var out map[string]any
				return c.GetV3(ctx, "/orgs/abc/settings", &out)
			},
			wantPath: "/api/v3/orgs/abc/settings",
		},
		{
			name: "PostV3 prefixes /api/v3",
			call: func(ctx context.Context, c *circleci.Client) error {
				var out map[string]any
				return c.PostV3(ctx, "/namespaces", map[string]string{"n": "v"}, &out)
			},
			wantPath: "/api/v3/namespaces",
		},
		{
			name: "DeleteV2 prefixes /api/v2",
			call: func(ctx context.Context, c *circleci.Client) error {
				return c.DeleteV2(ctx, "/context/abc")
			},
			wantPath: "/api/v2/context/abc",
		},
		{
			name: "PatchV2 prefixes /api/v2",
			call: func(ctx context.Context, c *circleci.Client) error {
				var out map[string]any
				return c.PatchV2(ctx, "/project/gh/o/p/settings", map[string]string{"a": "b"}, &out)
			},
			wantPath: "/api/v2/project/gh/o/p/settings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, rec := newRecordingServer(t, http.StatusOK, `{}`)
			c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

			if err := tt.call(context.Background(), c); err != nil {
				t.Fatalf("call returned error: %v", err)
			}
			if rec.path != tt.wantPath {
				t.Errorf("request path = %q, want %q", rec.path, tt.wantPath)
			}
		})
	}
}

func TestAuthHeader(t *testing.T) {
	t.Parallel()

	// Circle-Token is used for every version and both deployment types, because
	// Server only accepts Authorization: Bearer on recent images.
	for _, deployment := range []circleci.Deployment{circleci.DeploymentCloud, circleci.DeploymentServer} {
		t.Run(string(deployment), func(t *testing.T) {
			t.Parallel()

			srv, rec := newRecordingServer(t, http.StatusOK, `{}`)
			c := circleci.New(circleci.Config{Host: srv.URL, Token: "sekrit", Deployment: deployment})

			var out map[string]any
			if err := c.GetV2(context.Background(), "/me", &out); err != nil {
				t.Fatalf("GetV2 returned error: %v", err)
			}

			if got := rec.header.Get("Circle-Token"); got != "sekrit" {
				t.Errorf("Circle-Token header = %q, want %q", got, "sekrit")
			}
			if got := rec.header.Get("Authorization"); got != "" {
				t.Errorf("Authorization header = %q, want it unset", got)
			}
		})
	}
}

func TestRouteParamsAreEscaped(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, `{}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	// A name containing a slash or space must not corrupt the route.
	// circleci-sdk-go interpolates such values straight into the path with no
	// escaping, so a name like this silently produced a different request.
	var out map[string]any
	err := c.GetV2(context.Background(), "/context/%s/environment-variable/%s", &out,
		circleci.RouteParams("ctx-id", "MY VAR/WEIRD"))
	if err != nil {
		t.Fatalf("GetV2 returned error: %v", err)
	}

	// Assert on the raw request line: r.URL.Path is already percent-decoded, so
	// it would pass whether or not the value was escaped.
	wantURI := "/api/v2/context/ctx-id/environment-variable/MY%20VAR%2FWEIRD"
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", rec.requestURI, wantURI)
	}

	// And the server still decodes it back to the original value.
	wantPath := "/api/v2/context/ctx-id/environment-variable/MY VAR/WEIRD"
	if rec.path != wantPath {
		t.Errorf("decoded path = %q, want %q", rec.path, wantPath)
	}
}

func TestQueryHelpers(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, `{}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	var out map[string]any
	err := c.GetV3(context.Background(), "/orb/packages", &out,
		circleci.Filter("namespace_id", "ns-1"),
		circleci.Filter("certified", ""), // empty: must be omitted
		circleci.PageLimit(50),
		circleci.PageCursor(""), // empty: must be omitted
	)
	if err != nil {
		t.Fatalf("GetV3 returned error: %v", err)
	}

	want := "filter%5Bnamespace_id%5D=ns-1&page%5Blimit%5D=50"
	if rec.query != want {
		t.Errorf("query = %q, want %q", rec.query, want)
	}
}

func TestPageLimitOmittedWhenNonPositive(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, `{}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	var out map[string]any
	if err := c.GetV3(context.Background(), "/orgs", &out, circleci.PageLimit(0)); err != nil {
		t.Fatalf("GetV3 returned error: %v", err)
	}

	if rec.query != "" {
		t.Errorf("query = %q, want it empty so the server default applies", rec.query)
	}
}

func TestLegacyHostWithVersionSuffixStillRoutes(t *testing.T) {
	t.Parallel()

	// Someone upgrading from an older release may still have
	// host = "<origin>/api/v2" in their configuration. The client must not
	// produce /api/v2/api/v2/...
	srv, rec := newRecordingServer(t, http.StatusOK, `{}`)
	c := circleci.New(circleci.Config{Host: srv.URL + "/api/v2", Token: "tok"})

	var out map[string]any
	if err := c.GetV2(context.Background(), "/me", &out); err != nil {
		t.Fatalf("GetV2 returned error: %v", err)
	}

	if rec.path != "/api/v2/me" {
		t.Errorf("request path = %q, want %q", rec.path, "/api/v2/me")
	}
}

func TestDeleteSkipsDecodingEmptyBody(t *testing.T) {
	t.Parallel()

	// A 204 carries no body; decoding it would fail.
	srv, _ := newRecordingServer(t, http.StatusNoContent, ``)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if err := c.DeleteV3(context.Background(), "/namespaces/abc"); err != nil {
		t.Fatalf("DeleteV3 returned error: %v", err)
	}
}
