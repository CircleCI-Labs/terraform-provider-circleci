// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

// Package circleci is the provider's CircleCI API client.
//
// It is the only package internal/provider may import for API access. Resources
// and data sources must not reach past it to internal/httpcl, so that the
// transport underneath can be replaced without touching any resource.
//
// The API version is chosen per call, not per client: Host is a bare origin and
// each verb helper prepends its own /api/vN prefix. That is what makes it
// possible to serve v3 on CircleCI Cloud and v2 on CircleCI Server from a single
// client.
//
// The request/response conventions here (version-prefixed verbs, the v3
// data/attributes/references envelope, cursor pagination and the v3 error
// envelope) follow github.com/CircleCI-Public/circleci-cli's internal/apiclient,
// which is MIT licensed. See internal/httpcl/DIVERGENCES.md.
package circleci

import (
	"context"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-log/tflog"

	"terraform-provider-circleci/internal/httpcl"
)

// Deployment identifies which kind of CircleCI installation is being targeted.
// It selects the API version each entity uses, because CircleCI Server does not
// route /api/v3 to the public API service.
type Deployment string

const (
	// DeploymentCloud is circleci.com, where v3 is available.
	DeploymentCloud Deployment = "cloud"
	// DeploymentServer is a self-hosted CircleCI Server installation, which
	// serves v2 but not v3 (excepting the separately hosted runner API).
	DeploymentServer Deployment = "server"
)

// DefaultHost is the CircleCI Cloud API origin. It is a bare origin with no
// /api/vN suffix; version prefixes are added per call.
const DefaultHost = "https://circleci.com"

// authHeader is the CircleCI personal access token header. It is used for every
// API version and both deployment types.
//
// circleci-cli sends "Authorization: Bearer <token>" instead, but Server only
// accepts Bearer from recent images, whereas Circle-Token is accepted by v1.1,
// v2 and v3 on both Cloud and Server. Using it unconditionally keeps Server
// working and matches what this provider has always sent.
const authHeader = "Circle-Token"

// Config configures a Client.
type Config struct {
	// Host is the CircleCI origin, e.g. "https://circleci.com". Any trailing
	// /api/vN is stripped by NormalizeHost.
	Host string
	// Token is a CircleCI personal access token.
	Token string
	// Deployment selects v3 (cloud) or v2 (server) routing. Defaults to
	// DeploymentCloud when empty.
	Deployment Deployment
	// RunnerHost is the origin serving the self-hosted runner admin API. On
	// CircleCI Cloud that is a different origin from Host; on CircleCI Server the
	// installation serves it itself. Defaults to DefaultRunnerHost.
	RunnerHost string
	// UserAgent is sent on every request. Defaults to "terraform-provider-circleci".
	UserAgent string
	// Transport overrides the HTTP transport, for tests.
	Transport http.RoundTripper
}

// Client is an authenticated CircleCI API client.
type Client struct {
	http *httpcl.Client
	// raw carries no base URL, for the APIs that live on another origin — today
	// only the self-hosted runner admin API.
	raw        *httpcl.Client
	deployment Deployment
	host       string
	runnerHost string
}

// NormalizeHost trims trailing slashes and any /api/vN suffix from a host, so
// that a value carried over from an older provider release (which documented
// host = "https://circleci.com/api/v2") still resolves correctly. It reports
// whether a version suffix was removed, so callers can warn.
func NormalizeHost(host string) (normalized string, hadVersionSuffix bool) {
	normalized = strings.TrimRight(host, "/")
	for _, suffix := range []string{"/api/v3", "/api/v2", "/api/v1.1", "/api/v1"} {
		if trimmed, ok := strings.CutSuffix(normalized, suffix); ok {
			return strings.TrimRight(trimmed, "/"), true
		}
	}

	return normalized, false
}

// New creates a Client from cfg.
func New(cfg Config) *Client {
	host, _ := NormalizeHost(cfg.Host)
	if host == "" {
		host = DefaultHost
	}

	deployment := cfg.Deployment
	if deployment == "" {
		deployment = DeploymentCloud
	}

	userAgent := cfg.UserAgent
	if userAgent == "" {
		userAgent = "terraform-provider-circleci"
	}

	runnerHost, _ := NormalizeHost(cfg.RunnerHost)
	if runnerHost == "" {
		runnerHost = DefaultRunnerHost
	}

	transport := httpcl.Config{
		AuthToken:  cfg.Token,
		AuthHeader: authHeader,
		UserAgent:  userAgent,
		Transport:  cfg.Transport,
	}

	main := transport
	main.BaseURL = host

	return &Client{
		http:       httpcl.New(main),
		raw:        httpcl.New(transport),
		deployment: deployment,
		host:       host,
		runnerHost: runnerHost,
	}
}

