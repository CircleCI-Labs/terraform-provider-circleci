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

// slackIntegrationResponse is a literal fixture matching
// slackintegration.toSlackIntegrationEntity: attributes.type/workspace_name/
// team_id/status/created_at/updated_at, and an org reference carrying the
// org's name as an attribute (unlike the bare {"id"} references channel
// configs and preferences use).
const slackIntegrationResponse = `{
  "data": {
    "id": "si-1",
    "attributes": {
      "type": "slack",
      "workspace_name": "alpha-team",
      "team_id": "T-alpha",
      "status": "active",
      "created_at": "2026-01-02T03:04:05.000Z",
      "updated_at": "2026-01-02T03:04:05.000Z"
    },
    "references": {
      "org": {
        "id": "org-1",
        "attributes": {"name": "org-alpha"}
      }
    }
  }
}`

func TestGetNotificationIntegration(t *testing.T) {
	t.Parallel()

	srv, calls := newSettingsServer(t, http.StatusOK, slackIntegrationResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	integration, err := c.GetNotificationIntegration(context.Background(), "si-1")
	if err != nil {
		t.Fatalf("GetNotificationIntegration returned error: %v", err)
	}

	got := (*calls)[0]
	if want := "/api/v3/notification/integrations/si-1"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	if integration.ID != "si-1" {
		t.Errorf("ID = %q, want %q", integration.ID, "si-1")
	}
	if integration.Type != circleci.NotificationIntegrationTypeSlack {
		t.Errorf("Type = %q, want %q", integration.Type, circleci.NotificationIntegrationTypeSlack)
	}
	if integration.WorkspaceName != "alpha-team" {
		t.Errorf("WorkspaceName = %q, want %q", integration.WorkspaceName, "alpha-team")
	}
	if integration.Status != circleci.NotificationIntegrationStatusActive {
		t.Errorf("Status = %q, want %q", integration.Status, circleci.NotificationIntegrationStatusActive)
	}
	if integration.OrgID != "org-1" {
		t.Errorf("OrgID = %q, want %q", integration.OrgID, "org-1")
	}
	if integration.OrgName != "org-alpha" {
		t.Errorf("OrgName = %q, want %q: the org reference carries a name attribute", integration.OrgName, "org-alpha")
	}
	if integration.CreatedAt != "2026-01-02T03:04:05.000Z" {
		t.Errorf("CreatedAt = %q, want the millisecond-precision UTC timestamp unchanged", integration.CreatedAt)
	}
}

func TestListNotificationIntegrationsFilters(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Query().Get("filter[org_id]"), "org-1"; got != want {
			t.Errorf("filter[org_id] = %q, want %q", got, want)
		}
		if got, want := r.URL.Query().Get("filter[type]"), "slack"; got != want {
			t.Errorf("filter[type] = %q, want %q", got, want)
		}

		var env struct {
			Data json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal([]byte(slackIntegrationResponse), &env)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[` + string(env.Data) + `]}`))
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	integrations, err := c.ListNotificationIntegrations(context.Background(), circleci.ListNotificationIntegrationsOptions{
		OrgID: "org-1",
		Type:  circleci.NotificationIntegrationTypeSlack,
	})
	if err != nil {
		t.Fatalf("ListNotificationIntegrations returned error: %v", err)
	}
	if len(integrations) != 1 {
		t.Fatalf("got %d integrations, want 1", len(integrations))
	}
	if integrations[0].ID != "si-1" {
		t.Errorf("ID = %q, want %q", integrations[0].ID, "si-1")
	}
}

func TestSetNotificationIntegrationStatus(t *testing.T) {
	t.Parallel()

	srv, calls := newSettingsServer(t, http.StatusOK, slackIntegrationResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.SetNotificationIntegrationStatus(context.Background(), "si-1", circleci.NotificationIntegrationStatusActive)
	if err != nil {
		t.Fatalf("SetNotificationIntegrationStatus returned error: %v", err)
	}

	got := (*calls)[0]
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if want := "/api/v3/notification/integrations/si-1/set-status"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	var sent struct {
		Data struct {
			Attributes struct {
				Status string `json:"status"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if sent.Data.Attributes.Status != "active" {
		t.Errorf("status = %q, want %q", sent.Data.Attributes.Status, "active")
	}
}

func TestDeleteNotificationIntegration(t *testing.T) {
	t.Parallel()

	srv, calls := newSettingsServer(t, http.StatusNoContent, "")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if err := c.DeleteNotificationIntegration(context.Background(), "si-1"); err != nil {
		t.Fatalf("DeleteNotificationIntegration returned error: %v", err)
	}

	got := (*calls)[0]
	if got.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", got.method)
	}
	if want := "/api/v3/notification/integrations/si-1"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
}

func TestGetNotificationIntegrationNotFound(t *testing.T) {
	t.Parallel()

	// A revoked integration is excluded from lookups entirely, surfacing as a
	// plain 404 like any other missing id.
	srv, _ := newSettingsServer(t, http.StatusNotFound, `{"error":{"id":"trace-1","title":"Not found."}}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetNotificationIntegration(context.Background(), "revoked")
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}
