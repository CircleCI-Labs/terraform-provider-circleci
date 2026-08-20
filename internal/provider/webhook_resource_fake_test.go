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
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// This file backs `circleci_webhook` (webhook_resource.go) and its singular
// data source (webhook_data_source.go) with an in-process stand-in for the
// real webhook API — not for
// github.com/CircleCI-Public/circleci-sdk-go/webhook's idea of the API — so
// that a field-name mismatch between the two shows up as a real test failure
// rather than being hidden by a fake that was written to match the client
// instead of the server.
//
// THE REQUEST KEYS ARE HYPHENATED AND THE RESPONSE KEYS ARE SNAKE_CASE, and this
// fake models that asymmetry deliberately. The webhook routes read `verify-tls`
// and `signing-secret` on the way in and report `verify_tls` and `signing_secret`
// on the way out; the request side ignores keys it does not recognize, so the
// snake_case spellings are accepted with a 2xx and silently discarded. The
// published OpenAPI document says so, and circleci.WebhookInput records the
// live-API transcript that proves it.
//
// This fake previously read the snake_case spellings on the request, which is the
// whole reason a client sending them shipped: the fake agreed with the client, so
// every mocked test passed while every real webhook was created with no signing
// secret and TLS verification off. A fake that cannot be wrong in the way the API
// is wrong cannot catch the bug the API causes.
//
// So buildRecord below reads ONLY the hyphenated keys, defaults verify_tls to
// false when the hyphenated key is absent (which is what the API stores, proven
// against production), and shouts if a request carries the snake_case spelling.
// A client sending snake_case now fails here three ways over: the explicit
// complaint, the wire assertion in
// TestWebhookResourceUnit_SecretAndVerifyTLSReachTheWire, and
// "Provider produced inconsistent result after apply: .verify_tls: was cty.True,
// but now cty.False" out of TestWebhookResourceUnit_CRUD — the same error the
// real API produces.
//
// webhook_data_source.go reads the API's real signing_secret response value.
// That value is only ever the "****" mask or "" — the API never discloses a real
// secret — so the data source still does not surface it as a Sensitive string:
// TestWebhookDataSourceUnit_SigningSecretIsAlwaysNull asserts it comes back null
// either way rather than exposing "****" as if it were a credential a
// configuration could pass to a receiver. That attribute is queued for removal in
// 1.0 in favor of `circleci_webhooks`' `has_signing_secret`.

