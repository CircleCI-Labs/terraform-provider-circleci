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
//
// Status is a property of the listing rather than of the agent. The API
// computes it by cross-referencing the organization's in-flight tasks, which it
// only does for an org-id-scoped listing; the resource-class and namespace
// listings leave it unset. Because the field is `json:"status,omitempty"`
// server-side, those listings omit the key entirely and Status decodes to "".
// The only two values it ever holds are "busy" and "idle".
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

// ListRunnersParams scopes a runner listing.
//
// The API honours exactly one scope per request, in a fixed priority order:
// OrgID wins outright; failing that, ResourceClass is taken only when
// Namespace is empty, and Namespace only when ResourceClass is empty. Setting
// both of the latter with no OrgID is the one combination that errors — HTTP
// 400 "must specify exactly one of resource-class or namespace". Every other
// over-specified combination succeeds
// while silently ignoring the narrower filter, so callers must not send more than
// one; the provider's data source enforces that at plan time.
type ListRunnersParams struct {
	ResourceClass string
	Namespace     string
	OrgID         string
}

// runnerItems is the response envelope for a runner listing.
//
// This type exists because of a real bug. The API answers `{"items": [...]}`,
// but circleci-sdk-go decodes `GET /api/v3/runner` into a bare []Runner, so
// ListRunners always returned an empty slice against production. The SDK's
// own test fake serves a bare array too, so the mistake was self-consistent
// across two layers of fake and invisible in tests.
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

// Runner resource classes, tokens and task counts.
//
// The runner API serves two competing surfaces for resource classes: the
// legacy, flat one this client uses (mounted at /api/v3/runner/resource and
// /api/v3/runner/token on the runner host) and a newer JSON:API one (mounted
// at /api/v3/runner/resource-classes on circleci.com). DESIGN.md records the
// decision to stay on the legacy surface: it is what CircleCI Server also
// serves, and the newer one was being actively reshaped. The shapes below are
// confirmed against the legacy surface's own behaviour directly, not against
// circleci-sdk-go or the OpenAPI spec — see DESIGN.md's "Mocks are derived
// from production service source".
//
// circleci-cli's internal/apiclient/runner.go (MIT) covers the same legacy
// surface and its patterns are cribbed here (route names, the {"items": [...]}
// envelope, the resource-class/namespace/org-id query parameters), but it is
// under internal/ upstream and so cannot be imported.

// runnerResourceRoute is the collection route for runner resource classes.
const runnerResourceRoute = "/api/v3/runner/resource"

// runnerResourceItemRoute addresses one resource class by id. Appending
// "/force" (see DeleteResourceClass) deletes it even if tokens still reference
// it.
const runnerResourceItemRoute = runnerResourceRoute + "/%s"

// runnerTokenRoute is the collection route for runner tokens.
const runnerTokenRoute = "/api/v3/runner/token"

// runnerTokenItemRoute addresses one token by id.
const runnerTokenItemRoute = runnerTokenRoute + "/%s"

// runnerTasksRoute answers the count of tasks queued but not yet claimed by a
// runner, for a resource class.
const runnerTasksRoute = "/api/v3/runner/tasks"

// runnerTasksRunningRoute answers the count of tasks currently running on a
// runner, for a resource class.
const runnerTasksRunningRoute = runnerTasksRoute + "/running"

// ResourceClass is a runner resource class: a named pool of self-hosted runner
// capacity that jobs target via `resource_class` in their config.
//
// The three fields here are the intersection of two different response shapes.
// The create route answers with exactly these three fields; the list route
// adds `active_tasks` (int) and a `runners` array of the same shape as
// Runner, with status populated. Those two are deliberately not decoded —
// nothing in the provider exposes them, and a listing
// that embedded every agent would make circleci_runner_resource_classes a
// live-state data source rather than a configuration one. Adding them later is a
// pure addition; the decode already tolerates their presence.
//
// There is no created_at on this surface. The newer resource-classes surface has
// one; this one does not.
type ResourceClass struct {
	ID string `json:"id"`
	// ResourceClass is the "namespace/name" slug, e.g. "acme/linux".
	ResourceClass string `json:"resource_class"`
	Description   string `json:"description"`
}

// resourceClassItems is the response envelope for a resource class listing.
// See the runnerItems doc comment on ListRunners for why this matters: the API
// answers `{"items": [...]}`, not a bare array.
type resourceClassItems struct {
	Items []ResourceClass `json:"items"`
}

