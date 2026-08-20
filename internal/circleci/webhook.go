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

// The event names the webhook API accepts. These are values, not keys, and no
// rewriting happens to them in either direction — they are hyphenated on the
// request and hyphenated in the response. The *keys* around them are the
// asymmetric part; see WebhookInput.
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
// of Events happens within Scope, as the API *returns* it.
//
// Response keys are snake_case: verify_tls and signing_secret. Request keys are
// not — they are hyphenated — which is why writes go through WebhookInput and
// this type is never sent as a request body. See WebhookInput for the evidence.
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
	// "workflow-completed". Event names are hyphenated in both directions; only
	// the keys differ between request and response.
	Events []string `json:"events"`
	// VerifyTLS reads back as verify_tls, snake_case. The request key is
	// verify-tls; see WebhookInput.
	VerifyTLS bool         `json:"verify_tls"`
	Scope     WebhookScope `json:"scope"`
	// SigningSecret is always masked, and reads back as signing_secret,
	// snake_case. The request key is signing-secret; see WebhookInput. The key is
	// absent altogether from the response of a webhook that has no secret, which
	// decodes to "" — see WebhookSigningSecretMask and HasSigningSecret.
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
// # THE REQUEST KEYS ARE HYPHENATED. THE RESPONSE KEYS ARE SNAKE_CASE.
//
// This is not a typo, and it is not a mistake to "clean up". The webhook routes
// read `verify-tls` and `signing-secret` on the way in and report `verify_tls`
// and `signing_secret` on the way out. The request side ignores any key it does
// not recognize, so the snake_case spellings are accepted with a 2xx and
// silently dropped. Reproduced against the live API on 2026-08-20, same project,
// two creates seconds apart:
//
//	POST /api/v2/webhook  {"verify-tls":true,"signing-secret":"..."}
//	  -> 201 {"verify_tls":true,"signing_secret":"****"}          honoured
//
//	POST /api/v2/webhook  {"verify_tls":true,"signing_secret":"..."}
//	  -> 201 {"verify_tls":false}                                 both dropped
//
//	PUT  /api/v2/webhook/{id} {"verify-tls":false,"signing-secret":""}
//	  -> 200 {"verify_tls":false}                                 honoured
//
//	PUT  /api/v2/webhook/{id} {"verify_tls":false,"signing_secret":""}
//	  -> 200 {"verify_tls":true,"signing_secret":"****"}           unchanged: dropped
//
// The published OpenAPI document says exactly this. Both keys are also optional
// on create: a body carrying neither is a 201 whose stored verify_tls is
// **false**, which is what makes the snake_case spelling so quiet — nothing
// fails, TLS verification is simply off and the receiver has no secret to
// authenticate deliveries with.
//
// Provider consequences of getting this wrong, both of which shipped once:
//
//   - the signing secret never reaches CircleCI, so a receiver cannot tell a
//     genuine delivery from a forged POST while the configuration says it can;
//   - `verify_tls = true` in a configuration produces a webhook with TLS
//     verification off, and because the provider writes state from the response
//     the apply then fails with "Provider produced inconsistent result after
//     apply: .verify_tls: was cty.True, but now cty.False".
//
// # WHY TWO TYPES RATHER THAN A CUSTOM MarshalJSON
//
// A single struct cannot carry both spellings in plain tags, so the asymmetry has
// to live somewhere. It lives in the type system here — Webhook decodes
// responses, WebhookInput encodes requests — rather than in a MarshalJSON on a
// shared struct, because:
//
//   - the two directions already differ in more than spelling. The request takes
//     no id and no timestamps, and must send a real secret where the response
//     only ever holds the "****" mask. A shared struct needs `omitempty` on
//     fields whose zero value is meaningful (verify_tls false) and invites a
//     masked value being written back as a literal one;
//   - a MarshalJSON hides the wire names inside a method body, where the next
//     reader looking at struct tags sees `json:"verify_tls"` and concludes the
//     request sends snake_case. Tags are what people read and what people grep;
//   - the asymmetry is then unit-testable by serialising this type alone, with
//     no server involved: TestWebhookInputMarshalsHyphenatedRequestKeys.
//
// Anyone tempted to make these two tags match Webhook's should run that test and
// then TestAccWebhookResource against a real organization before believing it.
type WebhookInput struct {
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	Events []string `json:"events"`
	// VerifyTLS is sent as verify-tls, hyphenated, and NOT as verify_tls. See the
	// type comment: verify_tls on a request is dropped and TLS verification falls
	// to the server-side default of off.
	VerifyTLS bool `json:"verify-tls"`
	// SigningSecret is sent as signing-secret, hyphenated, and NOT as
	// signing_secret. See the type comment: signing_secret on a request is
	// dropped and the webhook is stored with no secret at all.
	SigningSecret string       `json:"signing-secret"`
	Scope         WebhookScope `json:"scope,omitzero"`
}

// CreateWebhook creates an outbound webhook and returns it as stored.
//
// The request body's verify-tls and signing-secret keys are hyphenated while the
// response's are snake_case, and sending the response spelling gets a 201 with
// both values silently discarded. WebhookInput documents the API behaviour and
// the live-API evidence; read it before changing a tag here.
//
// Getting it wrong is a security bug rather than a cosmetic one: the signing
// secret is what lets a receiver distinguish a genuine CircleCI delivery from a
// forged POST, and a practitioner who configured one has every reason to believe
// it is in force.
func (c *Client) CreateWebhook(ctx context.Context, input WebhookInput) (*Webhook, error) {
	var created Webhook
	if err := c.PostV2(ctx, webhooksRoute, input, &created); err != nil {
		return nil, err
	}

	return &created, nil
}

// GetWebhook returns one webhook by id. A missing webhook is reported as an error
// satisfying IsNotFound.
//
// A webhook the token may not see is a different answer, and the difference
// matters for drift detection. The route sits in front of a separate webhook
// service whose gRPC statuses are mapped one for one: NOT_FOUND becomes 404, and
// PERMISSION_DENIED becomes **403** carrying "The webhook does not exist or you
// do not have the required permission" — an anti-enumeration message delivered
// with the forbidden status rather than folded into the 404. So 404 really does
// mean gone and is safe to treat as drift, while 403 means "cannot tell" and must
// not be, or a token that merely lost permission would drop a live webhook — and
// its signing secret — out of state and have it recreated.
//
// INVALID_ARGUMENT and FAILED_PRECONDITION both become 400, so a malformed id and
// a rejected event name are indistinguishable by status alone.
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
