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

const checkoutKeyBody = `{
  "public_key": "ssh-rsa AAAA",
  "type": "deploy-key",
  "fingerprint": "c9:0b:1c:4f:d5:65:56:b9:ad:88:f9:81:2b:37:74:2f",
  "preferred": true,
  "created_at": "2015-09-21T17:29:21.042Z"
}`

// TestGetCheckoutKeyWireFormat is the load-bearing test for the project slug.
//
// The slug contains "/" separators. Passing it through RouteParams would escape
// them to %2F, which does not match the API route, so the client interpolates the
// slug into the route with each segment escaped separately. r.URL.Path is already
// percent-decoded and so cannot tell the two apart: assert on the raw request
// line instead.
func TestGetCheckoutKeyWireFormat(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, checkoutKeyBody)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	key, err := c.GetCheckoutKey(context.Background(), "gh/acme/widgets",
		"c9:0b:1c:4f:d5:65:56:b9:ad:88:f9:81:2b:37:74:2f")
	if err != nil {
		t.Fatalf("GetCheckoutKey returned error: %v", err)
	}

	wantURI := "/api/v2/project/gh/acme/widgets/checkout-key/" +
		"c9:0b:1c:4f:d5:65:56:b9:ad:88:f9:81:2b:37:74:2f"
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q (the slug separators must stay literal)", rec.requestURI, wantURI)
	}
	if strings.Contains(rec.requestURI, "%2F") {
		t.Errorf("raw request URI = %q, must not contain an escaped slug separator", rec.requestURI)
	}

	if key.PublicKey != "ssh-rsa AAAA" {
		t.Errorf("PublicKey = %q, want %q", key.PublicKey, "ssh-rsa AAAA")
	}
	if key.Type != circleci.CheckoutKeyTypeDeployKey {
		t.Errorf("Type = %q, want %q", key.Type, circleci.CheckoutKeyTypeDeployKey)
	}
	if key.Fingerprint != "c9:0b:1c:4f:d5:65:56:b9:ad:88:f9:81:2b:37:74:2f" {
		t.Errorf("Fingerprint = %q, want the fingerprint from the body", key.Fingerprint)
	}
	if !key.Preferred {
		t.Error("Preferred = false, want true")
	}
	if key.CreatedAt != "2015-09-21T17:29:21.042Z" {
		t.Errorf("CreatedAt = %q, want %q", key.CreatedAt, "2015-09-21T17:29:21.042Z")
	}
}

// TestCheckoutKeyRouteEscapesSegmentsNotSeparators covers the middle ground: a
// character that must be escaped inside a segment, on a route that must keep its
// separators.
func TestCheckoutKeyRouteEscapesSegmentsNotSeparators(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, checkoutKeyBody)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	// A SHA256 fingerprint contains "/", "+" and "=", which the API expects
	// URL-encoded, unlike the slug separators.
	_, err := c.GetCheckoutKey(context.Background(), "circleci/my org/my+repo", "SHA256:a/b+c=")
	if err != nil {
		t.Fatalf("GetCheckoutKey returned error: %v", err)
	}

	wantURI := "/api/v2/project/circleci/my%20org/my+repo/checkout-key/SHA256:a%2Fb+c="
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", rec.requestURI, wantURI)
	}
}

func TestGetCheckoutKeyNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusNotFound, `{"message":"Checkout key not found"}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetCheckoutKey(context.Background(), "gh/acme/widgets", "aa:bb")
	if err == nil {
		t.Fatal("GetCheckoutKey returned no error for a 404")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
	if got, want := circleci.Detail(err), "Checkout key not found"; !strings.Contains(got, want) {
		t.Errorf("Detail(err) = %q, want it to contain %q", got, want)
	}
}

func TestGetCheckoutKeyServerErrorIsNotNotFound(t *testing.T) {
	t.Parallel()

	// A 5xx whose body mentions 404 must not be mistaken for a missing key, which
	// is what substring matching on the error text used to do.
	srv, _ := newRecordingServer(t, http.StatusBadGateway, `{"message":"upstream returned 404"}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetCheckoutKey(context.Background(), "gh/acme/widgets", "aa:bb")
	if err == nil {
		t.Fatal("GetCheckoutKey returned no error for a 502")
	}
	if circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = true, want false for a 502", err)
	}
}

