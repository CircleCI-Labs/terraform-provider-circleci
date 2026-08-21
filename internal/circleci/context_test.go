// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
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

	// The shape matches what the API actually returns: items of
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

func TestCreateContext(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	client, seen := newListServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}

		// The create response is deliberately narrow: id, name, created_at only.
		writeListJSON(w, `{"id":"ctx-1","name":"build","created_at":"2024-01-02T03:04:05.000Z"}`)
	})

	got, err := client.CreateContext(context.Background(), testContextOrgID, "build")
	if err != nil {
		t.Fatalf("CreateContext returned error: %v", err)
	}
	if got.ID != "ctx-1" || got.Name != "build" || got.CreatedAt != "2024-01-02T03:04:05.000Z" {
		t.Errorf("CreateContext = %+v, want id ctx-1 named build", got)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	if got := (*seen)[0]; got.method != http.MethodPost || got.path != "/api/v2/context" {
		t.Errorf("request = %s %s, want POST /api/v2/context", got.method, got.path)
	}

	// The body nests owner.id/owner.type rather than sending a bare
	// organization_id.
	owner, ok := gotBody["owner"].(map[string]any)
	if !ok {
		t.Fatalf("request body owner = %v, want an object", gotBody["owner"])
	}
	if owner["id"] != testContextOrgID {
		t.Errorf("request body owner.id = %v, want %q", owner["id"], testContextOrgID)
	}
	if owner["type"] != circleci.ContextOwnerTypeOrganization {
		t.Errorf("request body owner.type = %v, want %q", owner["type"], circleci.ContextOwnerTypeOrganization)
	}
	if gotBody["name"] != "build" {
		t.Errorf("request body name = %v, want %q", gotBody["name"], "build")
	}
}

func TestCreateContextAPIError(t *testing.T) {
	t.Parallel()

	// This matches the original implementation: the "account" owner type is
	// documented but rejected outright.
	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListError(w, http.StatusBadRequest, "Invalid owner type - only organization is supported at present")
	})

	_, err := client.CreateContext(context.Background(), testContextOrgID, "build")
	if err == nil {
		t.Fatal("CreateContext returned no error for a 400, want one")
	}
	if detail := circleci.Detail(err); detail == "" {
		t.Error("Detail() = \"\", want the server message")
	}
}

