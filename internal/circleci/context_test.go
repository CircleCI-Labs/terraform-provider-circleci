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

const testContextOrgID = "1d1b2f5a-6c8d-4a3e-9f0b-2c4d6e8a0b1c"

// listRequest is one request as a list-endpoint mock server saw it.
type listRequest struct {
	method string
	path   string
	rawURI string
	query  string
}

// newListServer serves handler and records every request, so tests can assert on
// the exact path and query parameters sent.
func newListServer(t *testing.T, handler http.HandlerFunc) (*circleci.Client, *[]listRequest) {
	t.Helper()

	var seen []listRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, listRequest{
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

// writeListJSON is the mock-server helper for a 200 JSON body.
func writeListJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// writeListError answers with a v2 error body, which is always {"message": ...}.
func writeListError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"message":"` + message + `"}`))
}

// pageListServer serves the given bodies in order, repeating the last one, and
// records the requests.
func pageListServer(t *testing.T, pages ...string) (*circleci.Client, *[]listRequest) {
	t.Helper()

	var calls int

	return newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		body := pages[min(calls, len(pages)-1)]
		calls++
		writeListJSON(w, body)
	})
}

func TestListContexts(t *testing.T) {
	t.Parallel()

	// The shape mirrors the API's the CircleCI API: items of
	// {name, id, created_at} with a *string next_page_token, so the last page
	// carries the key with an explicit null.
	client, seen := pageListServer(t,
		`{"items":[{"name":"build","id":"c1","created_at":"2024-01-18T02:16:55Z"}],"next_page_token":"tok-2"}`,
		`{"items":[{"name":"deploy","id":"c2","created_at":"2024-02-18T02:16:55Z"}],"next_page_token":null}`,
	)

	contexts, err := client.ListContexts(context.Background(), testContextOrgID)
	if err != nil {
		t.Fatalf("ListContexts returned error: %v", err)
	}

	if len(contexts) != 2 {
		t.Fatalf("context count = %d, want 2 (both pages drained)", len(contexts))
	}
	if contexts[0].ID != "c1" || contexts[0].Name != "build" {
		t.Errorf("first context = %+v, want id c1 named build", contexts[0])
	}
	if contexts[0].CreatedAt != "2024-01-18T02:16:55Z" {
		t.Errorf("first context created_at = %q, want the timestamp verbatim", contexts[0].CreatedAt)
	}
	if contexts[1].ID != "c2" {
		t.Errorf("second context id = %q, want c2", contexts[1].ID)
	}

	if len(*seen) != 2 {
		t.Fatalf("request count = %d, want 2", len(*seen))
	}

	// The owner is a query parameter, not a path segment, and owner-type is
	// always "organization" because the handler rejects anything else.
	wantPath := "/api/v2/context"
	wantFirstQuery := "owner-id=" + testContextOrgID + "&owner-type=organization"
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath || got.query != wantFirstQuery {
		t.Errorf("first request = %s %s?%s, want GET %s?%s", got.method, got.path, got.query, wantPath, wantFirstQuery)
	}

	// The first page must not send page-token at all; the second must send the
	// token the first page returned.
	wantSecondQuery := wantFirstQuery + "&page-token=tok-2"
	if got := (*seen)[1]; got.path != wantPath || got.query != wantSecondQuery {
		t.Errorf("second request = %s?%s, want %s?%s", got.path, got.query, wantPath, wantSecondQuery)
	}
}

func TestListContextsEmpty(t *testing.T) {
	t.Parallel()

	client, seen := pageListServer(t, `{"items":[],"next_page_token":null}`)

	contexts, err := client.ListContexts(context.Background(), testContextOrgID)
	if err != nil {
		t.Fatalf("ListContexts returned error: %v", err)
	}
	if len(contexts) != 0 {
		t.Errorf("context count = %d, want 0", len(contexts))
	}
	if len(*seen) != 1 {
		t.Errorf("request count = %d, want 1", len(*seen))
	}
}

func TestListContextsStopsWhenTokenRepeats(t *testing.T) {
	t.Parallel()

	// A server that always echoes a token must not spin forever.
	client, seen := pageListServer(t, `{"items":[],"next_page_token":"same"}`)

	if _, err := client.ListContexts(context.Background(), testContextOrgID); err != nil {
		t.Fatalf("ListContexts returned error: %v", err)
	}
	if len(*seen) != 1 {
		t.Errorf("request count = %d, want 1", len(*seen))
	}
}

func TestListContextsError(t *testing.T) {
	t.Parallel()

	// v2 errors are always {"message": "..."} and the detail must reach the
	// diagnostic rather than being flattened to a status code.
	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListError(w, http.StatusForbidden, "Permission denied.")
	})

	_, err := client.ListContexts(context.Background(), testContextOrgID)
	if err == nil {
		t.Fatal("ListContexts returned no error for a 403, want one")
	}
	if detail := circleci.Detail(err); detail == "" {
		t.Error("Detail() = \"\", want the server message")
	}
}

func TestListContextsEscapesQuery(t *testing.T) {
	t.Parallel()

	// The organization id arrives from configuration, so anything
	// query-significant in it must be escaped rather than adding a parameter.
	client, seen := pageListServer(t, `{"items":[],"next_page_token":null}`)

	if _, err := client.ListContexts(context.Background(), "org&owner-type=account"); err != nil {
		t.Fatalf("ListContexts returned error: %v", err)
	}

	wantURI := "/api/v2/context?owner-id=org%26owner-type%3Daccount&owner-type=organization"
	if got := (*seen)[0].rawURI; got != wantURI {
		t.Errorf("raw request URI = %q, want %q", got, wantURI)
	}
}
