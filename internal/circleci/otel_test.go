// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const testOTelOrgID = "b9291e0d-a11e-41fb-8517-c545388b5953"

// otelExporterListBody is a two-element list response. Note that it is a bare
// JSON array with no items/next_page_token envelope, unlike most v2 collections.
const otelExporterListBody = `[
  {
    "id": "123e4127-e89b-12d3-a456-426123417400",
    "org_id": "b9291e0d-a11e-41fb-8517-c545388b5953",
    "endpoint": "otel.example.com:4317",
    "protocol": "grpc",
    "insecure": false,
    "headers": {"x-api-key": "xxxx"}
  },
  {
    "id": "223e4127-e89b-12d3-a456-426123417400",
    "org_id": "b9291e0d-a11e-41fb-8517-c545388b5953",
    "endpoint": "otel-2.example.com:4318",
    "protocol": "http",
    "insecure": true,
    "issues": ["endpoint does not resolve"]
  }
]`

// TestListOTelExportersScopesByQueryParameter pins the org-id *query* parameter.
// Nearly every other v2 collection is scoped by a path segment, and the
// parameter is hyphenated (org-id) where the request body uses org_id.
func TestListOTelExportersScopesByQueryParameter(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, otelExporterListBody)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	exporters, err := c.ListOTelExporters(context.Background(), testOTelOrgID)
	if err != nil {
		t.Fatalf("ListOTelExporters returned error: %v", err)
	}

	wantURI := "/api/v2/otel/exporters?org-id=" + testOTelOrgID
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", rec.requestURI, wantURI)
	}

	if got, want := len(exporters), 2; got != want {
		t.Fatalf("len(exporters) = %d, want %d", got, want)
	}

	first := exporters[0]
	if first.ID != "123e4127-e89b-12d3-a456-426123417400" {
		t.Errorf("ID = %q, want the id from the body", first.ID)
	}
	if first.Endpoint != "otel.example.com:4317" {
		t.Errorf("Endpoint = %q, want %q", first.Endpoint, "otel.example.com:4317")
	}
	if first.Protocol != circleci.OTelProtocolGRPC {
		t.Errorf("Protocol = %q, want %q", first.Protocol, circleci.OTelProtocolGRPC)
	}
	if first.Insecure {
		t.Error("Insecure = true, want false")
	}
	// Header values are never disclosed: every read answers with the redaction
	// placeholder, which is why the provider must not refresh them from the API.
	if got, want := first.Headers["x-api-key"], circleci.OTelRedactedHeaderValue; got != want {
		t.Errorf("Headers[\"x-api-key\"] = %q, want the redaction placeholder %q", got, want)
	}

	second := exporters[1]
	if !second.Insecure {
		t.Error("Insecure = false on the second exporter, want true")
	}
	if got, want := second.Issues, []string{"endpoint does not resolve"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Issues = %#v, want %#v", got, want)
	}
	if second.Headers != nil {
		t.Errorf("Headers = %#v, want nil when the response omits them", second.Headers)
	}
}

func TestListOTelExportersEmpty(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusOK, `[]`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	exporters, err := c.ListOTelExporters(context.Background(), testOTelOrgID)
	if err != nil {
		t.Fatalf("ListOTelExporters returned error: %v", err)
	}
	if len(exporters) != 0 {
		t.Errorf("len(exporters) = %d, want 0", len(exporters))
	}
}

// TestGetOTelExporterFiltersTheList covers the missing route: the API has no
// GET for a single exporter, so a read lists the organization's exporters and
// selects from them.
func TestGetOTelExporterFiltersTheList(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, otelExporterListBody)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	exporter, err := c.GetOTelExporter(context.Background(),
		testOTelOrgID, "223e4127-e89b-12d3-a456-426123417400")
	if err != nil {
		t.Fatalf("GetOTelExporter returned error: %v", err)
	}

	// The read goes to the collection, not to /otel/exporters/{id}.
	wantURI := "/api/v2/otel/exporters?org-id=" + testOTelOrgID
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", rec.requestURI, wantURI)
	}
	if exporter.Endpoint != "otel-2.example.com:4318" {
		t.Errorf("Endpoint = %q, want the second exporter's", exporter.Endpoint)
	}
}

