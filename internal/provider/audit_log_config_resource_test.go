// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const testAuditLogConfigOrg = "b9291e0d-a11e-41fb-8517-c545388b5953"

// auditLogConfigAPI is an in-memory stand-in for the audit-log/configs v2
// endpoints.
type auditLogConfigAPI struct {
	mu       sync.Mutex
	configs  []map[string]any
	requests []string
	created  int
	// connectionFails makes create/update answer 400, simulating an
	// unreachable destination.
	connectionFails bool
	// hasAccess answers the .../audit-log/access route.
	hasAccess bool
	// updateStatus is the connection_status an update reports. The real API
	// re-verifies connectivity during an update whenever is_disabled is false, so
	// this genuinely can differ from what the last read returned. Every fixture here
	// used to hardcode CONNECTED on both create and update, which is exactly why a
	// plan carrying the stale value forward never failed in tests.
	updateStatus string
}

// setUpdateStatus makes the next update report a different connection_status.
func (a *auditLogConfigAPI) setUpdateStatus(status string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updateStatus = status
}

func newAuditLogConfigServer(t *testing.T, api *auditLogConfigAPI) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.requests = append(api.requests, r.Method+" "+r.RequestURI)
		api.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		orgPrefix := "/api/v2/organizations/" + testAuditLogConfigOrg + "/audit-log/configs"
		accessRoute := "/api/v2/organizations/" + testAuditLogConfigOrg + "/audit-log/access"
		bareRoute := "/api/v2/audit-log/configs"

		switch {
		case r.Method == http.MethodPost && r.URL.Path == orgPrefix:
			api.handleCreate(t, w, r)
		case r.Method == http.MethodGet && r.URL.Path == orgPrefix:
			api.handleList(w)
		case r.Method == http.MethodGet && r.URL.Path == accessRoute:
			api.handleAccess(w)
		case r.Method == http.MethodPut && r.URL.Path == bareRoute:
			api.handleUpdate(t, w, r)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, bareRoute+"/"):
			api.handleGet(w, strings.TrimPrefix(r.URL.Path, bareRoute+"/"))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, bareRoute+"/"):
			api.handleDelete(w, strings.TrimPrefix(r.URL.Path, bareRoute+"/"))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

type auditLogConfigWriteBody struct {
	ID         string         `json:"id"`
	OrgID      string         `json:"org_id"`
	TargetType string         `json:"target_type"`
	IsDisabled bool           `json:"is_disabled"`
	Config     map[string]any `json:"config"`
	Purpose    string         `json:"purpose"`
}

func (a *auditLogConfigAPI) handleCreate(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	var body auditLogConfigWriteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("create body is not JSON: %v", err)
	}
	if body.Purpose != "audit-logs" {
		t.Errorf("create body purpose = %q, want %q", body.Purpose, "audit-logs")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.connectionFails {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Failed to connect to S3. Please check your configuration and try again."}`))

		return
	}

	a.created++
	id := fmt.Sprintf("00000000-0000-0000-0000-00000000000%d", a.created)

	config := map[string]any{
		"id":                id,
		"org_id":            testAuditLogConfigOrg,
		"target_type":       body.TargetType,
		"is_disabled":       body.IsDisabled,
		"config":            body.Config,
		"created_by":        "11111111-1111-1111-1111-111111111111",
		"created_at":        "2024-01-02T03:04:05Z",
		"updated_at":        "2024-01-02T03:04:05Z",
		"connection_status": "CONNECTED",
	}
	a.configs = append(a.configs, config)

	// The handler answers 200, not 201, on a successful create.
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(config)
}

func (a *auditLogConfigAPI) handleList(w http.ResponseWriter) {
	a.mu.Lock()
	defer a.mu.Unlock()

	_ = json.NewEncoder(w).Encode(map[string]any{"items": a.configs})
}

func (a *auditLogConfigAPI) handleAccess(w http.ResponseWriter) {
	a.mu.Lock()
	defer a.mu.Unlock()

	_ = json.NewEncoder(w).Encode(map[string]any{"has_access": a.hasAccess})
}

