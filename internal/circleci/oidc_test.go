// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"terraform-provider-circleci/internal/circleci"
)

const oidcClaimsBody = `{
  "org_id": "11111111-1111-1111-1111-111111111111",
  "project_id": "22222222-2222-2222-2222-222222222222",
  "audience": ["https://sts.amazonaws.com", "my-audience"],
  "audience_updated_at": "2024-01-02T03:04:05Z",
  "ttl": "1h30m",
  "ttl_updated_at": "2024-01-02T03:04:06Z"
}`

func TestGetOIDCCustomClaimsOrgRoute(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, oidcClaimsBody)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	claims, err := c.GetOIDCCustomClaims(context.Background(), "11111111-1111-1111-1111-111111111111", "")
	if err != nil {
		t.Fatalf("GetOIDCCustomClaims returned error: %v", err)
	}

	wantURI := "/api/v2/org/11111111-1111-1111-1111-111111111111/oidc-custom-claims"
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", rec.requestURI, wantURI)
	}

	if got, want := len(claims.Audience), 2; got != want {
		t.Fatalf("len(Audience) = %d, want %d", got, want)
	}
	if claims.Audience[0] != "https://sts.amazonaws.com" {
		t.Errorf("Audience[0] = %q, want %q", claims.Audience[0], "https://sts.amazonaws.com")
	}
	if claims.TTL != "1h30m" {
		t.Errorf("TTL = %q, want %q", claims.TTL, "1h30m")
	}
	if claims.TTLUpdatedAt != "2024-01-02T03:04:06Z" {
		t.Errorf("TTLUpdatedAt = %q, want the timestamp from the body", claims.TTLUpdatedAt)
	}
	if claims.IsZero() {
		t.Error("IsZero() = true for a response with both claims set")
	}
}

func TestGetOIDCCustomClaimsProjectRoute(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, oidcClaimsBody)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetOIDCCustomClaims(context.Background(),
		"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")
	if err != nil {
		t.Fatalf("GetOIDCCustomClaims returned error: %v", err)
	}

	wantURI := "/api/v2/org/11111111-1111-1111-1111-111111111111" +
		"/project/22222222-2222-2222-2222-222222222222/oidc-custom-claims"
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", rec.requestURI, wantURI)
	}
}

