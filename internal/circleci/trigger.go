// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Trigger routes.
//
// Like pipeline definitions, these live under /api/v2 but are served by the
// public API service, and CircleCI Server does not route them there. Callers
// must gate on Client.IsCloud before using them.
const triggersRoute = "/projects/%s/pipeline-definitions/%s/triggers"

// triggerRoute addresses a single trigger by id, directly under the project —
// unlike triggersRoute above (create and list), which is nested under the
// pipeline definition:
//
//	GET|POST /api/v2/projects/{project_id}/pipeline-definitions/{pipeline_definition_id}/triggers
//	GET|PATCH|DELETE /api/v2/projects/{project_id}/triggers/{trigger_id}
const triggerRoute = "/projects/%s/triggers/%s"

// The event source providers the trigger endpoints accept. One endpoint covers
// several contracts, and which of a trigger's fields are required — even which
// are permitted — depends on which of these was chosen, so the provider validates
// per provider rather than per attribute.
const (
	// TriggerProviderGitHubApp is CircleCI's GitHub App integration. Its event
	// source is keyed by GitHub's own numeric repository id.
	TriggerProviderGitHubApp = "github_app"
	// TriggerProviderGitHubServer is a GitHub Enterprise Server installation. Same
	// contract as TriggerProviderGitHubApp, but the repository ids are allocated by
	// that installation rather than by github.com.
	TriggerProviderGitHubServer = "github_server"
	// TriggerProviderGitHubOAuth is the older GitHub OAuth integration. Narrower
	// contract than the GitHub App one: it requires an event preset, accepts only
	// two of them, and does not support being disabled.
	TriggerProviderGitHubOAuth = "github_oauth"
	// TriggerProviderWebhook mints an inbound URL that starts the pipeline when
	// posted to.
	TriggerProviderWebhook = "webhook"
	// TriggerProviderSchedule fires on a cron expression, and is the only provider
	// that accepts default pipeline parameters.
	TriggerProviderSchedule = "schedule"
)

// TriggerEventSourceProviders returns every event source provider the trigger
// endpoints accept.
//
// It exists so that the resource's schema validator, its attribute description
// and this client cannot drift from one another — the same reason WebhookEvents
// exists. GitLab is absent because these endpoints do not serve it at all, so a
// trigger cannot be created for a GitLab project through this API however the
// attribute is spelled.
//
// bitbucket_dc (Bitbucket Data Center) is deliberately absent, and that is a
// judgement rather than a fact: the create handler implements it and the
// published descriptions of GET/PATCH/DELETE name it, but the documented set of
// accepted values on *create* does not. A bitbucket_dc trigger can therefore be
// read — and imported — but not written from a configuration. See DESIGN.md.
func TriggerEventSourceProviders() []string {
	return []string{
		TriggerProviderGitHubApp,
		TriggerProviderGitHubServer,
		TriggerProviderGitHubOAuth,
		TriggerProviderWebhook,
		TriggerProviderSchedule,
	}
}

// TriggerRepoEventSourceProviders returns the providers whose event source is a
// repository, so that a create body must carry event_source.repo.external_id.
//
// github_oauth belongs here and was missing for a long time: the create path
// populated a repo only for github_app and github_server, so every github_oauth
// trigger was rejected with an opaque HTTP 400. The API decides this per
// provider in one switch — its github_oauth arm is the same one as github_app's
// — so the provider keeps one list rather than repeating the set at each place
// that needs it.
//
// The external id is a *numeric* repository id for every provider in this list:
// the API parses it as a 64-bit integer and answers 400 with no field reference
// when it does not parse. See TriggerRepoExternalIDIsValid.
func TriggerRepoEventSourceProviders() []string {
	return []string{
		TriggerProviderGitHubApp,
		TriggerProviderGitHubServer,
		TriggerProviderGitHubOAuth,
	}
}

// TriggerProviderNeedsRepo reports whether an event source provider requires a
// repository external id.
func TriggerProviderNeedsRepo(provider string) bool {
	return slices.Contains(TriggerRepoEventSourceProviders(), provider)
}

