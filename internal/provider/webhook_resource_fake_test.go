// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// This file backs `circleci_webhook` (webhook_resource.go) and its singular
// data source (webhook_data_source.go) with an in-process stand-in for the
// real webhook API — not for
// github.com/CircleCI-Public/circleci-sdk-go/webhook's idea of the API — so
// that a field-name mismatch between the two shows up as a real test failure
// rather than being hidden by a fake that was written to match the client
// instead of the server.
//
// The real wire shape is verified against
// the CircleCI API's
// openapi_definitions/v2_endpoints/webhook/schemas.yaml, which documents
// "verify_tls" and "signing_secret" (both snake_case) as the webhook object's
// field names — matching internal/circleci/webhook.go in this repository.
//
// github.com/CircleCI-Public/circleci-sdk-go/webhook.Webhook instead tags those
// two fields `json:"verify-tls"` and `json:"signing-secret"` (hyphenated). Because
// the API ignores keys it does not recognize, every request the SDK sent silently
// dropped both: a webhook created through this resource had NO signing secret
// whatever was configured. That was a shipped security bug (issue #25).
//
// webhook_resource.go has been migrated to internal/circleci, whose tags match the
// API, and TestWebhookResourceUnit_SecretAndVerifyTLSReachTheWire now asserts both
// that the correct keys are sent and that the hyphenated ones are absent.
//
// webhook_data_source.go is migrated too, so it now reads the API's real
// signing_secret value rather than the SDK's mistagged (and always-empty) field.
// That value is only ever the "****" mask or "" — the API never discloses a real
// secret — so the data source still does not surface it as a Sensitive string:
// TestWebhookDataSourceUnit_SigningSecretIsAlwaysNull asserts it comes back null
// either way rather than exposing "****" as if it were a credential a
// configuration could pass to a receiver. That attribute is queued for removal in
// 1.0 in favor of `circleci_webhooks`' `has_signing_secret` (issue #21).

type fakeWebhookAPI struct {
	t *testing.T

	mu       sync.Mutex
	webhooks map[string]map[string]any
	nextID   int
	requests []fakeRecordedRequest
}

func newFakeWebhookAPI(t *testing.T) (*fakeWebhookAPI, string) {
	t.Helper()

	api := &fakeWebhookAPI{webhooks: map[string]map[string]any{}, t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/webhook", api.create)
	mux.HandleFunc("GET /api/v2/webhook/{id}", api.get)
	mux.HandleFunc("PUT /api/v2/webhook/{id}", api.update)
	mux.HandleFunc("DELETE /api/v2/webhook/{id}", api.delete)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"unrecognized route: `+r.Method+" "+r.URL.Path+`"}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return api, srv.URL
}

func (a *fakeWebhookAPI) record(r *http.Request, body map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, fakeRecordedRequest{Method: r.Method, Path: r.URL.Path, Body: body})
}

func (a *fakeWebhookAPI) recorded() []fakeRecordedRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]fakeRecordedRequest(nil), a.requests...)
}

func (a *fakeWebhookAPI) lastRequest(t *testing.T, method, path string) fakeRecordedRequest {
	t.Helper()

	var last fakeRecordedRequest
	found := false
	for _, req := range a.recorded() {
		if req.Method == method && req.Path == path {
			last = req
			found = true
		}
	}
	if !found {
		t.Fatalf("no %s request to %s recorded (all: %+v)", method, path, a.recorded())
	}

	return last
}

// buildRecord answers exactly like the real API would: verify_tls defaults to
// true when absent, and signing_secret is stored only when the REAL key
// ("signing_secret") is present in the body — never the SDK's misspelled
// "signing-secret".
func (a *fakeWebhookAPI) buildRecord(id string, body map[string]any) map[string]any {
	verifyTLS := true
	if v, ok := body["verify_tls"].(bool); ok {
		verifyTLS = v
	}

	secret := ""
	if v, ok := body["signing_secret"].(string); ok {
		secret = v
	}
	masked := ""
	if secret != "" {
		masked = "****"
	}

	events := body["events"]

	scope := map[string]any{"id": "", "type": ""}
	if s, ok := body["scope"].(map[string]any); ok {
		scope = map[string]any{"id": s["id"], "type": s["type"]}
	}

	return map[string]any{
		"id":             id,
		"name":           body["name"],
		"url":            body["url"],
		"verify_tls":     verifyTLS,
		"signing_secret": masked,
		"scope":          scope,
		"events":         events,
		"created_at":     "2024-07-01T00:00:00.000Z",
		"updated_at":     "2024-07-01T00:00:00.000Z",
	}
}

