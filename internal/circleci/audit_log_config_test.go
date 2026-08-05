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

const (
	testAuditLogOrgID  = "b9291e0d-a11e-41fb-8517-c545388b5953"
	testAuditLogID     = "123e4127-e89b-12d3-a456-426123417400"
	testAuditLogUserID = "223e4127-e89b-12d3-a456-426123417400"
)

// auditLogConfigBody is a single config, in the shape the API accepts: a
// flat object with no data/attributes envelope, "items" only wrapping a
// list.
const auditLogConfigBody = `{
  "id": "123e4127-e89b-12d3-a456-426123417400",
  "org_id": "b9291e0d-a11e-41fb-8517-c545388b5953",
  "target_type": "S3",
  "is_disabled": false,
  "config": {
    "arn": "arn:aws:iam::123456789012:role/circleci-audit-logs",
    "region": "us-east-1",
    "bucket_name": "acme-audit-logs",
    "bucket_prefix": "circleci",
    "endpoint": ""
  },
  "created_by": "223e4127-e89b-12d3-a456-426123417400",
  "created_at": "2024-01-02T03:04:05Z",
  "updated_at": "2024-01-03T04:05:06Z",
  "connection_status": "CONNECTED"
}`

func TestListAuditLogConfigsRoute(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, `{"items": [`+auditLogConfigBody+`]}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	configs, err := c.ListAuditLogConfigs(context.Background(), testAuditLogOrgID)
	if err != nil {
		t.Fatalf("ListAuditLogConfigs returned error: %v", err)
	}

	wantURI := "/api/v2/organizations/" + testAuditLogOrgID + "/audit-log/configs"
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", rec.requestURI, wantURI)
	}

	if len(configs) != 1 {
		t.Fatalf("len(configs) = %d, want 1", len(configs))
	}

	got := configs[0]
	if got.ID != testAuditLogID {
		t.Errorf("ID = %q, want %q", got.ID, testAuditLogID)
	}
	if got.TargetType != circleci.AuditLogTargetTypeS3 {
		t.Errorf("TargetType = %q, want %q", got.TargetType, circleci.AuditLogTargetTypeS3)
	}
	if got.ConnectionStatus != circleci.AuditLogConnectionStatusConnected {
		t.Errorf("ConnectionStatus = %q, want %q", got.ConnectionStatus, circleci.AuditLogConnectionStatusConnected)
	}
	if got.Config.BucketName != "acme-audit-logs" {
		t.Errorf("Config.BucketName = %q, want %q", got.Config.BucketName, "acme-audit-logs")
	}
}

func TestListAuditLogConfigsEmpty(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusOK, `{"items": []}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	configs, err := c.ListAuditLogConfigs(context.Background(), testAuditLogOrgID)
	if err != nil {
		t.Fatalf("ListAuditLogConfigs returned error: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("len(configs) = %d, want 0", len(configs))
	}
}

// TestGetAuditLogConfigRoute pins the single-config route: it is NOT nested
// under /organizations/{org_id}/..., unlike list and create.
func TestGetAuditLogConfigRoute(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, auditLogConfigBody)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	config, err := c.GetAuditLogConfig(context.Background(), testAuditLogID)
	if err != nil {
		t.Fatalf("GetAuditLogConfig returned error: %v", err)
	}

	wantURI := "/api/v2/audit-log/configs/" + testAuditLogID
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", rec.requestURI, wantURI)
	}
	if config.OrgID != testAuditLogOrgID {
		t.Errorf("OrgID = %q, want %q", config.OrgID, testAuditLogOrgID)
	}
	if config.CreatedBy != testAuditLogUserID {
		t.Errorf("CreatedBy = %q, want %q", config.CreatedBy, testAuditLogUserID)
	}
}

