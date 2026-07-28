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