type fakeWebhookAPI struct {
	t *testing.T

	mu       sync.Mutex
	webhooks map[string]map[string]any
	nextID   int
	requests []fakeRecordedRequest

	// updateResponseOverride, when non-nil, replaces the given keys of whatever
	// an update (PUT) would otherwise store and echo back, regardless of what
	// was sent. Every other response this fake gives is derived from the
	// request, so there is no other way to make an update answer with a value
	// that genuinely differs in *content* from the one a test sent — which is
	// exactly what a caller must be able to do to tell apart "state written
	// from the request" from "state written from the response". See
	// setUpdateResponseOverride.
	updateResponseOverride map[string]any
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

// newFakeWebhookClient starts the stand-in API and returns it alongside a
// circleci.Client pointed at it, for tests that drive the resource's Go
// methods directly rather than through Terraform.
func newFakeWebhookClient(t *testing.T) (*fakeWebhookAPI, *circleci.Client) {
	t.Helper()

	api, host := newFakeWebhookAPI(t)

	return api, circleci.New(circleci.Config{Host: host, Token: "fake"})
}

// setUpdateResponseOverride overrides the given fields of whatever an update
// (PUT) answers with. See updateResponseOverride.
func (a *fakeWebhookAPI) setUpdateResponseOverride(fields map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.updateResponseOverride = fields
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

// reorderedLikeTheAPI returns an unordered collection in a DIFFERENT order from
// the one it was given.
//
// This is not gratuitous. CircleCI stores `events` and
// `pr_only_branch_overrides` as unordered collections and reports them back in an
// order of its own choosing. Verified against the live API:
//
//	PATCH pr_only_branch_overrides ["zebra","alpha","main","beta"]
//	→ GET  pr_only_branch_overrides ["zebra","main","alpha","beta"]
//
// stable across subsequent reads, but not the order it was given. Declared as
// Terraform lists, both attributes therefore showed a change on every plan with
// nothing to apply.
//
// The whole fake-backed suite missed that because every fake here echoed the
// submitted order straight back, which is the one behaviour the real API does not
// have. A fake that cannot be wrong in the way the API is wrong cannot catch the
// bug the API causes. Reversing is the cheapest order that differs for any
// collection of two or more elements, so an order-sensitive regression — reverting
// either attribute to a ListAttribute, say — fails the suite immediately with
// "Provider produced inconsistent result after apply".
//
// A collection of one element is returned unchanged, which is unavoidable and
// harmless: the tests that must detect ordering all submit two or more.
func reorderedLikeTheAPI(collection any) any {
	values, ok := collection.([]any)
	if !ok {
		return collection
	}

	// A copy, never a reversal in place: the same slice is held by the recorded
	// request bodies the tests assert the *sent* order on.
	reordered := make([]any, 0, len(values))
	for index := len(values) - 1; index >= 0; index-- {
		reordered = append(reordered, values[index])
	}

	return reordered
}

// webhookRequestOnlyKeys are the two request keys the webhook routes spell with a
// hyphen, mapped to the snake_case key the response spells them with. Nothing
// else in the body differs between the two directions: name, url, events and
// scope are single words.
var webhookRequestOnlyKeys = map[string]string{
	"verify-tls":     "verify_tls",
	"signing-secret": "signing_secret",
}

// rejectResponseSpellings fails the test when a request body carries the
// snake_case spelling of a key the request side reads with a hyphen.
//
// The real API would answer 2xx and drop the value, and this fake drops it too —
// see buildRecord, which reads only the hyphenated keys. But "dropped" surfaces
// downstream as an inconsistent-result error about verify_tls, or (for the signing
// secret, which is never read back into state) as nothing at all. Naming the cause
// where it happens is worth one assertion in the handler.
func (a *fakeWebhookAPI) rejectResponseSpellings(body map[string]any) {
	for hyphenated, snake := range webhookRequestOnlyKeys {
		if _, present := body[snake]; present {
			a.t.Errorf("request body carries %q; the webhook routes read %q on the way in and "+
				"only report %q on the way out, so this value is silently discarded. See "+
				"circleci.WebhookInput", snake, hyphenated, snake)
		}
	}
}

// buildRecord answers exactly like the real API would.
//
// The two hyphenated request keys are read under their REQUEST spelling and
// echoed under their RESPONSE spelling. Verified against production:
//
//	POST {"verify-tls":true,"signing-secret":"x"}  -> {"verify_tls":true,"signing_secret":"****"}
//	POST {"verify_tls":true,"signing_secret":"x"}  -> {"verify_tls":false}
//	POST (neither key at all)                      -> {"verify_tls":false}
//
// So verify_tls defaults to FALSE, not true, when the hyphenated key is absent —
// which is precisely what makes a client sending snake_case fail here the way it
// fails in production rather than passing.
//
// A webhook with no signing secret has no signing_secret key in the response at
// all, rather than an empty one; both decode to "" in Go, and this fake omits it
// the way production does. An EMPTY signing-secret in a request means "leave it
// alone", not "clear it" — also verified against production:
//
//	POST {"signing-secret":""}     -> no signing_secret key: none stored
//	PUT  {"signing-secret":"x"}    -> {"signing_secret":"****"}
//	PUT  {"signing-secret":""}     -> {"signing_secret":"****"} still there
//
// so a webhook's secret cannot be removed through this API, only replaced.
//
// existing is the stored record on an update and nil on a create. The update route
// select-keys the body, so a key absent from a PUT leaves the stored value alone
// rather than resetting it to a default.
//
// The stored events are deliberately in a different order from the submitted
// ones; see reorderedLikeTheAPI.
func (a *fakeWebhookAPI) buildRecord(id string, body, existing map[string]any) map[string]any {
	record := map[string]any{
		"id":         id,
		"created_at": "2024-07-01T00:00:00.000Z",
		"updated_at": "2024-07-01T00:00:00.000Z",
		"verify_tls": false,
		"scope":      map[string]any{"id": "", "type": ""},
	}
	// An update starts from what is stored: an absent key is "leave alone".
	for key, value := range existing {
		record[key] = value
	}

	for _, key := range []string{"name", "url"} {
		if value, present := body[key]; present {
			record[key] = value
		}
	}
	// Not body["events"] as submitted: the API returns the events in an order of
	// its own. See reorderedLikeTheAPI.
	if events, present := body["events"]; present {
		record["events"] = reorderedLikeTheAPI(events)
	}
	if scope, ok := body["scope"].(map[string]any); ok {
		record["scope"] = map[string]any{"id": scope["id"], "type": scope["type"]}
	}

	// The hyphenated request key, never the snake_case one.
	if verifyTLS, ok := body["verify-tls"].(bool); ok {
		record["verify_tls"] = verifyTLS
	}
	if secret, ok := body["signing-secret"].(string); ok && secret != "" {
		record["signing_secret"] = "****"
	}

	return record
}

// createWebhookRequiredFields are the keys the create route rejects a body for
// omitting: name, events, url and a nested scope. Verified one at a time against
// production — dropping any one of the four is a 400 "Invalid request body."
//
// verify-tls and signing-secret are NOT in this list, and that is not an
// oversight. A create carrying neither is a 201 whose stored verify_tls is false
// and which has no signing secret, which is exactly the shape a client sending the
// snake_case spellings produces. A fake that 400'd instead would turn the quiet
// failure this bug is made of into a loud one and stop reproducing it.
//
// The update route requires nothing at all: the handler select-keys the body, so
// an absent key leaves the stored value alone. That asymmetry is why validation
// lives in create rather than in buildRecord, which both routes share.
var createWebhookRequiredFields = []string{"name", "events", "url", "scope"}

func (a *fakeWebhookAPI) create(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(a.t, r)
	a.record(r, body)
	a.rejectResponseSpellings(body)

	w.Header().Set("Content-Type", "application/json")

	for _, field := range createWebhookRequiredFields {
		if _, present := body[field]; !present {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message":"Invalid request body: missing `+field+`"}`)

			return
		}
	}

	a.mu.Lock()
	a.nextID++
	id := fmt.Sprintf("33333333-4444-5555-6666-%012d", a.nextID)
	record := a.buildRecord(id, body, nil)
	a.webhooks[id] = record
	a.mu.Unlock()

	// 201, not 200: the create handler answers with response/created, and its
	// OpenAPI block documents 201. Only the update route answers 200. The client
	// treats any 2xx as success, so this is fake fidelity rather than a bug it was
	// hiding.
	w.WriteHeader(http.StatusCreated)
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
	a.rejectResponseSpellings(body)

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

	updated := a.buildRecord(id, body, existing)
	// The scope cannot be updated (see webhook.go's UpdateWebhook comment);
	// keep the original. Verified against production: a PUT carrying a different
	// scope answers 200 with the original scope unchanged.
	updated["scope"] = existing["scope"]
	updated["created_at"] = existing["created_at"]

	for field, value := range a.updateResponseOverride {
		updated[field] = value
	}

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
	_, _ = io.WriteString(w, `{"message":"Webhook deleted."}`)
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
				//
				// Two events, in an order the fake will not echo back: the fake returns
				// them reversed, the way the real API returns them in an order of its
				// own (see reorderedLikeTheAPI). That is what makes this step exercise
				// `events` being a Set rather than a List.
				Config: webhookFakeResourceConfig(host, "hook-1", "https://example.com/hook-2", "s3cr3t", []string{"workflow-completed", "job-completed"}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("url"), knownvalue.StringExact("https://example.com/hook-2")),
					statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("events"), knownvalue.SetExact([]knownvalue.Check{
						knownvalue.StringExact("workflow-completed"),
						knownvalue.StringExact("job-completed"),
					})),
				},
			},
			{
				// The identical configuration, replanned: the plan must be empty.
				//
				// This is the shape of test the original bug needed and did not have.
				// `events` was a ListAttribute, and the API returns the events in an
				// order of its own choosing, so Terraform compared the configured order
				// against the returned order and planned a change on every run for ever,
				// with nothing to apply. Now that it is a Set the order is not part of
				// the value, so a re-plan against unchanged state is empty.
				Config:             webhookFakeResourceConfig(host, "hook-1", "https://example.com/hook-2", "s3cr3t", []string{"workflow-completed", "job-completed"}),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
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

// TestWebhookResourceUnit_ImportWarnsSigningSecretIsUnset proves ImportState
// (webhook_resource.go) tells the practitioner what
// TestWebhookResourceUnit_CRUD's ImportStateVerifyIgnore only documents in a
// comment: the signing secret cannot be read back, so it must be supplied from
// the configuration before plan or apply can proceed.
func TestWebhookResourceUnit_ImportWarnsSigningSecretIsUnset(t *testing.T) {
	t.Parallel()

	schema := webhookResourceSchemaForTest(t)
	r := &webhookResource{}

	events, diags := types.SetValueFrom(t.Context(), types.StringType, []string{})
	if diags.HasError() {
		t.Fatalf("building an empty events set: %+v", diags)
	}

	priorState := tfsdk.State{Schema: schema}
	if diags := priorState.Set(t.Context(), webhookResourceModel{
		Id:                     types.StringNull(),
		Name:                   types.StringNull(),
		Url:                    types.StringNull(),
		VerifyTls:              types.BoolNull(),
		SigningSecret:          types.StringNull(),
		SigningSecretWO:        types.StringNull(),
		SigningSecretWOVersion: types.Int64Null(),
		ScopeId:                types.StringNull(),
		ScopeType:              types.StringNull(),
		Events:                 events,
		CreatedAt:              types.StringNull(),
		UpdatedAt:              types.StringNull(),
	}); diags.HasError() {
		t.Fatalf("could not build a prior state value: %+v", diags)
	}

	resp := &fwresource.ImportStateResponse{State: priorState}

	r.ImportState(t.Context(), fwresource.ImportStateRequest{ID: "scope-1/webhook-1"}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("ImportState diagnostics: %+v", resp.Diagnostics)
	}

	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("ImportState produced no warning that the signing secret cannot be read back")
	}

	var sawSecretWarning bool
	for _, d := range resp.Diagnostics.Warnings() {
		if strings.Contains(d.Summary(), "cannot be read") {
			sawSecretWarning = true
		}
	}
	if !sawSecretWarning {
		t.Errorf("ImportState warnings = %+v, want one about the signing secret being unreadable", resp.Diagnostics.Warnings())
	}
}