// TriggerRepoExternalIDIsValid reports whether an event source repository
// external id is in the form the API accepts: the VCS provider's own numeric
// repository id.
//
// Anything else is rejected outright, and the rejection carries no field
// reference — the handler collapses a repository-id parse failure into a bare
// "bad request" — so checking it before the request is the difference between a
// plan-time diagnostic naming the attribute and an apply-time error naming
// nothing.
func TriggerRepoExternalIDIsValid(externalID string) bool {
	_, err := strconv.ParseInt(externalID, 10, 64)

	return err == nil
}

// TriggerProviderRequiresRefs reports whether an event source provider requires
// both checkout_ref and config_ref.
//
// webhook and schedule both do: neither has a VCS event to take a ref from, so
// there is nothing to fall back to and the API rejects a create that
// omits either. The GitHub providers are the opposite case and cannot be
// answered here — a ref is required exactly when the event source repository
// differs from the pipeline definition's, and forbidden when it does not, which
// this provider cannot know without reading the definition.
func TriggerProviderRequiresRefs(provider string) bool {
	return provider == TriggerProviderWebhook || provider == TriggerProviderSchedule
}

// The two event presets the github_oauth contract accepts. They are named
// because GitHubOAuthTriggerEventPresets and TriggerEventPresets must agree on
// them: a preset in the narrow list but not the full one could never be set.
const (
	TriggerEventPresetAllPushes    = "all-pushes"
	TriggerEventPresetOnlyBuildPRs = "only-build-prs"
)

// TriggerEventPresets returns every event preset the trigger endpoints accept.
//
// The single source for the schema validator and the attribute description, as
// WebhookEvents is for webhook event names. Without it an unrecognized preset
// costs a round-trip and comes back as an opaque HTTP 400.
//
// This list is the *enforced* set, not a documented one. A preset is turned into
// trigger rules by a lookup in the API's own event mapping table, and a
// key that is not in that table fails the create with "invalid event key
// provided". Two consequences:
//
//   - "only-branch-delete" used to be in this list and is not a real key. It
//     appears in CircleCI's CLI and in one published spec snapshot, but the
//     mapping table has no entry for it, so every trigger configured with it
//     failed at apply time. It is removed rather than fixed: there is nothing to
//     map it to.
//   - the mapping table also holds one key this list omits on purpose,
//     "pr-comment-starts-with-at-chunk-ai". It is absent from the published v2
//     enum, so offering it would take the provider past the documented contract
//     for a preset nobody can be expected to want.
func TriggerEventPresets() []string {
	return []string{
		TriggerEventPresetAllPushes,
		"only-tags",
		"default-branch-pushes",
		TriggerEventPresetOnlyBuildPRs,
		"only-open-prs",
		"only-labeled-prs",
		"only-merged-prs",
		"only-ready-for-review-prs",
		"only-build-pushes-to-non-draft-prs",
		"only-merged-or-closed-prs",
		"pr-comment-equals-run-ci",
		"non-draft-pr-opened",
		"pushes-to-merge-queues",
	}
}

// GitHubOAuthTriggerEventPresets returns the only two presets a github_oauth
// event source accepts, out of the full TriggerEventPresets set.
//
// The narrowing is per provider rather than per attribute, so it cannot be
// expressed as a schema validator; the resource applies it in ValidateConfig.
func GitHubOAuthTriggerEventPresets() []string {
	return []string{TriggerEventPresetAllPushes, TriggerEventPresetOnlyBuildPRs}
}

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

// TriggerWebhookURLRedacted is the literal text a webhook trigger's
// event_source.webhook.url carries in its secret query parameter's VALUE, in
// place of the real secret, on every route except the one that mints it —
// e.g. "https://.../secret=**REDACTED**". The URL's host and path are real
// either way; only the secret parameter's value is replaced. GET and PATCH
// both do this, even to the very token that had just created the trigger.
// Confirmed against circleci.com 2026-08-22 (an earlier probe recorded here,
// on 2026-08-21, said the whole field read back as this literal string
// verbatim — that was imprecise: the query parameter's value is what does,
// not the URL as a whole, which is why URLIsRedacted below checks for it as a
// substring rather than by equality). See CreateTrigger's doc comment for the
// full transcript. Only a create response's URL ever carries the real secret.
const TriggerWebhookURLRedacted = "**REDACTED**"

