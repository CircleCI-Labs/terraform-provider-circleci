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

const testEnvVarProjectSlug = "circleci/AbCdEfG/HiJkLmN"

func TestListProjectEnvironmentVariables(t *testing.T) {
	t.Parallel()

	// The shape matches what the API actually returns: name, masked value, and
	// a created_at that is null for variables predating the timestamp.
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

// --- context environment variables ---

const testEnvVarContextID = "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"

func TestListContextEnvironmentVariables(t *testing.T) {
	t.Parallel()

	// The shape matches what the API actually returns:
	// {"items": [...], "next_page_token": ...} with truncated_value rather than
	// value, because the API never discloses the configured value.
	// truncated_value carries the tail of the value with NO mask prefix — the
	// API takes the last four characters, or the last floor(len/2) for a value
	// of eight characters or fewer. This fixture used to say "xxxx3cr3",
	// borrowing the *project* environment variable convention, which does
	// prefix the tail with "xxxx". The two are different shapes from different
	// routes, and a fixture in the wrong one is not evidence.
	client, seen := pageListServer(t,
		`{"items":[{"variable":"API_KEY","context_id":"`+testEnvVarContextID+`","truncated_value":"3cr3","created_at":"2024-01-02T03:04:05.000Z","updated_at":"2024-01-02T03:04:05.000Z"}],"next_page_token":"tok-2"}`,
		`{"items":[{"variable":"ZONE","context_id":"`+testEnvVarContextID+`","truncated_value":"st-1","created_at":"2024-02-01T00:00:00.000Z","updated_at":"2024-02-01T00:00:00.000Z"}],"next_page_token":null}`,
	)

	vars, err := client.ListContextEnvironmentVariables(context.Background(), testEnvVarContextID)
	if err != nil {
		t.Fatalf("ListContextEnvironmentVariables returned error: %v", err)
	}

	if len(vars) != 2 {
		t.Fatalf("variable count = %d, want 2 (both pages drained)", len(vars))
	}
	if vars[0].Variable != "API_KEY" || vars[0].TruncatedValue != "3cr3" {
		t.Errorf("first variable = %+v, want API_KEY truncated as 3cr3", vars[0])
	}
	if vars[0].ContextID != testEnvVarContextID {
		t.Errorf("first variable context_id = %q, want %q", vars[0].ContextID, testEnvVarContextID)
	}

	if len(*seen) != 2 {
		t.Fatalf("request count = %d, want 2", len(*seen))
	}
	wantPath := "/api/v2/context/" + testEnvVarContextID + "/environment-variable"
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath {
		t.Errorf("first request = %s %s, want GET %s", got.method, got.path, wantPath)
	}
	if got := (*seen)[1].query; got != "page-token=tok-2" {
		t.Errorf("second request query = %q, want %q", got, "page-token=tok-2")
	}
}

// TestListContextEnvironmentVariablesRepeatedPageTokenErrors covers the one
// failure mode worse than a wrong answer: a drain that never terminates.
//
// This route's pagination is broken server-side. Its handler reads the page token
// from the request's *path* parameters rather than its query string, and the route
// declares no such path parameter, so the page-token this client sends is never
// seen: every request is answered with the first page, and with the same
// next_page_token. A context holding more than one page of variables (the page
// size is fixed at 100 by the service, not by this client) therefore made the
// naive drain re-request page one for ever, accumulating duplicates until the
// provider ran out of memory.
//
// No fake in this repository could have caught that, because every one of them
// answers with next_page_token null and so terminates on the first page whatever
// the client does. The server below is the shape the real route has.
//
// The repeated token is a hard error rather than a quiet stop on purpose. The
// remaining variables cannot be fetched through this route at all, and silently
// returning the first page would make a data source under-report and could make a
// resource conclude one of its variables had been deleted.
func TestListContextEnvironmentVariablesRepeatedPageTokenErrors(t *testing.T) {
	t.Parallel()

	// Always the same page, always the same token, whatever page-token is sent.
	client, seen := pageListServer(t,
		`{"items":[{"variable":"API_KEY","context_id":"`+testEnvVarContextID+`","truncated_value":"3cr3",`+
			`"created_at":"2024-01-02T03:04:05.000Z","updated_at":"2024-01-02T03:04:05.000Z"}],`+
			`"next_page_token":"same-token"}`,
	)

	vars, err := client.ListContextEnvironmentVariables(context.Background(), testEnvVarContextID)
	if err == nil {
		t.Fatalf("ListContextEnvironmentVariables returned %d variables and no error against a route that "+
			"never advances its page token; a caller has no way to know the list is incomplete", len(vars))
	}
	if vars != nil {
		t.Errorf("a failed drain returned %d variables; it must return none rather than a partial list "+
			"a caller might mistake for the whole collection", len(vars))
	}
	for _, want := range []string{"page token", "incomplete"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q, so it does not say why the list is short", err, want)
		}
	}

	// Exactly two requests: the first page, then the one that proves the token is
	// ignored. Anything more means the guard fires late, and a guard that fires
	// late on a real API is a guard that never fires at all.
	if len(*seen) != 2 {
		t.Errorf("made %d requests, want 2 — the loop must stop as soon as the token repeats", len(*seen))
	}
}