// TestFakeAPIsDoNotEchoCollectionOrder guards the guard.
//
// Requirement for every test above that proves `events` and
// `pr_only_branch_overrides` are order-insensitive: the fakes must answer in a
// different order from the one they were given, because the real API does. A fake
// that quietly goes back to echoing the submitted order would make every one of
// those tests pass whatever the attribute's type is, which is precisely the state
// this suite was in while the permanent diff shipped.
func TestFakeAPIsDoNotEchoCollectionOrder(t *testing.T) {
	t.Parallel()

	t.Run("reorderedLikeTheAPI reverses", func(t *testing.T) {
		t.Parallel()

		got, ok := reorderedLikeTheAPI([]any{"a", "b", "c"}).([]any)
		if !ok {
			t.Fatalf("reorderedLikeTheAPI returned %T, want []any", got)
		}
		if len(got) != 3 || got[0] != "c" || got[1] != "b" || got[2] != "a" {
			t.Errorf("reorderedLikeTheAPI([a b c]) = %v, want [c b a]", got)
		}

		// Anything that is not a JSON array passes straight through, so an absent
		// key stays absent rather than becoming an empty array.
		if got := reorderedLikeTheAPI(nil); got != nil {
			t.Errorf("reorderedLikeTheAPI(nil) = %v, want nil", got)
		}
	})

	t.Run("the webhook fake stores events reordered", func(t *testing.T) {
		t.Parallel()

		api, _ := newFakeWebhookAPI(t)

		submitted := []any{"workflow-completed", "job-completed"}
		record := api.buildRecord("id", map[string]any{"events": submitted}, nil)

		stored, ok := record["events"].([]any)
		if !ok {
			t.Fatalf("the fake stored events as %T, want []any", record["events"])
		}
		if len(stored) != 2 || stored[0] != "job-completed" {
			t.Errorf("the fake stored events as %v, want them reordered relative to the submitted %v — "+
				"a fake that echoes the submitted order cannot catch the permanent diff on events",
				stored, submitted)
		}
		// The submitted slice must be untouched: tests assert on the order the
		// provider *sent*.
		if submitted[0] != "workflow-completed" {
			t.Errorf("the fake reversed the submitted slice in place (%v), corrupting the recorded "+
				"request body", submitted)
		}
	})

	t.Run("the project settings fake stores branch overrides reordered", func(t *testing.T) {
		t.Parallel()

		api, client := newFakeProjectSettingsAPI(t)

		branches := []string{"zebra", "alpha", "main", "beta"}
		if _, err := client.UpdateProjectSettings(t.Context(), "github", "acme", "repo",
			circleci.ProjectSettings{PROnlyBranchOverrides: &branches},
		); err != nil {
			t.Fatalf("could not write the branch overrides: %v", err)
		}

		read, err := client.GetProjectSettings(t.Context(), "github", "acme", "repo")
		if err != nil {
			t.Fatalf("could not read the branch overrides back: %v", err)
		}

		got := derefBranches(read.PROnlyBranchOverrides)
		if len(got) != len(branches) {
			t.Fatalf("read back %v, want the same four branches as %v", got, branches)
		}
		if got[0] == branches[0] && got[1] == branches[1] {
			t.Errorf("the fake read back %v, the order it was given — it must answer in an order of "+
				"its own, the way the real API does, or no test here can catch the permanent diff on "+
				"pr_only_branch_overrides", got)
		}

		// The sent order must still be recorded as sent.
		sent, ok := api.onlyPatch(t)["pr_only_branch_overrides"].([]any)
		if !ok {
			t.Fatalf("the fake recorded pr_only_branch_overrides as %T, want a list",
				api.onlyPatch(t)["pr_only_branch_overrides"])
		}
		if sent[0] != "zebra" || sent[1] != "alpha" {
			t.Errorf("the recorded PATCH body says %v was sent, but %v was: the fake reordered the "+
				"recorded body rather than a copy", sent, branches)
		}
	})
}