// TestGetOTelExporterNotFound covers drift detection: an exporter that is no
// longer in the list must be reported as ErrNotFound, so IsNotFound can drop it
// from state.
func TestGetOTelExporterNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusOK, otelExporterListBody)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetOTelExporter(context.Background(), testOTelOrgID, "no-such-exporter")
	if err == nil {
		t.Fatal("GetOTelExporter returned no error for an absent exporter")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestCreateOTelExporterPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		req      circleci.CreateOTelExporterRequest
		wantBody map[string]any
	}{
		{
			name: "with headers",
			req: circleci.CreateOTelExporterRequest{
				OrgID:    testOTelOrgID,
				Endpoint: "otel.example.com:4317",
				Protocol: circleci.OTelProtocolGRPC,
				Insecure: false,
				Headers:  map[string]string{"x-api-key": "secret"},
			},
			wantBody: map[string]any{
				"org_id":   testOTelOrgID,
				"endpoint": "otel.example.com:4317",
				"protocol": "grpc",
				"insecure": false,
				"headers":  map[string]any{"x-api-key": "secret"},
			},
		},
		{
			name: "without headers",
			req: circleci.CreateOTelExporterRequest{
				OrgID:    testOTelOrgID,
				Endpoint: "otel.example.com:4318",
				Protocol: circleci.OTelProtocolHTTP,
				Insecure: true,
			},
			wantBody: map[string]any{
				"org_id":   testOTelOrgID,
				"endpoint": "otel.example.com:4318",
				"protocol": "http",
				"insecure": true,
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
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{
				  "id": "123e4127-e89b-12d3-a456-426123417400",
				  "org_id": "b9291e0d-a11e-41fb-8517-c545388b5953",
				  "endpoint": "otel.example.com:4317",
				  "protocol": "grpc",
				  "insecure": false,
				  "headers": {"x-api-key": "xxxx"}
				}`))
			})
			c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

			exporter, err := c.CreateOTelExporter(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("CreateOTelExporter returned error: %v", err)
			}

			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			// The create route takes no org-id query parameter: the organization
			// travels in the body.
			if gotURI != "/api/v2/otel/exporters" {
				t.Errorf("raw request URI = %q, want %q", gotURI, "/api/v2/otel/exporters")
			}
			if !governanceJSONEqual(gotBody, tc.wantBody) {
				t.Errorf("request body = %#v, want %#v", gotBody, tc.wantBody)
			}
			if exporter.ID != "123e4127-e89b-12d3-a456-426123417400" {
				t.Errorf("ID = %q, want the id from the response", exporter.ID)
			}
		})
	}
}

func TestDeleteOTelExporterRoute(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusNoContent, ``)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	err := c.DeleteOTelExporter(context.Background(), "123e4127-e89b-12d3-a456-426123417400")
	if err != nil {
		t.Fatalf("DeleteOTelExporter returned error: %v", err)
	}

	// No org-id: the exporter ID alone identifies it.
	wantURI := "/api/v2/otel/exporters/123e4127-e89b-12d3-a456-426123417400"
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", rec.requestURI, wantURI)
	}
}

func TestDeleteOTelExporterNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusNotFound, `{"message":"Exporter not found"}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	err := c.DeleteOTelExporter(context.Background(), "123e4127-e89b-12d3-a456-426123417400")
	if err == nil {
		t.Fatal("DeleteOTelExporter returned no error for a 404")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestOTelExporterLimit(t *testing.T) {
	t.Parallel()

	if circleci.OTelExporterLimit != 5 {
		t.Errorf("OTelExporterLimit = %d, want 5", circleci.OTelExporterLimit)
	}
}
