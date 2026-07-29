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

const testWebhookScopeID = "2ddfa678-e24e-4417-b4be-404bab46dc51"

func TestListWebhooks(t *testing.T) {
	t.Parallel()

	// The shape mirrors the v2 API's list-webhooks-handler
	// (the CircleCI API): a nested scope object, snake_case
	// keys, hyphenated *event names*, and next_page_token explicitly null because
	// webhook-service does not paginate yet.
	client, seen := pageListServer(t, `{"items":[
		{"id":"8c6dfb88-d24d-408c-9775-140e8abb88d5","name":"webhook1","url":"https://www.url.com",
		 "events":["workflow-completed","job-completed"],"verify_tls":true,"signing_secret":"****",
		 "scope":{"id":"`+testWebhookScopeID+`","type":"project"},
		 "created_at":"2014-02-11T22:40:37Z","updated_at":"2014-02-11T22:40:37Z"},
		{"id":"11111111-1111-1111-1111-111111111111","name":"webhook2","url":"https://other.example.com",
		 "events":["job-completed"],"verify_tls":false,"signing_secret":"",
		 "scope":{"id":"`+testWebhookScopeID+`","type":"project"},
		 "created_at":"2015-02-11T22:40:37Z","updated_at":"2016-02-11T22:40:37Z"}
	],"next_page_token":null}`)

	webhooks, err := client.ListWebhooks(context.Background(), testWebhookScopeID)
	if err != nil {
		t.Fatalf("ListWebhooks returned error: %v", err)
	}

	if len(webhooks) != 2 {
		t.Fatalf("webhook count = %d, want 2", len(webhooks))
	}

	first := webhooks[0]
	if first.Name != "webhook1" || first.URL != "https://www.url.com" {
		t.Errorf("first webhook = %+v, want webhook1 at https://www.url.com", first)
	}
	if !first.VerifyTLS {
		t.Error("first webhook verify_tls = false, want true")
	}
	// The nested scope must be unpacked rather than left empty.
	if first.Scope.ID != testWebhookScopeID || first.Scope.Type != circleci.WebhookScopeTypeProject {
		t.Errorf("first webhook scope = %+v, want the project scope", first.Scope)
	}
	if len(first.Events) != 2 || first.Events[0] != "workflow-completed" {
		t.Errorf("first webhook events = %v, want the hyphenated event names", first.Events)
	}
	// The secret is masked, never disclosed, so only its presence is knowable.
	if first.SigningSecret != circleci.WebhookSigningSecretMask || !first.HasSigningSecret() {
		t.Errorf("first webhook signing_secret = %q, want %q", first.SigningSecret, circleci.WebhookSigningSecretMask)
	}
	if webhooks[1].HasSigningSecret() {
		t.Error("second webhook reports a signing secret, but the API sent \"\"")
	}
	if webhooks[1].UpdatedAt != "2016-02-11T22:40:37Z" {
		t.Errorf("second webhook updated_at = %q, want the timestamp verbatim", webhooks[1].UpdatedAt)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}

	// The scope is carried in query parameters, and scope-type is always
	// "project" because the API rejects anything else.
	wantPath := "/api/v2/webhook"
	wantQuery := "scope-id=" + testWebhookScopeID + "&scope-type=project"
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath || got.query != wantQuery {
		t.Errorf("request = %s %s?%s, want GET %s?%s", got.method, got.path, got.query, wantPath, wantQuery)
	}
}

func TestListWebhooksDrainsPages(t *testing.T) {
	t.Parallel()

	// webhook-service does not paginate today, but the field exists and the API
	// reserves the right to fill it, so a non-null token must be followed.
	client, seen := pageListServer(t,
		`{"items":[{"id":"w1","scope":{"id":"`+testWebhookScopeID+`","type":"project"}}],"next_page_token":"tok-2"}`,
		`{"items":[{"id":"w2","scope":{"id":"`+testWebhookScopeID+`","type":"project"}}],"next_page_token":null}`,
	)

	webhooks, err := client.ListWebhooks(context.Background(), testWebhookScopeID)
	if err != nil {
		t.Fatalf("ListWebhooks returned error: %v", err)
	}
	if len(webhooks) != 2 {
		t.Fatalf("webhook count = %d, want 2 (both pages drained)", len(webhooks))
	}
	if len(*seen) != 2 {
		t.Fatalf("request count = %d, want 2", len(*seen))
	}

	// Query parameters are sorted, so the token sorts before the scope.
	wantQuery := "page-token=tok-2&scope-id=" + testWebhookScopeID + "&scope-type=project"
	if got := (*seen)[1].query; got != wantQuery {
		t.Errorf("second request query = %q, want %q", got, wantQuery)
	}
}