// TestWebhookResourceUnit_RejectsUnknownEventName covers the schema-level
// validator on `events`.
//
// The attribute's description has always claimed the valid values are
// workflow-completed and job-completed, but nothing enforced it, so a typo cost a
// round-trip and came back as an opaque HTTP 400 from the API. The valid names
// come from circleci.WebhookEvents so the validator, the description and the
// client cannot drift apart.
func TestWebhookResourceUnit_RejectsUnknownEventName(t *testing.T) {
	api, host := newFakeWebhookAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: webhookFakeResourceConfig(host, "hook-1", "https://example.com/hook", "s3cr3t",
				[]string{"workflow-completed", "worfklow-completed"}),
			ExpectError: regexp.MustCompile(`(?s)Invalid Attribute Value Match.*worfklow-completed`),
		}},
	})

	// Validation happens before anything is written, so the invalid name must never
	// have reached the API at all.
	if requests := api.recorded(); len(requests) != 0 {
		t.Errorf("the provider made %d request(s) for a configuration that fails validation, want 0: %+v",
			len(requests), requests)
	}
}

// TestWebhookResourceUnit_SecretAndVerifyTLSReachTheWire is the money test for
// this file: it proves the signing secret and the TLS-verification flag actually
// reach the API under the keys the REQUEST side reads, which are hyphenated —
// `signing-secret` and `verify-tls` — even though the response reports them as
// `signing_secret` and `verify_tls`.
//
// It is a regression test for a shipped security bug, twice over. The keys were
// first hyphenated by accident (inherited from a third-party SDK), then "fixed" to
// snake_case to match the response — and snake_case is the spelling the request
// side ignores. Either way the effect is the same and silent: no signing secret is
// stored, and TLS verification falls to the server-side default of off.
//
// That is a security bug, not a cosmetic one: the signing secret is the only thing
// letting a receiver tell a genuine CircleCI delivery from a forged POST. A
// practitioner who set one had every reason to believe it was in force.
//
// Asserting the ABSENCE of the snake_case keys matters as much as the presence of
// the hyphenated ones. circleci.WebhookInput carries the live-API transcript.
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

	if create.Body["signing-secret"] != "s3cr3t" {
		t.Errorf(`create body["signing-secret"] = %v, want "s3cr3t" — a webhook created without its `+
			`signing secret cannot be authenticated by its receiver`, create.Body["signing-secret"])
	}
	if create.Body["verify-tls"] != true {
		t.Errorf(`create body["verify-tls"] = %v, want true`, create.Body["verify-tls"])
	}

	// The response spellings must not be present at all: the request side ignores
	// them, so a body carrying them is a 2xx with the values discarded.
	for _, wrong := range []string{"signing_secret", "verify_tls"} {
		if _, present := create.Body[wrong]; present {
			t.Errorf("create body carries %q, which is the RESPONSE spelling and is ignored on a "+
				"request — the signing secret is being silently discarded again", wrong)
		}
	}

	update := api.lastRequest(t, "PUT", "/api/v2/webhook/33333333-4444-5555-6666-000000000001")
	if update.Body["signing-secret"] != "rotated-s3cr3t" {
		t.Errorf(`update body["signing-secret"] = %v, want "rotated-s3cr3t" — a rotation that does not `+
			`reach the wire leaves the old secret live while state claims otherwise`, update.Body["signing-secret"])
	}
	// The update route is asymmetric in the same way the create route is.
	for _, wrong := range []string{"signing_secret", "verify_tls"} {
		if _, present := update.Body[wrong]; present {
			t.Errorf("update body carries %q, the response spelling, which the update route "+
				"ignores — the rotation never happens and state claims it did", wrong)
		}
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

// --- Update writes from the response, not the plan ---

// webhookResourceStateForTest builds a tfsdk.State from a webhookResourceModel,
// for the tests below that drive Update directly rather than through
// resource.UnitTest.
func webhookResourceStateForTest(t *testing.T, schema rschema.Schema, model webhookResourceModel) tfsdk.State {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a state value: %+v", diags)
	}

	return state
}

