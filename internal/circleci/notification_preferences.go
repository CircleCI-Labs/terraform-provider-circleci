// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// notificationPreferencesRoute serves both the GET (matrix read, scoped by
// filter[scope]) and the POST (bulk update) for notification preferences.
const notificationPreferencesRoute = "/notification/preferences"

// NotificationPreference is one row of a notification preference matrix, as
// served by /api/v3/notification/preferences.
//
// A preference is a fixed, CircleCI-defined catalog entry (there is no route to
// create or delete one); only IsEnabled is ever written, through
// UpdateNotificationPreferences. The rest of the fields describe the catalog
// entry for display purposes.
//
// Exactly one of UserID and ProjectID is set, selected by which scope the
// matrix was requested for. OrgID is set only for a project-scoped row.
type NotificationPreference struct {
	ID string
	// Name is the preference's display name.
	Name string
	// Channel is the delivery channel this row's IsEnabled applies to, such as
	// "email".
	Channel string
	// EntityScope is the row's own scope attribute (for example "actor"),
	// distinct from the user/project scope the matrix was requested for.
	EntityScope       string
	DisplayOrder      int
	Experimental      bool
	GroupID           string
	GroupName         string
	GroupDisplayOrder int
	GroupExperimental bool
	// SectionID, SectionName, SectionDisplayOrder and SectionExperimental are
	// nil when the preference belongs to no section.
	SectionID           *string
	SectionName         *string
	SectionDisplayOrder *int
	SectionExperimental *bool
	// UserConfigurable reports whether a practitioner may change IsEnabled at
	// all; some rows are informational only.
	UserConfigurable bool
	IsEnabled        bool
	UserID           string
	ProjectID        string
	OrgID            string
}

type preferenceAttributesWire struct {
	PreferenceName         string  `json:"preference_name"`
	Channel                string  `json:"channel"`
	Scope                  string  `json:"scope"`
	PreferenceDisplayOrder int     `json:"preference_display_order"`
	PreferenceExperimental bool    `json:"preference_experimental"`
	GroupID                string  `json:"group_id"`
	GroupName              string  `json:"group_name"`
	GroupDisplayOrder      int     `json:"group_display_order"`
	GroupExperimental      bool    `json:"group_experimental"`
	SectionID              *string `json:"section_id,omitempty"`
	SectionName            *string `json:"section_name,omitempty"`
	SectionDisplayOrder    *int    `json:"section_display_order,omitempty"`
	SectionExperimental    *bool   `json:"section_experimental,omitempty"`
	UserConfigurable       bool    `json:"user_configurable"`
	IsEnabled              bool    `json:"is_enabled"`
}

type preferenceReferencesWire struct {
	User    *notificationEntityRef `json:"user,omitempty"`
	Project *notificationEntityRef `json:"project,omitempty"`
	Org     *notificationEntityRef `json:"org,omitempty"`
}

type preferenceWire struct {
	ID         string                   `json:"id"`
	Attributes preferenceAttributesWire `json:"attributes"`
	References preferenceReferencesWire `json:"references"`
}

func (w preferenceWire) toPreference() *NotificationPreference {
	p := &NotificationPreference{
		ID:                  w.ID,
		Name:                w.Attributes.PreferenceName,
		Channel:             w.Attributes.Channel,
		EntityScope:         w.Attributes.Scope,
		DisplayOrder:        w.Attributes.PreferenceDisplayOrder,
		Experimental:        w.Attributes.PreferenceExperimental,
		GroupID:             w.Attributes.GroupID,
		GroupName:           w.Attributes.GroupName,
		GroupDisplayOrder:   w.Attributes.GroupDisplayOrder,
		GroupExperimental:   w.Attributes.GroupExperimental,
		SectionID:           w.Attributes.SectionID,
		SectionName:         w.Attributes.SectionName,
		SectionDisplayOrder: w.Attributes.SectionDisplayOrder,
		SectionExperimental: w.Attributes.SectionExperimental,
		UserConfigurable:    w.Attributes.UserConfigurable,
		IsEnabled:           w.Attributes.IsEnabled,
	}

	if w.References.User != nil {
		p.UserID = w.References.User.ID
	}
	if w.References.Project != nil {
		p.ProjectID = w.References.Project.ID
	}
	if w.References.Org != nil {
		p.OrgID = w.References.Org.ID
	}

	return p
}

