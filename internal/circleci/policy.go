// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// Routes for config policies.
//
// The {context} segment is the *policy* context, a namespace for a bundle of
// Rego policies. It has nothing to do with a CircleCI context (the resource that
// holds shared environment variables), despite the shared path segment name.
//
// These routes are owned by a dedicated backend, not by the core v2 API
// backend, and unlike the OTel exporter routes they ARE reachable on CircleCI
// Server: Server does deploy that backend, and its gateway forwards
// /api/v2/owner/<id>/context/<ctx>/policy-bundle and .../decision to it — and
// the decision prefix covers .../decision/settings. Nothing here is gated.
//
// {ownerID} must be a UUID: every handler validates it with an is-UUID rule, so
// an organization slug is a 400 rather than a lookup.
//
// Errors are the {"error": "<message>"} shape, which circleci.Detail already
// prefers over the raw body.
const (
	policyBundleRoute           = "/owner/%s/context/%s/policy-bundle"
	policyDocumentRoute         = "/owner/%s/context/%s/policy-bundle/%s"
	policyDecisionSettingsRoute = "/owner/%s/context/%s/decision/settings"
)

// PolicyContextConfig is the policy context that governs pipeline
// configuration, and the one nearly every caller wants. CircleCI also reserves
// "custom" for policies evaluated against caller-supplied data.
const PolicyContextConfig = "config"

// PolicyContextCustom is documented by CircleCI but NOT accepted by the API.
//
// Confirmed against every route that takes a {context} path segment: create
// bundle, get bundle and get document each validate it against the single
// value "config", and both decision-settings routes validate it the same way
// and then re-check it a second time. A request naming "custom" is rejected
// with HTTP 400 on all five. There is no route anywhere in the API for which
// "custom" is valid.
//
// It is retained as a named constant so the rejection is discoverable rather than
// mysterious, and it is deliberately not offered in the resource validators.
//
// PolicyContextCustom is the policy context for decisions made against
// arbitrary data rather than a pipeline's configuration.
const PolicyContextCustom = "custom"

// Policy is a single Rego document within a bundle.
//
// Note that the API caps a whole bundle upload at roughly 2.5 MiB and answers
// HTTP 413 beyond that; there is no per-policy limit.
type Policy struct {
	// Name is the policy's name within the bundle, conventionally a filename
	// ending in .rego.
	Name string `json:"name"`
	// Content is the Rego source.
	Content string `json:"content"`
	// CreatedAt is when the policy was first pushed.
	CreatedAt string `json:"created_at"`
	// CreatedBy identifies who pushed it.
	CreatedBy string `json:"created_by"`
}

// PolicyBundle is a policy context's whole set of policies, keyed by name.
//
// The API has no route that adds or removes one policy: POSTing a bundle
// replaces every policy in the context at once. A bundle is therefore the
// smallest unit that can be managed, which is why the Terraform resource is
// modelled on the bundle rather than on the individual policy.
type PolicyBundle map[string]Policy

// UnmarshalJSON decodes a bundle response.
//
// The published schema declares the bundle as an object whose additional
// properties carry an "items" keyword but no "type", which is not valid JSON
// Schema and so does not pin the entry down to either a Policy object or an
// array of them. This decoder was therefore written to tolerate all three
// plausible shapes: a bare Rego string, a Policy object, and a one-element array
// of Policy objects.
//
// This has since been confirmed against the API directly: **every entry is
// in practice a flat object**, and the other two branches below are
// unreachable in production. The tolerance is kept deliberately rather than
// tightened — the branches are cheap, they are covered by tests, and an
// experimental-adjacent endpoint whose own schema is malformed is not one to
// hard-code an assumption against. What must not happen is code elsewhere
// *relying* on a shape this decoder merely tolerates, so:
//
//   - created_at is always present and is an RFC 3339 timestamp (the service
//     marshals a time.Time, never null);
//   - name is always present and always equals the map key, so the
//     key-is-authoritative fallback below is belt and braces.
func (b *PolicyBundle) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decoding policy bundle: %w", err)
	}

	bundle := make(PolicyBundle, len(raw))
	for name, value := range raw {
		policy, err := decodePolicyEntry(name, value)
		if err != nil {
			return err
		}

		bundle[name] = policy
	}

	*b = bundle

	return nil
}

