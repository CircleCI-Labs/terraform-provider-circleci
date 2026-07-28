// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Routes for notification integrations. Today the only integration type is
// Slack, but the collection is already generic (filter[type]) on the API side,
// so the provider follows that rather than naming these "slack".
const (
	notificationIntegrationsRoute         = "/notification/integrations"
	notificationIntegrationRoute          = "/notification/integrations/%s"
	notificationIntegrationSetStatusRoute = "/notification/integrations/%s/set-status"
)

// NotificationIntegrationTypeSlack is the only notification integration type
// the API serves today.
const NotificationIntegrationTypeSlack = "slack"

// Notification integration statuses.
const (
	// NotificationIntegrationStatusActive means the integration is installed and
	// able to deliver.
	NotificationIntegrationStatusActive = "active"
	// NotificationIntegrationStatusDisabled means the integration is installed
	// but delivery is turned off. This and Active are the only statuses settable
	// through SetNotificationIntegrationStatus.
	NotificationIntegrationStatusDisabled = "disabled"
	// NotificationIntegrationStatusDisconnected means the workspace disconnected
	// the CircleCI app on Slack's side; CircleCI still holds the installation
	// row. This status is reported, never set.
	NotificationIntegrationStatusDisconnected = "disconnected"
	// NotificationIntegrationStatusRevoked means the installation was deleted
	// through DeleteNotificationIntegration. A revoked installation is excluded
	// from every list and lookup, so it is never actually observed; it exists
	// only to name the state a caller cannot see again.
	NotificationIntegrationStatusRevoked = "revoked"
)

// NotificationIntegration is an installed notification integration (a Slack
// workspace connection, today) as served by /api/v3/notification/integrations.
//
// There is no create route: an integration is installed through an OAuth flow
// in the CircleCI web UI, not through this API.
type NotificationIntegration struct {
	ID            string
	Type          string
	WorkspaceName string
	TeamID        string
	Status        string
	CreatedAt     string
	UpdatedAt     string
	OrgID         string
	OrgName       string
}

type notificationIntegrationAttributesWire struct {
	Type          string `json:"type"`
	WorkspaceName string `json:"workspace_name"`
	TeamID        string `json:"team_id"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type notificationIntegrationOrgAttributesWire struct {
	Name string `json:"name"`
}

type notificationIntegrationOrgRefWire struct {
	ID         string                                    `json:"id"`
	Attributes *notificationIntegrationOrgAttributesWire `json:"attributes,omitempty"`
}

type notificationIntegrationReferencesWire struct {
	Org notificationIntegrationOrgRefWire `json:"org"`
}

type notificationIntegrationWire struct {
	ID         string                                `json:"id"`
	Attributes notificationIntegrationAttributesWire `json:"attributes"`
	References notificationIntegrationReferencesWire `json:"references"`
}

func (w notificationIntegrationWire) toIntegration() *NotificationIntegration {
	i := &NotificationIntegration{
		ID:            w.ID,
		Type:          w.Attributes.Type,
		WorkspaceName: w.Attributes.WorkspaceName,
		TeamID:        w.Attributes.TeamID,
		Status:        w.Attributes.Status,
		CreatedAt:     w.Attributes.CreatedAt,
		UpdatedAt:     w.Attributes.UpdatedAt,
		OrgID:         w.References.Org.ID,
	}
	if w.References.Org.Attributes != nil {
		i.OrgName = w.References.Org.Attributes.Name
	}

	return i
}

// ListNotificationIntegrationsOptions scopes a ListNotificationIntegrations
// call. Every field is optional; an empty OrgID lists the calling user's
// integrations across every organization they belong to, excluding revoked
// ones.
type ListNotificationIntegrationsOptions struct {
	OrgID string
	// Type restricts the listing to one integration type. Leave empty for every
	// type; today that is only NotificationIntegrationTypeSlack.
	Type string
}

// ListNotificationIntegrations lists notification integrations, following the
// v3 cursor to the last page.
func (c *Client) ListNotificationIntegrations(
	ctx context.Context, opts ListNotificationIntegrationsOptions,
) ([]NotificationIntegration, error) {
	return DrainV3(ctx, func(ctx context.Context, cursor string) (List[NotificationIntegration], error) {
		var page List[notificationIntegrationWire]
		err := c.GetV3(ctx, notificationIntegrationsRoute, &page,
			Filter("org_id", opts.OrgID),
			Filter("type", opts.Type),
			PageCursor(cursor),
		)
		if err != nil {
			return List[NotificationIntegration]{}, err
		}

		out := List[NotificationIntegration]{Meta: page.Meta, Page: page.Page}
		for _, wire := range page.Data {
			out.Data = append(out.Data, *wire.toIntegration())
		}

		return out, nil
	})
}

// GetNotificationIntegration retrieves a single notification integration by
// its id. A revoked integration answers 404, indistinguishable from one that
// never existed.
func (c *Client) GetNotificationIntegration(ctx context.Context, id string) (*NotificationIntegration, error) {
	var env Entity[notificationIntegrationWire]
	if err := c.GetV3(ctx, notificationIntegrationRoute, &env, RouteParams(id)); err != nil {
		return nil, err
	}

	return env.Data.toIntegration(), nil
}

type setNotificationIntegrationStatusAttributesWire struct {
	Status string `json:"status"`
}

type setNotificationIntegrationStatusBody struct {
	Attributes setNotificationIntegrationStatusAttributesWire `json:"attributes"`
}

// SetNotificationIntegrationStatus sets an integration's status to
// NotificationIntegrationStatusActive or NotificationIntegrationStatusDisabled
// and returns the updated integration. Any other status is rejected by the API.
func (c *Client) SetNotificationIntegrationStatus(
	ctx context.Context, id, status string,
) (*NotificationIntegration, error) {
	var body Entity[setNotificationIntegrationStatusBody]
	body.Data.Attributes.Status = status

	var env Entity[notificationIntegrationWire]
	err := c.PostV3(ctx, notificationIntegrationSetStatusRoute, body, &env, RouteParams(id))
	if err != nil {
		return nil, err
	}

	return env.Data.toIntegration(), nil
}

// DeleteNotificationIntegration revokes a notification integration by id. This
// is permanent: reconnecting the same Slack workspace requires the OAuth
// installation flow again, not this API.
func (c *Client) DeleteNotificationIntegration(ctx context.Context, id string) error {
	return c.DeleteV3(ctx, notificationIntegrationRoute, RouteParams(id))
}
