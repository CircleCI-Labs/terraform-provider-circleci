// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
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