// decodePolicyEntry decodes one bundle entry. The bundle key is authoritative
// for the name: an entry that omits it still gets one.
func decodePolicyEntry(name string, value json.RawMessage) (Policy, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return Policy{Name: name}, nil
	}

	var policy Policy

	switch trimmed[0] {
	case '"':
		// The whole entry is the Rego source.
		var content string
		if err := json.Unmarshal(trimmed, &content); err != nil {
			return Policy{}, fmt.Errorf("decoding policy %q: %w", name, err)
		}

		policy.Content = content
	case '[':
		var documents []Policy
		if err := json.Unmarshal(trimmed, &documents); err != nil {
			return Policy{}, fmt.Errorf("decoding policy %q: %w", name, err)
		}
		// A bundle entry names one policy, so a list can only ever hold one
		// document. Take the first and ignore any surplus rather than fail.
		if len(documents) > 0 {
			policy = documents[0]
		}
	default:
		if err := json.Unmarshal(trimmed, &policy); err != nil {
			return Policy{}, fmt.Errorf("decoding policy %q: %w", name, err)
		}
	}

	if policy.Name == "" {
		policy.Name = name
	}

	return policy, nil
}

// Contents reduces a bundle to the name-to-Rego map that the upload route
// accepts, discarding the server-assigned metadata.
func (b PolicyBundle) Contents() map[string]string {
	contents := make(map[string]string, len(b))
	for name, policy := range b {
		contents[name] = policy.Content
	}

	return contents
}

// PolicyBundleDiff is what an upload reports: which policy names the request
// created, changed and removed relative to the bundle that was already there.
type PolicyBundleDiff struct {
	Created  []string `json:"created"`
	Modified []string `json:"modified"`
	Deleted  []string `json:"deleted"`
}

// policyBundlePayload is the upload body: {"policies": {"name.rego": "<rego>"}}.
//
// policies is never omitempty. An empty map is the only way to say "this context
// has no policies", and dropping the field would leave the existing bundle in
// place instead of clearing it.
type policyBundlePayload struct {
	Policies map[string]string `json:"policies"`
}

// PolicyDecisionSettings controls whether policy decisions are evaluated for a
// policy context.
//
// Enabled is a pointer, and omitempty, so that an unset value can never reach the
// wire as false and switch enforcement off by accident. That is the only reason:
// despite the route being a PATCH, it does **not** support a partial update. The
// handler validates the decoded body with a NotNil rule on enabled, so a request
// body of {} is rejected with HTTP 400 rather than leaving the current value
// alone. Every caller must set Enabled.
type PolicyDecisionSettings struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// GetPolicyBundle reads every policy in a policy context.
//
// A context with no policies answers 200 with an empty object rather than 404,
// so an empty bundle is a valid result and not a missing-resource signal.
func (c *Client) GetPolicyBundle(ctx context.Context, ownerID, policyContext string) (PolicyBundle, error) {
	var bundle PolicyBundle
	if err := c.GetV2(ctx, policyBundleRoute, &bundle, RouteParams(ownerID, policyContext)); err != nil {
		return nil, err
	}

	if bundle == nil {
		bundle = PolicyBundle{}
	}

	return bundle, nil
}