// TestOIDCCustomClaimsIsZero covers the drift signal: a scope with no
// customization answers 200, not 404, so IsNotFound cannot detect a reset.
func TestOIDCCustomClaimsIsZero(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusOK,
		`{"org_id":"11111111-1111-1111-1111-111111111111"}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	claims, err := c.GetOIDCCustomClaims(context.Background(), "11111111-1111-1111-1111-111111111111", "")
	if err != nil {
		t.Fatalf("GetOIDCCustomClaims returned error: %v", err)
	}

	if !claims.IsZero() {
		t.Error("IsZero() = false for a response carrying only org_id")
	}
}

func TestUpdateOIDCCustomClaimsPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		update   circleci.OIDCCustomClaimsUpdate
		wantBody map[string]any
	}{
		{
			name: "both claims",
			update: circleci.OIDCCustomClaimsUpdate{
				Audience: ptr([]string{"a", "b"}),
				TTL:      ptr("1h"),
			},
			wantBody: map[string]any{"audience": []any{"a", "b"}, "ttl": "1h"},
		},
		{
			name:     "ttl only leaves audience untouched",
			update:   circleci.OIDCCustomClaimsUpdate{TTL: ptr("30m")},
			wantBody: map[string]any{"ttl": "30m"},
		},
		{
			// An explicitly empty audience must reach the wire as [], not be
			// dropped: dropping it would mean "leave the audience alone".
			name:     "empty audience is sent",
			update:   circleci.OIDCCustomClaimsUpdate{Audience: ptr([]string{})},
			wantBody: map[string]any{"audience": []any{}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var (
				gotMethod string
				gotBody   map[string]any
			)

			srv := newGovernanceServer(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				_, _ = w.Write([]byte(oidcClaimsBody))
			})
			c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

			_, err := c.UpdateOIDCCustomClaims(context.Background(),
				"11111111-1111-1111-1111-111111111111", "", tc.update)
			if err != nil {
				t.Fatalf("UpdateOIDCCustomClaims returned error: %v", err)
			}

			if gotMethod != http.MethodPatch {
				t.Errorf("method = %q, want PATCH", gotMethod)
			}
			if !governanceJSONEqual(gotBody, tc.wantBody) {
				t.Errorf("request body = %#v, want %#v", gotBody, tc.wantBody)
			}
		})
	}
}

// TestDeleteOIDCCustomClaimsRequiresClaimsQuery pins the query parameter the
// API requires and the provider's per-claim delete: the route cannot delete
// "everything", it deletes exactly the claims it is told to.
func TestDeleteOIDCCustomClaimsRequiresClaimsQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		claims []string
		want   string
	}{
		{
			name:   "both claims",
			claims: []string{circleci.OIDCClaimAudience, circleci.OIDCClaimTTL},
			want: "/api/v2/org/11111111-1111-1111-1111-111111111111/oidc-custom-claims" +
				"?claims=audience%2Cttl",
		},
		{
			name:   "ttl only",
			claims: []string{circleci.OIDCClaimTTL},
			want: "/api/v2/org/11111111-1111-1111-1111-111111111111/oidc-custom-claims" +
				"?claims=ttl",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv, rec := newRecordingServer(t, http.StatusOK,
				`{"org_id":"11111111-1111-1111-1111-111111111111"}`)
			c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

			remaining, err := c.DeleteOIDCCustomClaims(context.Background(),
				"11111111-1111-1111-1111-111111111111", "", tc.claims)
			if err != nil {
				t.Fatalf("DeleteOIDCCustomClaims returned error: %v", err)
			}

			if rec.requestURI != tc.want {
				t.Errorf("raw request URI = %q, want %q", rec.requestURI, tc.want)
			}
			// The response body is not discarded: DELETE answers with whatever
			// customization survives.
			if remaining == nil || !remaining.IsZero() {
				t.Errorf("remaining = %#v, want a zero-value customization", remaining)
			}
		})
	}
}

func TestOIDCTTLPattern(t *testing.T) {
	t.Parallel()

	valid := []string{"1h", "30m", "1h30m", "500ms", "10s", "1.5h", "1us", "100ns", "1h30m10s"}
	for _, ttl := range valid {
		if !circleci.OIDCTTLPattern.MatchString(ttl) {
			t.Errorf("OIDCTTLPattern rejected %q, want it accepted", ttl)
		}
	}

	invalid := []string{"", "1", "h", "1 h", "-1h", "+1h", "1y", ".5h", "1.h"}
	for _, ttl := range invalid {
		if circleci.OIDCTTLPattern.MatchString(ttl) {
			t.Errorf("OIDCTTLPattern accepted %q, want it rejected", ttl)
		}
	}
}

// TestOIDCTTLPatternRejectsDaysAndWeeks is separate from the table above because
// it is the bug, not a case.
//
// The published OpenAPI document for these routes gives the ttl schema
// `pattern: ^([0-9]+(ms|s|m|h|d|w)){1,7}$`, so "d" and "w" look supported and a
// validator written from the specification accepts them. Nothing enforces that
// pattern: the API accepts exactly the durations Go's time.ParseDuration accepts,
// which has no unit longer than an hour, so "7d" fails with 400 once the apply is
// already running. Anything that reintroduces those units from the specification has to
// fail here.
func TestOIDCTTLPatternRejectsDaysAndWeeks(t *testing.T) {
	t.Parallel()

	for _, ttl := range []string{"1d", "1w", "7d", "1d12h", "2w3d"} {
		if circleci.OIDCTTLPattern.MatchString(ttl) {
			t.Errorf("OIDCTTLPattern accepted %q; the API rejects it with 400 because "+
				"time.ParseDuration has no d or w unit, so accepting it at plan time only "+
				"moves the failure into the apply", ttl)
		}
	}

	// The same claim, stated against the parser the API actually uses, so this
	// test still means something if the pattern is replaced by another mechanism.
	for _, ttl := range []string{"1d", "1w"} {
		if _, err := time.ParseDuration(ttl); err == nil {
			t.Errorf("time.ParseDuration(%q) succeeded; this test's premise no longer holds", ttl)
		}
	}

	// And every value the pattern does accept must be parseable, since the API
	// hands it straight to ParseDuration.
	for _, ttl := range []string{"1h", "90m", "1h30m", "500ms", "1.5h", "1us", "100ns"} {
		if !circleci.OIDCTTLPattern.MatchString(ttl) {
			t.Fatalf("OIDCTTLPattern rejected %q, which the API accepts", ttl)
		}
		if _, err := time.ParseDuration(ttl); err != nil {
			t.Errorf("time.ParseDuration(%q) failed (%v), so the pattern is looser than the API", ttl, err)
		}
	}
}