func TestCreateCheckoutKeyRequest(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotURI    string
		gotBody   []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURI = r.RequestURI
		gotBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		// A key created as "user-key" is reported as "github-user-key".
		_, _ = io.WriteString(w, `{"public_key":"ssh-rsa BBBB","type":"github-user-key",`+
			`"fingerprint":"ab:cd","preferred":false,"created_at":"2024-01-02T03:04:05.000Z"}`)
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	key, err := c.CreateCheckoutKey(context.Background(), "gh/acme/widgets", circleci.CheckoutKeyTypeUserKey)
	if err != nil {
		t.Fatalf("CreateCheckoutKey returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if want := "/api/v2/project/gh/acme/widgets/checkout-key"; gotURI != want {
		t.Errorf("raw request URI = %q, want %q", gotURI, want)
	}

	var body map[string]string
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("request body %q is not JSON: %v", gotBody, err)
	}
	if want := map[string]string{"type": "user-key"}; len(body) != 1 || body["type"] != want["type"] {
		t.Errorf("request body = %v, want %v", body, want)
	}

	if key.Type != circleci.CheckoutKeyTypeGitHubUserKey {
		t.Errorf("Type = %q, want the type the API reported (%q)", key.Type, circleci.CheckoutKeyTypeGitHubUserKey)
	}
	if got := key.InputType(); got != circleci.CheckoutKeyTypeUserKey {
		t.Errorf("InputType() = %q, want %q so it can be compared with the configured type", got, circleci.CheckoutKeyTypeUserKey)
	}
}

func TestInputTypeLeavesDeployKeyAlone(t *testing.T) {
	t.Parallel()

	key := circleci.CheckoutKey{Type: circleci.CheckoutKeyTypeDeployKey}
	if got := key.InputType(); got != circleci.CheckoutKeyTypeDeployKey {
		t.Errorf("InputType() = %q, want %q", got, circleci.CheckoutKeyTypeDeployKey)
	}
}

