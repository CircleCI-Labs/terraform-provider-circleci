// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

// itemsResponse is the v2 envelope for a collection that is not paginated:
//
//	{"items": [...]}
//
// It is deliberately a different type from PaginatedResponse. Several v2
// collections — context restrictions, pipeline definitions and a definition's
// triggers — return every item in a single response and omit next_page_token
// from the body entirely. Decoding those into PaginatedResponse would work, but
// it would also invite a DrainV2 loop that can never run more than once and
// would imply a pagination contract the endpoint does not offer.
type itemsResponse[T any] struct {
	Items []T `json:"items"`
}
