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
// Every the API route validates its context with
// validation.In(internal.Config), so a request naming "custom" is rejected with a
// 400. It is retained as a named constant so the rejection is discoverable rather
// than mysterious, but it is deliberately not offered in the resource validators.
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

// UnmarshalJSON decodes a bundle response, tolerating each of the shapes the
// endpoint's OpenAPI schema permits for a bundle entry.
//
// The published schema declares the bundle as an object whose additional
// properties carry an "items" keyword but no "type", which is not valid JSON
// Schema and so does not pin the entry down to either a Policy object or an
// array of them. Rather than guess, decode all three plausible shapes: a bare
// Rego string, a Policy object, and a one-element array of Policy objects.
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
// policy context. Enabled is a pointer so that a partial update can leave it
// alone, matching the PATCH semantics of the route.
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
// applying it.
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

// SetPolicyDecisionSettings applies a partial update to a policy context's
// decision settings and returns them as the API reports them afterwards.
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