// ListNotificationPreferencesOptions scopes a ListNotificationPreferences
// call. Scope is required; ProjectID and OrgID are required together when
// Scope is NotificationScopeProject and ignored for NotificationScopeUser.
type ListNotificationPreferencesOptions struct {
	Scope     string
	ProjectID string
	OrgID     string
}

// ListNotificationPreferences reads the full preference matrix for the calling
// user, or for one project, following the v3 cursor to the last page.
//
// As with channel configs, the API answers the whole matrix in a single page in
// practice; the cursor is followed anyway for forward compatibility.
func (c *Client) ListNotificationPreferences(
	ctx context.Context, opts ListNotificationPreferencesOptions,
) ([]NotificationPreference, error) {
	return DrainV3(ctx, func(ctx context.Context, cursor string) (List[NotificationPreference], error) {
		var page List[preferenceWire]
		err := c.GetV3(ctx, notificationPreferencesRoute, &page,
			Filter("scope", opts.Scope),
			Filter("project_id", opts.ProjectID),
			Filter("org_id", opts.OrgID),
			PageCursor(cursor),
		)
		if err != nil {
			return List[NotificationPreference]{}, err
		}

		out := List[NotificationPreference]{Meta: page.Meta, Page: page.Page}
		for _, wire := range page.Data {
			out.Data = append(out.Data, *wire.toPreference())
		}

		return out, nil
	})
}

// NotificationPreferenceUpdate is one row of a bulk preference update.
type NotificationPreferenceUpdate struct {
	PreferenceID string
	IsEnabled    bool
}

type preferenceUpdateItemWire struct {
	PreferenceID string `json:"preference_id"`
	IsEnabled    bool   `json:"is_enabled"`
}

type updatePreferencesAttributesWire struct {
	Scope   string                     `json:"scope"`
	Updates []preferenceUpdateItemWire `json:"updates"`
}

type updatePreferencesReferencesWire struct {
	Project *notificationEntityRef `json:"project,omitempty"`
	Org     *notificationEntityRef `json:"org,omitempty"`
}

type updatePreferencesBody struct {
	Attributes updatePreferencesAttributesWire  `json:"attributes"`
	References *updatePreferencesReferencesWire `json:"references,omitempty"`
}

// UpdateNotificationPreferences bulk-updates the given rows of the preference
// matrix and returns the full, refreshed matrix. It is partial: rows not named
// in updates are left exactly as they were.
//
// projectID and orgID are used, and required, only when scope is
// NotificationScopeProject; a user-scoped update carries no references.
func (c *Client) UpdateNotificationPreferences(
	ctx context.Context, scope, projectID, orgID string, updates []NotificationPreferenceUpdate,
) ([]NotificationPreference, error) {
	var body Entity[updatePreferencesBody]
	body.Data.Attributes.Scope = scope

	items := make([]preferenceUpdateItemWire, 0, len(updates))
	for _, u := range updates {
		items = append(items, preferenceUpdateItemWire(u))
	}
	body.Data.Attributes.Updates = items

	if scope == NotificationScopeProject {
		body.Data.References = &updatePreferencesReferencesWire{
			Project: &notificationEntityRef{ID: projectID},
			Org:     &notificationEntityRef{ID: orgID},
		}
	}

	var env List[preferenceWire]
	if err := c.PostV3(ctx, notificationPreferencesRoute, body, &env); err != nil {
		return nil, err
	}

	out := make([]NotificationPreference, 0, len(env.Data))
	for _, wire := range env.Data {
		out = append(out, *wire.toPreference())
	}

	return out, nil
}
