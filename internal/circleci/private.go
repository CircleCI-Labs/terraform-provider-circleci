// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"net/http"

	"terraform-provider-circleci/internal/httpcl"
)

// Private routes.
//
// Some capabilities have no public API at all: spend budgets, storage retention
// controls, organization contacts, and adding users to a group. They are served
// on a different origin from the public API, under a /private prefix, and they
// are not in any published specification.
//
// Everything here is therefore a deliberate, recorded exception.
// lists each one, what it gives us that no public route does, and the public-API
// request that would let us drop it. Nothing in this file should exist
// permanently.
//
// # WHY THERE IS NO PROVIDER ATTRIBUTE FOR THIS ORIGIN
//
// `runner_host` is a public provider attribute because on CircleCI Server the
// runner API is genuinely served by the customer's own installation, so only the
// practitioner knows the address. That reasoning does not carry over: a CircleCI
// Server installation does not route the private origin at all, so these
// capabilities are Cloud-only. Private routes exist on Cloud and nowhere else,
// Cloud has exactly one address, and a constant is the whole answer.
//
// The Config field below exists only so a test can point the client at an
// httptest server. It is not surfaced in the provider schema, and should not be.
//
// # EVERY CALLER MUST GATE ON IsCloud
//
// Because these are Cloud-only, a caller that forgets the gate produces a
// confusing failure against a Server installation rather than a clear one. See
// requirePrivateAPI in the provider package.
const DefaultPrivateHost = "https://app.circleci.com"

// PrivateHost reports the origin serving the private routes.
func (c *Client) PrivateHost() string { return c.privateHost }

// callPrivate issues a request against the private origin.
//
// It uses the raw client with an absolute URL, exactly as the runner calls do,
// because the private origin is not the one httpcl was configured with. Passing
// a nil body omits it, which matters for the GET routes.
func (c *Client) callPrivate(ctx context.Context, method, route string, body, dst any, opts ...func(*httpcl.Request)) error {
	reqOpts := make([]func(*httpcl.Request), 0, len(opts)+2)

	if body != nil {
		reqOpts = append(reqOpts, httpcl.Body(body))
	}

	if dst != nil {
		reqOpts = append(reqOpts, httpcl.JSONDecoder(dst))
	}

	reqOpts = append(reqOpts, opts...)

	_, err := c.raw.Call(ctx, httpcl.NewRequest(method, c.privateHost+route, reqOpts...))

	return err
}

// GetPrivate issues a GET against the private origin and decodes into dst.
func (c *Client) GetPrivate(ctx context.Context, route string, dst any, opts ...func(*httpcl.Request)) error {
	return c.callPrivate(ctx, http.MethodGet, route, nil, dst, opts...)
}

// PostPrivate issues a POST against the private origin.
func (c *Client) PostPrivate(ctx context.Context, route string, body, dst any, opts ...func(*httpcl.Request)) error {
	return c.callPrivate(ctx, http.MethodPost, route, body, dst, opts...)
}

// PutPrivate issues a PUT against the private origin.
func (c *Client) PutPrivate(ctx context.Context, route string, body, dst any, opts ...func(*httpcl.Request)) error {
	return c.callPrivate(ctx, http.MethodPut, route, body, dst, opts...)
}

// PatchPrivate issues a PATCH against the private origin.
func (c *Client) PatchPrivate(ctx context.Context, route string, body, dst any, opts ...func(*httpcl.Request)) error {
	return c.callPrivate(ctx, http.MethodPatch, route, body, dst, opts...)
}

// DeletePrivate issues a DELETE against the private origin.
func (c *Client) DeletePrivate(ctx context.Context, route string, opts ...func(*httpcl.Request)) error {
	return c.callPrivate(ctx, http.MethodDelete, route, nil, nil, opts...)
}
