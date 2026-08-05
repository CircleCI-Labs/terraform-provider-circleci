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

// userPreferencesMatrixResponse is a literal fixture for the shape of a
// user-scoped preference row: preference_name, channel, scope ("actor"),
// display order and group fields, no section, and a bare user reference.
const userPreferencesMatrixResponse = `{
  "data": [
    {
      "id": "pref-1",
      "attributes": {
        "preference_name": "Email Status",
        "channel": "email",
        "scope": "actor",
        "preference_display_order": 1,
        "preference_experimental": false,
        "group_id": "group-1",
        "group_name": "Group",
        "group_display_order": 1,
        "group_experimental": false,
        "user_configurable": true,
        "is_enabled": true
      },
      "references": {
        "user": {"id": "user-1"}
      }
    }
  ]
}`

// projectPreferencesMatrixResponse mirrors a project-scoped row: project + org
// references, no user reference.
const projectPreferencesMatrixResponse = `{
  "data": [
    {
      "id": "pref-2",
      "attributes": {
        "preference_name": "Slack Status",
        "channel": "slack",
        "scope": "actor",
        "preference_display_order": 2,
        "preference_experimental": false,
        "group_id": "group-1",
        "group_name": "Group",
        "group_display_order": 1,
        "group_experimental": false,
        "user_configurable": true,
        "is_enabled": false
      },
      "references": {
        "project": {"id": "project-1"},
        "org": {"id": "org-1"}
      }
    }
  ]
}`

func TestListNotificationPreferencesUserScope(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Query().Get("filter[scope]"), "user"; got != want {
			t.Errorf("filter[scope] = %q, want %q", got, want)
		}
		if r.URL.Query().Get("filter[project_id]") != "" {
			t.Error("filter[project_id] was sent for a user-scoped read, want omitted")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(userPreferencesMatrixResponse))
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	prefs, err := c.ListNotificationPreferences(context.Background(), circleci.ListNotificationPreferencesOptions{
		Scope: circleci.NotificationScopeUser,
	})
	if err != nil {
		t.Fatalf("ListNotificationPreferences returned error: %v", err)
	}
	if len(prefs) != 1 {
		t.Fatalf("got %d preferences, want 1", len(prefs))
	}

	p := prefs[0]
	if p.ID != "pref-1" {
		t.Errorf("ID = %q, want %q", p.ID, "pref-1")
	}
	if p.Name != "Email Status" {
		t.Errorf("Name = %q, want %q", p.Name, "Email Status")
	}
	if p.EntityScope != "actor" {
		t.Errorf("EntityScope = %q, want %q", p.EntityScope, "actor")
	}
	if p.UserID != "user-1" {
		t.Errorf("UserID = %q, want %q", p.UserID, "user-1")
	}
	if p.SectionID != nil {
		t.Errorf("SectionID = %v, want nil for a preference with no section", *p.SectionID)
	}
	if !p.IsEnabled {
		t.Error("IsEnabled = false, want true")
	}
}

func TestListNotificationPreferencesProjectScope(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Query().Get("filter[project_id]"), "project-1"; got != want {
			t.Errorf("filter[project_id] = %q, want %q", got, want)
		}
		if got, want := r.URL.Query().Get("filter[org_id]"), "org-1"; got != want {
			t.Errorf("filter[org_id] = %q, want %q", got, want)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(projectPreferencesMatrixResponse))
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	prefs, err := c.ListNotificationPreferences(context.Background(), circleci.ListNotificationPreferencesOptions{
		Scope:     circleci.NotificationScopeProject,
		ProjectID: "project-1",
		OrgID:     "org-1",
	})
	if err != nil {
		t.Fatalf("ListNotificationPreferences returned error: %v", err)
	}
	if len(prefs) != 1 {
		t.Fatalf("got %d preferences, want 1", len(prefs))
	}
	if prefs[0].ProjectID != "project-1" || prefs[0].OrgID != "org-1" {
		t.Errorf("ProjectID/OrgID = %q/%q, want %q/%q", prefs[0].ProjectID, prefs[0].OrgID, "project-1", "org-1")
	}
}