func (a *fakeWebhookAPI) create(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(a.t, r)
	a.record(r, body)

	a.mu.Lock()
	a.nextID++
	id := fmt.Sprintf("33333333-4444-5555-6666-%012d", a.nextID)
	record := a.buildRecord(id, body)
	a.webhooks[id] = record
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(record)
}

func (a *fakeWebhookAPI) get(w http.ResponseWriter, r *http.Request) {
	a.record(r, nil)

	w.Header().Set("Content-Type", "application/json")

	id := r.PathValue("id")
	a.mu.Lock()
	record, ok := a.webhooks[id]
	a.mu.Unlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Webhook not found"}`)

		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(record)
}

func (a *fakeWebhookAPI) update(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(a.t, r)
	a.record(r, body)

	w.Header().Set("Content-Type", "application/json")

	id := r.PathValue("id")

	a.mu.Lock()
	defer a.mu.Unlock()

	existing, ok := a.webhooks[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Webhook not found"}`)

		return
	}

	updated := a.buildRecord(id, body)
	// The scope cannot be updated (see webhook.go's WebhookService.Update
	// comment); keep the original.
	updated["scope"] = existing["scope"]
	updated["created_at"] = existing["created_at"]
	a.webhooks[id] = updated

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(updated)
}

func (a *fakeWebhookAPI) delete(w http.ResponseWriter, r *http.Request) {
	a.record(r, nil)

	w.Header().Set("Content-Type", "application/json")

	id := r.PathValue("id")

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.webhooks[id]; !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Webhook not found"}`)

		return
	}

	delete(a.webhooks, id)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"message":"ok"}`)
}

func webhookFakeProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host       = %q
  key        = "fake-token"
  deployment = "cloud"
}
`, host)
}

const fakeWebhookScopeID = "eeeeeeee-1111-2222-3333-444444444444"

