// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"fmt"
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

// contextEnvVarItem renders one item of the list route's response body.
func contextEnvVarItem(name, truncated string) string {
	return `{"variable":"` + name + `","context_id":"` + testEnvVarContextID + `","truncated_value":"` +
		truncated + `","created_at":"2024-01-02T03:04:05.000Z","updated_at":"2024-01-02T03:04:05.000Z"}`
}

// contextEnvVarPage renders a full page: contextEnvVarPageSizeInTests items
// named PAD000..PAD099, which is what the route sends whenever a context holds
// more than one page's worth.
func contextEnvVarPage() string {
	items := make([]string, 0, contextEnvVarPageSizeInTests)
	for i := range contextEnvVarPageSizeInTests {
		items = append(items, contextEnvVarItem(fmt.Sprintf("PAD%03d", i), "3cr3"))
	}

	return strings.Join(items, ",")
}

// contextEnvVarPageSizeInTests mirrors the page size the service fixes for this
// route. It is duplicated here rather than exported from the client, because a
// test that reads the number out of the code under test cannot notice the code
// getting it wrong.
const contextEnvVarPageSizeInTests = 100

// TestListContextEnvironmentVariables pins the whole-collection case: a context
// at or below the page size, answered in a single request.
//
// The response shape is what the API actually returns: {"items": [...],
// "next_page_token": null}, with truncated_value rather than value, because the
// API never discloses the configured value. truncated_value carries the tail of
// the value with NO mask prefix — the API takes the last four characters, or the
// last floor(len/2) for a value of eight characters or fewer. This fixture used
// to say "xxxx3cr3", borrowing the *project* environment variable convention,
// which does prefix the tail with "xxxx". The two are different shapes from
// different routes, and a fixture in the wrong one is not evidence.
//
// One request, and no page-token query parameter on it. The route ignores every
// spelling of that parameter (see circleci.ContextEnvVarsTruncatedError for the
// eight that were measured), so sending one would advertise a contract the route
// does not honour.
func TestListContextEnvironmentVariables(t *testing.T) {
	t.Parallel()

	client, seen := pageListServer(t,
		`{"items":[`+
			contextEnvVarItem("API_KEY", "3cr3")+`,`+
			contextEnvVarItem("ZONE", "st-1")+
			`],"next_page_token":null}`,
	)

	vars, err := client.ListContextEnvironmentVariables(context.Background(), testEnvVarContextID)
	if err != nil {
		t.Fatalf("ListContextEnvironmentVariables returned error: %v", err)
	}

	if len(vars) != 2 {
		t.Fatalf("variable count = %d, want 2", len(vars))
	}
	if vars[0].Variable != "API_KEY" || vars[0].TruncatedValue != "3cr3" {
		t.Errorf("first variable = %+v, want API_KEY truncated as 3cr3", vars[0])
	}
	if vars[0].ContextID != testEnvVarContextID {
		t.Errorf("first variable context_id = %q, want %q", vars[0].ContextID, testEnvVarContextID)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1 — this route serves one page and cannot be paged", len(*seen))
	}
	wantPath := "/api/v2/context/" + testEnvVarContextID + "/environment-variable"
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", got.method, got.path, wantPath)
	}
	if got := (*seen)[0].query; got != "" {
		t.Errorf("request query = %q, want it empty: the route ignores every page parameter, so "+
			"sending one claims a pagination contract that does not exist", got)
	}
}

// TestListContextEnvironmentVariablesTruncationDetectedOnFirstResponse is the
// regression test for a context that holds more variables than this route will
// disclose.
//
// The measured contract is in circleci.ContextEnvVarsTruncatedError. The part
// this test exists for: the advertised page token is derived from the LAST ITEM
// on the page, so it changes whenever the tail of page one changes. Two
// consecutive first-page requests against a context that is being written to
// come back with two DIFFERENT non-null tokens — measured on a real context by
// deleting a variable that sorts before the page boundary, which moved the token
// from "after ZZZ07" to "after ZZZ08".
//
// The server below reproduces exactly that: the same page every time, a fresh
// token every time. Detecting truncation by comparing one response's token
// against the token that was sent therefore never fires — it sees two unequal
// tokens, concludes pagination advanced, and drains for ever, adding a hundred
// duplicates per iteration. That is the failure mode worse than a wrong answer,
// and it is why the signal has to be the FIRST response's token on its own: a
// non-null token means "there is more, and no request can reach it".
//
// The server stops advertising a token after a few requests so that a
// regression here fails on the request count rather than hanging the suite.
func TestListContextEnvironmentVariablesTruncationDetectedOnFirstResponse(t *testing.T) {
	t.Parallel()

	var calls int
	client, seen := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		token := fmt.Sprintf(`"after-%d"`, calls)
		if calls > 4 {
			// Terminate a drain that should never have got this far, so the
			// assertions below run instead of the test timing out.
			token = "null"
		}
		writeListJSON(w, `{"items":[`+contextEnvVarPage()+`],"next_page_token":`+token+`}`)
	})

	vars, err := client.ListContextEnvironmentVariables(context.Background(), testEnvVarContextID)
	if err == nil {
		t.Fatalf("ListContextEnvironmentVariables returned %d variables and no error for a context that "+
			"holds more than the route discloses; a caller has no way to know the list is incomplete",
			len(vars))
	}
	if vars != nil {
		t.Errorf("returned %d variables alongside the error; a partial collection must not be handed "+
			"back through the normal return, where a caller would mistake it for the whole one", len(vars))
	}

	if len(*seen) != 1 {
		t.Errorf("made %d requests, want 1 — truncation is visible in the first response's token, and "+
			"a second request neither reaches the rest nor proves anything", len(*seen))
	}

	truncated, ok := circleci.AsContextEnvVarsTruncated(err)
	if !ok {
		t.Fatalf("error %v is not a *circleci.ContextEnvVarsTruncatedError, so a caller cannot tell an "+
			"incomplete list from any other failure and must treat every variable on the context as "+
			"unreadable", err)
	}
	if truncated.ContextID != testEnvVarContextID {
		t.Errorf("ContextID = %q, want %q", truncated.ContextID, testEnvVarContextID)
	}
	if len(truncated.Page) != contextEnvVarPageSizeInTests {
		t.Fatalf("Page holds %d variables, want the %d that were disclosed: a resource managing one of "+
			"them must still be able to refresh it", len(truncated.Page), contextEnvVarPageSizeInTests)
	}

	if got, found := truncated.Find("PAD000"); !found || got.Variable != "PAD000" {
		t.Errorf("Find(\"PAD000\") = %+v, %v; want the disclosed variable", got, found)
	}
	if _, found := truncated.Find("PAD999"); found {
		t.Error("Find(\"PAD999\") reported a variable that was not on the page")
	}
}