func TestListWebhooksEmpty(t *testing.T) {
	t.Parallel()

	client, _ := pageListServer(t, `{"items":[],"next_page_token":null}`)

	webhooks, err := client.ListWebhooks(context.Background(), testWebhookScopeID)
	if err != nil {
		t.Fatalf("ListWebhooks returned error: %v", err)
	}
	if len(webhooks) != 0 {
		t.Errorf("webhook count = %d, want 0", len(webhooks))
	}
}

func TestListWebhooksBadRequest(t *testing.T) {
	t.Parallel()

	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListError(w, http.StatusBadRequest, "Invalid scope parameters")
	})

	_, err := client.ListWebhooks(context.Background(), "not-a-uuid")
	if err == nil {
		t.Fatal("ListWebhooks returned no error for a 400, want one")
	}
	if detail := circleci.Detail(err); detail == "" {
		t.Error("Detail() = \"\", want the server message")
	}
}

// webhookCall is one request the fake webhook API received, including its decoded
// body — v2Request does not capture bodies, and the body is the whole point here.
type webhookCall struct {
	method string
	path   string
	body   map[string]any
}

// newWebhookServer serves the single-webhook routes, recording every call.
func newWebhookServer(t *testing.T, status int, response string) (*circleci.Client, *[]webhookCall) {
	t.Helper()

	var calls []webhookCall

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("request body is not JSON: %v (%s)", err, raw)
			}
		}
		calls = append(calls, webhookCall{method: r.Method, path: r.URL.Path, body: body})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)

	return circleci.New(circleci.Config{Host: srv.URL, Token: "tok"}), &calls
}

// storedWebhook is the response shape the API returns for a single webhook, with
// the signing secret masked exactly as production masks it.
const storedWebhook = `{
	"id": "33333333-4444-5555-6666-777777777777",
	"name": "hook-1",
	"url": "https://example.com/hook",
	"verify_tls": true,
	"signing_secret": "****",
	"scope": {"id": "2ddfa678-e24e-4417-b4be-404bab46dc51", "type": "project"},
	"events": ["workflow-completed"],
	"created_at": "2024-07-01T00:00:00.000Z",
	"updated_at": "2024-07-01T00:00:00.000Z"
}`

// TestCreateWebhookSendsSnakeCaseSecretAndVerifyTLS is the client-level regression
// test for the shipped security bug.
//
// circleci-sdk-go tagged these two fields `json:"signing-secret"` and
// `json:"verify-tls"`. The API reads snake_case and ignores keys it does not
// recognize, so every webhook this provider created had NO signing secret, whatever
// was configured. Asserting the hyphenated keys are absent matters as much as
// asserting the correct ones are present.
func TestCreateWebhookSendsSnakeCaseSecretAndVerifyTLS(t *testing.T) {
	t.Parallel()

	client, calls := newWebhookServer(t, http.StatusOK, storedWebhook)

	created, err := client.CreateWebhook(context.Background(), circleci.WebhookInput{
		Name:          "hook-1",
		URL:           "https://example.com/hook",
		Events:        []string{"workflow-completed"},
		VerifyTLS:     true,
		SigningSecret: "s3cr3t",
		Scope:         circleci.WebhookScope{ID: testWebhookScopeID, Type: circleci.WebhookScopeTypeProject},
	})
	if err != nil {
		t.Fatalf("CreateWebhook returned error: %v", err)
	}
	if created.ID != "33333333-4444-5555-6666-777777777777" {
		t.Errorf("created id = %q, unexpected", created.ID)
	}

	if len(*calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(*calls))
	}
	call := (*calls)[0]

	if call.method != http.MethodPost || call.path != "/api/v2/webhook" {
		t.Errorf("request = %s %s, want POST /api/v2/webhook", call.method, call.path)
	}
	if call.body["signing_secret"] != "s3cr3t" {
		t.Errorf("signing_secret = %v, want s3cr3t — a webhook created without its secret "+
			"cannot be authenticated by its receiver", call.body["signing_secret"])
	}
	if call.body["verify_tls"] != true {
		t.Errorf("verify_tls = %v, want true", call.body["verify_tls"])
	}
	for _, wrong := range []string{"signing-secret", "verify-tls"} {
		if _, present := call.body[wrong]; present {
			t.Errorf("body carries %q, which the API does not read — the SDK's tags appear to "+
				"have come back", wrong)
		}
	}

	// The scope is nested, and required on create.
	scope, _ := call.body["scope"].(map[string]any)
	if scope["id"] != testWebhookScopeID || scope["type"] != "project" {
		t.Errorf("scope = %v, want id=%s type=project", call.body["scope"], testWebhookScopeID)
	}
}

