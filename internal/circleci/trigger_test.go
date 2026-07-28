// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const (
	testTriggerProjectID    = "33333333-3333-3333-3333-333333333333"
	testTriggerDefinitionID = "44444444-4444-4444-4444-444444444444"
)

func TestListTriggers(t *testing.T) {
	t.Parallel()

	// The shape mirrors the API's
	// the CircleCI API (an {"items": [...]} envelope with
	// no next_page_token) and the schedule sub-shape the API returns, where
	// attribution_actor is an object even though a create accepts a bare string.
	client, seen := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{"items":[
			{"id":"t1","name":"acme-bot","event_name":"push","description":"push","created_at":"2024-06-01T09:00:00Z",
			 "checkout_ref":"main","config_ref":"main","event_preset":"github_app.push",
			 "event_source":{"provider":"github_app","repo":{"full_name":"acme/api","external_id":"123456"}}},
			{"id":"t2","name":"nightly","event_name":"Nightly build","checkout_ref":"main","config_ref":"main",
			 "disabled":true,
			 "event_source":{"provider":"schedule",
			                 "schedule":{"cron_expression":"0 0 * * *","attribution_actor":{"id":"a1b2c3"}}},
			 "parameters":{"deploy":true,"replicas":3,"env":"prod","tags":["a","b"]}},
			{"id":"t3","name":"inbound","event_name":"Inbound webhook",
			 "event_source":{"provider":"webhook",
			                 "webhook":{"url":"https://example.com/private/soc/e/t3?secret=**REDACTED**","sender":"inbound"}}}
		]}`)
	})

	triggers, err := client.ListTriggers(context.Background(), testTriggerProjectID, testTriggerDefinitionID)
	if err != nil {
		t.Fatalf("ListTriggers returned error: %v", err)
	}

	if len(triggers) != 3 {
		t.Fatalf("trigger count = %d, want 3", len(triggers))
	}

	repoTrigger := triggers[0]
	if repoTrigger.EventSource.Provider != "github_app" || repoTrigger.EventSource.Repo.FullName != "acme/api" {
		t.Errorf("first trigger event source = %+v, want the github_app repo source", repoTrigger.EventSource)
	}
	if repoTrigger.EventPreset != "github_app.push" || repoTrigger.CheckoutRef != "main" {
		t.Errorf("first trigger = %+v, want the push preset on main", repoTrigger)
	}
	// disabled is omitted rather than sent as false for an enabled trigger.
	if repoTrigger.Disabled != nil || repoTrigger.IsDisabled() {
		t.Errorf("first trigger disabled = %v, want an omitted field reported as enabled", repoTrigger.Disabled)
	}

	scheduled := triggers[1]
	if !scheduled.IsDisabled() {
		t.Error("second trigger IsDisabled() = false, want true")
	}
	if scheduled.EventSource.Schedule.CronExpression != "0 0 * * *" {
		t.Errorf("second trigger cron = %q, want \"0 0 * * *\"", scheduled.EventSource.Schedule.CronExpression)
	}
	if scheduled.EventSource.Schedule.AttributionActor.ID != "a1b2c3" {
		t.Errorf("second trigger attribution actor = %+v, want id a1b2c3", scheduled.EventSource.Schedule.AttributionActor)
	}

	// Parameters are passed through as stored JSON, so a value may be any JSON
	// type. Each must render without scientific notation or Go syntax.
	params := scheduled.ParameterStrings()
	wantParams := map[string]string{
		"deploy":   "true",
		"replicas": "3",
		"env":      "prod",
		"tags":     `["a","b"]`,
	}
	if len(params) != len(wantParams) {
		t.Fatalf("parameters = %v, want %v", params, wantParams)
	}
	for name, want := range wantParams {
		if params[name] != want {
			t.Errorf("parameter %q = %q, want %q", name, params[name], want)
		}
	}

	webhookTrigger := triggers[2]
	if webhookTrigger.EventSource.Webhook.Sender != "inbound" {
		t.Errorf("third trigger webhook = %+v, want sender inbound", webhookTrigger.EventSource.Webhook)
	}
	if webhookTrigger.EventSource.Webhook.URL == "" {
		t.Error("third trigger webhook url is empty, want the inbound endpoint")
	}
	// A trigger with no parameters reports nil, not an empty map, so callers can
	// tell "none" from "an empty one".
	if webhookTrigger.ParameterStrings() != nil {
		t.Errorf("third trigger parameters = %v, want nil", webhookTrigger.ParameterStrings())
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1 (the endpoint is not paginated)", len(*seen))
	}

	wantPath := "/api/v2/projects/" + testTriggerProjectID +
		"/pipeline-definitions/" + testTriggerDefinitionID + "/triggers"
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath || got.query != "" {
		t.Errorf("request = %s %s?%s, want GET %s with no query", got.method, got.path, got.query, wantPath)
	}
}

func TestListTriggersEmpty(t *testing.T) {
	t.Parallel()

	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{"items":[]}`)
	})

	triggers, err := client.ListTriggers(context.Background(), testTriggerProjectID, testTriggerDefinitionID)
	if err != nil {
		t.Fatalf("ListTriggers returned error: %v", err)
	}
	if len(triggers) != 0 {
		t.Errorf("trigger count = %d, want 0", len(triggers))
	}
}

func TestListTriggersNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListError(w, http.StatusNotFound, "Pipeline definition not found.")
	})

	_, err := client.ListTriggers(context.Background(), testTriggerProjectID, testTriggerDefinitionID)
	if !circleci.IsNotFound(err) {
		t.Errorf("ListTriggers error = %v, want a not found error", err)
	}
}

func TestListTriggersEscapesRouteParams(t *testing.T) {
	t.Parallel()

	// Both ids arrive from configuration, so each must be escaped as one path
	// segment rather than changing which route is called.
	client, seen := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{"items":[]}`)
	})

	if _, err := client.ListTriggers(context.Background(), "proj/../evil", "pipe/../evil"); err != nil {
		t.Fatalf("ListTriggers returned error: %v", err)
	}

	wantURI := "/api/v2/projects/proj%2F..%2Fevil/pipeline-definitions/pipe%2F..%2Fevil/triggers"
	if got := (*seen)[0].rawURI; got != wantURI {
		t.Errorf("raw request URI = %q, want %q", got, wantURI)
	}
}
