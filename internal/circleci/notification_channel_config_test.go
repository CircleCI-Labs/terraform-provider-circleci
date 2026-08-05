// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// newChannelConfigServer returns a server that records every request and
// answers each with body, mirroring newSettingsServer.
func newChannelConfigServer(t *testing.T, status int, body string) (*httptest.Server, *[]recordedCall) {
	t.Helper()

	return newSettingsServer(t, status, body)
}

// userScopedChannelConfigResponse is a literal fixture for the shape a
// user-scoped channel config read returns: a user-scoped email config, with
// the org and user references but no project reference.
const userScopedChannelConfigResponse = `{
  "data": {
    "id": "cc-1",
    "attributes": {
      "channel_type": "email",
      "target": "me@example.com",
      "is_enabled": true
    },
    "references": {
      "user": {"id": "user-1"},
      "org": {"id": "org-1"}
    }
  }
}`

// projectScopedChannelConfigResponse mirrors
// TestV3ChannelConfigByIDProjectScope's project + org references, with no user
// reference.
const projectScopedChannelConfigResponse = `{
  "data": {
    "id": "cc-2",
    "attributes": {
      "channel_type": "slack",
      "target": "C0123456789",
      "channel_name": "#builds",
      "is_enabled": false
    },
    "references": {
      "project": {"id": "project-1"},
      "org": {"id": "org-1"}
    }
  }
}`

