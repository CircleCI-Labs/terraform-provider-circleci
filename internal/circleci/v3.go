// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"errors"
)

// Entity is the v3 envelope for a single resource:
//
//	{"data": {"id": ..., "attributes": {...}, "references": {...}}}
//
// It is also the request envelope for creates and full replacements.
type Entity[T any] struct {
	Data T `json:"data"`
}

// Page holds the cursors for a v3 collection response. A nil cursor means there
// is no page in that direction.
type Page struct {
	Next *string `json:"next"`
	Prev *string `json:"prev"`
}

// Meta holds optional metadata on a v3 collection response. TotalCount is nil
// when the server does not supply it, which the v3 design permits.
type Meta struct {
	TotalCount *int `json:"total_count"`
}

// List is the v3 envelope for a collection, including its pagination cursors:
//
//	{"data": [...], "meta": {"total_count": N}, "page": {"next": ..., "prev": ...}}
type List[T any] struct {
	Data []T  `json:"data"`
	Meta Meta `json:"meta"`
	Page Page `json:"page"`
}

// NextCursor returns the cursor for the following page, or "" when the current
// page is the last one.
func (l List[T]) NextCursor() string {
	if l.Page.Next == nil {
		return ""
	}

	return *l.Page.Next
}

// PaginatedResponse is the v2 collection envelope. v2 paginates with an opaque
// next_page_token rather than v3's cursors.
type PaginatedResponse[T any] struct {
	Items         []T    `json:"items"`
	NextPageToken string `json:"next_page_token"`
}

// ErrPaginationDidNotAdvance reports that a collection endpoint handed back the
// same page token it was given, so draining it would never finish.
//
// This is not hypothetical. The context environment variable route reads its page
// token from the *path* parameters on a route that has no such parameter, so the
// token supplied in the query string is never seen: every request returns the
// first page alongside the same token. Before this guard, a context with more than
// one page of variables hung Terraform and grew the slice until the process died.
//
// Draining stops with this error rather than returning what it has. A partial
// collection is indistinguishable from a complete one to every caller here, and
// several of them feed lists that Terraform would then reconcile — so silently
// short-changing the list is a worse failure than refusing to produce one.
var ErrPaginationDidNotAdvance = errors.New(
	"the API returned the same page token it was given, so pagination cannot advance; " +
		"this is a bug in the endpoint rather than in the configuration",
)

// DrainV3 accumulates every page of a v3 collection. fetch is called once per
// page with the cursor to request; an empty cursor requests the first page.
//
// Terminates on an empty page, and on a cursor that repeats — see
// ErrPaginationDidNotAdvance. The empty-page check alone is not enough: an
// endpoint that returns a full first page forever satisfies it on every
// iteration.
func DrainV3[T any](ctx context.Context, fetch func(ctx context.Context, cursor string) (List[T], error)) ([]T, error) {
	var (
		all    []T
		cursor string
	)

	for {
		page, err := fetch(ctx, cursor)
		if err != nil {
			return nil, err
		}

		all = append(all, page.Data...)

		next := page.NextCursor()
		if next == "" || len(page.Data) == 0 {
			return all, nil
		}

		if next == cursor {
			return nil, ErrPaginationDidNotAdvance
		}

		cursor = next
	}
}

// DrainV2 accumulates every page of a v2 collection. fetch is called once per
// page with the page token to request; an empty token requests the first page.
//
// Terminates on an empty page, and on a token that repeats — see
// ErrPaginationDidNotAdvance.
func DrainV2[T any](ctx context.Context, fetch func(ctx context.Context, pageToken string) (PaginatedResponse[T], error)) ([]T, error) {
	var (
		all   []T
		token string
	)

	for {
		page, err := fetch(ctx, token)
		if err != nil {
			return nil, err
		}

		all = append(all, page.Items...)

		if page.NextPageToken == "" || len(page.Items) == 0 {
			return all, nil
		}

		if page.NextPageToken == token {
			return nil, ErrPaginationDidNotAdvance
		}

		token = page.NextPageToken
	}
}
