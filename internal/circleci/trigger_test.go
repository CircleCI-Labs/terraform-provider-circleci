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

func TestCreateTriggerGithubAppRequest(t *testing.T) {
	t.Parallel()

	srv, rec := recordedBodyServer(t, `{"id":"t1","event_source":{"provider":"github_app",
		"repo":{"full_name":"acme/api","external_id":"123456"}},"event_preset":"all-pushes"}`)
	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	input := circleci.CreateTriggerInput{
		EventSource: circleci.TriggerEventSourceInput{
			Provider: "github_app",
			Repo:     &circleci.RepoInput{ExternalID: "123456"},
		},
		EventPreset: "all-pushes",
	}

	created, err := client.CreateTrigger(context.Background(), testTriggerProjectID, testTriggerDefinitionID, input)
	if err != nil {
		t.Fatalf("CreateTrigger returned error: %v", err)
	}

	if rec.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", rec.Method)
	}
	wantPath := "/api/v2/projects/" + testTriggerProjectID + "/pipeline-definitions/" + testTriggerDefinitionID + "/triggers"
	if rec.Path != wantPath {
		t.Errorf("path = %q, want %q", rec.Path, wantPath)
	}

	eventSource, _ := rec.Body["event_source"].(map[string]any)
	if eventSource["provider"] != "github_app" {
		t.Errorf("request event_source.provider = %v, want github_app", eventSource["provider"])
	}
	repo, _ := eventSource["repo"].(map[string]any)
	if repo["external_id"] != "123456" {
		t.Errorf("request event_source.repo.external_id = %v, want 123456", repo["external_id"])
	}
	// The create body must not carry webhook or schedule for a github_app
	// trigger: the API's createRequest.Validate rejects a webhook object for
	// any provider other than "webhook".
	if _, present := eventSource["webhook"]; present {
		t.Error("request event_source carries webhook for a github_app trigger, want it omitted")
	}

	if created.ID != "t1" {
		t.Errorf("created.ID = %q, want t1", created.ID)
	}
}

func TestCreateTriggerWebhookRequest(t *testing.T) {
	t.Parallel()

	srv, rec := recordedBodyServer(t, `{"id":"t2","event_name":"deploy-hook",
		"event_source":{"provider":"webhook","webhook":{"url":"https://example.com/hook?secret=abc","sender":"datadog"}}}`)
	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	input := circleci.CreateTriggerInput{
		EventSource: circleci.TriggerEventSourceInput{
			Provider: "webhook",
			Webhook:  &circleci.TriggerWebhookInput{Sender: "datadog"},
		},
		EventName: "deploy-hook",
	}

	created, err := client.CreateTrigger(context.Background(), testTriggerProjectID, testTriggerDefinitionID, input)
	if err != nil {
		t.Fatalf("CreateTrigger returned error: %v", err)
	}

	eventSource, _ := rec.Body["event_source"].(map[string]any)
	webhook, _ := eventSource["webhook"].(map[string]any)
	if webhook["sender"] != "datadog" {
		t.Errorf("request event_source.webhook.sender = %v, want datadog", webhook["sender"])
	}
	if _, present := eventSource["repo"]; present {
		t.Error("request event_source carries repo for a webhook trigger, want it omitted")
	}
	if rec.Body["event_name"] != "deploy-hook" {
		t.Errorf("request event_name = %v, want deploy-hook", rec.Body["event_name"])
	}

	if created.EventSource.Webhook.Sender != "datadog" {
		t.Errorf("created event_source.webhook.sender = %q, want datadog", created.EventSource.Webhook.Sender)
	}
}

func TestCreateTriggerScheduleRequest(t *testing.T) {
	t.Parallel()

	// AttributionActor on create is the bare alias string; the API resolves it
	// to an object carrying the concrete actor id on read, per
	// openapi_definitions/v2_endpoints/trigger examples.
	srv, rec := recordedBodyServer(t, `{"id":"t3","event_name":"nightly",
		"event_source":{"provider":"schedule","schedule":{"cron_expression":"0 0 * * *",
		"attribution_actor":{"id":"a1b2c3"}}},"parameters":{"env":"prod"}}`)
	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	input := circleci.CreateTriggerInput{
		EventSource: circleci.TriggerEventSourceInput{
			Provider: "schedule",
			Schedule: &circleci.TriggerScheduleInput{
				CronExpression:   "0 0 * * *",
				AttributionActor: "system",
			},
		},
		EventName:   "nightly",
		CheckoutRef: "main",
		ConfigRef:   "main",
		Parameters:  map[string]string{"env": "prod"},
	}

	if _, err := client.CreateTrigger(context.Background(), testTriggerProjectID, testTriggerDefinitionID, input); err != nil {
		t.Fatalf("CreateTrigger returned error: %v", err)
	}

	eventSource, _ := rec.Body["event_source"].(map[string]any)
	schedule, _ := eventSource["schedule"].(map[string]any)
	if schedule["cron_expression"] != "0 0 * * *" {
		t.Errorf("request event_source.schedule.cron_expression = %v, want \"0 0 * * *\"", schedule["cron_expression"])
	}
	if schedule["attribution_actor"] != "system" {
		t.Errorf("request event_source.schedule.attribution_actor = %v, want bare string \"system\"", schedule["attribution_actor"])
	}
	if rec.Body["parameters"] == nil {
		t.Error("request has no parameters, want env")
	}
	if rec.Body["checkout_ref"] != "main" || rec.Body["config_ref"] != "main" {
		t.Errorf("request checkout_ref/config_ref = %v/%v, want main/main", rec.Body["checkout_ref"], rec.Body["config_ref"])
	}
}