// TriggerWebhook is the inbound endpoint of a "webhook" provider trigger.
//
// URL contains the trigger's secret as a query parameter when the caller is
// allowed to see it, and TriggerWebhookURLRedacted in the secret parameter's
// place otherwise, so it must be treated as sensitive.
type TriggerWebhook struct {
	URL    string `json:"url"`
	Sender string `json:"sender"`
}

// URLIsRedacted reports whether URL carries the placeholder every read
// answers with in place of the real secret, rather than the real signed URL a
// create response carries. It is a substring check, not an equality check:
// URL's host and path stay real even when the secret is redacted, so the
// placeholder never occupies the whole field — only its query parameter's
// value does. A caller that wants to keep a previously-seen real URL in
// place — rather than overwriting it with one whose secret is redacted —
// should check this first.
func (w TriggerWebhook) URLIsRedacted() bool {
	return strings.Contains(w.URL, TriggerWebhookURLRedacted)
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
//
// CreatedAt is empty on the value CreateTrigger returns and only on that one:
// the create route omits created_at while Get, Update and List all send it. See
// CreateTrigger's doc comment for the measured responses.
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
// Sender is ever accepted, matching what the create and update routes
// themselves accept for event_source.webhook.
type TriggerWebhookInput struct {
	Sender string `json:"sender"`
}

// TriggerScheduleInput is the create/update body's event_source.schedule.
// AttributionActor is sent as the bare alias string ("current" or "system")
// the caller configured — unlike the read shape (TriggerSchedule), where the
// API resolves it to an object carrying the concrete actor id. Confirmed
// against the API's own OpenAPI schemas and examples.
type TriggerScheduleInput struct {
	CronExpression   string `json:"cron_expression"`
	AttributionActor string `json:"attribution_actor,omitempty"`
}

// TriggerEventSourceInput is the create body's event_source. Repo, Webhook and
// Schedule are pointers so that only the one matching Provider is serialized:
// the API rejects a repo on a webhook trigger and a webhook on anything else.
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

// CreateTrigger creates a trigger on a pipeline definition. The route is nested
// under the pipeline definition — see triggersRoute — unlike Get/Update/Delete
// below.
//
// The response is NOT the same shape GetTrigger returns: it answers HTTP 200
// (not 201) and its body omits created_at. Probed 2026-08-21 against
// circleci.com, one webhook trigger and one schedule trigger:
//
//	POST .../pipeline-definitions/{d}/triggers -> 200
//	  {"id","name","event_name","description","checkout_ref","config_ref",
//	   "event_source","disabled"}            (+ "parameters", when sent)
//	GET  .../projects/{p}/triggers/{id}      -> the same keys PLUS
//	  "created_at":"2026-08-21T15:28:58.954521Z"
//	PATCH .../projects/{p}/triggers/{id}     -> also carries created_at
//	GET  .../pipeline-definitions/{d}/triggers -> also carries created_at
//
// So a caller that needs CreatedAt after a create has to read the trigger back.
// POST is the only one of the four routes missing the field, which is exactly
// what made it easy to assume it was there. This doc comment previously said the create
// response "returns it as stored", and trigger_resource.go's Create dropped its
// read-back citing that sentence — leaving created_at as "" in state forever.
// The published OpenAPI document is what says otherwise; the API is what was
// measured.
//
// The read-back is not interchangeable with this response in the other
// direction either: GET and PATCH both return event_source.webhook.url with
// its secret query parameter's value replaced by TriggerWebhookURLRedacted
// (observed above with the creating token, and reconfirmed 2026-08-22 against
// a live acceptance run — see URLIsRedacted's own comment for the correction
// that run produced), while this response carries the real signed URL.
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
// Repo field at all, so a trigger's event source repository is immutable
// after creation; only the webhook sender and the schedule are.
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