func webhookFakeResourceConfig(host, name, url, secret string, events []string) string {
	eventsList := ""
	for i, e := range events {
		if i > 0 {
			eventsList += ", "
		}
		eventsList += fmt.Sprintf("%q", e)
	}

	return webhookFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_webhook" "test" {
  name           = %[1]q
  url            = %[2]q
  signing_secret = %[3]q
  scope_id       = %[4]q
  scope_type     = "project"
  events         = [%[5]s]
}
`, name, url, secret, fakeWebhookScopeID, eventsList)
}

// TestWebhookResourceUnit_CRUD exercises create, read-back, update-in-place
// and delete.
func TestWebhookResourceUnit_CRUD(t *testing.T) {
	_, host := newFakeWebhookAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: webhookFakeResourceConfig(host, "hook-1", "https://example.com/hook", "s3cr3t", []string{"workflow-completed"}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("name"), knownvalue.StringExact("hook-1")),
					statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("url"), knownvalue.StringExact("https://example.com/hook")),
					statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("scope_id"), knownvalue.StringExact(fakeWebhookScopeID)),
					statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("verify_tls"), knownvalue.Bool(true)),
					// The configured secret must read back exactly as configured:
					// Read() never touches signing_secret (see webhook_resource.go),
					// so there is no permanent diff even though the real API masks
					// it on every response.
					statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("signing_secret"), knownvalue.StringExact("s3cr3t")),
				},
			},
			{
				// A no-op re-apply (refresh + plan + apply) must not show any diff
				// on signing_secret or verify_tls, proving item 3 of the known bug
				// history: the mask never becomes a permanent diff.
				Config: webhookFakeResourceConfig(host, "hook-1", "https://example.com/hook", "s3cr3t", []string{"workflow-completed"}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("signing_secret"), knownvalue.StringExact("s3cr3t")),
				},
			},
			{
				// Update events and url in place.
				Config: webhookFakeResourceConfig(host, "hook-1", "https://example.com/hook-2", "s3cr3t", []string{"workflow-completed", "job-completed"}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("url"), knownvalue.StringExact("https://example.com/hook-2")),
					statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("events"), knownvalue.ListExact([]knownvalue.Check{
						knownvalue.StringExact("workflow-completed"),
						knownvalue.StringExact("job-completed"),
					})),
				},
			},
			{
				// Rename in place. `name` is updatable on this resource, so a rename
				// must be an update rather than a replacement — and the new name has
				// to survive the read-back. Nothing else exercised `name` on the
				// update path, which is the same gap that let circleci_pipeline's
				// Update silently drop fields (bug history item 2 in
				// pipeline_resource_fake_test.go).
				Config: webhookFakeResourceConfig(host, "hook-renamed", "https://example.com/hook-2", "s3cr3t", []string{"workflow-completed", "job-completed"}),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_webhook.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("name"), knownvalue.StringExact("hook-renamed")),
				},
			},
			{
				ResourceName:            "circleci_webhook.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"signing_secret"},
				ImportStateIdFunc:       importStateIDFor("circleci_webhook.test", "scope_id"),
			},
		},
	})
}

// TestWebhookResourceUnit_SecretAndVerifyTLSReachTheWire is the money test for
// this file: it proves signing_secret and verify_tls actually reach the API under
// the field names the API reads.
//
// It is a regression test for a shipped security bug.
// github.com/CircleCI-Public/circleci-sdk-go/webhook.Webhook tags these two fields
// `json:"verify-tls"` and `json:"signing-secret"` — hyphenated — while the webhook
// service documents and reads `verify_tls` and `signing_secret` (confirmed against
// the API's openapi_definitions/v2_endpoints/webhook/schemas.yaml). The
// API ignores keys it does not recognize, so while the resource was built on the
// SDK every webhook it created had **no signing secret at all**, no matter what the
// practitioner configured, and TLS verification silently took the server default.
//
// That is a security bug, not a cosmetic one: the signing secret is the only thing
// letting a receiver tell a genuine CircleCI delivery from a forged POST. A
// practitioner who set one had every reason to believe it was in force.
//
// The resource now goes through the provider's own client
// (internal/circleci/webhook.go), whose tags match the API. Asserting the absence
// of the hyphenated keys matters as much as the presence of the correct ones — if
// anything reintroduces the SDK types, the extra keys come back and this fails.
func TestWebhookResourceUnit_SecretAndVerifyTLSReachTheWire(t *testing.T) {
	api, host := newFakeWebhookAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: webhookFakeResourceConfig(host, "hook-1", "https://example.com/hook", "s3cr3t", []string{"workflow-completed"}),
			},
			{
				// Rotating the secret must also reach the wire. A create-only fix
				// would leave rotation silently broken, which is arguably worse:
				// the practitioner believes they have revoked the old secret.
				Config: webhookFakeResourceConfig(host, "hook-1", "https://example.com/hook", "rotated-s3cr3t", []string{"workflow-completed"}),
			},
		},
	})

	create := api.lastRequest(t, "POST", "/api/v2/webhook")

	if create.Body["signing_secret"] != "s3cr3t" {
		t.Errorf(`create body["signing_secret"] = %v, want "s3cr3t" — a webhook created without its `+
			`signing secret cannot be authenticated by its receiver`, create.Body["signing_secret"])
	}
	if create.Body["verify_tls"] != true {
		t.Errorf(`create body["verify_tls"] = %v, want true`, create.Body["verify_tls"])
	}

	// The SDK's misspellings must not be present at all.
	for _, wrong := range []string{"signing-secret", "verify-tls"} {
		if _, present := create.Body[wrong]; present {
			t.Errorf("create body carries %q, which the API does not read — the SDK types appear to have "+
				"been reintroduced, and the signing secret is being silently discarded again", wrong)
		}
	}

	update := api.lastRequest(t, "PUT", "/api/v2/webhook/33333333-4444-5555-6666-000000000001")
	if update.Body["signing_secret"] != "rotated-s3cr3t" {
		t.Errorf(`update body["signing_secret"] = %v, want "rotated-s3cr3t" — a rotation that does not `+
			`reach the wire leaves the old secret live while state claims otherwise`, update.Body["signing_secret"])
	}
}

// TestWebhookResourceUnit_URLValidatorRejectsPrivateAndNonHTTPS exercises the
// resource-schema-level defense in depth: a RegexMatches validator requiring
// https://, and WebhookURLValidator rejecting private/loopback targets.
func TestWebhookResourceUnit_URLValidatorRejectsPrivateAndNonHTTPS(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "http not https", url: "http://example.com/hook"},
		{name: "loopback", url: "https://127.0.0.1/hook"},
		{name: "localhost", url: "https://localhost/hook"},
		{name: "private RFC1918", url: "https://10.0.0.5/hook"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := webhookFakeResourceConfig("http://127.0.0.1:1", "hook-1", tc.url, "s3cr3t", []string{"workflow-completed"})

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: cfg,
					// Two different mechanisms produce these, hence the alternation:
					// the schema's RegexMatches validator answers "Invalid Attribute
					// Value Match ... must be a valid HTTPS URL", while
					// WebhookURLValidator answers with its own summary for private and
					// loopback targets.
					ExpectError: regexp.MustCompile(`(?s)(must be a valid HTTPS URL|Invalid Webhook URL|Invalid URL)`),
				}},
			})
		})
	}
}

// TestWebhookResourceUnit_Create4xxIsADiagnosticNotAPanic guards against a
// regression in the error-handling path.
func TestWebhookResourceUnit_Create4xxIsADiagnosticNotAPanic(t *testing.T) {
	_, host := newFakeWebhookAPI(t)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/webhook", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":"events must not be empty"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_ = host

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      webhookFakeResourceConfig(srv.URL, "hook-1", "https://example.com/hook", "s3cr3t", []string{"workflow-completed"}),
			ExpectError: regexp.MustCompile(`(?s)Error creating CircleCI webhook.*events must not be empty`),
		}},
	})
}

// --- singular data source ---

func TestWebhookDataSourceUnit_Read(t *testing.T) {
	api, host := newFakeWebhookAPI(t)
	api.mu.Lock()
	api.webhooks["fixed-id"] = map[string]any{
		"id":             "fixed-id",
		"name":           "hook-1",
		"url":            "https://example.com/hook",
		"verify_tls":     true,
		"signing_secret": "",
		"scope":          map[string]any{"id": fakeWebhookScopeID, "type": "project"},
		"events":         []any{"workflow-completed"},
		"created_at":     "2024-07-01T00:00:00.000Z",
		"updated_at":     "2024-07-01T00:00:00.000Z",
	}
	api.mu.Unlock()

	cfg := webhookFakeProviderConfig(host) + `