func TestGetTriggerRequest(t *testing.T) {
	t.Parallel()

	// Get addresses the trigger directly under the project, NOT nested under
	// its pipeline definition — confirmed against the API's
	// the CircleCI API route registration.
	srv, rec := recordedBodyServer(t, `{"id":"t1","event_source":{"provider":"github_app"}}`)
	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	found, err := client.GetTrigger(context.Background(), testTriggerProjectID, "t1")
	if err != nil {
		t.Fatalf("GetTrigger returned error: %v", err)
	}

	if rec.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", rec.Method)
	}
	wantPath := "/api/v2/projects/" + testTriggerProjectID + "/triggers/t1"
	if rec.Path != wantPath {
		t.Errorf("path = %q, want %q", rec.Path, wantPath)
	}
	if found.ID != "t1" {
		t.Errorf("found.ID = %q, want t1", found.ID)
	}
}

func TestGetTriggerNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusNotFound, `{"message":"Trigger not found."}`)
	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := client.GetTrigger(context.Background(), testTriggerProjectID, "nope")
	if !circleci.IsNotFound(err) {
		t.Errorf("GetTrigger error = %v, want a not found error", err)
	}
}

func TestUpdateTriggerRequest(t *testing.T) {
	t.Parallel()

	// The update body's event_source has no repo field at all — verified
	// against handler_update.go's updateRequestEventSource — so this pins that
	// a repo is never sent even for a github_app trigger's update.
	srv, rec := recordedBodyServer(t, `{"id":"t1","disabled":true,
		"event_source":{"provider":"github_app","repo":{"full_name":"acme/api","external_id":"123456"}}}`)
	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	disabled := true
	input := circleci.UpdateTriggerInput{
		EventSource: &circleci.UpdateTriggerEventSourceInput{Provider: "github_app"},
		Disabled:    &disabled,
	}

	updated, err := client.UpdateTrigger(context.Background(), testTriggerProjectID, "t1", input)
	if err != nil {
		t.Fatalf("UpdateTrigger returned error: %v", err)
	}

	if rec.Method != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", rec.Method)
	}
	wantPath := "/api/v2/projects/" + testTriggerProjectID + "/triggers/t1"
	if rec.Path != wantPath {
		t.Errorf("path = %q, want %q", rec.Path, wantPath)
	}
	if rec.Body["disabled"] != true {
		t.Errorf("request disabled = %v, want true", rec.Body["disabled"])
	}
	eventSource, _ := rec.Body["event_source"].(map[string]any)
	if _, present := eventSource["repo"]; present {
		t.Error("update request event_source carries repo, want it omitted (repo is immutable on update)")
	}

	if !updated.IsDisabled() {
		t.Error("updated.IsDisabled() = false, want true")
	}
}

func TestDeleteTriggerRequest(t *testing.T) {
	t.Parallel()

	// Delete, like Get and Update, addresses the trigger directly under the
	// project rather than nested under its pipeline definition.
	srv, rec := recordedBodyServer(t, `{"message":"Trigger deleted."}`)
	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if err := client.DeleteTrigger(context.Background(), testTriggerProjectID, "t1"); err != nil {
		t.Fatalf("DeleteTrigger returned error: %v", err)
	}

	if rec.Method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", rec.Method)
	}
	wantPath := "/api/v2/projects/" + testTriggerProjectID + "/triggers/t1"
	if rec.Path != wantPath {
		t.Errorf("path = %q, want %q", rec.Path, wantPath)
	}
}

func TestDeleteTriggerNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusNotFound, `{"message":"Trigger not found."}`)
	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	err := client.DeleteTrigger(context.Background(), testTriggerProjectID, "nope")
	if !circleci.IsNotFound(err) {
		t.Errorf("DeleteTrigger error = %v, want a not found error", err)
	}
}
