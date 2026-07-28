// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"net/http"

	"terraform-provider-circleci/internal/httpcl"
)

// DefaultRunnerHost is the origin serving the self-hosted runner admin API on
// CircleCI Cloud. It is a different origin from the main API.
//
// On CircleCI Server the same API is served by the installation itself, which is
// why the host is configurable.
const DefaultRunnerHost = "https://runner.circleci.com"

// Runner is a registered self-hosted runner agent.
//
// LastUsed is a pointer because the API sends null for an agent that has never
// claimed a task.
type Runner struct {
	ResourceClass  string  `json:"resource_class,omitempty"`
	Hostname       string  `json:"hostname"`
	Name           string  `json:"name"`
	FirstConnected string  `json:"first_connected"`
	LastConnected  string  `json:"last_connected"`
	LastUsed       *string `json:"last_used"`
	IP             string  `json:"ip,omitempty"`
	Version        string  `json:"version"`
	Status         string  `json:"status,omitempty"`
}

// ListRunnersParams scopes a runner listing. The API requires exactly one of
// ResourceClass or Namespace.
type ListRunnersParams struct {
	ResourceClass string
	Namespace     string
	OrgID         string
}

// runnerItems is the response envelope for a runner listing.
//
// This type exists because of a real bug. The runner admin API answers
// `{"items": [...]}`, but circleci-sdk-go decodes `GET /api/v3/runner` into a
// bare []Runner, so ListRunners always returned an empty slice against
// production. The SDK's own test fake serves a bare array too, so the mistake was
// self-consistent across two layers of fake and invisible in tests.
type runnerItems struct {
	Items []Runner `json:"items"`
}

// ListRunners returns the runner agents matching params.
//
// It goes to the runner host rather than the main API host, so it uses an
// absolute URL rather than one of the version-prefixed verbs.
func (c *Client) ListRunners(ctx context.Context, params ListRunnersParams) ([]Runner, error) {
	var envelope runnerItems

	_, err := c.raw.Call(ctx, httpcl.NewRequest(
		http.MethodGet,
		c.runnerHost+"/api/v3/runner",
		httpcl.JSONDecoder(&envelope),
		httpcl.OptionalQueryParam("resource-class", params.ResourceClass),
		httpcl.OptionalQueryParam("namespace", params.Namespace),
		httpcl.OptionalQueryParam("org-id", params.OrgID),
	))
	if err != nil {
		return nil, err
	}

	return envelope.Items, nil
}

// RunnerHost reports the origin this client uses for the runner admin API.
func (c *Client) RunnerHost() string { return c.runnerHost }