data "circleci_webhook" "test" {
  id = "fixed-id"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("data.circleci_webhook.test", tfjsonpath.New("name"), knownvalue.StringExact("hook-1")),
				statecheck.ExpectKnownValue("data.circleci_webhook.test", tfjsonpath.New("verify_tls"), knownvalue.Bool(true)),
			},
		}},
	})
}

// TestWebhookDataSourceUnit_SigningSecretIsAlwaysNull covers the read side of
// the design decision documented on webhook_data_source.go's schema and in
// DESIGN.md's "Values the API never returns are not exposed as strings": even
// when the real API masks a configured secret as "****" (server/.../webhook/
// schemas.yaml's documented behavior), signing_secret is null, never the
// literal mask. Before this resource was migrated off circleci-sdk-go, the
// same outcome happened for the wrong reason — the SDK tagged the field
// `json:"signing-secret"` and the response body had no such key, so the value
// came back "" (a plain empty string) by mistake rather than null by design.
// The distinction matters: "" is indistinguishable from "no secret is
// configured", whereas null says plainly that this attribute cannot tell you.
func TestWebhookDataSourceUnit_SigningSecretIsAlwaysNull(t *testing.T) {
	api, host := newFakeWebhookAPI(t)
	api.mu.Lock()
	api.webhooks["fixed-id"] = map[string]any{
		"id":             "fixed-id",
		"name":           "hook-1",
		"url":            "https://example.com/hook",
		"verify_tls":     true,
		"signing_secret": "****", // the real API's masked-but-present value
		"scope":          map[string]any{"id": fakeWebhookScopeID, "type": "project"},
		"events":         []any{"workflow-completed"},
		"created_at":     "2024-07-01T00:00:00.000Z",
		"updated_at":     "2024-07-01T00:00:00.000Z",
	}
	api.webhooks["no-secret-id"] = map[string]any{
		"id":             "no-secret-id",
		"name":           "hook-2",
		"url":            "https://example.com/hook2",
		"verify_tls":     true,
		"signing_secret": "", // no secret configured at all
		"scope":          map[string]any{"id": fakeWebhookScopeID, "type": "project"},
		"events":         []any{"workflow-completed"},
		"created_at":     "2024-07-01T00:00:00.000Z",
		"updated_at":     "2024-07-01T00:00:00.000Z",
	}
	api.mu.Unlock()

	cfg := webhookFakeProviderConfig(host) + `
data "circleci_webhook" "with_secret" {
  id = "fixed-id"
}
data "circleci_webhook" "without_secret" {
  id = "no-secret-id"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			ConfigStateChecks: []statecheck.StateCheck{
				// Neither the mask nor a plain "" is ever exposed: a Sensitive string
				// holding "****" looks exactly like a real credential a configuration
				// could pass to a receiver, and never would be one.
				statecheck.ExpectKnownValue("data.circleci_webhook.with_secret", tfjsonpath.New("signing_secret"), knownvalue.Null()),
				statecheck.ExpectKnownValue("data.circleci_webhook.without_secret", tfjsonpath.New("signing_secret"), knownvalue.Null()),
			},
		}},
	})
}

func TestWebhookDataSourceUnit_NotFoundDiagnostic(t *testing.T) {
	_, host := newFakeWebhookAPI(t)

	cfg := webhookFakeProviderConfig(host) + `
data "circleci_webhook" "test" {
  id = "does-not-exist"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)Unable to Read CircleCI webhook.*Webhook not found`),
		}},
	})
}