// TestUpdateWebhookOmitsScope covers the deliberate zeroing in UpdateWebhook: the
// scope is not updatable, and the API silently ignores a different one rather than
// rejecting it — so sending it would invite the belief that a webhook can be moved
// between projects.
func TestUpdateWebhookOmitsScope(t *testing.T) {
	t.Parallel()

	client, calls := newWebhookServer(t, http.StatusOK, storedWebhook)

	_, err := client.UpdateWebhook(context.Background(), "33333333-4444-5555-6666-777777777777",
		circleci.WebhookInput{
			Name:          "hook-renamed",
			URL:           "https://example.com/hook-2",
			Events:        []string{"workflow-completed", "job-completed"},
			VerifyTLS:     false,
			SigningSecret: "rotated",
			// Deliberately set: UpdateWebhook must clear it.
			Scope: circleci.WebhookScope{ID: "should-be-dropped", Type: "project"},
		})
	if err != nil {
		t.Fatalf("UpdateWebhook returned error: %v", err)
	}

	call := (*calls)[0]
	if call.method != http.MethodPut ||
		call.path != "/api/v2/webhook/33333333-4444-5555-6666-777777777777" {
		t.Errorf("request = %s %s, want PUT /api/v2/webhook/{id}", call.method, call.path)
	}
	if _, present := call.body["scope"]; present {
		t.Errorf("update body carries a scope (%v); it is not updatable and must be omitted",
			call.body["scope"])
	}
	// A rotated secret must reach the wire, or the practitioner believes they have
	// revoked the old one when they have not.
	if call.body["signing_secret"] != "rotated" {
		t.Errorf("signing_secret = %v, want rotated", call.body["signing_secret"])
	}
	// false must be sent, not omitted: omitempty on a bool would make "disable TLS
	// verification" unexpressible.
	if call.body["verify_tls"] != false {
		t.Errorf("verify_tls = %v, want false to be sent explicitly", call.body["verify_tls"])
	}
}

func TestGetWebhookAndDelete(t *testing.T) {
	t.Parallel()

	client, calls := newWebhookServer(t, http.StatusOK, storedWebhook)

	found, err := client.GetWebhook(context.Background(), "33333333-4444-5555-6666-777777777777")
	if err != nil {
		t.Fatalf("GetWebhook returned error: %v", err)
	}
	// The API only ever returns the mask, which is why HasSigningSecret exists and
	// why no data source exposes the value itself.
	if found.SigningSecret != circleci.WebhookSigningSecretMask {
		t.Errorf("signing secret read back as %q, want the mask %q",
			found.SigningSecret, circleci.WebhookSigningSecretMask)
	}
	if !found.HasSigningSecret() {
		t.Error("HasSigningSecret() = false for a webhook with a masked secret, want true")
	}

	if err := client.DeleteWebhook(context.Background(), "33333333-4444-5555-6666-777777777777"); err != nil {
		t.Fatalf("DeleteWebhook returned error: %v", err)
	}

	if len(*calls) != 2 {
		t.Fatalf("made %d requests, want 2", len(*calls))
	}
	if (*calls)[1].method != http.MethodDelete {
		t.Errorf("second request method = %q, want DELETE", (*calls)[1].method)
	}
}

func TestWebhookNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newWebhookServer(t, http.StatusNotFound, `{"message":"Webhook not found"}`)

	if _, err := client.GetWebhook(context.Background(), "missing"); !circleci.IsNotFound(err) {
		t.Errorf("GetWebhook error = %v, want one satisfying IsNotFound", err)
	}
	if err := client.DeleteWebhook(context.Background(), "missing"); !circleci.IsNotFound(err) {
		t.Errorf("DeleteWebhook error = %v, want one satisfying IsNotFound", err)
	}
}

// TestWebhookWithoutSecretReadsBackEmpty covers the other side of the mask: a
// webhook with no secret reads back as "" rather than "****", which is the only way
// HasSigningSecret can distinguish the two.
func TestWebhookWithoutSecretReadsBackEmpty(t *testing.T) {
	t.Parallel()

	client, _ := newWebhookServer(t, http.StatusOK, `{
		"id": "33333333-4444-5555-6666-777777777777",
		"name": "hook-1",
		"url": "https://example.com/hook",
		"verify_tls": true,
		"signing_secret": "",
		"scope": {"id": "2ddfa678-e24e-4417-b4be-404bab46dc51", "type": "project"},
		"events": ["workflow-completed"]
	}`)

	found, err := client.GetWebhook(context.Background(), "33333333-4444-5555-6666-777777777777")
	if err != nil {
		t.Fatalf("GetWebhook returned error: %v", err)
	}
	if found.HasSigningSecret() {
		t.Error("HasSigningSecret() = true for a webhook with no secret, want false")
	}
}
