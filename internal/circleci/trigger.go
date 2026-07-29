// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// Trigger routes.
//
// Like pipeline definitions, these live under /api/v2 but are served by the
// public API service, and CircleCI Server does not route them there. Callers
// must gate on Client.IsCloud before using them.
const triggersRoute = "/projects/%s/pipeline-definitions/%s/triggers"

// triggerRoute addresses a single trigger by id, directly under the project —
// unlike triggersRoute above (create and list), which is nested under the
// pipeline definition. Confirmed against the API's
// the CircleCI API route registration:
//
//	GET|POST /api/v2/projects/{project_id}/pipeline-definitions/{pipeline_definition_id}/triggers
//	GET|PATCH|DELETE /api/v2/projects/{project_id}/triggers/{trigger_id}
const triggerRoute = "/projects/%s/triggers/%s"

// TriggerAttributionActor is the actor a scheduled trigger's pipelines are
// attributed to. A read reports the actor as an object carrying its id, even
// though a create accepts a bare string.
type TriggerAttributionActor struct {
	ID string `json:"id"`
}

// TriggerSchedule is the schedule of a "schedule" provider trigger. It is the
// zero value for every other provider.
type TriggerSchedule struct {
	CronExpression   string                  `json:"cron_expression"`
	AttributionActor TriggerAttributionActor `json:"attribution_actor"`
}

// TriggerWebhook is the inbound endpoint of a "webhook" provider trigger.
//
// URL contains the trigger's secret as a query parameter when the caller is
// allowed to see it, and the literal string "**REDACTED**" in place of the
// secret otherwise, so it must be treated as sensitive.
type TriggerWebhook struct {
	URL    string `json:"url"`
	Sender string `json:"sender"`
}

// TriggerEventSource says what makes a trigger fire. Exactly one of Repo,
// Webhook and Schedule is meaningful, selected by Provider.
type TriggerEventSource struct {
	Provider string          `json:"provider"`
	Repo     Repo            `json:"repo"`
	Webhook  TriggerWebhook  `json:"webhook"`
	Schedule TriggerSchedule `json:"schedule"`
}

// Trigger is a CircleCI pipeline trigger: the event that starts a pipeline
// definition.
//
// Name and Description are both deprecated by the API in favour of
// EventSource.Webhook.Sender and EventName respectively, and the API populates
// Description with the same value as EventName. They are kept because the API
// still sends them.
//
// Disabled is a pointer because the API omits the field rather than sending
// false for an enabled trigger, so nil and false are both "enabled" and must
// not be conflated with "unknown".
type Trigger struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	EventName   string             `json:"event_name"`
	Description string             `json:"description"`
	CreatedAt   string             `json:"created_at"`
	CheckoutRef string             `json:"checkout_ref"`
	ConfigRef   string             `json:"config_ref"`
	EventSource TriggerEventSource `json:"event_source"`
	// EventPreset is the single event key the trigger's rules share, or "" when
	// the rules cover more than one event.
	EventPreset string `json:"event_preset"`
	// Parameters are the default pipeline parameters the trigger supplies. The
	// API passes the stored JSON through unchanged, so a value may be any JSON
	// type rather than only a string; use ParameterStrings to render them.
	Parameters map[string]any `json:"parameters"`
	Disabled   *bool          `json:"disabled"`
}

// IsDisabled reports whether the trigger is disabled, treating an omitted field
// as enabled.
func (t Trigger) IsDisabled() bool { return t.Disabled != nil && *t.Disabled }

// ParameterStrings renders the trigger's default pipeline parameters as strings,
// which is the only form Terraform can hold in a map attribute. It returns nil
// when there are none, so the caller can distinguish "no parameters" from "an
// empty parameter".
func (t Trigger) ParameterStrings() map[string]string {
	if len(t.Parameters) == 0 {
		return nil
	}

	rendered := make(map[string]string, len(t.Parameters))
	for name, value := range t.Parameters {
		rendered[name] = formatParameterValue(value)
	}

	return rendered
}

// formatParameterValue renders one decoded JSON value as a string. Scalars are
// rendered as they were written rather than through %v, so that a large integer
// parameter does not come back as "1e+06"; anything composite is re-encoded as
// JSON so no information is lost.
func formatParameterValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprintf("%v", typed)
		}

		return string(encoded)
	}
}

// ListTriggers returns every trigger attached to a pipeline definition.
//
// The response carries no next_page_token: this endpoint returns the whole set
// in one body, so there is nothing to drain. The result is nil when the
// definition has no triggers.
//
// CircleCI Cloud only. See triggersRoute.
func (c *Client) ListTriggers(ctx context.Context, projectID, pipelineDefinitionID string) ([]Trigger, error) {
	var response itemsResponse[Trigger]
	if err := c.GetV2(ctx, triggersRoute, &response, RouteParams(projectID, pipelineDefinitionID)); err != nil {
		return nil, err
	}

	return response.Items, nil
}