// ResourceClassInput is the create body for a runner resource class.
//
// OrganizationID is carried here even though the create route does not read
// an org_id from the body at all: it derives the owning organization from
// resource_class's namespace prefix and the caller's own admin permissions on
// that namespace. Sending it anyway is harmless and keeps the field consistent
// with TokenInput and with organization_id being Required on the Terraform
// schema.
//
// "Harmless" is a fact about this surface specifically, re-confirmed against
// the service: it binds with gin's default JSON binding into a struct naming
// only resource_class and description, so an unrecognised key is discarded
// rather than rejected. The newer resource-classes surface does the
// opposite — it answers HTTP 400 for an unexpected field — so this extra key
// is one of the things that would have to go if the client ever moved there.
type ResourceClassInput struct {
	OrganizationID string `json:"org_id"`
	ResourceClass  string `json:"resource_class"`
	Description    string `json:"description,omitempty"`
}

// ListResourceClasses returns the resource classes matching namespace and/or
// orgID. At least one should be set; the API answers HTTP 400 for neither.
//
// When both are set, the server checks orgID first and ignores namespace
// entirely, so the result is every resource class the organization owns, not the
// namespace-scoped subset — callers that need an exact "namespace/name" match
// filter the result themselves rather than relying on the namespace parameter
// to narrow it.
func (c *Client) ListResourceClasses(ctx context.Context, namespace, orgID string) ([]ResourceClass, error) {
	var envelope resourceClassItems

	_, err := c.raw.Call(ctx, httpcl.NewRequest(
		http.MethodGet,
		c.runnerHost+runnerResourceRoute,
		httpcl.JSONDecoder(&envelope),
		httpcl.OptionalQueryParam("namespace", namespace),
		httpcl.OptionalQueryParam("org-id", orgID),
	))
	if err != nil {
		return nil, err
	}

	return envelope.Items, nil
}

// CreateResourceClass creates a runner resource class and returns it as stored.
//
// A namespace holds at most 650 resource classes; the next create answers
// HTTP 403 with a message naming the limit. A duplicate answers HTTP 409, and
// a malformed resource_class HTTP 400
// "resource class not valid" — which is why the provider checks the format
// against the service's own rules at plan time.
//
// resource_class's namespace half has to already exist (claimed through
// circleci_orb_namespace or the CLI) before this can succeed, and a namespace
// that does not exist — confirmed live, not merely undocumented — answers
// HTTP 404 `{"message":"not found with provided token: check permissions to
// view or admin self-hosted runners"}`, the same response an unclaimed
// namespace and a namespace some *other* caller owns both produce. That
// wording is misleading taken alone (it reads like a token problem even when
// the token is fine and the namespace is simply missing), but it is the
// service's only signal here, so the provider surfaces it verbatim via
// circleci.Detail rather than rewording it into a guess. This is the same
// absence/permission ambiguity IsUnauthorized's doc comment describes for the
// main v3 API, on the legacy runner surface's own bare-message error shape.
func (c *Client) CreateResourceClass(ctx context.Context, input ResourceClassInput) (*ResourceClass, error) {
	var created ResourceClass

	_, err := c.raw.Call(ctx, httpcl.NewRequest(
		http.MethodPost,
		c.runnerHost+runnerResourceRoute,
		httpcl.Body(input),
		httpcl.JSONDecoder(&created),
	))
	if err != nil {
		return nil, err
	}

	return &created, nil
}

// DeleteResourceClass deletes a resource class by id. With force, it deletes
// even if tokens still reference it; without force, the API answers HTTP 409
// for a resource class that still has tokens. A missing resource class answers
// HTTP 404, satisfying IsNotFound — the same status an unauthorized caller
// gets, so absence and a permissions problem cannot be told apart from the
// status code alone.
func (c *Client) DeleteResourceClass(ctx context.Context, id string, force bool) error {
	route := runnerResourceItemRoute
	if force {
		route += "/force"
	}

	_, err := c.raw.Call(ctx, httpcl.NewRequest(
		http.MethodDelete,
		c.runnerHost+route,
		httpcl.RouteParams(id),
	))

	return err
}