func TestDeleteContext(t *testing.T) {
	t.Parallel()

	const contextID = "ctx-1"

	client, seen := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{"message":"Context deleted."}`)
	})

	if err := client.DeleteContext(context.Background(), contextID); err != nil {
		t.Fatalf("DeleteContext returned error: %v", err)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	wantPath := "/api/v2/context/" + contextID
	if got := (*seen)[0]; got.method != http.MethodDelete || got.path != wantPath {
		t.Errorf("request = %s %s, want DELETE %s", got.method, got.path, wantPath)
	}
}

// TestDeleteContextMissingAnswers403 documents an anti-enumeration behavior:
// every route addressing a context by id maps a context that no longer exists
// to 403, not 404. circleci.IsNotFound must NOT match this — see
// context_resource.go's Delete for why 403 is instead treated as an
// already-absent context, not folded into IsNotFound at the client layer
// where it would also swallow a token that merely lost permission.
func TestDeleteContextMissingAnswers403(t *testing.T) {
	t.Parallel()

	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListError(w, http.StatusForbidden, "Forbidden")
	})

	err := client.DeleteContext(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("DeleteContext returned no error for a 403, want one")
	}
	if circleci.IsNotFound(err) {
		t.Error("IsNotFound(err) = true for a 403, want false: a permission failure must not be conflated with absence")
	}
	if !circleci.IsUnauthorized(err) {
		t.Error("IsUnauthorized(err) = false for a 403, want true")
	}
}

func TestGetContextNotFound(t *testing.T) {
	t.Parallel()

	// A genuine 404 (the rare race where the existence check succeeds but
	// the read itself then fails) must still satisfy IsNotFound.
	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListError(w, http.StatusNotFound, "context not found")
	})

	_, err := client.GetContext(context.Background(), "ctx-1")
	if !circleci.IsNotFound(err) {
		t.Errorf("GetContext error = %v, want a not found error", err)
	}
}

// TestGetContextDecodesOrgID pins the field that makes an import verifiable.
//
// The body is the measured shape of GET /api/v2/context/{id} on CircleCI Cloud,
// including the inline environment_variables and restrictions that this client
// deliberately does not decode. They are here so that a future change which does
// decode them starts from the real shape — note that an inline variable object
// has no context_id, unlike the dedicated list route's.
//
// Context.OrgID went missing for a whole release behind a comment asserting that
// this route "does not report which organization a context belongs to", which is
// why the import trusted an unvalidated string from the practitioner.
func TestGetContextDecodesOrgID(t *testing.T) {
	t.Parallel()

	client, seen := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{
			"id": "ctx-1",
			"name": "build",
			"created_at": "2024-01-02T03:04:05.000Z",
			"org_id": "`+testContextOrgID+`",
			"environment_variables": [
				{
					"variable": "PROBE_VAR",
					"truncated_value": "ghij",
					"created_at": "2024-01-02T03:04:05.000Z",
					"updated_at": "2024-01-02T03:04:05.000Z"
				}
			],
			"restrictions": [
				{
					"context_id": "ctx-1",
					"id": "rst-1",
					"name": "All members",
					"restriction_type": "group",
					"restriction_value": "`+testContextOrgID+`"
				}
			]
		}`)
	})

	found, err := client.GetContext(context.Background(), "ctx-1")
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}

	if found.OrgID != testContextOrgID {
		t.Errorf("OrgID = %q, want %q", found.OrgID, testContextOrgID)
	}
	if found.ID != "ctx-1" {
		t.Errorf("ID = %q, want %q", found.ID, "ctx-1")
	}
	if found.Name != "build" {
		t.Errorf("Name = %q, want %q", found.Name, "build")
	}
	if found.CreatedAt != "2024-01-02T03:04:05.000Z" {
		t.Errorf("CreatedAt = %q, want %q", found.CreatedAt, "2024-01-02T03:04:05.000Z")
	}

	if got, want := (*seen)[0].path, "/api/v2/context/ctx-1"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

// TestCreateAndListContextsReportNoOrgID pins the asymmetry documented on
// Context.OrgID: only the single-context read reports the owning organization.
//
// This is the trap the field introduces. A caller that reads OrgID off a
// CreateContext or ListContexts result gets "" and, if it stores that, writes an
// empty organization into Terraform state. The bodies below are the measured
// create and list shapes; neither has an org_id key.
//
// Unlike the other tests added with the org_id fix, this one passes both before
// and after it — it guards a change nobody has made yet rather than reproducing a
// shipped bug. The change it guards against is the tempting one: having
// CreateContext fill OrgID in from the organization argument it was passed, so
// the field looks uniformly populated. That would be a value this client invented
// rather than one the API reported, and it would make every future caller's
// "OrgID is set, so it must be verified" assumption wrong.
func TestCreateAndListContextsReportNoOrgID(t *testing.T) {
	t.Parallel()

	created, err := func() (*circleci.Context, error) {
		client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeListJSON(w, `{"id":"ctx-1","name":"build","created_at":"2024-01-02T03:04:05.000Z"}`)
		})

		return client.CreateContext(context.Background(), testContextOrgID, "build")
	}()
	if err != nil {
		t.Fatalf("CreateContext: %v", err)
	}
	if created.OrgID != "" {
		t.Errorf("CreateContext OrgID = %q, want empty: the create route does not report org_id", created.OrgID)
	}

	client, _ := pageListServer(t,
		`{"items":[{"id":"ctx-1","name":"build","created_at":"2024-01-02T03:04:05.000Z"}],"next_page_token":null}`,
	)

	listed, err := client.ListContexts(context.Background(), testContextOrgID)
	if err != nil {
		t.Fatalf("ListContexts: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListContexts returned %d contexts, want 1", len(listed))
	}
	if listed[0].OrgID != "" {
		t.Errorf("ListContexts OrgID = %q, want empty: the list route does not report org_id", listed[0].OrgID)
	}
}
