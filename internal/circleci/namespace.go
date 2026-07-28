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
type Namespace struct {
	ID   string
	Name string
}

// namespaceWire is the v3 entity shape every /namespaces response uses. The
// by-name lookup, the by-id lookup, create and rename all return it.
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

// renameNamespaceRequest is the body of POST /namespaces/{id}/rename.
type renameNamespaceRequest struct {
	Name string `json:"name"`
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

// RenameNamespace renames an existing namespace in place.
//
// This is a genuine update rather than a replacement: the namespace keeps its ID
// and its orbs. Orb references in configuration files that used the old name
// stop resolving, so callers should surface that.
func (c *Client) RenameNamespace(ctx context.Context, id, name string) (*Namespace, error) {
	var env Entity[namespaceWire]
	err := c.PostV3(ctx, "/namespaces/%s/rename", renameNamespaceRequest{Name: name}, &env,
		RouteParams(id),
	)
	if err != nil {
		return nil, err
	}

	return env.Data.toNamespace(), nil
}

// DeleteNamespace deletes a namespace by its UUID, along with the orbs it owns.
func (c *Client) DeleteNamespace(ctx context.Context, id string) error {
	return c.DeleteV3(ctx, "/namespaces/%s", RouteParams(id))
}
