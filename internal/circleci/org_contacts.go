// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"net/http"
)

// organizationContactsRoute is GET/PUT /api/private/organization/{orgID}/contacts.
//
// Unlike the private routes in private.go (spend budgets, storage retention,
// adding a user to a group), this one is not served on DefaultPrivateHost. The
// org-migration CLI draws the distinction explicitly: its
// "private" client targets the same host as the ordinary v2/v3 API (what this
// package calls Client.Host) at the path prefix /api/private/, while its "app"
// client — the one matching this package's private.go — targets a separate
// origin for CIAM-style routes (groups, SSO). Org contacts is the former, so it
// is called here through c.call with an explicit /api/private route rather than
// through GetPrivate/PutPrivate.
//
// Cloud only, same as every other /private route: a Server installation's
// gateway does not send /api/private to the core v2 API backend. Callers must
// gate on Client.IsCloud.
const organizationContactsRoute = "/api/private/organization/%s/contacts"

// OrganizationContacts holds an organization's technical (primary) and
// security contact email lists.
//
// The service treats each list as unordered: a read can (and in practice does)
// report addresses in a different order from how they were last written. Any
// caller that cares about order-independence — this includes the provider's
// resource model — must compare and store these as sets, not sequences; see
// circleci_webhook's `events` for the same lesson learned the hard way.
//
// Each list may hold at most 5 addresses; the service rejects a write that
// would exceed that with an error (observed as HTTP 422, "too many contacts").
type OrganizationContacts struct {
	Primary  []string `json:"primary"`
	Security []string `json:"security"`
}

// GetOrganizationContacts reads an organization's contact lists.
//
// A nonexistent organization answers 404, which satisfies IsNotFound.
func (c *Client) GetOrganizationContacts(ctx context.Context, orgID string) (*OrganizationContacts, error) {
	var contacts OrganizationContacts

	err := c.call(ctx, http.MethodGet, organizationContactsRoute, nil, &contacts, []RequestOption{RouteParams(orgID)})
	if err != nil {
		return nil, err
	}

	return &contacts, nil
}

// SetOrganizationContacts overwrites both of an organization's contact lists in
// full. This is PUT semantics: an omitted list is not left alone, it is cleared.
// Callers that only mean to manage one list must supply the other's current
// value themselves.
//
// Primary and Security are sent as-is (including empty, non-nil slices, which
// clear a list) rather than substituting nil, because encoding/json renders a
// nil slice as JSON null and an empty slice as [], and only the latter is known
// to be what the service expects for "no contacts of this kind" — see
// how the org-migration CLI gets and sets contacts.
func (c *Client) SetOrganizationContacts(ctx context.Context, orgID string, contacts OrganizationContacts) (*OrganizationContacts, error) {
	if contacts.Primary == nil {
		contacts.Primary = []string{}
	}

	if contacts.Security == nil {
		contacts.Security = []string{}
	}

	var updated OrganizationContacts

	err := c.call(
		ctx, http.MethodPut, organizationContactsRoute, contacts, &updated, []RequestOption{RouteParams(orgID)},
	)
	if err != nil {
		return nil, err
	}

	return &updated, nil
}