// TestListContextEnvironmentVariablesTruncatedErrorDoesNotAdviseTheMissingRoute
// guards the wording of the diagnostic a practitioner sees.
//
// The previous message ended "or read them individually by name". There is no
// such route: GET /context/{id}/environment-variable/{name} answers 404 page not
// found, measured. Advice that cannot be followed is worse than none, because it
// sends the reader looking for a route rather than at the only fix that works,
// which is splitting the variables across more than one context.
func TestListContextEnvironmentVariablesTruncatedErrorDoesNotAdviseTheMissingRoute(t *testing.T) {
	t.Parallel()

	client, _ := pageListServer(t, `{"items":[`+contextEnvVarPage()+`],"next_page_token":"after-PAD099"}`)

	_, err := client.ListContextEnvironmentVariables(context.Background(), testEnvVarContextID)
	if err == nil {
		t.Fatal("ListContextEnvironmentVariables returned no error for a truncated list")
	}

	msg := err.Error()
	if strings.Contains(msg, "individually by name") {
		t.Errorf("error %q tells the practitioner to read the variables individually by name; that "+
			"route does not exist and answers 404", msg)
	}
	for _, want := range []string{testEnvVarContextID, "100", "more than one context"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q, so it does not say which context is affected, "+
				"how many variables were disclosed, or what to do about it", msg, want)
		}
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

// TestContextEnvironmentVariableInputMarshalsValueKey is the wire-format test
// for the upsert body, and it needs no server: it serialises the request type
// and looks at the keys.
//
// PUT /context/{id}/environment-variable/{name} reads exactly one request key,
// "value", and ignores every other one. Measured against a real context:
//
//	{"value":"ABCDEFGHIJKL"} -> 200, stored, truncated_value "IJKL"
//	{"val":"ABCDEFGHIJKL"}   -> 200, stored EMPTY, truncated_value ""
//	{"Value":"ABCDEFGHIJKL"} -> 200, stored EMPTY, truncated_value ""
//	{}                       -> 400 {"message":"Invalid body."}
//
// So the only body the route rejects is an empty object. A misspelled key looks
// exactly like a correct one from the outside — same status, same response body,
// created_at and updated_at both set — and the variable exists from then on with
// no secret in it. Nothing downstream can detect it either: the value is never
// returned on any route, and truncated_value is "" both for a dropped value and
// for a deliberately empty one.
//
// This is the same silent-drop class as the webhook signing-secret defect (see
// TestWebhookInputMarshalsHyphenatedRequestKeys), which shipped because nothing
// pinned the request shape. The client here has always sent the right key; this
// test is what keeps it that way. Asserting the plausible misspellings are
// ABSENT matters as much as asserting "value" is present, because a type
// carrying both spellings would satisfy a presence-only check while telling the
// reader something false about the route.
func TestContextEnvironmentVariableInputMarshalsValueKey(t *testing.T) {
	t.Parallel()

	body, err := json.Marshal(circleci.ContextEnvironmentVariableInput{Value: "s3cr3t"})
	if err != nil {
		t.Fatalf("marshalling ContextEnvironmentVariableInput: %v", err)
	}

	var keys map[string]any
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatalf("serialised ContextEnvironmentVariableInput is not a JSON object: %v (%s)", err, body)
	}

	if keys["value"] != "s3cr3t" {
		t.Errorf(`serialised body["value"] = %v, want "s3cr3t" — that is the only key the route reads, `+
			`and a body without it is a 200 that stores an empty secret (%s)`, keys["value"], body)
	}

	for _, wrong := range []string{"val", "Value", "env_value", "environment_value", "secret", "name"} {
		if _, present := keys[wrong]; present {
			t.Errorf("serialised body carries %q; the route ignores every key but \"value\", so the "+
				"secret would be silently discarded and the variable stored empty (%s)", wrong, body)
		}
	}

	// Exactly one key. The name travels in the path, and the read type's masked
	// tail must never be echoed back as if it were a value.
	if len(keys) != 1 {
		t.Errorf("serialised body = %s, want only {\"value\": ...}", body)
	}
}
