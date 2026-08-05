// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Routes for notification channel configs, served behind the public API's
// v3 façade; CircleCI Server does not route /api/v3, so every call here is
// Cloud-only.
const (
	notificationChannelConfigsRoute      = "/notification/channel-configs"
	notificationChannelConfigRoute       = "/notification/channel-configs/%s"
	notificationChannelConfigUpdateRoute = "/notification/channel-configs/%s/update"
)

// Notification scopes, shared with notification preferences. A channel config
// or preference matrix is always either the calling user's own, or a project's.
const (
	// NotificationScopeUser selects the calling user's own channel configs or
	// preferences. The user is inferred from the API token; there is no way to
	// address another user's configuration through this API.
	NotificationScopeUser = "user"
	// NotificationScopeProject selects a project's channel configs or
	// preferences, which additionally requires a project and an organization.
	NotificationScopeProject = "project"
)

// Notification channel types accepted by channel configs.
const (
	// NotificationChannelTypeEmail delivers to an email address.
	NotificationChannelTypeEmail = "email"
	// NotificationChannelTypeSlack delivers to a Slack channel, addressed by
	// its channel ID. Posting requires an active circleci_notification_integrations
	// Slack installation for the organization.
	NotificationChannelTypeSlack = "slack"
)

// NotificationChannelConfig is a single channel a user or a project delivers
// notifications through, as served by /api/v3/notification/channel-configs.
//
// Exactly one of UserID and ProjectID is set, selected by Scope. OrgID is always
// set: even a user-scoped config is tied to one organization.
type NotificationChannelConfig struct {
	ID string
	// Scope is NotificationScopeUser or NotificationScopeProject, derived from
	// which reference the API returned rather than sent back explicitly.
	Scope       string
	ChannelType string
	// Target is the delivery address: an email address, or a Slack channel ID.
	Target string
	// ChannelName is populated for a project-scoped Slack config only, resolved
	// from the channel ID. It is empty otherwise.
	ChannelName string
	IsEnabled   bool
	UserID      string
	ProjectID   string
	OrgID       string
}

// notificationEntityRef is a bare {"id": "..."} reference, used throughout the
// notification routes for the user/project/org relationships.
type notificationEntityRef struct {
	ID string `json:"id"`
}

// channelConfigAttributesWire is the attributes object of a channel config
// entity, on both read and write.
type channelConfigAttributesWire struct {
	ChannelType string  `json:"channel_type"`
	Target      *string `json:"target,omitempty"`
	ChannelName *string `json:"channel_name,omitempty"`
	IsEnabled   bool    `json:"is_enabled"`
}

// channelConfigReferencesWire holds the related-entity references. Exactly one
// of User and Project is populated, depending on scope; Org is always present.
type channelConfigReferencesWire struct {
	User    *notificationEntityRef `json:"user,omitempty"`
	Project *notificationEntityRef `json:"project,omitempty"`
	Org     *notificationEntityRef `json:"org,omitempty"`
}

// channelConfigWire is the v3 entity shape for a channel config, returned by
// every channel-config route.
type channelConfigWire struct {
	ID         string                      `json:"id"`
	Attributes channelConfigAttributesWire `json:"attributes"`
	References channelConfigReferencesWire `json:"references"`
}

func (w channelConfigWire) toChannelConfig() *NotificationChannelConfig {
	cc := &NotificationChannelConfig{
		ID:          w.ID,
		ChannelType: w.Attributes.ChannelType,
		IsEnabled:   w.Attributes.IsEnabled,
	}
	if w.Attributes.Target != nil {
		cc.Target = *w.Attributes.Target
	}
	if w.Attributes.ChannelName != nil {
		cc.ChannelName = *w.Attributes.ChannelName
	}
	if w.References.Org != nil {
		cc.OrgID = w.References.Org.ID
	}

	switch {
	case w.References.Project != nil:
		cc.Scope = NotificationScopeProject
		cc.ProjectID = w.References.Project.ID
	case w.References.User != nil:
		cc.Scope = NotificationScopeUser
		cc.UserID = w.References.User.ID
	}

	return cc
}

// CreateNotificationChannelConfigRequest is the input to
// CreateNotificationChannelConfig.
//
// The API upserts on (scope, entity, channel_type): creating a config for an
// entity/channel_type pair that already has one replaces it in place rather
// than erroring, but the provider only ever creates through Terraform's own
// lifecycle, so that is not relied upon here.
type CreateNotificationChannelConfigRequest struct {
	// Scope is NotificationScopeUser or NotificationScopeProject.
	Scope       string
	ChannelType string
	Target      string
	IsEnabled   bool
	// ProjectID is required when Scope is NotificationScopeProject, and ignored
	// otherwise.
	ProjectID string
	// OrgID is always required: even a user-scoped config is tied to one
	// organization.
	OrgID string
}