func TestDeleteCheckoutKeyRequest(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotURI    string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURI = r.RequestURI

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":"ok"}`)
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if err := c.DeleteCheckoutKey(context.Background(), "gh/acme/widgets", "aa:bb:cc"); err != nil {
		t.Fatalf("DeleteCheckoutKey returned error: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if want := "/api/v2/project/gh/acme/widgets/checkout-key/aa:bb:cc"; gotURI != want {
		t.Errorf("raw request URI = %q, want %q", gotURI, want)
	}
}

func TestListCheckoutKeysPaginates(t *testing.T) {
	t.Parallel()

	var (
		mu   sync.Mutex
		uris []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		uris = append(uris, r.RequestURI)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page-token") {
		case "":
			_, _ = io.WriteString(w, `{"items":[{"fingerprint":"aa","type":"deploy-key"}],"next_page_token":"tok-2"}`)
		case "tok-2":
			_, _ = io.WriteString(w, `{"items":[{"fingerprint":"bb","type":"github-user-key"}],"next_page_token":null}`)
		default:
			t.Errorf("unexpected page-token %q", r.URL.Query().Get("page-token"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	keys, err := c.ListCheckoutKeys(context.Background(), "gh/acme/widgets", circleci.CheckoutKeyDigestSHA256)
	if err != nil {
		t.Fatalf("ListCheckoutKeys returned error: %v", err)
	}

	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2 (both pages)", len(keys))
	}
	if keys[0].Fingerprint != "aa" || keys[1].Fingerprint != "bb" {
		t.Errorf("fingerprints = %q, %q, want \"aa\", \"bb\" in page order", keys[0].Fingerprint, keys[1].Fingerprint)
	}

	mu.Lock()
	defer mu.Unlock()

	want := []string{
		"/api/v2/project/gh/acme/widgets/checkout-key?digest=sha256",
		"/api/v2/project/gh/acme/widgets/checkout-key?digest=sha256&page-token=tok-2",
	}
	if len(uris) != len(want) {
		t.Fatalf("requests = %q, want %d requests", uris, len(want))
	}
	for i, got := range uris {
		if got != want[i] {
			t.Errorf("request %d URI = %q, want %q", i, got, want[i])
		}
	}
}

func TestListCheckoutKeysOmitsEmptyDigest(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, `{"items":[],"next_page_token":null}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if _, err := c.ListCheckoutKeys(context.Background(), "gh/acme/widgets", ""); err != nil {
		t.Fatalf("ListCheckoutKeys returned error: %v", err)
	}

	if want := "/api/v2/project/gh/acme/widgets/checkout-key"; rec.requestURI != want {
		t.Errorf("raw request URI = %q, want %q so the API default digest applies", rec.requestURI, want)
	}
}

func TestCheckoutKeyRejectsMalformedProjectSlug(t *testing.T) {
	t.Parallel()

	// Requests must not be sent at all for a slug that cannot address a project:
	// the resulting 404 would be indistinguishable from a missing key.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the client made a request for a malformed project slug")
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	for _, slug := range []string{"", "gh/acme", "gh//widgets", "gh/acme/widgets/extra"} {
		t.Run("slug="+slug, func(t *testing.T) {
			ctx := context.Background()

			if _, err := c.GetCheckoutKey(ctx, slug, "aa:bb"); err == nil {
				t.Errorf("GetCheckoutKey(%q) returned no error", slug)
			}
			if _, err := c.ListCheckoutKeys(ctx, slug, ""); err == nil {
				t.Errorf("ListCheckoutKeys(%q) returned no error", slug)
			}
			if _, err := c.CreateCheckoutKey(ctx, slug, circleci.CheckoutKeyTypeDeployKey); err == nil {
				t.Errorf("CreateCheckoutKey(%q) returned no error", slug)
			}
			if err := c.DeleteCheckoutKey(ctx, slug, "aa:bb"); err == nil {
				t.Errorf("DeleteCheckoutKey(%q) returned no error", slug)
			}
		})
	}
}

// TestCheckoutKeyProjectPathRejectsDotSegments is the regression test for
// checkoutKeyProjectPath's dot-segment defence in depth: a slug segment that is
// exactly "." or ".." survives url.PathEscape unchanged (it doesn't touch dots)
// and url.Parse doesn't clean dot-segments out of a path either, so either one
// would put a literal "./" or "../" into the outbound request path and
// retarget it at a different route. This is defence in depth, not a fix for a
// reachable bug: a slug comes from Terraform configuration or from CircleCI's
// own API responses, never from a third party.
//
// It also checks the legitimate cases still parse, including a segment that
// merely contains a dot (a repository named "my.repo"), which must remain
// valid — rejecting that would break real configurations.
func TestCheckoutKeyProjectPathRejectsDotSegments(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		reqURIs []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqURIs = append(reqURIs, r.RequestURI)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, checkoutKeyBody)
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	tests := []struct {
		name    string
		slug    string
		wantErr bool
	}{
		{name: "dot in first segment", slug: "./acme/widgets", wantErr: true},
		{name: "dot-dot in first segment", slug: "../acme/widgets", wantErr: true},
		{name: "dot in middle segment", slug: "gh/./widgets", wantErr: true},
		{name: "dot-dot in middle segment", slug: "gh/../widgets", wantErr: true},
		{name: "dot in last segment", slug: "gh/acme/.", wantErr: true},
		{name: "dot-dot in last segment", slug: "gh/acme/..", wantErr: true},
		{name: "ordinary slug", slug: "gh/acme/widgets", wantErr: false},
		{name: "segment merely containing a dot", slug: "gh/acme/my.repo", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.GetCheckoutKey(context.Background(), tt.slug, "aa:bb")
			if tt.wantErr && err == nil {
				t.Errorf("GetCheckoutKey(%q) returned no error, want one", tt.slug)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("GetCheckoutKey(%q) returned error: %v, want none", tt.slug, err)
			}

			err = c.DeleteCheckoutKey(context.Background(), tt.slug, "aa:bb")
			if tt.wantErr && err == nil {
				t.Errorf("DeleteCheckoutKey(%q) returned no error, want one", tt.slug)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("DeleteCheckoutKey(%q) returned error: %v, want none", tt.slug, err)
			}
		})
	}

	// Sanity check the legitimate requests actually reached the server, so a
	// mistake that also broke the happy path wouldn't be masked by an
	// error-only assertion.
	mu.Lock()
	defer mu.Unlock()
	if len(reqURIs) != 4 {
		t.Fatalf("made %d requests, want 4 (get+delete for each of the two legitimate slugs): %v", len(reqURIs), reqURIs)
	}
}

// TestCheckoutKeyDecodesUnderscoreFieldNames pins the JSON field names against
// the real API.
//
// The struct tags were originally `public-key` and `created-at`, so PublicKey and
// CreatedAt were silently empty against production for every read, list and
// create. The bug was invisible because the mocks used the same hyphenated keys.
// The API force-converts its kebab-case keywords to snake_case before
// responding, so the wire format is `public_key` and `created_at`.
//
// This fixture is written in the production shape deliberately: if the tags
// regress, this test fails even if every mock is changed to match.
func TestCheckoutKeyDecodesUnderscoreFieldNames(t *testing.T) {
	t.Parallel()

	const body = `{
		"type": "deploy-key",
		"public_key": "ssh-rsa AAAAB3Nza",
		"fingerprint": "c9:0b:1c:4f",
		"preferred": true,
		"created_at": "2026-01-02T03:04:05.000Z"
	}`

	srv, _ := newRecordingServer(t, 200, body)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	key, err := c.GetCheckoutKey(context.Background(), "gh/acme/repo", "c9:0b:1c:4f")
	if err != nil {
		t.Fatalf("GetCheckoutKey returned error: %v", err)
	}

	if key.PublicKey != "ssh-rsa AAAAB3Nza" {
		t.Errorf("PublicKey = %q, want the value from the public_key field", key.PublicKey)
	}
	if key.CreatedAt != "2026-01-02T03:04:05.000Z" {
		t.Errorf("CreatedAt = %q, want the value from the created_at field", key.CreatedAt)
	}
	if key.Fingerprint != "c9:0b:1c:4f" {
		t.Errorf("Fingerprint = %q, want %q", key.Fingerprint, "c9:0b:1c:4f")
	}
	if !key.Preferred {
		t.Error("Preferred = false, want true")
	}
}

// TestCheckoutKeyInputTypeHandlesAnyVCS covers the type the API echoes back.
//
// The API rewrites a requested `user-key` to "<vcs-type>-user-key", so a
// Bitbucket project answers `bitbucket-user-key`, not `github-user-key`. Mapping
// only the GitHub form meant the type round-tripped wrong on Bitbucket, producing
// a permanent diff.
func TestCheckoutKeyInputTypeHandlesAnyVCS(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"github-user-key":    circleci.CheckoutKeyTypeUserKey,
		"bitbucket-user-key": circleci.CheckoutKeyTypeUserKey,
		"gitlab-user-key":    circleci.CheckoutKeyTypeUserKey,
		"user-key":           circleci.CheckoutKeyTypeUserKey,
		"deploy-key":         circleci.CheckoutKeyTypeDeployKey,
	}

	for reported, want := range tests {
		key := circleci.CheckoutKey{Type: reported}
		if got := key.InputType(); got != want {
			t.Errorf("CheckoutKey{Type: %q}.InputType() = %q, want %q", reported, got, want)
		}
	}
}