// TriggerWebhookInput is the create/update body's event_source.webhook: only
// Sender is ever accepted, matching the API's
// the CircleCI API's createRequestWebhook and
// handler_update.go's updateRequestWebhook.
type TriggerWebhookInput struct {
	Sender string `json:"sender"`
}

// TriggerScheduleInput is the create/update body's event_source.schedule.
// AttributionActor is sent as the bare alias string ("current" or "system")
// the caller configured — unlike the read shape (TriggerSchedule), where the
// API resolves it to an object carrying the concrete actor id. Confirmed
// against the API's openapi_definitions/v2_endpoints/trigger
// schemas and examples.
type TriggerScheduleInput struct {
	CronExpression   string `json:"cron_expression"`
	AttributionActor string `json:"attribution_actor,omitempty"`
}

// TriggerEventSourceInput is the create body's event_source. Repo, Webhook and
// Schedule are pointers so that only the one matching Provider is serialized:
// the API's createRequest.Validate rejects a repo on a webhook trigger and a
// webhook on anything else.
type TriggerEventSourceInput struct {
	Provider string                `json:"provider"`
	Repo     *RepoInput            `json:"repo,omitempty"`
	Webhook  *TriggerWebhookInput  `json:"webhook,omitempty"`
	Schedule *TriggerScheduleInput `json:"schedule,omitempty"`
}

// CreateTriggerInput is the create body for a trigger. Name and Description
// are deliberately omitted: they are deprecated in favour of
// EventSource.Webhook.Sender and EventName respectively (see Trigger's own
// doc comment), and the provider never populates them.
type CreateTriggerInput struct {
	EventSource TriggerEventSourceInput `json:"event_source"`
	EventPreset string                  `json:"event_preset,omitempty"`
	CheckoutRef string                  `json:"checkout_ref,omitempty"`
	ConfigRef   string                  `json:"config_ref,omitempty"`
	Parameters  map[string]string       `json:"parameters,omitempty"`
	EventName   string                  `json:"event_name,omitempty"`
	Disabled    *bool                   `json:"disabled,omitempty"`
}

// CreateTrigger creates a trigger on a pipeline definition and returns it as
// stored. The route is nested under the pipeline definition — see
// triggersRoute — unlike Get/Update/Delete below.
//
// CircleCI Cloud only. See triggersRoute.
func (c *Client) CreateTrigger(ctx context.Context, projectID, pipelineDefinitionID string, input CreateTriggerInput) (*Trigger, error) {
	var created Trigger
	if err := c.PostV2(ctx, triggersRoute, input, &created, RouteParams(projectID, pipelineDefinitionID)); err != nil {
		return nil, err
	}

	return &created, nil
}

// GetTrigger returns one trigger by id. Unlike Create/List, this addresses the
// trigger directly under the project rather than nested under its pipeline
// definition — see triggerRoute. A missing trigger is reported as an error
// satisfying IsNotFound.
//
// CircleCI Cloud only. See triggerRoute.
func (c *Client) GetTrigger(ctx context.Context, projectID, id string) (*Trigger, error) {
	var found Trigger
	if err := c.GetV2(ctx, triggerRoute, &found, RouteParams(projectID, id)); err != nil {
		return nil, err
	}

	return &found, nil
}

// UpdateTriggerEventSourceInput is the update body's event_source. There is no
// Repo field at all — matching handler_update.go's updateRequestEventSource
// exactly — so a trigger's event source repository is immutable after
// creation; only the webhook sender and the schedule are.
type UpdateTriggerEventSourceInput struct {
	Provider string                `json:"provider"`
	Webhook  *TriggerWebhookInput  `json:"webhook,omitempty"`
	Schedule *TriggerScheduleInput `json:"schedule,omitempty"`
}

// UpdateTriggerInput is the update body for a trigger.
type UpdateTriggerInput struct {
	EventSource *UpdateTriggerEventSourceInput `json:"event_source,omitempty"`
	EventPreset string                         `json:"event_preset,omitempty"`
	CheckoutRef string                         `json:"checkout_ref,omitempty"`
	ConfigRef   string                         `json:"config_ref,omitempty"`
	Parameters  map[string]string              `json:"parameters,omitempty"`
	EventName   string                         `json:"event_name,omitempty"`
	Disabled    *bool                          `json:"disabled,omitempty"`
}

// UpdateTrigger updates a trigger's mutable fields and returns it as stored.
//
// CircleCI Cloud only. See triggerRoute.
func (c *Client) UpdateTrigger(ctx context.Context, projectID, id string, input UpdateTriggerInput) (*Trigger, error) {
	var updated Trigger
	if err := c.PatchV2(ctx, triggerRoute, input, &updated, RouteParams(projectID, id)); err != nil {
		return nil, err
	}

	return &updated, nil
}

// DeleteTrigger deletes a trigger by id.
//
// CircleCI Cloud only. See triggerRoute.
func (c *Client) DeleteTrigger(ctx context.Context, projectID, id string) error {
	return c.DeleteV2(ctx, triggerRoute, RouteParams(projectID, id))
}