// TestGetAuditLogConfigNotFound covers the anti-enumeration behaviour: a
// missing config and an unauthorized one both answer 404, so IsNotFound
// alone drives drift detection.
func TestGetAuditLogConfigNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusNotFound, `{"message":"Resource does not exist or unauthorized"}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetAuditLogConfig(context.Background(), testAuditLogID)
	if err == nil {
		t.Fatal("GetAuditLogConfig returned no error for a 404")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestCreateAuditLogConfigPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		req      circleci.CreateAuditLogConfigRequest
		wantBody map[string]any
	}{
		{
			name: "S3",
			req: circleci.CreateAuditLogConfigRequest{
				OrgID:      testAuditLogOrgID,
				TargetType: circleci.AuditLogTargetTypeS3,
				IsDisabled: false,
				Config: circleci.AuditLogS3Config{
					ARN:          "arn:aws:iam::123456789012:role/circleci-audit-logs",
					Region:       "us-east-1",
					BucketName:   "acme-audit-logs",
					BucketPrefix: "circleci",
				},
			},
			wantBody: map[string]any{
				"target_type": "S3",
				"is_disabled": false,
				"purpose":     "audit-logs",
				"config": map[string]any{
					"arn":           "arn:aws:iam::123456789012:role/circleci-audit-logs",
					"region":        "us-east-1",
					"bucket_name":   "acme-audit-logs",
					"bucket_prefix": "circleci",
				},
			},
		},
		{
			name: "S3_COMPATIBLE, created disabled",
			req: circleci.CreateAuditLogConfigRequest{
				OrgID:      testAuditLogOrgID,
				TargetType: circleci.AuditLogTargetTypeS3Compatible,
				IsDisabled: true,
				Config: circleci.AuditLogS3Config{
					ARN:        "arn:minio:iam::role/circleci-audit-logs",
					BucketName: "acme-audit-logs",
					Endpoint:   "https://minio.example.com",
				},
			},
			wantBody: map[string]any{
				"target_type": "S3_COMPATIBLE",
				"is_disabled": true,
				"purpose":     "audit-logs",
				"config": map[string]any{
					"arn":         "arn:minio:iam::role/circleci-audit-logs",
					"bucket_name": "acme-audit-logs",
					"endpoint":    "https://minio.example.com",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var (
				gotMethod string
				gotURI    string
				gotBody   map[string]any
			)

			srv := newGovernanceServer(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotURI = r.RequestURI
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				// The handler answers 200, not 201, on a successful create.
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(auditLogConfigBody))
			})
			c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

			config, err := c.CreateAuditLogConfig(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("CreateAuditLogConfig returned error: %v", err)
			}

			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			wantURI := "/api/v2/organizations/" + testAuditLogOrgID + "/audit-log/configs"
			if gotURI != wantURI {
				t.Errorf("raw request URI = %q, want %q", gotURI, wantURI)
			}
			if !governanceJSONEqual(gotBody, tc.wantBody) {
				t.Errorf("request body = %#v, want %#v", gotBody, tc.wantBody)
			}
			if config.ID != testAuditLogID {
				t.Errorf("ID = %q, want the id from the response", config.ID)
			}
		})
	}
}

// TestUpdateAuditLogConfigPayload pins the update route: a PUT to the bare
// collection with the id in the body, not a per-id PUT.
func TestUpdateAuditLogConfigPayload(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotURI    string
		gotBody   map[string]any
	)

	srv := newGovernanceServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURI = r.RequestURI
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(auditLogConfigBody))
	})
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.UpdateAuditLogConfig(context.Background(), circleci.UpdateAuditLogConfigRequest{
		ID:         testAuditLogID,
		OrgID:      testAuditLogOrgID,
		TargetType: circleci.AuditLogTargetTypeS3,
		IsDisabled: true,
		Config: circleci.AuditLogS3Config{
			ARN:        "arn:aws:iam::123456789012:role/circleci-audit-logs",
			Region:     "us-east-1",
			BucketName: "acme-audit-logs",
		},
	})
	if err != nil {
		t.Fatalf("UpdateAuditLogConfig returned error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotURI != "/api/v2/audit-log/configs" {
		t.Errorf("raw request URI = %q, want %q (the bare collection, no id in the path)", gotURI, "/api/v2/audit-log/configs")
	}

	wantBody := map[string]any{
		"id":          testAuditLogID,
		"org_id":      testAuditLogOrgID,
		"target_type": "S3",
		"is_disabled": true,
		"purpose":     "audit-logs",
		"config": map[string]any{
			"arn":         "arn:aws:iam::123456789012:role/circleci-audit-logs",
			"region":      "us-east-1",
			"bucket_name": "acme-audit-logs",
		},
	}
	if !governanceJSONEqual(gotBody, wantBody) {
		t.Errorf("request body = %#v, want %#v", gotBody, wantBody)
	}
}

func TestDeleteAuditLogConfigRoute(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusNoContent, ``)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if err := c.DeleteAuditLogConfig(context.Background(), testAuditLogID); err != nil {
		t.Fatalf("DeleteAuditLogConfig returned error: %v", err)
	}

	wantURI := "/api/v2/audit-log/configs/" + testAuditLogID
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", rec.requestURI, wantURI)
	}
}

func TestGetAuditLogAccessRoute(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, `{"has_access": true}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	hasAccess, err := c.GetAuditLogAccess(context.Background(), testAuditLogOrgID)
	if err != nil {
		t.Fatalf("GetAuditLogAccess returned error: %v", err)
	}

	wantURI := "/api/v2/organizations/" + testAuditLogOrgID + "/audit-log/access"
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", rec.requestURI, wantURI)
	}
	if !hasAccess {
		t.Error("hasAccess = false, want true")
	}
}

func TestGetAuditLogAccessDenied(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusOK, `{"has_access": false}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	hasAccess, err := c.GetAuditLogAccess(context.Background(), testAuditLogOrgID)
	if err != nil {
		t.Fatalf("GetAuditLogAccess returned error: %v", err)
	}
	if hasAccess {
		t.Error("hasAccess = true, want false")
	}
}

func TestDeleteAuditLogConfigNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusNotFound, `{"message":"Resource does not exist or unauthorized"}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	err := c.DeleteAuditLogConfig(context.Background(), testAuditLogID)
	if err == nil {
		t.Fatal("DeleteAuditLogConfig returned no error for a 404")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}
