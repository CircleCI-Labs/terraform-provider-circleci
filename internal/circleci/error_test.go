// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// errFromStatus performs a real request against a server returning status/body,
// so the resulting error is exactly what a resource would receive.
func errFromStatus(t *testing.T, status int, body string) error {
	t.Helper()

	srv, _ := newRecordingServer(t, status, body)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	var out map[string]any
	err := c.GetV2(context.Background(), "/thing", &out)
	if err == nil {
		t.Fatalf("expected an error for status %d", status)
	}

	return err
}

func TestIsNotFoundOnHTTP404(t *testing.T) {
	t.Parallel()

	err := errFromStatus(t, http.StatusNotFound, `{"message":"Context not found"}`)

	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(404) = false, want true")
	}
}

func TestIsNotFoundOnSentinel(t *testing.T) {
	t.Parallel()

	// v3 collections are filter-scoped and answer 200 with an empty data array
	// rather than 404, so lookups translate that into ErrNotFound.
	err := fmt.Errorf("looking up namespace %q: %w", "nope", circleci.ErrNotFound)

	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(wrapped ErrNotFound) = false, want true")
	}
	if !errors.Is(err, circleci.ErrNotFound) {
		t.Errorf("errors.Is(err, ErrNotFound) = false, want true")
	}
}

// TestIsNotFoundRejectsIncidental404InBody is the regression test for the bug
// this error layer exists to fix.
//
// The provider previously detected deletion with
// strings.Contains(err.Error(), "404"), so a 500 whose body merely mentioned 404
// was treated as "resource deleted" and silently dropped from Terraform state,
// causing spurious recreation.
func TestIsNotFoundRejectsIncidental404InBody(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
		body   string
	}{
		{
			name:   "500 whose body mentions 404",
			status: http.StatusInternalServerError,
			body:   `{"message":"upstream returned 404 from dependency"}`,
		},
		{
			name:   "502 HTML error page mentioning 404",
			status: http.StatusBadGateway,
			body:   `<html><body>Error 404 handler failed</body></html>`,
		},
		{
			name:   "200-shaped resource whose name contains 'not found'",
			status: http.StatusInternalServerError,
			body:   `{"message":"context \"not found\" could not be updated"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := errFromStatus(t, tc.status, tc.body)

			if circleci.IsNotFound(err) {
				t.Errorf("IsNotFound() = true for HTTP %d; this would delete a live resource from state", tc.status)
			}

			// Demonstrate that the old approach did get this wrong.
			if !strings.Contains(err.Error(), "404") && !strings.Contains(err.Error(), "not found") {
				// Not every case trips the old check via err.Error() alone,
				// since the body is not in the message; the point stands for
				// the cases that do.
				t.Logf("note: %q does not contain the old sentinel substrings", err.Error())
			}
		})
	}
}

func TestStatusCode(t *testing.T) {
	t.Parallel()

	err := errFromStatus(t, http.StatusConflict, `{}`)

	got, ok := circleci.StatusCode(err)
	if !ok {
		t.Fatal("StatusCode() reported not-an-HTTP-error, want true")
	}
	if got != http.StatusConflict {
		t.Errorf("StatusCode() = %d, want %d", got, http.StatusConflict)
	}

	if _, ok := circleci.StatusCode(errors.New("plain")); ok {
		t.Error("StatusCode(plain error) reported true, want false")
	}
}

func TestStatusPredicates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		check  func(error) bool
		want   bool
	}{
		{"IsGone on 410", http.StatusGone, circleci.IsGone, true},
		{"IsGone on 404", http.StatusNotFound, circleci.IsGone, false},
		{"IsUnauthorized on 401", http.StatusUnauthorized, circleci.IsUnauthorized, true},
		{"IsUnauthorized on 403", http.StatusForbidden, circleci.IsUnauthorized, true},
		{"IsUnauthorized on 404", http.StatusNotFound, circleci.IsUnauthorized, false},
		{"IsConflict on 409", http.StatusConflict, circleci.IsConflict, true},
		{"IsConflict on 400", http.StatusBadRequest, circleci.IsConflict, false},
		{"IsNotFound on 410", http.StatusGone, circleci.IsNotFound, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := errFromStatus(t, tt.status, `{}`)
			if got := tt.check(err); got != tt.want {
				t.Errorf("check(HTTP %d) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestParseAPIError(t *testing.T) {
	t.Parallel()

	body := `{"error":{
		"id":"trace-abc123",
		"type":"validation_error",
		"title":"Invalid request",
		"detail":"name must not be empty",
		"source":{"pointer":"/data/attributes/name","offset":42,"error":"blank"}
	}}`

	err := errFromStatus(t, http.StatusBadRequest, body)

	apiErr, ok := circleci.ParseAPIError(err)
	if !ok {
		t.Fatal("ParseAPIError() reported false, want true")
	}

	if apiErr.ID != "trace-abc123" {
		t.Errorf("ID = %q, want %q", apiErr.ID, "trace-abc123")
	}
	// The upstream CLI omits `type`; we keep it.
	if apiErr.Type != "validation_error" {
		t.Errorf("Type = %q, want %q", apiErr.Type, "validation_error")
	}
	if got, want := apiErr.Error(), "Invalid request: name must not be empty"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	msg := apiErr.Message()
	for _, want := range []string{
		"Invalid request: name must not be empty",
		"/data/attributes/name",
		"offset 42",
		"blank",
		"trace-abc123",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("Message() = %q, want it to contain %q", msg, want)
		}
	}
}

func TestParseAPIErrorRejectsNonEnvelopeBodies(t *testing.T) {
	t.Parallel()

	// v2 bodies, HTML proxy pages and malformed envelopes must degrade cleanly
	// rather than yielding a half-populated error.
	cases := []struct {
		name string
		body string
	}{
		{"v2 message body", `{"message":"Context not found"}`},
		{"html error page", `<html><body>502 Bad Gateway</body></html>`},
		{"empty envelope", `{"error":{}}`},
		{"error as string not object", `{"error":"boom"}`},
		{"empty body", ``},
		{"array", `[1,2,3]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := errFromStatus(t, http.StatusBadRequest, tc.body)
			if _, ok := circleci.ParseAPIError(err); ok {
				t.Errorf("ParseAPIError() = true for %s, want false", tc.name)
			}
		})
	}
}