// Deployment reports which installation type this client targets.
func (c *Client) Deployment() Deployment { return c.deployment }

// Host reports the normalized origin this client targets.
func (c *Client) Host() string { return c.host }

// IsCloud reports whether this client targets CircleCI Cloud, and therefore
// whether v3-only resources are usable.
func (c *Client) IsCloud() bool { return c.deployment == DeploymentCloud }

// UseV3 reports whether v3 should be used for an entity that exists on both v2
// and v3. Server does not route /api/v3 to the public API service, so it always
// takes the v2 path.
func (c *Client) UseV3() bool { return c.IsCloud() }

// --- v2 verbs ---

// GetV2 issues a GET against /api/v2 and decodes the response into dst.
func (c *Client) GetV2(ctx context.Context, route string, dst any, opts ...RequestOption) error {
	return c.call(ctx, http.MethodGet, "/api/v2"+route, nil, dst, opts)
}

// PostV2 issues a POST against /api/v2 and decodes the response into dst.
func (c *Client) PostV2(ctx context.Context, route string, body, dst any, opts ...RequestOption) error {
	return c.call(ctx, http.MethodPost, "/api/v2"+route, body, dst, opts)
}

// PutV2 issues a PUT against /api/v2 and decodes the response into dst.
func (c *Client) PutV2(ctx context.Context, route string, body, dst any, opts ...RequestOption) error {
	return c.call(ctx, http.MethodPut, "/api/v2"+route, body, dst, opts)
}

// PatchV2 issues a PATCH against /api/v2 and decodes the response into dst.
func (c *Client) PatchV2(ctx context.Context, route string, body, dst any, opts ...RequestOption) error {
	return c.call(ctx, http.MethodPatch, "/api/v2"+route, body, dst, opts)
}

// DeleteV2 issues a DELETE against /api/v2.
func (c *Client) DeleteV2(ctx context.Context, route string, opts ...RequestOption) error {
	return c.call(ctx, http.MethodDelete, "/api/v2"+route, nil, nil, opts)
}

// --- v1.1 verbs ---
//
// v1.1 is in maintenance and has a live deprecation initiative, so these exist
// only for the handful of operations with no v2 or v3 equivalent. Do not reach for
// them otherwise.

// PostV1 issues a POST against /api/v1.1. A nil dst skips decoding, which several
// v1.1 routes require because they answer with an empty body.
func (c *Client) PostV1(ctx context.Context, route string, body, dst any, opts ...RequestOption) error {
	return c.call(ctx, http.MethodPost, "/api/v1.1"+route, body, dst, opts)
}

// --- v3 verbs ---

// GetV3 issues a GET against /api/v3 and decodes the response into dst.
func (c *Client) GetV3(ctx context.Context, route string, dst any, opts ...RequestOption) error {
	return c.call(ctx, http.MethodGet, "/api/v3"+route, nil, dst, opts)
}

// PostV3 issues a POST against /api/v3 and decodes the response into dst. v3
// uses POST rather than PATCH for partial updates, via /update-style routes.
func (c *Client) PostV3(ctx context.Context, route string, body, dst any, opts ...RequestOption) error {
	return c.call(ctx, http.MethodPost, "/api/v3"+route, body, dst, opts)
}

// DeleteV3 issues a DELETE against /api/v3.
func (c *Client) DeleteV3(ctx context.Context, route string, opts ...RequestOption) error {
	return c.call(ctx, http.MethodDelete, "/api/v3"+route, nil, nil, opts)
}

// call builds and executes a request. A nil dst skips response decoding, which
// matters for 204 responses that carry no body.
func (c *Client) call(ctx context.Context, method, route string, body, dst any, opts []RequestOption) error {
	reqOpts := make([]func(*httpcl.Request), 0, len(opts)+2)
	if body != nil {
		reqOpts = append(reqOpts, httpcl.Body(body))
	}
	if dst != nil {
		reqOpts = append(reqOpts, httpcl.JSONDecoder(dst))
	}
	reqOpts = append(reqOpts, opts...)

	_, err := c.http.Call(ctx, httpcl.NewRequest(method, route, reqOpts...))

	return err
}

func init() {
	// Route httpcl's request logging into Terraform's logger. See
	// internal/httpcl/debug.go.
	httpcl.Debug = func(ctx context.Context, msg string, args ...any) {
		fields := make(map[string]any, len(args)/2)
		for i := 0; i+1 < len(args); i += 2 {
			key, ok := args[i].(string)
			if !ok {
				continue
			}
			fields[key] = args[i+1]
		}
		tflog.Debug(ctx, msg, fields)
	}
}