func TestListContextEnvironmentVariablesEmpty(t *testing.T) {
	t.Parallel()

	client, _ := pageListServer(t, `{"items":[],"next_page_token":null}`)

	vars, err := client.ListContextEnvironmentVariables(context.Background(), testEnvVarContextID)
	if err != nil {
		t.Fatalf("ListContextEnvironmentVariables returned error: %v", err)
	}
	if len(vars) != 0 {
		t.Errorf("variable count = %d, want 0", len(vars))
	}
}

func TestUpsertContextEnvironmentVariable(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	client, seen := newListServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}

		// The PUT response carries no value or truncated_value at all.
		writeListJSON(w, `{"variable":"API_KEY","context_id":"`+testEnvVarContextID+`",`+
			`"created_at":"2024-01-02T03:04:05.000Z","updated_at":"2024-06-01T00:00:00.000Z"}`)
	})

	got, err := client.UpsertContextEnvironmentVariable(context.Background(), testEnvVarContextID, "API_KEY", "s3cr3t")
	if err != nil {
		t.Fatalf("UpsertContextEnvironmentVariable returned error: %v", err)
	}
	if got.Variable != "API_KEY" || got.ContextID != testEnvVarContextID {
		t.Errorf("UpsertContextEnvironmentVariable = %+v, want variable API_KEY on context %q", got, testEnvVarContextID)
	}
	if got.TruncatedValue != "" {
		t.Errorf("TruncatedValue = %q, want \"\": the PUT response never carries one", got.TruncatedValue)
	}
	if got.UpdatedAt != "2024-06-01T00:00:00.000Z" {
		t.Errorf("UpdatedAt = %q, want the timestamp verbatim", got.UpdatedAt)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	wantPath := "/api/v2/context/" + testEnvVarContextID + "/environment-variable/API_KEY"
	if got := (*seen)[0]; got.method != http.MethodPut || got.path != wantPath {
		t.Errorf("request = %s %s, want PUT %s", got.method, got.path, wantPath)
	}
	if gotBody["value"] != "s3cr3t" {
		t.Errorf("request body value = %v, want %q", gotBody["value"], "s3cr3t")
	}
	// The request must carry nothing but the value: no name (it is in the
	// path) and certainly no echo of an existing masked value.
	if len(gotBody) != 1 {
		t.Errorf("request body = %+v, want only {\"value\": ...}", gotBody)
	}
}

func TestUpsertContextEnvironmentVariableEscapesName(t *testing.T) {
	t.Parallel()

	// The variable name is a path segment, so anything path-significant in it
	// must be escaped rather than corrupting the route.
	client, seen := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{"variable":"weird","context_id":"`+testEnvVarContextID+`","created_at":"","updated_at":""}`)
	})

	if _, err := client.UpsertContextEnvironmentVariable(context.Background(), testEnvVarContextID, "MY VAR/WEIRD", "v"); err != nil {
		t.Fatalf("UpsertContextEnvironmentVariable returned error: %v", err)
	}

	wantURI := "/api/v2/context/" + testEnvVarContextID + "/environment-variable/MY%20VAR%2FWEIRD"
	if got := (*seen)[0].rawURI; got != wantURI {
		t.Errorf("raw request URI = %q, want %q", got, wantURI)
	}
}

func TestDeleteContextEnvironmentVariable(t *testing.T) {
	t.Parallel()

	client, seen := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{"message":"Environment variable deleted."}`)
	})

	if err := client.DeleteContextEnvironmentVariable(context.Background(), testEnvVarContextID, "API_KEY"); err != nil {
		t.Fatalf("DeleteContextEnvironmentVariable returned error: %v", err)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	wantPath := "/api/v2/context/" + testEnvVarContextID + "/environment-variable/API_KEY"
	if got := (*seen)[0]; got.method != http.MethodDelete || got.path != wantPath {
		t.Errorf("request = %s %s, want DELETE %s", got.method, got.path, wantPath)
	}
}

