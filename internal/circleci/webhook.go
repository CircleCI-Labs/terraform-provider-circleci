// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Webhook routes. Webhooks are served by v2 on every deployment, and the
// collection is scoped by scope-id and scope-type *query* parameters rather than
// a path segment.
const webhooksRoute = "/webhook"

// WebhookScopeTypeProject is the only scope-type the webhook endpoints accept.
// The API rejects anything else with HTTP 400, so the provider always sends this
// value.
const WebhookScopeTypeProject = "project"

// WebhookSigningSecretMask is what the API substitutes for a webhook's signing
// secret. A webhook with a secret reads back as this placeholder and one without
// reads back as "", so the configured value can never be recovered from a read.
const WebhookSigningSecretMask = "****"

// The event names the webhook API accepts. The names stay hyphenated even though
// the surrounding JSON keys do not: the API converts keys to snake_case, not
// values.
const (
	// WebhookEventWorkflowCompleted fires when a workflow finishes, whatever its
	// outcome.
	WebhookEventWorkflowCompleted = "workflow-completed"
	// WebhookEventJobCompleted fires when an individual job finishes, whatever
	// its outcome.
	WebhookEventJobCompleted = "job-completed"
)

// WebhookEvents returns every event name the webhook API accepts.
//
// It exists so the provider's schema validator, the attribute description and
// this client cannot drift from one another. Without it an unrecognized event
// name costs a round-trip and comes back as an opaque HTTP 400.
func WebhookEvents() []string {
	return []string{WebhookEventJobCompleted, WebhookEventWorkflowCompleted}
}

// WebhookScope identifies what a webhook watches. Today only projects can be
// watched, so Type is always WebhookScopeTypeProject.
type WebhookScope struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// Webhook is an outbound webhook: an HTTPS endpoint CircleCI posts to when one
// of Events happens within Scope.
//
// The wire format nests the scope, unlike the flat scope_id/scope_type the
// create body takes. CreatedAt and UpdatedAt are kept as the strings the API
// sent (RFC 3339 timestamps) so that they round-trip into Terraform state
// exactly as received.
type Webhook struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	// Events are the event names that trigger delivery, for example
	// "workflow-completed". The names stay hyphenated even though the surrounding
	// JSON keys do not: the API converts keys to snake_case, not values.
	Events    []string     `json:"events"`
	VerifyTLS bool         `json:"verify_tls"`
	Scope     WebhookScope `json:"scope"`
	// SigningSecret is always masked. See WebhookSigningSecretMask.
	SigningSecret string `json:"signing_secret"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// HasSigningSecret reports whether a signing secret is configured on the
// webhook. The value itself is never disclosed, so presence is the only fact a
// read can establish.
func (w Webhook) HasSigningSecret() bool { return w.SigningSecret != "" }

// webhookRoute addresses a single webhook by id.
const webhookRoute = "/webhook/%s"

// WebhookInput is the create/update body for a webhook.
//
// It is a separate type from Webhook rather than a reuse of it, for two reasons.
// The create body takes a nested scope but no id or timestamps, so sending Webhook
// back would either emit fields the API rejects or need enough `omitempty` tags to
// make the read path ambiguous. And SigningSecret must be sent as typed, while on
// Webhook the same field only ever holds the mask — one struct doing both jobs is
// how a masked value ends up being written back as a literal "****".
type WebhookInput struct {
	Name          string       `json:"name"`
	URL           string       `json:"url"`
	Events        []string     `json:"events"`
	VerifyTLS     bool         `json:"verify_tls"`
	SigningSecret string       `json:"signing_secret"`
	Scope         WebhookScope `json:"scope,omitzero"`
}

// CreateWebhook creates an outbound webhook and returns it as stored.
//
// This replaces circleci-sdk-go's webhook.Create. The SDK tags these two fields
// `json:"verify-tls"` and `json:"signing-secret"` — hyphenated — while the API
// documents and accepts `verify_tls` and `signing_secret`. The API ignores keys it
// does not recognize, so a webhook created through the SDK silently got NO signing
// secret however carefully one was configured, and TLS verification fell to the
// server-side default instead of the requested value.
//
// That is a security bug rather than a cosmetic one: the signing secret is what
// lets a receiver distinguish a genuine CircleCI delivery from a forged POST, and
// a practitioner who configured one had every reason to believe it was in force.
func (c *Client) CreateWebhook(ctx context.Context, input WebhookInput) (*Webhook, error) {
	var created Webhook
	if err := c.PostV2(ctx, webhooksRoute, input, &created); err != nil {
		return nil, err
	}

	return &created, nil
}

// GetWebhook returns one webhook by id. A missing webhook is reported as an error
// satisfying IsNotFound.
func (c *Client) GetWebhook(ctx context.Context, id string) (*Webhook, error) {
	var found Webhook
	if err := c.GetV2(ctx, webhookRoute, &found, RouteParams(id)); err != nil {
		return nil, err
	}

	return &found, nil
}

// UpdateWebhook replaces a webhook's mutable fields and returns it as stored.
//
// The route is a PUT, and the scope is not updatable — the API keeps whatever the
// webhook was created with — so Scope is left zero here and omitted by omitzero.
// Sending a different scope would be silently ignored rather than rejected, which
// is worth knowing before anyone tries to "move" a webhook between projects.
func (c *Client) UpdateWebhook(ctx context.Context, id string, input WebhookInput) (*Webhook, error) {
	input.Scope = WebhookScope{}

	var updated Webhook
	if err := c.PutV2(ctx, webhookRoute, input, &updated, RouteParams(id)); err != nil {
		return nil, err
	}

	return &updated, nil
}

// DeleteWebhook deletes a webhook by id.
func (c *Client) DeleteWebhook(ctx context.Context, id string) error {
	return c.DeleteV2(ctx, webhookRoute, RouteParams(id))
}

// ListWebhooks returns every outbound webhook watching a project, following
// pagination to the last page. The result is nil when the project has none.
//
// The webhook service does not paginate today and always answers with a null
// next_page_token, but the response carries the field and the API reserves the
// right to start filling it, so the drain is real rather than decorative.
func (c *Client) ListWebhooks(ctx context.Context, projectID string) ([]Webhook, error) {
	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[Webhook], error) {
		var page PaginatedResponse[Webhook]
		err := c.GetV2(ctx, webhooksRoute, &page,
			Query("scope-id", projectID),
			Query("scope-type", WebhookScopeTypeProject),
			PageToken(pageToken),
		)

		return page, err
	})
}