func (a *auditLogConfigAPI) handleGet(w http.ResponseWriter, id string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, config := range a.configs {
		if config["id"] == id {
			_ = json.NewEncoder(w).Encode(config)

			return
		}
	}

	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"message":"Resource does not exist or unauthorized"}`))
}

func (a *auditLogConfigAPI) handleUpdate(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	var body auditLogConfigWriteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("update body is not JSON: %v", err)
	}
	if body.Purpose != "audit-logs" {
		t.Errorf("update body purpose = %q, want %q", body.Purpose, "audit-logs")
	}
	if body.ID == "" {
		t.Error("update body carried no id; the update route has no id in the path")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Connectivity is only re-verified when is_disabled is false, unlike
	// create which always verifies it.
	if a.connectionFails && !body.IsDisabled {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Failed to connect to S3. Please check your configuration and try again."}`))

		return
	}

	for i, config := range a.configs {
		if config["id"] != body.ID {
			continue
		}

		updated := map[string]any{
			"id":                body.ID,
			"org_id":            config["org_id"],
			"target_type":       body.TargetType,
			"is_disabled":       body.IsDisabled,
			"config":            body.Config,
			"created_by":        config["created_by"],
			"created_at":        config["created_at"],
			"updated_at":        "2024-02-03T04:05:06Z",
			"connection_status": a.connectionStatusForUpdate(),
		}
		a.configs[i] = updated

		_ = json.NewEncoder(w).Encode(updated)

		return
	}

	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"message":"Resource does not exist or unauthorized"}`))
}

func (a *auditLogConfigAPI) handleDelete(w http.ResponseWriter, id string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i, config := range a.configs {
		if config["id"] == id {
			a.configs = append(a.configs[:i], a.configs[i+1:]...)
			w.WriteHeader(http.StatusNoContent)

			return
		}
	}

	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"message":"Resource does not exist or unauthorized"}`))
}

// recorded returns the request lines seen so far.
func (a *auditLogConfigAPI) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.requests...)
}

// removeAll deletes every config, simulating removal outside Terraform.
func (a *auditLogConfigAPI) removeAll() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.configs = nil
}

func auditLogConfigProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
`, host)
}

func auditLogConfigResourceConfig(host, targetType, region, endpoint string, isDisabled bool) string {
	extra := ""
	if region != "" {
		extra += fmt.Sprintf("  region        = %q\n", region)
	}
	if endpoint != "" {
		extra += fmt.Sprintf("  endpoint      = %q\n", endpoint)
	}

	return auditLogConfigProviderConfig(host) + fmt.Sprintf(`