// GetPolicyDocument reads one named policy from a policy context. It answers
// ErrNotFound (via a 404) when the bundle has no such policy.
func (c *Client) GetPolicyDocument(ctx context.Context, ownerID, policyContext, name string) (*Policy, error) {
	// The single-document route returns a FLAT policy object, not the name-keyed
	// map the bundle route returns:
	//
	//   GET .../policy-bundle           -> {"<name>.rego": {"name", "content", ...}}
	//   GET .../policy-bundle/<name>    -> {"name", "content", "created_by", ...}
	//
	// Decoding this with the bundle decoder read "name", "content", "created_by"
	// and "created_at" as four bundle entries, so the lookup by name failed, the
	// single-entry fallback failed, and every policy that existed was reported as
	// ErrNotFound.
	var policy Policy

	err := c.GetV2(ctx, policyDocumentRoute, &policy, RouteParams(ownerID, policyContext, name))
	if err != nil {
		return nil, err
	}

	// The route answers 404 for an unknown policy, so reaching here with nothing
	// decoded means the body was not a policy at all.
	if policy.Name == "" && policy.Content == "" {
		return nil, fmt.Errorf("policy %q: %w", name, ErrNotFound)
	}

	return &policy, nil
}

// SetPolicyBundle replaces every policy in a policy context with policies.
//
// This is a whole-bundle replacement, not a merge: a policy absent from
// policies is deleted, and passing an empty map empties the context. That is the
// only write the API offers, and it is why nothing smaller than a bundle can be
// managed independently.
//
// When dryRun is true the API reports the diff the upload would produce without
// applying it. A real upload answers 201 and a dry run answers 200; both carry
// the same body, whose three arrays are each omitted when empty, so an upload
// that changed nothing decodes as a zero-valued PolicyBundleDiff.
//
// Rego that does not parse is a 400 ("invalid rego content: ..."), and a bundle
// over the service's size budget is a 413 — the size limit is enforced by a
// max-body-size middleware ahead of the handler, so it applies to the encoded
// request rather than to the sum of the policy strings.
func (c *Client) SetPolicyBundle(
	ctx context.Context, ownerID, policyContext string, policies map[string]string, dryRun bool,
) (*PolicyBundleDiff, error) {
	if policies == nil {
		policies = map[string]string{}
	}

	opts := []RequestOption{RouteParams(ownerID, policyContext)}
	if dryRun {
		opts = append(opts, Query("dry", "true"))
	}

	var diff PolicyBundleDiff

	err := c.PostV2(ctx, policyBundleRoute, policyBundlePayload{Policies: policies}, &diff, opts...)
	if err != nil {
		return nil, err
	}

	return &diff, nil
}

// GetPolicyDecisionSettings reads whether policy decisions are enabled for a
// policy context.
//
// The response always carries enabled: the handler answers with a pointer to a
// concrete bool, so the field is present even when evaluation is off. An absent
// field would therefore mean the body was not this endpoint's.
//
// Note that this READ is guarded by the same "edit-org-policies" permission as the
// write, not by the "view" permission the bundle routes use, so a read-only token
// that can list policies cannot tell whether they are being enforced.
func (c *Client) GetPolicyDecisionSettings(
	ctx context.Context, ownerID, policyContext string,
) (*PolicyDecisionSettings, error) {
	var settings PolicyDecisionSettings

	err := c.GetV2(ctx, policyDecisionSettingsRoute, &settings, RouteParams(ownerID, policyContext))
	if err != nil {
		return nil, err
	}

	return &settings, nil
}

// SetPolicyDecisionSettings writes a policy context's decision settings and
// returns them as the API reports them afterwards.
//
// Despite being a PATCH this is not a partial update: settings.Enabled must be
// non-nil or the request is a 400. See PolicyDecisionSettings.
func (c *Client) SetPolicyDecisionSettings(
	ctx context.Context, ownerID, policyContext string, settings PolicyDecisionSettings,
) (*PolicyDecisionSettings, error) {
	var updated PolicyDecisionSettings

	err := c.PatchV2(ctx, policyDecisionSettingsRoute, settings, &updated, RouteParams(ownerID, policyContext))
	if err != nil {
		return nil, err
	}

	return &updated, nil
}