// TestContextEnvironmentVariableMissingContextAnswers403 documents the same
// anti-enumeration behavior as context_test.go's
// TestDeleteContextMissingAnswers403, for both mutating routes.
func TestContextEnvironmentVariableMissingContextAnswers403(t *testing.T) {
	t.Parallel()

	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListError(w, http.StatusForbidden, "Forbidden")
	})

	if _, err := client.UpsertContextEnvironmentVariable(context.Background(), "does-not-exist", "API_KEY", "v"); circleci.IsNotFound(err) || !circleci.IsUnauthorized(err) {
		t.Errorf("UpsertContextEnvironmentVariable error = %v, want IsUnauthorized and not IsNotFound", err)
	}

	if err := client.DeleteContextEnvironmentVariable(context.Background(), "does-not-exist", "API_KEY"); circleci.IsNotFound(err) || !circleci.IsUnauthorized(err) {
		t.Errorf("DeleteContextEnvironmentVariable error = %v, want IsUnauthorized and not IsNotFound", err)
	}
}

// envVarCall is one request the fake project envvar API received.
type envVarCall struct {
	method     string
	path       string
	requestURI string
	body       []byte
}

// newEnvVarAPI serves the single-variable envvar routes (create, get-by-name,
// delete-by-name) and records every call, answering a successful request with
// testEnvVarBody. A non-empty notFoundMessage answers every request with a 404
// carrying that message instead.
func newEnvVarAPI(t *testing.T, notFoundMessage string) (*httptest.Server, func() []envVarCall) {
	t.Helper()

	var (
		mu    sync.Mutex
		calls []envVarCall
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		calls = append(calls, envVarCall{
			method: r.Method, path: r.URL.Path, requestURI: r.RequestURI, body: body,
		})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if notFoundMessage != "" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": notFoundMessage})

			return
		}

		if r.Method == http.MethodDelete {
			// The delete route answers 200 with
			// {"message": "Environment variable deleted."}.
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Environment variable deleted."})

			return
		}

		_, _ = w.Write([]byte(testEnvVarBody))
	}))
	t.Cleanup(srv.Close)

	return srv, func() []envVarCall {
		mu.Lock()
		defer mu.Unlock()

		return append([]envVarCall(nil), calls...)
	}
}

// The shape matches what the API actually returns: {name, value, created_at},
// with value already masked even on the create response — the literal value
// configured is never echoed back by any route.
const testEnvVarBody = `{"name":"API_TOKEN","value":"xxxx1234","created_at":"2023-04-14T21:20:14.000Z"}`

func TestCreateProjectEnvironmentVariable(t *testing.T) {
	t.Parallel()

	srv, recorded := newEnvVarAPI(t, "")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	envVar, err := c.CreateProjectEnvironmentVariable(context.Background(), testEnvVarProjectSlug,
		circleci.ProjectEnvironmentVariableInput{Name: "API_TOKEN", Value: "s3cr3t1234"},
	)
	if err != nil {
		t.Fatalf("CreateProjectEnvironmentVariable returned error: %v", err)
	}

	// The response value is masked, never the literal value that was sent.
	if envVar.Name != "API_TOKEN" || envVar.Value != "xxxx1234" {
		t.Errorf("environment variable = %+v, want the masked response body", envVar)
	}

	calls := recorded()
	if len(calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(calls))
	}
	if calls[0].method != http.MethodPost {
		t.Errorf("method = %q, want POST", calls[0].method)
	}
	if want := "/api/v2/project/" + testEnvVarProjectSlug + "/envvar"; calls[0].path != want {
		t.Errorf("path = %q, want %q", calls[0].path, want)
	}

	var sentBody map[string]string
	if err := json.Unmarshal(calls[0].body, &sentBody); err != nil {
		t.Fatalf("request body did not decode as JSON: %v", err)
	}
	if sentBody["name"] != "API_TOKEN" || sentBody["value"] != "s3cr3t1234" {
		t.Errorf("request body = %v, want the literal configured name and value", sentBody)
	}
}