resource "circleci_audit_log_config" "test" {
  organization_id = %q
  target_type     = %q
  is_disabled     = %t
  arn             = "arn:aws:iam::123456789012:role/circleci-audit-logs"
  bucket_name     = "acme-audit-logs"
%s}
`, testAuditLogConfigOrg, targetType, isDisabled, extra)
}

func TestAccAuditLogConfigResource(t *testing.T) {
	api := &auditLogConfigAPI{}
	srv := newAuditLogConfigServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: auditLogConfigResourceConfig(srv.URL, "S3", "us-east-1", "", false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_audit_log_config.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact("00000000-0000-0000-0000-000000000001"),
					),
					statecheck.ExpectKnownValue(
						"circleci_audit_log_config.test",
						tfjsonpath.New("target_type"),
						knownvalue.StringExact("S3"),
					),
					statecheck.ExpectKnownValue(
						"circleci_audit_log_config.test",
						tfjsonpath.New("region"),
						knownvalue.StringExact("us-east-1"),
					),
					statecheck.ExpectKnownValue(
						"circleci_audit_log_config.test",
						tfjsonpath.New("is_disabled"),
						knownvalue.Bool(false),
					),
					statecheck.ExpectKnownValue(
						"circleci_audit_log_config.test",
						tfjsonpath.New("connection_status"),
						knownvalue.StringExact("CONNECTED"),
					),
					statecheck.ExpectKnownValue(
						"circleci_audit_log_config.test",
						tfjsonpath.New("endpoint"),
						knownvalue.Null(),
					),
				},
			},
			// Changing is_disabled must be a genuine in-place update, not a
			// replacement: the id must stay the same.
			{
				Config: auditLogConfigResourceConfig(srv.URL, "S3", "us-east-1", "", true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_audit_log_config.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact("00000000-0000-0000-0000-000000000001"),
					),
					statecheck.ExpectKnownValue(
						"circleci_audit_log_config.test",
						tfjsonpath.New("is_disabled"),
						knownvalue.Bool(true),
					),
				},
			},
			// Import testing: a bare id, unlike circleci_otel_exporter.
			{
				ResourceName:                         "circleci_audit_log_config.test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "id",
				ImportStateId:                        "00000000-0000-0000-0000-000000000001",
			},
			// Delete testing automatically occurs in TestCase.
		},
	})

	var sawCreate, sawGet, sawUpdate, sawDelete bool
	for _, req := range api.recorded() {
		switch req {
		case "POST /api/v2/organizations/" + testAuditLogConfigOrg + "/audit-log/configs":
			sawCreate = true
		case "GET /api/v2/audit-log/configs/00000000-0000-0000-0000-000000000001":
			sawGet = true
		case "PUT /api/v2/audit-log/configs":
			sawUpdate = true
		case "DELETE /api/v2/audit-log/configs/00000000-0000-0000-0000-000000000001":
			sawDelete = true
		}
	}

	if !sawCreate {
		t.Errorf("no create request, got %v", api.recorded())
	}
	if !sawGet {
		t.Errorf("no get request against the bare (non-org-scoped) route, got %v", api.recorded())
	}
	if !sawUpdate {
		t.Errorf("no update request, got %v", api.recorded())
	}
	if !sawDelete {
		t.Errorf("no delete request, got %v", api.recorded())
	}
}

// TestAccAuditLogConfigResource_S3Compatible covers the other target type,
// where endpoint is required and region is left to the server's default.
func TestAccAuditLogConfigResource_S3Compatible(t *testing.T) {
	api := &auditLogConfigAPI{}
	srv := newAuditLogConfigServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: auditLogConfigResourceConfig(srv.URL, "S3_COMPATIBLE", "", "https://minio.example.com", false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_audit_log_config.test",
						tfjsonpath.New("target_type"),
						knownvalue.StringExact("S3_COMPATIBLE"),
					),
					statecheck.ExpectKnownValue(
						"circleci_audit_log_config.test",
						tfjsonpath.New("endpoint"),
						knownvalue.StringExact("https://minio.example.com"),
					),
					statecheck.ExpectKnownValue(
						"circleci_audit_log_config.test",
						tfjsonpath.New("region"),
						knownvalue.Null(),
					),
				},
			},
		},
	})
}

// TestAccAuditLogConfigResource_DeletedOutsideTerraform proves drift
// detection: the single-config GET answers 404 for a config that is gone, and
// that must drop the resource from state.
func TestAccAuditLogConfigResource_DeletedOutsideTerraform(t *testing.T) {
	api := &auditLogConfigAPI{}
	srv := newAuditLogConfigServer(t, api)

	config := auditLogConfigResourceConfig(srv.URL, "S3", "us-east-1", "", false)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig:          func() { api.removeAll() },
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccAuditLogConfigResource_ConnectionCheckFailure surfaces the owning
// service's connectivity error message rather than an opaque 400.
func TestAccAuditLogConfigResource_ConnectionCheckFailure(t *testing.T) {
	api := &auditLogConfigAPI{connectionFails: true}
	srv := newAuditLogConfigServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      auditLogConfigResourceConfig(srv.URL, "S3", "us-east-1", "", false),
			ExpectError: regexp.MustCompile(`(?s)Failed to connect to S3`),
		}},
	})
}

func TestAccAuditLogConfigResource_RejectsInvalidTargetType(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      auditLogConfigResourceConfig("http://127.0.0.1:1", "GCS", "us-east-1", "", false),
			ExpectError: regexp.MustCompile(`(?s)target_type value must be one of`),
		}},
	})
}

func TestAccAuditLogConfigResource_RejectsInvalidARN(t *testing.T) {
	cfg := auditLogConfigProviderConfig("http://127.0.0.1:1") + fmt.Sprintf(`
