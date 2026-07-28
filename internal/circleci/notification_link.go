// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// notificationLinksRoute lists the authenticated caller's external identity
// links (e.g. a linked Slack user), proxied to the notifications service's
// internal API exactly like the other /api/v3/notification/* routes in
// notification_integration.go.
//
// There is a DELETE for this same route (see the CircleCI API
// in the notifications service), but no create: a link is established as a
// side effect of the Slack user-linking OAuth flow
// (SlackUserLinkCallback/"/connect" in that service), a browser consent step
// Terraform cannot drive. That is also why there is no resource here — see
// DESIGN.md's "Notification links have no resource" entry — and this file
// implements only List.
const notificationLinksRoute = "/notification/links"

// NotificationLink is one external identity linked to a CircleCI user on a
// third-party connection (Slack, today).
//
// Field names and the "not UUID-addressable" shape are taken from
// notifications' the CircleCI API: Link carries no
// id, because response.DataEntity.ID uses `json:"id,omitzero"` and the
// handler never sets it. The real identity of a link is the triple
// (user, ConnectionType, ExternalScopeID).
//
// EXPERIMENTAL upstream: ListLinks' own doc comment says "Field names,
// request and response shapes, and pagination semantics are not yet stable.
// Do not depend on this endpoint in production clients." — carried through to
// circleci_notification_links' schema description.
type NotificationLink struct {
	// ConnectionType is the third-party connection the link is on. "slack" is
	// the only value today.
	ConnectionType string
	// ExternalID is the identity's id on the external connection, e.g. a Slack
	// user id such as "U0123".
	ExternalID string
	// ExternalScopeID is the connection-specific scope the link lives in, e.g.
	// a Slack workspace/team id such as "T0123". Together with ConnectionType
	// and the owning user, this is the link's unlink key.
	ExternalScopeID string
	// DisplayName is the external identity's display name, captured at link
	// time.
	DisplayName string
	// UserID is the CircleCI user (UUID) the link belongs to.
	UserID string
}

type notificationLinkAttributesWire struct {
	ConnectionType  string `json:"connection_type"`
	ExternalID      string `json:"external_id"`
	ExternalScopeID string `json:"external_scope_id"`
	DisplayName     string `json:"display_name"`
}

type notificationLinkUserRefWire struct {
	ID string `json:"id"`
}

type notificationLinkReferencesWire struct {
	User notificationLinkUserRefWire `json:"user"`
}

// notificationLinkWire is one v3 data entity for a link. It deliberately has
// no ID field: links.Link (the upstream attributes struct) never sets one, so
// decoding it would always be the zero UUID.
type notificationLinkWire struct {
	Attributes notificationLinkAttributesWire `json:"attributes"`
	References notificationLinkReferencesWire `json:"references"`
}

func (w notificationLinkWire) toLink() NotificationLink {
	return NotificationLink{
		ConnectionType:  w.Attributes.ConnectionType,
		ExternalID:      w.Attributes.ExternalID,
		ExternalScopeID: w.Attributes.ExternalScopeID,
		DisplayName:     w.Attributes.DisplayName,
		UserID:          w.References.User.ID,
	}
}

// ListNotificationLinksOptions scopes a ListNotificationLinks call.
type ListNotificationLinksOptions struct {
	// UserID is filter[user_id]: "me" or the caller's own UUID. The API rejects
	// any other value with 403 (cross-user access is forbidden), so this is
	// never a way to read another user's links. Defaults to "me" when empty,
	// since the filter is required upstream.
	UserID string
	// ConnectionType is filter[connection_type]. Leave empty to list every known
	// connection type; today that is only "slack".
	ConnectionType string
	// TeamID is filter[team_id]. Leave empty to list every scope.
	TeamID string
}

// ListNotificationLinks lists the links matching opts, following the v3
// cursor to the last page.
//
// The route's own handler never populates a next-page cursor
// (links.Handler.ListLinks passes response.Collection{Items: items} with no
// NextCursor), so in practice this always returns everything in one request;
// DrainV3 is used anyway so a future server-side change to add real
// pagination is handled without a client change.
func (c *Client) ListNotificationLinks(ctx context.Context, opts ListNotificationLinksOptions) ([]NotificationLink, error) {
	userID := opts.UserID
	if userID == "" {
		userID = "me"
	}

	return DrainV3(ctx, func(ctx context.Context, cursor string) (List[NotificationLink], error) {
		var page List[notificationLinkWire]
		err := c.GetV3(ctx, notificationLinksRoute, &page,
			Filter("user_id", userID),
			Filter("connection_type", opts.ConnectionType),
			Filter("team_id", opts.TeamID),
			PageCursor(cursor),
		)
		if err != nil {
			return List[NotificationLink]{}, err
		}

		out := List[NotificationLink]{Meta: page.Meta, Page: page.Page}
		for _, wire := range page.Data {
			out.Data = append(out.Data, wire.toLink())
		}

		return out, nil
	})
}