func TestCreateProjectEnvironmentVariableRejectsMalformedSlug(t *testing.T) {
	t.Parallel()

	c := circleci.New(circleci.Config{Host: "http://127.0.0.1:1", Token: "tok"})

	_, err := c.CreateProjectEnvironmentVariable(context.Background(), "not-a-slug",
		circleci.ProjectEnvironmentVariableInput{Name: "X", Value: "y"},
	)
	if err == nil {
		t.Fatal("CreateProjectEnvironmentVariable returned no error for a malformed slug, want one")
	}
	if !strings.Contains(err.Error(), "project slug") {
		t.Errorf("error = %v, want it to name the project slug", err)
	}
}

func TestGetProjectEnvironmentVariable(t *testing.T) {
	t.Parallel()

	srv, recorded := newEnvVarAPI(t, "")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	envVar, err := c.GetProjectEnvironmentVariable(context.Background(), testEnvVarProjectSlug, "API_TOKEN")
	if err != nil {
		t.Fatalf("GetProjectEnvironmentVariable returned error: %v", err)
	}
	if envVar.Value != "xxxx1234" {
		t.Errorf("Value = %q, want the masked value %q", envVar.Value, "xxxx1234")
	}

	calls := recorded()
	if len(calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(calls))
	}
	if calls[0].method != http.MethodGet {
		t.Errorf("method = %q, want GET", calls[0].method)
	}
	if want := "/api/v2/project/" + testEnvVarProjectSlug + "/envvar/API_TOKEN"; calls[0].path != want {
		t.Errorf("path = %q, want %q", calls[0].path, want)
	}
}

func TestGetProjectEnvironmentVariableEscapesName(t *testing.T) {
	t.Parallel()

	srv, recorded := newEnvVarAPI(t, "")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if _, err := c.GetProjectEnvironmentVariable(context.Background(), testEnvVarProjectSlug, "MY VAR"); err != nil {
		t.Fatalf("GetProjectEnvironmentVariable returned error: %v", err)
	}

	calls := recorded()
	if want := "/api/v2/project/" + testEnvVarProjectSlug + "/envvar/MY%20VAR"; calls[0].requestURI != want {
		t.Errorf("raw request URI = %q, want %q", calls[0].requestURI, want)
	}
}

func TestGetProjectEnvironmentVariableNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newEnvVarAPI(t, "Environment variable not found.")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetProjectEnvironmentVariable(context.Background(), testEnvVarProjectSlug, "NOPE")
	if !circleci.IsNotFound(err) {
		t.Errorf("GetProjectEnvironmentVariable error = %v, want a not found error", err)
	}
}

func TestDeleteProjectEnvironmentVariable(t *testing.T) {
	t.Parallel()

	srv, recorded := newEnvVarAPI(t, "")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if err := c.DeleteProjectEnvironmentVariable(context.Background(), testEnvVarProjectSlug, "API_TOKEN"); err != nil {
		t.Fatalf("DeleteProjectEnvironmentVariable returned error: %v", err)
	}

	calls := recorded()
	if len(calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(calls))
	}
	if calls[0].method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", calls[0].method)
	}
	if want := "/api/v2/project/" + testEnvVarProjectSlug + "/envvar/API_TOKEN"; calls[0].path != want {
		t.Errorf("path = %q, want %q", calls[0].path, want)
	}
}

func TestDeleteProjectEnvironmentVariableNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newEnvVarAPI(t, "Environment variable not found.")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	err := c.DeleteProjectEnvironmentVariable(context.Background(), testEnvVarProjectSlug, "NOPE")
	if !circleci.IsNotFound(err) {
		t.Errorf("DeleteProjectEnvironmentVariable error = %v, want a not found error", err)
	}
}