func TestGetNotificationChannelConfigUserScope(t *testing.T) {
	t.Parallel()

	srv, calls := newChannelConfigServer(t, http.StatusOK, userScopedChannelConfigResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	cc, err := c.GetNotificationChannelConfig(context.Background(), "cc-1")
	if err != nil {
		t.Fatalf("GetNotificationChannelConfig returned error: %v", err)
	}

	got := (*calls)[0]
	if want := "/api/v3/notification/channel-configs/cc-1"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	if cc.ID != "cc-1" {
		t.Errorf("ID = %q, want %q", cc.ID, "cc-1")
	}
	if cc.Scope != circleci.NotificationScopeUser {
		t.Errorf("Scope = %q, want %q: a user reference with no project reference means user scope", cc.Scope, circleci.NotificationScopeUser)
	}
	if cc.UserID != "user-1" {
		t.Errorf("UserID = %q, want %q", cc.UserID, "user-1")
	}
	if cc.OrgID != "org-1" {
		t.Errorf("OrgID = %q, want %q", cc.OrgID, "org-1")
	}
	if cc.Target != "me@example.com" {
		t.Errorf("Target = %q, want %q", cc.Target, "me@example.com")
	}
	if !cc.IsEnabled {
		t.Error("IsEnabled = false, want true")
	}
	if cc.ChannelName != "" {
		t.Errorf("ChannelName = %q, want empty: email configs carry no channel_name", cc.ChannelName)
	}
}

func TestGetNotificationChannelConfigProjectScope(t *testing.T) {
	t.Parallel()

	srv, _ := newChannelConfigServer(t, http.StatusOK, projectScopedChannelConfigResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	cc, err := c.GetNotificationChannelConfig(context.Background(), "cc-2")
	if err != nil {
		t.Fatalf("GetNotificationChannelConfig returned error: %v", err)
	}

	if cc.Scope != circleci.NotificationScopeProject {
		t.Errorf("Scope = %q, want %q", cc.Scope, circleci.NotificationScopeProject)
	}
	if cc.ProjectID != "project-1" {
		t.Errorf("ProjectID = %q, want %q", cc.ProjectID, "project-1")
	}
	if cc.UserID != "" {
		t.Errorf("UserID = %q, want empty for a project-scoped config", cc.UserID)
	}
	if cc.ChannelName != "#builds" {
		t.Errorf("ChannelName = %q, want %q: project-scoped Slack configs resolve a channel_name", cc.ChannelName, "#builds")
	}
}

func TestGetNotificationChannelConfigNotFound(t *testing.T) {
	t.Parallel()

	// A missing or denied config answers with {"error":{"id","title"}} and no
	// "detail" field.
	srv, _ := newChannelConfigServer(t, http.StatusNotFound, `{"error":{"id":"trace-1","title":"Not found."}}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetNotificationChannelConfig(context.Background(), "missing")
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestCreateNotificationChannelConfigUserScope(t *testing.T) {
	t.Parallel()

	srv, calls := newChannelConfigServer(t, http.StatusOK, userScopedChannelConfigResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.CreateNotificationChannelConfig(context.Background(), circleci.CreateNotificationChannelConfigRequest{
		Scope:       circleci.NotificationScopeUser,
		ChannelType: circleci.NotificationChannelTypeEmail,
		Target:      "me@example.com",
		IsEnabled:   true,
		OrgID:       "org-1",
	})
	if err != nil {
		t.Fatalf("CreateNotificationChannelConfig returned error: %v", err)
	}

	got := (*calls)[0]
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if want := "/api/v3/notification/channel-configs"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	// The wire body must match the create route's fixture exactly:
	// data.attributes.scope, no data.references.project, and data.references.org.
	var sent struct {
		Data struct {
			Attributes struct {
				Scope       string `json:"scope"`
				ChannelType string `json:"channel_type"`
				Target      string `json:"target"`
				IsEnabled   bool   `json:"is_enabled"`
			} `json:"attributes"`
			References struct {
				Project *struct {
					ID string `json:"id"`
				} `json:"project"`
				Org struct {
					ID string `json:"id"`
				} `json:"org"`
			} `json:"references"`
		} `json:"data"`
	}
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}

	if sent.Data.Attributes.Scope != "user" {
		t.Errorf("scope = %q, want %q", sent.Data.Attributes.Scope, "user")
	}
	if sent.Data.Attributes.ChannelType != "email" {
		t.Errorf("channel_type = %q, want %q", sent.Data.Attributes.ChannelType, "email")
	}
	if sent.Data.References.Project != nil {
		t.Error("references.project was sent for a user-scoped config, want it omitted")
	}
	if sent.Data.References.Org.ID != "org-1" {
		t.Errorf("references.org.id = %q, want %q", sent.Data.References.Org.ID, "org-1")
	}
}

func TestCreateNotificationChannelConfigProjectScope(t *testing.T) {
	t.Parallel()

	srv, calls := newChannelConfigServer(t, http.StatusOK, projectScopedChannelConfigResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.CreateNotificationChannelConfig(context.Background(), circleci.CreateNotificationChannelConfigRequest{
		Scope:       circleci.NotificationScopeProject,
		ChannelType: circleci.NotificationChannelTypeSlack,
		Target:      "C0123456789",
		IsEnabled:   false,
		ProjectID:   "project-1",
		OrgID:       "org-1",
	})
	if err != nil {
		t.Fatalf("CreateNotificationChannelConfig returned error: %v", err)
	}

	var sent struct {
		Data struct {
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
	if err := json.Unmarshal((*calls)[0].body, &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}

	if sent.Data.References.Project.ID != "project-1" {
		t.Errorf("references.project.id = %q, want %q", sent.Data.References.Project.ID, "project-1")
	}
	if sent.Data.References.Org.ID != "org-1" {
		t.Errorf("references.org.id = %q, want %q", sent.Data.References.Org.ID, "org-1")
	}
}

func TestUpdateNotificationChannelConfigRouteAndBody(t *testing.T) {
	t.Parallel()

	srv, calls := newChannelConfigServer(t, http.StatusOK, userScopedChannelConfigResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.UpdateNotificationChannelConfig(context.Background(), "cc-1", "new@example.com", false)
	if err != nil {
		t.Fatalf("UpdateNotificationChannelConfig returned error: %v", err)
	}

	got := (*calls)[0]
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST: v3 spells partial updates as POST to a /update route", got.method)
	}
	if want := "/api/v3/notification/channel-configs/cc-1/update"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	var sent struct {
		Data struct {
			Attributes struct {
				Target    string `json:"target"`
				IsEnabled bool   `json:"is_enabled"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if sent.Data.Attributes.Target != "new@example.com" {
		t.Errorf("target = %q, want %q", sent.Data.Attributes.Target, "new@example.com")
	}
	if sent.Data.Attributes.IsEnabled {
		t.Error("is_enabled = true, want false")
	}
}

func TestDeleteNotificationChannelConfig(t *testing.T) {
	t.Parallel()

	srv, calls := newChannelConfigServer(t, http.StatusNoContent, "")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if err := c.DeleteNotificationChannelConfig(context.Background(), "cc-1"); err != nil {
		t.Fatalf("DeleteNotificationChannelConfig returned error: %v", err)
	}

	got := (*calls)[0]
	if got.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", got.method)
	}
	if want := "/api/v3/notification/channel-configs/cc-1"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
}

func TestListNotificationChannelConfigsFilters(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Query().Get("filter[scope]"), "project"; got != want {
			t.Errorf("filter[scope] = %q, want %q", got, want)
		}
		if got, want := r.URL.Query().Get("filter[project_id]"), "project-1"; got != want {
			t.Errorf("filter[project_id] = %q, want %q", got, want)
		}
		if got, want := r.URL.Query().Get("filter[org_id]"), "org-1"; got != want {
			t.Errorf("filter[org_id] = %q, want %q", got, want)
		}

		w.Header().Set("Content-Type", "application/json")
		// A single-page v3 collection omits "page" entirely.
		_, _ = io.WriteString(w, `{"data":[`+trimEnvelope(projectScopedChannelConfigResponse)+`]}`)
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	configs, err := c.ListNotificationChannelConfigs(context.Background(), circleci.ListNotificationChannelConfigsOptions{
		Scope:     circleci.NotificationScopeProject,
		ProjectID: "project-1",
		OrgID:     "org-1",
	})
	if err != nil {
		t.Fatalf("ListNotificationChannelConfigs returned error: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("got %d configs, want 1", len(configs))
	}
	if configs[0].ID != "cc-2" {
		t.Errorf("ID = %q, want %q", configs[0].ID, "cc-2")
	}
}

// trimEnvelope extracts the {"data": ...} payload's data value as a raw JSON
// object, so a single-entity fixture can be reused inside a collection.
func trimEnvelope(entityJSON string) string {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(entityJSON), &env); err != nil {
		panic(err)
	}

	return string(env.Data)
}