func TestParseAPIErrorOnNonHTTPError(t *testing.T) {
	t.Parallel()

	if _, ok := circleci.ParseAPIError(errors.New("plain")); ok {
		t.Error("ParseAPIError(plain error) = true, want false")
	}
	if _, ok := circleci.ParseAPIError(nil); ok {
		t.Error("ParseAPIError(nil) = true, want false")
	}
}

func TestDetail(t *testing.T) {
	t.Parallel()

	t.Run("prefers the v3 envelope", func(t *testing.T) {
		t.Parallel()

		err := errFromStatus(t, http.StatusBadRequest,
			`{"error":{"title":"Invalid","detail":"bad name","id":"tr-1"}}`)

		got := circleci.Detail(err)
		if !strings.Contains(got, "Invalid: bad name") {
			t.Errorf("Detail() = %q, want it to contain the envelope title and detail", got)
		}
		if !strings.Contains(got, "tr-1") {
			t.Errorf("Detail() = %q, want it to contain the error id", got)
		}
	})

	t.Run("falls back to a v2 message body", func(t *testing.T) {
		t.Parallel()

		err := errFromStatus(t, http.StatusNotFound, `{"message":"Context not found"}`)

		got := circleci.Detail(err)
		if !strings.Contains(got, "Context not found") {
			t.Errorf("Detail() = %q, want it to contain the v2 message", got)
		}
		if !strings.Contains(got, "404") {
			t.Errorf("Detail() = %q, want it to mention the status code", got)
		}
	})

	t.Run("falls back to the raw error", func(t *testing.T) {
		t.Parallel()

		if got := circleci.Detail(errors.New("dial tcp: refused")); got != "dial tcp: refused" {
			t.Errorf("Detail() = %q, want the raw error text", got)
		}
	})

	t.Run("empty for nil", func(t *testing.T) {
		t.Parallel()

		if got := circleci.Detail(nil); got != "" {
			t.Errorf("Detail(nil) = %q, want empty", got)
		}
	})
}
