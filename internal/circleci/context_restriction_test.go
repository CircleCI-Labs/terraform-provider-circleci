// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const testRestrictionContextID = "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"

func TestListContextRestrictions(t *testing.T) {
	t.Parallel()

	// The shape matches what the API actually returns: an {"items": [...]}
	// envelope with no next_page_token at all, whose entries carry project_id
	// only for project restrictions.
	client, seen := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{"items":[
			{"context_id":"`+testRestrictionContextID+`","id":"r1","name":"acme/api","restriction_type":"project","restriction_value":"22222222-2222-2222-2222-222222222222","project_id":"22222222-2222-2222-2222-222222222222"},
			{"context_id":"`+testRestrictionContextID+`","id":"r2","name":"All members","restriction_type":"group","restriction_value":"96d25399-ab81-4499-969f-f890a383209e"},
			{"context_id":"`+testRestrictionContextID+`","id":"r3","name":"","restriction_type":"expression","restriction_value":"some expression"}
		]}`)
	})

	restrictions, err := client.ListContextRestrictions(context.Background(), testRestrictionContextID)
	if err != nil {
		t.Fatalf("ListContextRestrictions returned error: %v", err)
	}

	if len(restrictions) != 3 {
		t.Fatalf("restriction count = %d, want 3", len(restrictions))
	}

	project := restrictions[0]
	if project.RestrictionType != circleci.ContextRestrictionTypeProject {
		t.Errorf("first restriction type = %q, want %q", project.RestrictionType, circleci.ContextRestrictionTypeProject)
	}
	if project.ProjectID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("first restriction project_id = %q, want the project uuid", project.ProjectID)
	}
	if project.ContextID != testRestrictionContextID {
		t.Errorf("first restriction context_id = %q, want %q", project.ContextID, testRestrictionContextID)
	}

	// A group restriction has no project_id, which must read back as "" rather
	// than repeating the restriction value.
	if got := restrictions[1]; got.ProjectID != "" || got.RestrictionValue == "" {
		t.Errorf("group restriction = %+v, want an empty project_id and a value", got)
	}
	if got := restrictions[2]; got.Name != "" || got.RestrictionType != circleci.ContextRestrictionTypeExpression {
		t.Errorf("expression restriction = %+v, want an empty name and type expression", got)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1 (the endpoint is not paginated)", len(*seen))
	}

	wantPath := "/api/v2/context/" + testRestrictionContextID + "/restrictions"
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath || got.query != "" {
		t.Errorf("request = %s %s?%s, want GET %s with no query", got.method, got.path, got.query, wantPath)
	}
}

func TestListContextRestrictionsEmpty(t *testing.T) {
	t.Parallel()

	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{"items":[]}`)
	})

	restrictions, err := client.ListContextRestrictions(context.Background(), testRestrictionContextID)
	if err != nil {
		t.Fatalf("ListContextRestrictions returned error: %v", err)
	}
	if len(restrictions) != 0 {
		t.Errorf("restriction count = %d, want 0", len(restrictions))
	}
}

func TestListContextRestrictionsNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListError(w, http.StatusNotFound, "Context not found.")
	})

	_, err := client.ListContextRestrictions(context.Background(), testRestrictionContextID)
	if !circleci.IsNotFound(err) {
		t.Errorf("ListContextRestrictions error = %v, want a not found error", err)
	}
}

func TestListContextRestrictionsEscapesRouteParams(t *testing.T) {
	t.Parallel()

	// The context id arrives from configuration, so anything path-significant in
	// it must be escaped rather than changing which route is called.
	client, seen := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{"items":[]}`)
	})

	if _, err := client.ListContextRestrictions(context.Background(), "ctx/../evil"); err != nil {
		t.Fatalf("ListContextRestrictions returned error: %v", err)
	}

	// Assert on the raw request line: r.URL.Path is already percent-decoded, so
	// it would look the same whether or not the value was escaped.
	wantURI := "/api/v2/context/ctx%2F..%2Fevil/restrictions"
	if got := (*seen)[0].rawURI; got != wantURI {
		t.Errorf("raw request URI = %q, want %q", got, wantURI)
	}
}

func TestCreateContextRestriction(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	client, seen := newListServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}

		w.WriteHeader(http.StatusCreated)
		// The create response has no "name" field at all.
		writeListJSON(w, `{"context_id":"`+testRestrictionContextID+`","id":"r1",`+
			`"restriction_type":"project","restriction_value":"proj-1","project_id":"proj-1"}`)
	})

	got, err := client.CreateContextRestriction(context.Background(), testRestrictionContextID, circleci.CreateContextRestrictionRequest{
		RestrictionType:  circleci.ContextRestrictionTypeProject,
		RestrictionValue: "proj-1",
	})
	if err != nil {
		t.Fatalf("CreateContextRestriction returned error: %v", err)
	}
	if got.ID != "r1" || got.ProjectID != "proj-1" || got.Name != "" {
		t.Errorf("CreateContextRestriction = %+v, want id r1, project_id proj-1, and an empty name", got)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	wantPath := "/api/v2/context/" + testRestrictionContextID + "/restrictions"
	if got := (*seen)[0]; got.method != http.MethodPost || got.path != wantPath {
		t.Errorf("request = %s %s, want POST %s", got.method, got.path, wantPath)
	}
	if gotBody["restriction_type"] != "project" || gotBody["restriction_value"] != "proj-1" {
		t.Errorf("request body = %+v, want restriction_type=project and restriction_value=proj-1", gotBody)
	}
}

func TestCreateContextRestrictionAPIError(t *testing.T) {
	t.Parallel()

	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListError(w, http.StatusConflict, "The restriction you're trying to add already exists.")
	})

	_, err := client.CreateContextRestriction(context.Background(), testRestrictionContextID, circleci.CreateContextRestrictionRequest{
		RestrictionType:  circleci.ContextRestrictionTypeExpression,
		RestrictionValue: "true",
	})
	if err == nil {
		t.Fatal("CreateContextRestriction returned no error for a 409, want one")
	}
	if detail := circleci.Detail(err); detail == "" {
		t.Error("Detail() = \"\", want the server message")
	}
}

func TestDeleteContextRestriction(t *testing.T) {
	t.Parallel()

	client, seen := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{"message":"Context restriction deleted."}`)
	})

	if err := client.DeleteContextRestriction(context.Background(), testRestrictionContextID, "r1"); err != nil {
		t.Fatalf("DeleteContextRestriction returned error: %v", err)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	wantPath := "/api/v2/context/" + testRestrictionContextID + "/restrictions/r1"
	if got := (*seen)[0]; got.method != http.MethodDelete || got.path != wantPath {
		t.Errorf("request = %s %s, want DELETE %s", got.method, got.path, wantPath)
	}
}

// TestDeleteContextRestrictionMissingContextAnswers403 documents the same
// anti-enumeration behavior as context_test.go's TestDeleteContextMissingAnswers403:
// a restriction on a context that no longer exists cannot be told apart from an
// unauthorized request.
func TestDeleteContextRestrictionMissingContextAnswers403(t *testing.T) {
	t.Parallel()

	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListError(w, http.StatusForbidden, "Forbidden")
	})

	err := client.DeleteContextRestriction(context.Background(), "does-not-exist", "r1")
	if circleci.IsNotFound(err) {
		t.Error("IsNotFound(err) = true for a 403, want false")
	}
	if !circleci.IsUnauthorized(err) {
		t.Error("IsUnauthorized(err) = false for a 403, want true")
	}
}
