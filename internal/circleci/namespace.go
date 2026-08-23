// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"fmt"
)

// Namespace is an orb registry namespace: the globally unique prefix that owns a
// set of orbs, as in "<namespace>/<orb>".
//
// A namespace is also what a self-hosted runner resource class is named after —
// a resource class is "<namespace>/<class>" — so an organization needs a
// namespace before it can register runners.
//
// Namespaces used to be reachable only over GraphQL. They are now served by the
// v3 REST API, which CircleCI Server does not route, so every call here is
// Cloud-only.
//
// A namespace is permanent once created: a name is a global, one-per-organization
// claim with no self-service way to rename or delete it. This client
// deliberately exposes no RenameNamespace or DeleteNamespace — see
// internal/provider/orb_namespace_resource.go's Delete method and the
// orbNamespaceNameImmutable plan modifier for how the resource built on this
// client acts on the finding below.
//
// [NET, measured against a live organization-admin token]: the v3 API does
// route both POST /namespaces/{id}/rename and DELETE /namespaces/{id}, but
// every call to either answered 403 Forbidden unconditionally — on a
// namespace the calling organization had just created, on one belonging to an
// unrelated organization, on a no-op rename of a namespace to its own
// existing name, and on a namespace whose owning organization had since been
// deleted. DELETE of an id that never existed answers 404, not 403, so the
// route does distinguish absence from refusal; the 403 for an id that does
// exist is a deliberate block, not a catch-all error. CircleCI's support
// documentation (https://support.circleci.com/hc/en-us/articles/21518826780827,
// "Transferring and Renaming Namespaces") describes a rename or transfer as a
// support-ticket process, and this investigation found no equivalent
// self-service process documented for deletion at all.
type Namespace struct {
	ID   string
	Name string
}

// namespaceWire is the v3 entity shape every /namespaces response uses. The
// by-name lookup, the by-id lookup and create all return it.
type namespaceWire struct {
	ID         string `json:"id"`
	Attributes struct {
		Name string `json:"name"`
	} `json:"attributes"`
}

func (w namespaceWire) toNamespace() *Namespace {
	return &Namespace{ID: w.ID, Name: w.Attributes.Name}
}

// CreateNamespaceRequest is the body of POST /namespaces.
//
// Note that it is a bare object rather than a data/attributes envelope: the
// namespace routes predate that convention and still take flat bodies, unlike
// the orb routes in orb.go.
type CreateNamespaceRequest struct {
	Name           string `json:"name"`
	OrganizationID string `json:"org_id"`
}

// GetNamespace resolves a namespace by its name.
//
// The route is the /namespaces collection scoped by filter[name], but it answers
// with a single entity rather than a list. An absent namespace normally comes
// back as an HTTP 404; an empty payload is translated into ErrNotFound too, so
// that callers only ever have to test with IsNotFound.
func (c *Client) GetNamespace(ctx context.Context, name string) (*Namespace, error) {
	var env Entity[namespaceWire]
	if err := c.GetV3(ctx, "/namespaces", &env, Filter("name", name)); err != nil {
		return nil, err
	}

	if env.Data.ID == "" {
		return nil, fmt.Errorf("namespace %q: %w", name, ErrNotFound)
	}

	return env.Data.toNamespace(), nil
}

// GetNamespaceByID retrieves a namespace by its UUID.
func (c *Client) GetNamespaceByID(ctx context.Context, id string) (*Namespace, error) {
	var env Entity[namespaceWire]
	if err := c.GetV3(ctx, "/namespaces/%s", &env, RouteParams(id)); err != nil {
		return nil, err
	}

	if env.Data.ID == "" {
		return nil, fmt.Errorf("namespace %q: %w", id, ErrNotFound)
	}

	return env.Data.toNamespace(), nil
}

// CreateNamespace creates a namespace owned by an organization.
//
// Namespace names are globally unique across all of CircleCI, so a name already
// taken by another organization fails rather than being scoped away.
func (c *Client) CreateNamespace(ctx context.Context, req CreateNamespaceRequest) (*Namespace, error) {
	var env Entity[namespaceWire]
	if err := c.PostV3(ctx, "/namespaces", req, &env); err != nil {
		return nil, err
	}

	return env.Data.toNamespace(), nil
}