func TestUpdateNotificationPreferencesUserScope(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodPost; got != want {
			t.Errorf("method = %q, want %q", got, want)
		}
		if got, want := r.URL.Path, "/api/v3/notification/preferences"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}

		var sent struct {
			Data struct {
				Attributes struct {
					Scope   string `json:"scope"`
					Updates []struct {
						PreferenceID string `json:"preference_id"`
						IsEnabled    bool   `json:"is_enabled"`
					} `json:"updates"`
				} `json:"attributes"`
				References json.RawMessage `json:"references"`
			} `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		if sent.Data.Attributes.Scope != "user" {
			t.Errorf("scope = %q, want %q", sent.Data.Attributes.Scope, "user")
		}
		if sent.Data.References != nil {
			t.Errorf("references = %s, want omitted for a user-scoped update", sent.Data.References)
		}
		if len(sent.Data.Attributes.Updates) != 1 || sent.Data.Attributes.Updates[0].PreferenceID != "pref-1" {
			t.Errorf("updates = %+v, want exactly one update for pref-1", sent.Data.Attributes.Updates)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(userPreferencesMatrixResponse))
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	prefs, err := c.UpdateNotificationPreferences(context.Background(), circleci.NotificationScopeUser, "", "",
		[]circleci.NotificationPreferenceUpdate{{PreferenceID: "pref-1", IsEnabled: false}},
	)
	if err != nil {
		t.Fatalf("UpdateNotificationPreferences returned error: %v", err)
	}
	if len(prefs) != 1 {
		t.Fatalf("got %d preferences in the refreshed matrix, want 1", len(prefs))
	}
}

func TestUpdateNotificationPreferencesProjectScope(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sent struct {
			Data struct {
				Attributes struct {
					Scope string `json:"scope"`
				} `json:"attributes"`
				References struct {
					Project struct {
						ID string `json:"id"`
					} `json:"project"`
					Org struct {
						ID string `json:"id"`
					} `json:"org"`
				} `json:"references"`
			} `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		if sent.Data.Attributes.Scope != "project" {
			t.Errorf("scope = %q, want %q", sent.Data.Attributes.Scope, "project")
		}
		if sent.Data.References.Project.ID != "project-1" {
			t.Errorf("references.project.id = %q, want %q", sent.Data.References.Project.ID, "project-1")
		}
		if sent.Data.References.Org.ID != "org-1" {
			t.Errorf("references.org.id = %q, want %q", sent.Data.References.Org.ID, "org-1")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(projectPreferencesMatrixResponse))
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.UpdateNotificationPreferences(context.Background(), circleci.NotificationScopeProject, "project-1", "org-1",
		[]circleci.NotificationPreferenceUpdate{{PreferenceID: "pref-2", IsEnabled: false}},
	)
	if err != nil {
		t.Fatalf("UpdateNotificationPreferences returned error: %v", err)
	}
}

func TestListNotificationPreferencesMissingScope(t *testing.T) {
	t.Parallel()

	// filter[scope] missing entirely maps to a 400 with the v3 error envelope
	// (no "detail" field).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"id":"trace-1","title":"filter[scope] must be \"user\" or \"project\""}}`))
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.ListNotificationPreferences(context.Background(), circleci.ListNotificationPreferencesOptions{})
	if err == nil {
		t.Fatal("ListNotificationPreferences returned no error for a missing scope filter")
	}
	if circleci.IsNotFound(err) {
		t.Error("IsNotFound(err) = true, want false: a 400 is not a not-found")
	}
}

// sectionedPreferencesMatrixResponse is the shape the API sends for a
// preference row that *does* belong to a section.
//
// Two things about it are taken from the API rather than invented. First,
// none of the section_* fields carry omitempty, so they are always present
// and are explicitly null for a row with no section — the fixtures above,
// which omit the keys entirely, exercise only the absent case and would not
// notice a wrong tag name. Second, the API sends two more section fields
// than this client models (section_description and section_docs_ref); they
// are included here so the response the client is asked to decode is the
// real one, unknown fields and all.
const sectionedPreferencesMatrixResponse = `{
  "data": [
    {
      "id": "pref-3",
      "attributes": {
        "preference_name": "Deploy Finished",
        "channel": "slack",
        "scope": "actor",
        "preference_display_order": 3,
        "preference_experimental": true,
        "group_id": "group-2",
        "group_name": "Deploys",
        "group_display_order": 2,
        "group_experimental": false,
        "section_id": "section-1",
        "section_name": "Release tracking",
        "section_display_order": 4,
        "section_experimental": true,
        "section_description": "Notifications about releases",
        "section_docs_ref": "https://circleci.com/docs/deploy",
        "user_configurable": false,
        "is_enabled": true
      },
      "references": {
        "user": {"id": "user-1"}
      }
    }
  ]
}`