resource "circleci_audit_log_config" "test" {
  organization_id = %q
  target_type     = "S3"
  arn             = "not-an-arn"
  bucket_name     = "acme-audit-logs"
  region          = "us-east-1"
}
`, testAuditLogConfigOrg)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)must be an AWS or MinIO IAM role ARN`),
		}},
	})
}

func TestAccAuditLogConfigResource_RejectsBucketPrefixWithSlashes(t *testing.T) {
	cfg := auditLogConfigProviderConfig("http://127.0.0.1:1") + fmt.Sprintf(`
resource "circleci_audit_log_config" "test" {
  organization_id = %q
  target_type     = "S3"
  arn             = "arn:aws:iam::123456789012:role/circleci-audit-logs"
  bucket_name     = "acme-audit-logs"
  bucket_prefix   = "/leading-slash"
  region          = "us-east-1"
}
`, testAuditLogConfigOrg)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)must not have a leading or trailing`),
		}},
	})
}

// TestAccAuditLogConfigResource_RequiresCloud covers the Server rejection at
// plan time, in the resource's own test file since cloud_only_test.go's table
// is closed over resources that existed at the time it was written.
func TestAccAuditLogConfigResource_RequiresCloud(t *testing.T) {
	cfg := `
provider "circleci" {
  host       = "https://circleci.example.com"
  key        = "fake"
  deployment = "server"
}
` + fmt.Sprintf(`
resource "circleci_audit_log_config" "test" {
  organization_id = %q
  target_type     = "S3"
  arn             = "arn:aws:iam::123456789012:role/circleci-audit-logs"
  bucket_name     = "acme-audit-logs"
  region          = "us-east-1"
}
`, testAuditLogConfigOrg)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(auditLogConfigTypeName + ` requires CircleCI Cloud`),
		}},
	})
}

// connectionStatusForUpdate reports what an update should answer with. Caller holds mu.
func (a *auditLogConfigAPI) connectionStatusForUpdate() string {
	if a.updateStatus != "" {
		return a.updateStatus
	}

	return "CONNECTED"
}

// TestAccAuditLogConfigResource_ConnectionStatusMayChangeOnUpdate pins the fix for a
// crash in this resource's own headline workflow.
//
// connection_status reports real delivery outcomes, and the API re-verifies
// connectivity during an update whenever is_disabled is false. The plan, though,
// carried the value from the last read forward — a plain Computed attribute is
// proposed as its prior value, never as unknown — so re-enabling a config whose status
// was DISCONNECTED planned DISCONNECTED, the API answered CONNECTED, and apply died
// with "Provider produced inconsistent result after apply".
//
// The resource now marks the attribute unknown in ModifyPlan when, and only when,
// something else is actually changing. The "only when" matters as much as the "when":
// marking a computed attribute unknown is itself a change, so doing it unconditionally
// manufactured a permanent diff and broke four other tests in this file.
func TestAccAuditLogConfigResource_ConnectionStatusMayChangeOnUpdate(t *testing.T) {
	api := &auditLogConfigAPI{}
	srv := newAuditLogConfigServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: auditLogConfigResourceConfig(srv.URL, "S3", "us-east-1", "", false),
			},
			{
				// The destination went unreachable and then came back: the update
				// re-verifies and answers a status the plan could not have known.
				PreConfig: func() { api.setUpdateStatus("DISCONNECTED") },
				Config:    auditLogConfigResourceConfig(srv.URL, "S3", "us-east-1", "", true),
			},
			{
				// And a plan with nothing changed must still be empty — the guard
				// against manufacturing a diff.
				Config:   auditLogConfigResourceConfig(srv.URL, "S3", "us-east-1", "", true),
				PlanOnly: true,
			},
		},
	})
}