// TestWebhookResourceUnit_UpdateWritesFromResponseNotPlan is the regression
// test for the bug in webhookResource.Update (webhook_resource.go): only id,
// created_at and updated_at were copied from updatedWebhook — the object
// UpdateWebhook reports having actually stored — while name, url, verify_tls
// and events were left holding whatever the plan held, even though the same
// response carries all of them.
//
// This is a lower-severity sibling of the pr_only_branch_overrides bug fixed
// for circleci_project's Update: Read (webhook_resource.go) unconditionally
// refreshes every one of these fields from GetWebhook, so state written from
// the plan self-corrects on the very next plan or refresh rather than
// persisting. What this bug cost, before the fix, was a one-cycle window in
// which Terraform's state disagreed with the API about what an update had
// actually done — not permanent drift.
//
// The only thing that can tell "state written from the plan" apart from
// "state written from the response" is a case where the two disagree in
// *content*, so the fake is told (setUpdateResponseOverride) to answer the PUT
// below with a name, a URL, a verify_tls and an events set that all genuinely
// differ from what the plan sent. Only state built from the response can end
// up holding those values.
//
// Update is driven directly, not through resource.UnitTest: a full
// apply-then-refresh cycle would call Read afterwards, which self-heals
// exactly the drift this test exists to catch, masking the bug entirely.
func TestWebhookResourceUnit_UpdateWritesFromResponseNotPlan(t *testing.T) {
	t.Parallel()

	api, client := newFakeWebhookClient(t)

	const id = "fixed-webhook-id"
	api.mu.Lock()
	api.webhooks[id] = map[string]any{
		"id":             id,
		"name":           "hook-1",
		"url":            "https://example.com/hook",
		"verify_tls":     true,
		"signing_secret": "****",
		"scope":          map[string]any{"id": fakeWebhookScopeID, "type": "project"},
		"events":         []any{"workflow-completed"},
		"created_at":     "2024-07-01T00:00:00.000Z",
		"updated_at":     "2024-07-01T00:00:00.000Z",
	}
	api.mu.Unlock()

	// The PUT below answers with these, regardless of what the plan sends: a
	// name, a URL, a verify_tls and an events set that all genuinely differ in
	// content from the request, so any of them appearing in the final state is
	// unambiguous about where it came from.
	api.setUpdateResponseOverride(map[string]any{
		"name":       "server-renamed-hook",
		"url":        "https://example.com/hook-normalised",
		"verify_tls": false,
		"events":     []any{"job-completed"},
	})

	schema := webhookResourceSchemaForTest(t)

	prior := webhookResourceModel{
		Id:                     types.StringValue(id),
		Name:                   types.StringValue("hook-1"),
		Url:                    types.StringValue("https://example.com/hook"),
		VerifyTls:              types.BoolValue(true),
		SigningSecret:          types.StringValue("s3cr3t"),
		SigningSecretWO:        types.StringNull(),
		SigningSecretWOVersion: types.Int64Null(),
		ScopeId:                types.StringValue(fakeWebhookScopeID),
		ScopeType:              types.StringValue("project"),
		Events:                 types.SetValueMust(types.StringType, []attr.Value{types.StringValue("workflow-completed")}),
		CreatedAt:              types.StringValue("2024-07-01T00:00:00.000Z"),
		UpdatedAt:              types.StringValue("2024-07-01T00:00:00.000Z"),
	}
	state := webhookResourceStateForTest(t, schema, prior)

	// The plan: a different name and URL than the fake will answer with, and a
	// two-member events set (still true, but not what the response reports)
	// verify_tls, so every overridden field disagrees with what gets sent.
	plan := prior
	plan.Name = types.StringValue("hook-renamed-by-config")
	plan.Url = types.StringValue("https://example.com/hook-2")
	plan.VerifyTls = types.BoolValue(true)
	plan.Events = types.SetValueMust(types.StringType, []attr.Value{
		types.StringValue("workflow-completed"),
		types.StringValue("job-completed"),
	})
	planState := webhookResourceStateForTest(t, schema, plan)

	r := &webhookResource{client: client}
	resp := &fwresource.UpdateResponse{State: state}

	assertNoPanic(t, func() {
		r.Update(t.Context(), fwresource.UpdateRequest{
			Config: configForTest(t, schema, plan),
			Plan:   tfsdk.Plan{Schema: schema, Raw: planState.Raw},
			State:  state,
		}, resp)
	})

	if resp.Diagnostics.HasError() {
		t.Fatalf("Update diagnostics: %+v", resp.Diagnostics)
	}

	var got webhookResourceModel
	if diags := resp.State.Get(t.Context(), &got); diags.HasError() {
		t.Fatalf("could not read the resulting state: %+v", diags)
	}

	if got.Name.ValueString() != "server-renamed-hook" {
		t.Errorf("name after Update = %q, want %q (what the API reported) rather than %q (the plan)",
			got.Name.ValueString(), "server-renamed-hook", plan.Name.ValueString())
	}
	if got.Url.ValueString() != "https://example.com/hook-normalised" {
		t.Errorf("url after Update = %q, want %q (what the API reported) rather than %q (the plan)",
			got.Url.ValueString(), "https://example.com/hook-normalised", plan.Url.ValueString())
	}
	if got.VerifyTls.ValueBool() != false {
		t.Errorf("verify_tls after Update = %v, want false (what the API reported) rather than %v (the plan)",
			got.VerifyTls.ValueBool(), plan.VerifyTls.ValueBool())
	}

	var events []string
	if diags := got.Events.ElementsAs(t.Context(), &events, false); diags.HasError() {
		t.Fatalf("could not read events from state: %+v", diags)
	}
	if len(events) != 1 || events[0] != "job-completed" {
		t.Errorf("events after Update = %v, want exactly [job-completed] (what the API reported), not the "+
			"two-event set the plan sent", events)
	}

	// signing_secret must still come from the plan: CircleCI never returns the
	// real value on any route, so this is the one field that is correct to
	// leave alone rather than take from the response.
	if got.SigningSecret.ValueString() != "s3cr3t" {
		t.Errorf(`signing_secret after Update = %q, want %q — it must be preserved from the plan `+
			`(the API never returns a usable value), and a future change must not "fix" that into a regression`,
			got.SigningSecret.ValueString(), "s3cr3t")
	}
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