// TestListNotificationPreferencesSectionFields covers the section_* block of a
// preference row, which no other test reaches: every other fixture describes a
// row with no section, so a misspelled section tag would decode to nil there
// and look correct. A read-only attribute that is silently null forever is the
// hardest kind of wrong field name to notice, so it is asserted directly.
func TestListNotificationPreferencesSectionFields(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sectionedPreferencesMatrixResponse))
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	prefs, err := c.ListNotificationPreferences(context.Background(), circleci.ListNotificationPreferencesOptions{
		Scope: circleci.NotificationScopeUser,
	})
	if err != nil {
		t.Fatalf("ListNotificationPreferences returned error: %v", err)
	}
	if len(prefs) != 1 {
		t.Fatalf("got %d preferences, want 1", len(prefs))
	}

	p := prefs[0]
	if p.SectionID == nil || *p.SectionID != "section-1" {
		t.Errorf("SectionID = %v, want %q", p.SectionID, "section-1")
	}
	if p.SectionName == nil || *p.SectionName != "Release tracking" {
		t.Errorf("SectionName = %v, want %q", p.SectionName, "Release tracking")
	}
	if p.SectionDisplayOrder == nil || *p.SectionDisplayOrder != 4 {
		t.Errorf("SectionDisplayOrder = %v, want 4", p.SectionDisplayOrder)
	}
	if p.SectionExperimental == nil || !*p.SectionExperimental {
		t.Errorf("SectionExperimental = %v, want true", p.SectionExperimental)
	}

	// The rest of the row is asserted here too: preference_display_order,
	// preference_experimental, the group_* block and user_configurable are
	// otherwise only ever read from a fixture where they hold their zero value,
	// which a wrong tag name would also produce.
	if p.DisplayOrder != 3 {
		t.Errorf("DisplayOrder = %d, want 3", p.DisplayOrder)
	}
	if !p.Experimental {
		t.Error("Experimental = false, want true")
	}
	if p.GroupID != "group-2" || p.GroupName != "Deploys" {
		t.Errorf("group = %q/%q, want group-2/Deploys", p.GroupID, p.GroupName)
	}
	if p.GroupDisplayOrder != 2 {
		t.Errorf("GroupDisplayOrder = %d, want 2", p.GroupDisplayOrder)
	}
	if p.UserConfigurable {
		t.Error("UserConfigurable = true, want false")
	}
}

// TestListNotificationPreferencesExplicitNullSection covers the same fields on
// the other side of the branch: the service sends them as explicit nulls rather
// than omitting them, and a null must decode to a nil pointer, not to a zero
// value that would read as "section 0" in Terraform state.
func TestListNotificationPreferencesExplicitNullSection(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"pref-4","attributes":{
			"preference_name":"Email Status","channel":"email","scope":"actor",
			"preference_display_order":1,"preference_experimental":false,
			"group_id":"group-1","group_name":"Group","group_display_order":1,
			"group_experimental":false,
			"section_id":null,"section_name":null,"section_display_order":null,
			"section_experimental":null,"section_description":null,"section_docs_ref":null,
			"user_configurable":true,"is_enabled":true
		},"references":{"user":{"id":"user-1"}}}]}`))
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	prefs, err := c.ListNotificationPreferences(context.Background(), circleci.ListNotificationPreferencesOptions{
		Scope: circleci.NotificationScopeUser,
	})
	if err != nil {
		t.Fatalf("ListNotificationPreferences returned error: %v", err)
	}
	if len(prefs) != 1 {
		t.Fatalf("got %d preferences, want 1", len(prefs))
	}

	p := prefs[0]
	if p.SectionID != nil || p.SectionName != nil || p.SectionDisplayOrder != nil || p.SectionExperimental != nil {
		t.Errorf("section fields = %v/%v/%v/%v, want all nil for an explicit null section",
			p.SectionID, p.SectionName, p.SectionDisplayOrder, p.SectionExperimental)
	}
}