// Token is a runner authentication token: a credential a self-hosted runner
// agent presents to claim tasks for a resource class.
type Token struct {
	ID            string `json:"id"`
	ResourceClass string `json:"resource_class"`
	Nickname      string `json:"nickname"`
	CreatedAt     string `json:"created_at"`
	// Token is the secret value. It is populated only in CreateToken's response;
	// ListTokens omits the field entirely rather than sending an empty string
	// (the field is tagged `json:"token,omitempty"`), because the API hands out
	// a token's secret exactly once, at creation, and never discloses it again.
	Token string `json:"token,omitempty"`
}

// tokenItems is the response envelope for a token listing.
type tokenItems struct {
	Items []Token `json:"items"`
}

// TokenInput is the create body for a runner token.
//
// OrganizationID is carried for the same reason as on ResourceClassInput:
// the create route does not read an org_id from the body and instead derives
// the owning organization from resource_class's namespace, but the extra
// field is harmlessly ignored rather than rejected.
type TokenInput struct {
	OrganizationID string `json:"org_id"`
	ResourceClass  string `json:"resource_class"`
	Nickname       string `json:"nickname"`
}

// CreateToken creates a runner token and returns it, including the secret
// value. This is the only response that ever carries the secret — see Token's
// doc comment.
//
// A resource class holds at most 10 tokens; the eleventh create answers HTTP
// 403 with a message naming the limit. A resource class that does not exist
// answers HTTP 400 `no such resource class "..."`, not 404, because the
// insert itself fails rather than a lookup happening first.
func (c *Client) CreateToken(ctx context.Context, input TokenInput) (*Token, error) {
	var created Token

	_, err := c.raw.Call(ctx, httpcl.NewRequest(
		http.MethodPost,
		c.runnerHost+runnerTokenRoute,
		httpcl.Body(input),
		httpcl.JSONDecoder(&created),
	))
	if err != nil {
		return nil, err
	}

	return &created, nil
}

// ListTokens returns the tokens for a resource class. The API requires
// resourceClass to be non-empty, answering HTTP 400 otherwise.
func (c *Client) ListTokens(ctx context.Context, resourceClass string) ([]Token, error) {
	var envelope tokenItems

	_, err := c.raw.Call(ctx, httpcl.NewRequest(
		http.MethodGet,
		c.runnerHost+runnerTokenRoute,
		httpcl.JSONDecoder(&envelope),
		httpcl.QueryParam("resource-class", resourceClass),
	))
	if err != nil {
		return nil, err
	}

	return envelope.Items, nil
}

// DeleteToken deletes a token by id. A missing token answers HTTP 404,
// satisfying IsNotFound — the same status an unauthorized caller gets when
// the token's namespace cannot be resolved.
func (c *Client) DeleteToken(ctx context.Context, id string) error {
	_, err := c.raw.Call(ctx, httpcl.NewRequest(
		http.MethodDelete,
		c.runnerHost+runnerTokenItemRoute,
		httpcl.RouteParams(id),
	))

	return err
}

// UnclaimedTaskCount returns the number of tasks queued for resourceClass that
// no runner has claimed yet. A resource class the caller cannot resolve
// answers HTTP 404, satisfying IsNotFound.
func (c *Client) UnclaimedTaskCount(ctx context.Context, resourceClass string) (int, error) {
	var resp struct {
		UnclaimedTaskCount int `json:"unclaimed_task_count"`
	}

	_, err := c.raw.Call(ctx, httpcl.NewRequest(
		http.MethodGet,
		c.runnerHost+runnerTasksRoute,
		httpcl.JSONDecoder(&resp),
		httpcl.QueryParam("resource-class", resourceClass),
	))
	if err != nil {
		return 0, err
	}

	return resp.UnclaimedTaskCount, nil
}

// RunningTaskCount returns the number of tasks currently running on runners in
// resourceClass. A resource class the caller cannot resolve answers HTTP 404,
// satisfying IsNotFound.
func (c *Client) RunningTaskCount(ctx context.Context, resourceClass string) (int, error) {
	var resp struct {
		RunningRunnerTasks int `json:"running_runner_tasks"`
	}

	_, err := c.raw.Call(ctx, httpcl.NewRequest(
		http.MethodGet,
		c.runnerHost+runnerTasksRunningRoute,
		httpcl.JSONDecoder(&resp),
		httpcl.QueryParam("resource-class", resourceClass),
	))
	if err != nil {
		return 0, err
	}

	return resp.RunningRunnerTasks, nil
}