type createChannelConfigAttributesWire struct {
	Scope       string  `json:"scope"`
	ChannelType string  `json:"channel_type"`
	Target      *string `json:"target,omitempty"`
	IsEnabled   bool    `json:"is_enabled"`
}

type createChannelConfigReferencesWire struct {
	Project *notificationEntityRef `json:"project,omitempty"`
	Org     *notificationEntityRef `json:"org"`
}

type createChannelConfigBody struct {
	Attributes createChannelConfigAttributesWire `json:"attributes"`
	References createChannelConfigReferencesWire `json:"references"`
}

// CreateNotificationChannelConfig creates a channel config for the calling
// user, or for a project when req.Scope is NotificationScopeProject.
//
// The calling user is inferred from the API token: there is no field that
// addresses another user, and a user-scoped config's user reference in the
// response is always the token's own user.
func (c *Client) CreateNotificationChannelConfig(
	ctx context.Context, req CreateNotificationChannelConfigRequest,
) (*NotificationChannelConfig, error) {
	var body Entity[createChannelConfigBody]
	body.Data.Attributes = createChannelConfigAttributesWire{
		Scope:       req.Scope,
		ChannelType: req.ChannelType,
		Target:      &req.Target,
		IsEnabled:   req.IsEnabled,
	}
	body.Data.References.Org = &notificationEntityRef{ID: req.OrgID}
	if req.Scope == NotificationScopeProject {
		body.Data.References.Project = &notificationEntityRef{ID: req.ProjectID}
	}

	var env Entity[channelConfigWire]
	if err := c.PostV3(ctx, notificationChannelConfigsRoute, body, &env); err != nil {
		return nil, err
	}

	return env.Data.toChannelConfig(), nil
}

// GetNotificationChannelConfig retrieves a single channel config by its id.
// Only the calling user's own configs, and configs of projects the caller can
// view, are reachable; anything else answers 404 rather than 403.
func (c *Client) GetNotificationChannelConfig(ctx context.Context, id string) (*NotificationChannelConfig, error) {
	var env Entity[channelConfigWire]
	if err := c.GetV3(ctx, notificationChannelConfigRoute, &env, RouteParams(id)); err != nil {
		return nil, err
	}

	return env.Data.toChannelConfig(), nil
}

type updateChannelConfigAttributesWire struct {
	Target    *string `json:"target,omitempty"`
	IsEnabled *bool   `json:"is_enabled,omitempty"`
}

type updateChannelConfigBody struct {
	Attributes updateChannelConfigAttributesWire `json:"attributes"`
}

// UpdateNotificationChannelConfig applies a full replacement of target and
// is_enabled to an existing channel config, addressed by id. channel_type and
// scope cannot be changed through this route.
func (c *Client) UpdateNotificationChannelConfig(
	ctx context.Context, id, target string, isEnabled bool,
) (*NotificationChannelConfig, error) {
	var body Entity[updateChannelConfigBody]
	body.Data.Attributes.Target = &target
	body.Data.Attributes.IsEnabled = &isEnabled

	var env Entity[channelConfigWire]
	if err := c.PostV3(ctx, notificationChannelConfigUpdateRoute, body, &env, RouteParams(id)); err != nil {
		return nil, err
	}

	return env.Data.toChannelConfig(), nil
}

// DeleteNotificationChannelConfig deletes a channel config by id.
func (c *Client) DeleteNotificationChannelConfig(ctx context.Context, id string) error {
	return c.DeleteV3(ctx, notificationChannelConfigRoute, RouteParams(id))
}

// ListNotificationChannelConfigsOptions scopes a ListNotificationChannelConfigs
// call. Scope is required; ProjectID and OrgID are required together when
// Scope is NotificationScopeProject and ignored for NotificationScopeUser.
type ListNotificationChannelConfigsOptions struct {
	Scope     string
	ProjectID string
	OrgID     string
}

// ListNotificationChannelConfigs lists the channel configs for the calling
// user, or for one project, following the v3 cursor to the last page.
//
// In practice the API answers every channel-config listing in a single page:
// there is no route that could ever return enough rows to paginate. The cursor
// is still followed for forward compatibility rather than assumed away.
func (c *Client) ListNotificationChannelConfigs(
	ctx context.Context, opts ListNotificationChannelConfigsOptions,
) ([]NotificationChannelConfig, error) {
	return DrainV3(ctx, func(ctx context.Context, cursor string) (List[NotificationChannelConfig], error) {
		var page List[channelConfigWire]
		err := c.GetV3(ctx, notificationChannelConfigsRoute, &page,
			Filter("scope", opts.Scope),
			Filter("project_id", opts.ProjectID),
			Filter("org_id", opts.OrgID),
			PageCursor(cursor),
		)
		if err != nil {
			return List[NotificationChannelConfig]{}, err
		}

		out := List[NotificationChannelConfig]{Meta: page.Meta, Page: page.Page}
		for _, wire := range page.Data {
			out.Data = append(out.Data, *wire.toChannelConfig())
		}

		return out, nil
	})
}