// TestProjectEnvVarNameRouteRejectsDotSegments is the regression test for
// projectEnvVarNameRoute's name check: a name that is exactly "." or ".."
// survives url.PathEscape unchanged (it doesn't touch dots) and url.Parse
// doesn't clean dot-segments out of a path either, so either one would put a
// literal "./" or "../" into the outbound request path and retarget it at a
// different route — the same risk checkoutKeyProjectPath already closes for
// the slug, but the name is appended as a trailing segment after the slug is
// already escaped, so that check does not reach it. This is defence in
// depth, not a fix for a reachable bug: a name comes from Terraform
// configuration or from CircleCI's own API responses, never from a third
// party.
//
// It also pins that an ordinary name, and one that merely contains a dot,
// remain valid — rejecting either would break real configurations, even
// though CircleCI environment variable names are conventionally shell
// identifiers that would not contain one.
func TestProjectEnvVarNameRouteRejectsDotSegments(t *testing.T) {
	t.Parallel()

	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the client made a request for a dot-segment name, URI = %q", r.RequestURI)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, testEnvVarBody)
	}))
	t.Cleanup(badSrv.Close)

	badClient := circleci.New(circleci.Config{Host: badSrv.URL, Token: "tok"})

	for _, name := range []string{".", ".."} {
		t.Run("name="+name, func(t *testing.T) {
			if _, err := badClient.GetProjectEnvironmentVariable(context.Background(), testEnvVarProjectSlug, name); err == nil {
				t.Errorf("GetProjectEnvironmentVariable(name=%q) returned no error, want one", name)
			}
			if err := badClient.DeleteProjectEnvironmentVariable(context.Background(), testEnvVarProjectSlug, name); err == nil {
				t.Errorf("DeleteProjectEnvironmentVariable(name=%q) returned no error, want one", name)
			}
		})
	}

	srv, recorded := newEnvVarAPI(t, "")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	for _, name := range []string{"API_TOKEN", "MY.VAR"} {
		if _, err := c.GetProjectEnvironmentVariable(context.Background(), testEnvVarProjectSlug, name); err != nil {
			t.Errorf("GetProjectEnvironmentVariable(name=%q) returned error: %v, want none", name, err)
		}
	}

	calls := recorded()
	if len(calls) != 2 {
		t.Fatalf("made %d requests, want 2 (one per legitimate name)", len(calls))
	}
	if want := "/api/v2/project/" + testEnvVarProjectSlug + "/envvar/API_TOKEN"; calls[0].requestURI != want {
		t.Errorf("first request URI = %q, want %q", calls[0].requestURI, want)
	}
	if want := "/api/v2/project/" + testEnvVarProjectSlug + "/envvar/MY.VAR"; calls[1].requestURI != want {
		t.Errorf("second request URI = %q, want %q", calls[1].requestURI, want)
	}
}

// TestContextEnvVarNameRejectsDotSegments is the regression test for
// checkContextEnvVarName: a name that is exactly "." or ".." survives
// url.PathEscape unchanged and url.Parse doesn't clean dot-segments out of a
// path either, so either one would put a literal "./" or "../" into the
// outbound request path in place of the variable name and retarget it at a
// different route. Unlike the project environment-variable and checkout-key
// routes, this one builds its route through RouteParams rather than manual
// string concatenation, but RouteParams escapes each value as its own
// segment the same way, so the same gap applies. This is defence in depth,
// not a fix for a reachable bug: a name comes from Terraform configuration or
// from CircleCI's own API responses, never from a third party.
func TestContextEnvVarNameRejectsDotSegments(t *testing.T) {
	t.Parallel()

	badClient, seen := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{"variable":"x","context_id":"`+testEnvVarContextID+`","created_at":"","updated_at":""}`)
	})

	for _, name := range []string{".", ".."} {
		t.Run("name="+name, func(t *testing.T) {
			if _, err := badClient.UpsertContextEnvironmentVariable(context.Background(), testEnvVarContextID, name, "v"); err == nil {
				t.Errorf("UpsertContextEnvironmentVariable(name=%q) returned no error, want one", name)
			}
			if err := badClient.DeleteContextEnvironmentVariable(context.Background(), testEnvVarContextID, name); err == nil {
				t.Errorf("DeleteContextEnvironmentVariable(name=%q) returned no error, want one", name)
			}
		})
	}

	if len(*seen) != 0 {
		t.Fatalf("made %d requests, want 0: a dot-segment name must be rejected before any request", len(*seen))
	}

	// An ordinary name must still reach the server.
	if _, err := badClient.UpsertContextEnvironmentVariable(context.Background(), testEnvVarContextID, "API_KEY", "v"); err != nil {
		t.Errorf("UpsertContextEnvironmentVariable(name=%q) returned error: %v, want none", "API_KEY", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("made %d requests, want 1 for the legitimate name", len(*seen))
	}
	wantURI := "/api/v2/context/" + testEnvVarContextID + "/environment-variable/API_KEY"
	if got := (*seen)[0].rawURI; got != wantURI {
		t.Errorf("raw request URI = %q, want %q", got, wantURI)
	}
}
