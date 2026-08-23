// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// This file stands up an in-memory fake of the v3 notification channel-config,
// preferences and integration routes, so the notification resources and data
// sources can be driven through real Terraform plans and applies without a
// CircleCI account.

// notificationFakeChannelConfig is one stored channel config row.
type notificationFakeChannelConfig struct {
	ID          string
	Scope       string
	ChannelType string
	Target      string
	ChannelName string
	IsEnabled   bool
	ProjectID   string
	OrgID       string
	UserID      string
}

// notificationFakePreference is one fixed catalog row. IsEnabled is the
// current value, seeded from a default and mutated only through the bulk
// update route, mirroring how a real preference is never created or deleted.
type notificationFakePreference struct {
	ID         string
	EntityType string // "user" or "project": which scope this row belongs to.
	Name       string
	Channel    string
	GroupID    string
	GroupName  string
	IsEnabled  bool
	// SectionID and SectionName are empty for a row that belongs to no section,
	// which the fake renders as the explicit JSON nulls the real API sends.
	SectionID   string
	SectionName string
}

// notificationFakeIntegration is one stored integration row.
type notificationFakeIntegration struct {
	ID            string
	Type          string
	WorkspaceName string
	TeamID        string
	Status        string
	OrgID         string
	OrgName       string
}

// notificationFakeRequest is one request the fake received.
type notificationFakeRequest struct {
	Method string
	Path   string
	Body   string
}

// notificationFakeAPI is a fake of the CircleCI v3 notification API.
type notificationFakeAPI struct {
	server *httptest.Server

	mu           sync.Mutex
	requests     []notificationFakeRequest
	channelCfgs  map[string]*notificationFakeChannelConfig
	preferences  map[string]*notificationFakePreference
	integrations map[string]*notificationFakeIntegration
	nextID       int
}

const notificationFakeTimestamp = "2026-01-02T03:04:05.000Z"

// newNotificationFakeAPI starts a fake v3 API and stops it when the test ends.
func newNotificationFakeAPI(t *testing.T) *notificationFakeAPI {
	t.Helper()

	api := &notificationFakeAPI{
		channelCfgs:  map[string]*notificationFakeChannelConfig{},
		preferences:  map[string]*notificationFakePreference{},
		integrations: map[string]*notificationFakeIntegration{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/notification/channel-configs", api.listChannelConfigs)
	mux.HandleFunc("POST /api/v3/notification/channel-configs", api.createChannelConfig)
	mux.HandleFunc("GET /api/v3/notification/channel-configs/{id}", api.getChannelConfig)
	mux.HandleFunc("POST /api/v3/notification/channel-configs/{id}/update", api.updateChannelConfig)
	mux.HandleFunc("DELETE /api/v3/notification/channel-configs/{id}", api.deleteChannelConfig)
	mux.HandleFunc("GET /api/v3/notification/preferences", api.listPreferences)
	mux.HandleFunc("POST /api/v3/notification/preferences", api.updatePreferences)
	mux.HandleFunc("GET /api/v3/notification/integrations", api.listIntegrations)
	mux.HandleFunc("GET /api/v3/notification/integrations/{id}", api.getIntegration)
	mux.HandleFunc("POST /api/v3/notification/integrations/{id}/set-status", api.setIntegrationStatus)
	mux.HandleFunc("DELETE /api/v3/notification/integrations/{id}", api.deleteIntegration)

	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.record(r)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(api.server.Close)

	return api
}

// URL is the fake's origin, for the provider's host attribute.
func (a *notificationFakeAPI) URL() string { return a.server.URL }

func (a *notificationFakeAPI) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))

	a.mu.Lock()
	defer a.mu.Unlock()

	a.requests = append(a.requests, notificationFakeRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Body:   string(body),
	})
}

// requestsFor returns every recorded request whose method matches and whose
// path contains the given fragment.
// requestsEndingIn matches on an exact path suffix.
//
// requestsFor below matches a substring, which cannot distinguish a create from an
// update on this API: the update route is the collection path plus "/{id}/update",
// so counting creates with a substring match counts every update as a create too.
func (a *notificationFakeAPI) requestsEndingIn(method, pathSuffix string) []notificationFakeRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	var matching []notificationFakeRequest
	for _, req := range a.requests {
		if req.Method == method && strings.HasSuffix(req.Path, pathSuffix) {
			matching = append(matching, req)
		}
	}

	return matching
}

func (a *notificationFakeAPI) requestsFor(method, pathContains string) []notificationFakeRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	var matching []notificationFakeRequest
	for _, req := range a.requests {
		if req.Method == method && strings.Contains(req.Path, pathContains) {
			matching = append(matching, req)
		}
	}

	return matching
}

func (a *notificationFakeAPI) mintID() string {
	a.nextID++

	return fmt.Sprintf("nfake-%08d-0000-0000-0000-000000000000", a.nextID)
}

func notificationFakeWriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// notificationFakeWriteError answers with the v3 error envelope the API
// actually sends: {"error":{"id","title"}}, with no "detail" field.
func notificationFakeWriteError(w http.ResponseWriter, status int, title string) {
	notificationFakeWriteJSON(w, status, map[string]any{
		"error": map[string]any{"id": "fake-trace", "title": title},
	})
}

func notificationFakeDecode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		notificationFakeWriteError(w, http.StatusBadRequest, "Bad Request: "+err.Error())

		return false
	}

	return true
}

// --- channel configs ---

func (a *notificationFakeAPI) channelConfigEntity(cc *notificationFakeChannelConfig) map[string]any {
	attrs := map[string]any{
		"channel_type": cc.ChannelType,
		"is_enabled":   cc.IsEnabled,
	}
	// Verified against the real API [NET]: a project-scoped, channel_type =
	// "email" config never echoes target back, on create, get or list, even
	// though the create request carried one and the API accepted it. Every
	// other scope/channel_type combination probed did echo it back. An
	// earlier version of this fake always included target, which is the wrong
	// belief -- it let TestAccNotificationChannelConfigResource_ProjectSlack
	// and the user-scope tests pass while hiding the one combination that
	// actually drops it.
	targetIsNeverEchoed := cc.Scope == "project" && cc.ChannelType == "email"
	if !targetIsNeverEchoed {
		attrs["target"] = cc.Target
	}
	if cc.ChannelName != "" {
		attrs["channel_name"] = cc.ChannelName
	}

	refs := map[string]any{"org": map[string]any{"id": cc.OrgID}}
	if cc.Scope == "project" {
		refs["project"] = map[string]any{"id": cc.ProjectID}
	} else {
		refs["user"] = map[string]any{"id": cc.UserID}
	}

	return map[string]any{"id": cc.ID, "attributes": attrs, "references": refs}
}

// notificationFakeCallerUserID is the fixed id the fake reports for any
// user-scoped config, standing in for "whoever the API token authenticates
// as" in the real API.
const notificationFakeCallerUserID = "nfake-user-0000-0000-0000-000000000000"

func (a *notificationFakeAPI) listChannelConfigs(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("filter[scope]")
	if scope == "" {
		notificationFakeWriteError(w, http.StatusBadRequest, `filter[scope] must be "user" or "project"`)

		return
	}
	projectID := r.URL.Query().Get("filter[project_id]")
	orgID := r.URL.Query().Get("filter[org_id]")

	a.mu.Lock()
	defer a.mu.Unlock()

	data := make([]map[string]any, 0, len(a.channelCfgs))
	for _, cc := range a.channelCfgs {
		if cc.Scope != scope {
			continue
		}
		if scope == "project" && (cc.ProjectID != projectID || cc.OrgID != orgID) {
			continue
		}
		data = append(data, a.channelConfigEntity(cc))
	}

	// A single-page v3 collection omits "page" entirely.
	notificationFakeWriteJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (a *notificationFakeAPI) createChannelConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Data struct {
			Attributes struct {
				Scope       string `json:"scope"`
				ChannelType string `json:"channel_type"`
				Target      string `json:"target"`
				IsEnabled   bool   `json:"is_enabled"`
			} `json:"attributes"`
			References struct {
				Project *struct {
					ID string `json:"id"`
				} `json:"project"`
				Org struct {
					ID string `json:"id"`
				} `json:"org"`
			} `json:"references"`
		} `json:"data"`
	}
	if !notificationFakeDecode(w, r, &body) {
		return
	}

	if body.Data.Attributes.Scope != "user" && body.Data.Attributes.Scope != "project" {
		notificationFakeWriteError(w, http.StatusBadRequest, `scope must be "user" or "project"`)

		return
	}
	if body.Data.Attributes.Scope == "project" && body.Data.References.Project == nil {
		notificationFakeWriteError(w, http.StatusBadRequest, "data.references.project is required for project scope")

		return
	}
	switch body.Data.Attributes.ChannelType {
	case "email", "slack":
	default:
		notificationFakeWriteError(w, http.StatusBadRequest, "invalid channel_type")

		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// [NET] measured, and the asymmetry this whole family's write path was
	// found to have: a project-scoped Slack channel config is rejected
	// outright when the organization has no active Slack integration
	// installed -- the real API answers exactly this 404, not a 400 naming
	// the missing integration -- while a user-scoped one (below, no check at
	// all) is accepted regardless, even naming a channel ID CircleCI never
	// validated. See notificationChannelConfigWarnIfSlackUnintegrated in
	// notification_channel_config_resource.go for how the provider responds
	// to the user-scoped half of that asymmetry.
	if body.Data.Attributes.Scope == "project" && body.Data.Attributes.ChannelType == "slack" &&
		!a.hasActiveSlackIntegrationLocked(body.Data.References.Org.ID) {
		notificationFakeWriteError(w, http.StatusNotFound, "Resource does not exist or unauthorized")

		return
	}

	cc := &notificationFakeChannelConfig{
		ID:          a.mintID(),
		Scope:       body.Data.Attributes.Scope,
		ChannelType: body.Data.Attributes.ChannelType,
		Target:      body.Data.Attributes.Target,
		IsEnabled:   body.Data.Attributes.IsEnabled,
		OrgID:       body.Data.References.Org.ID,
	}
	if cc.Scope == "project" {
		cc.ProjectID = body.Data.References.Project.ID
		if cc.ChannelType == "slack" {
			cc.ChannelName = "#" + cc.Target
		}
	} else {
		cc.UserID = notificationFakeCallerUserID
	}
	a.channelCfgs[cc.ID] = cc

	notificationFakeWriteJSON(w, http.StatusOK, map[string]any{"data": a.channelConfigEntity(cc)})
}

func (a *notificationFakeAPI) getChannelConfig(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	cc, ok := a.channelCfgs[r.PathValue("id")]
	if !ok {
		notificationFakeWriteError(w, http.StatusNotFound, "Not found.")

		return
	}

	notificationFakeWriteJSON(w, http.StatusOK, map[string]any{"data": a.channelConfigEntity(cc)})
}

func (a *notificationFakeAPI) updateChannelConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Data struct {
			Attributes struct {
				Target    *string `json:"target"`
				IsEnabled *bool   `json:"is_enabled"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if !notificationFakeDecode(w, r, &body) {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	cc, ok := a.channelCfgs[r.PathValue("id")]
	if !ok {
		notificationFakeWriteError(w, http.StatusNotFound, "Not found.")

		return
	}

	if body.Data.Attributes.Target != nil {
		cc.Target = *body.Data.Attributes.Target
		if cc.Scope == "project" && cc.ChannelType == "slack" {
			cc.ChannelName = "#" + cc.Target
		}
	}
	if body.Data.Attributes.IsEnabled != nil {
		cc.IsEnabled = *body.Data.Attributes.IsEnabled
	}

	notificationFakeWriteJSON(w, http.StatusOK, map[string]any{"data": a.channelConfigEntity(cc)})
}

func (a *notificationFakeAPI) deleteChannelConfig(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	id := r.PathValue("id")
	if _, ok := a.channelCfgs[id]; !ok {
		notificationFakeWriteError(w, http.StatusNotFound, "Not found.")

		return
	}
	delete(a.channelCfgs, id)
	w.WriteHeader(http.StatusNoContent)
}

// --- preferences ---

// seedPreference adds a fixed catalog row directly, standing in for
// CircleCI's pre-seeded preference catalog.
func (a *notificationFakeAPI) seedPreference(entityType, name, channel string, defaultEnabled bool) string {
	a.mu.Lock()
	defer a.mu.Unlock()

	id := a.mintID()
	a.preferences[id] = &notificationFakePreference{
		ID:         id,
		EntityType: entityType,
		Name:       name,
		Channel:    channel,
		GroupID:    "nfake-group",
		GroupName:  "General",
		IsEnabled:  defaultEnabled,
	}

	return id
}

// seedPreferenceInSection adds a catalog row that belongs to a section.
//
// Sections are the one part of a preference row that is optional upstream, and
// therefore the one part a wrong json tag would leave permanently null without
// any test noticing: seedPreference's rows have no section, so section_id and
// section_name decode to nil on those whether the tags are right or not.
func (a *notificationFakeAPI) seedPreferenceInSection(
	entityType, name, channel, sectionID, sectionName string, defaultEnabled bool,
) string {
	id := a.seedPreference(entityType, name, channel, defaultEnabled)

	a.mu.Lock()
	defer a.mu.Unlock()

	a.preferences[id].SectionID = sectionID
	a.preferences[id].SectionName = sectionName

	return id
}

func (a *notificationFakeAPI) preferenceEntity(p *notificationFakePreference, scope, userID, projectID, orgID string) map[string]any {
	attrs := map[string]any{
		"preference_name":          p.Name,
		"channel":                  p.Channel,
		"scope":                    "actor",
		"preference_display_order": 1,
		"preference_experimental":  false,
		"group_id":                 p.GroupID,
		"group_name":               p.GroupName,
		"group_display_order":      1,
		"group_experimental":       false,
		"user_configurable":        true,
		"is_enabled":               p.IsEnabled,
	}

	// The section fields carry no omitempty on the API, so they are always
	// present on the wire and explicitly null for a row with no section — not
	// omitted, as an earlier version of this fake had it. section_description
	// and section_docs_ref are sent by the API but not modelled by this
	// provider's client; they are included so the client is exercised against a
	// response carrying fields it does not know about.
	attrs["section_id"] = nil
	attrs["section_name"] = nil
	attrs["section_display_order"] = nil
	attrs["section_experimental"] = nil
	attrs["section_description"] = nil
	attrs["section_docs_ref"] = nil
	if p.SectionID != "" {
		attrs["section_id"] = p.SectionID
		attrs["section_name"] = p.SectionName
		attrs["section_display_order"] = 4
		attrs["section_experimental"] = true
		attrs["section_description"] = "Notifications about releases"
		attrs["section_docs_ref"] = "https://circleci.com/docs/deploy"
	}

	var refs map[string]any
	if scope == "project" {
		refs = map[string]any{
			"project": map[string]any{"id": projectID},
			"org":     map[string]any{"id": orgID},
		}
	} else {
		refs = map[string]any{"user": map[string]any{"id": userID}}
	}

	return map[string]any{"id": p.ID, "attributes": attrs, "references": refs}
}

func (a *notificationFakeAPI) listPreferences(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("filter[scope]")
	if scope == "" {
		notificationFakeWriteError(w, http.StatusBadRequest, `filter[scope] must be "user" or "project"`)

		return
	}
	projectID := r.URL.Query().Get("filter[project_id]")
	orgID := r.URL.Query().Get("filter[org_id]")

	a.mu.Lock()
	defer a.mu.Unlock()

	data := make([]map[string]any, 0, len(a.preferences))
	for _, p := range a.preferences {
		if p.EntityType != scope {
			continue
		}
		data = append(data, a.preferenceEntity(p, scope, notificationFakeCallerUserID, projectID, orgID))
	}

	notificationFakeWriteJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (a *notificationFakeAPI) updatePreferences(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Data struct {
			Attributes struct {
				Scope   string `json:"scope"`
				Updates []struct {
					PreferenceID string `json:"preference_id"`
					IsEnabled    bool   `json:"is_enabled"`
				} `json:"updates"`
			} `json:"attributes"`
			References struct {
				Project *struct {
					ID string `json:"id"`
				} `json:"project"`
				Org *struct {
					ID string `json:"id"`
				} `json:"org"`
			} `json:"references"`
		} `json:"data"`
	}
	if !notificationFakeDecode(w, r, &body) {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, u := range body.Data.Attributes.Updates {
		p, ok := a.preferences[u.PreferenceID]
		if !ok {
			notificationFakeWriteError(w, http.StatusBadRequest, "unknown preference id "+u.PreferenceID)

			return
		}
		p.IsEnabled = u.IsEnabled
	}

	projectID, orgID := "", ""
	if body.Data.References.Project != nil {
		projectID = body.Data.References.Project.ID
	}
	if body.Data.References.Org != nil {
		orgID = body.Data.References.Org.ID
	}

	data := make([]map[string]any, 0, len(a.preferences))
	for _, p := range a.preferences {
		if p.EntityType != body.Data.Attributes.Scope {
			continue
		}
		data = append(data, a.preferenceEntity(p, body.Data.Attributes.Scope, notificationFakeCallerUserID, projectID, orgID))
	}

	notificationFakeWriteJSON(w, http.StatusOK, map[string]any{"data": data})
}

// --- integrations ---

// hasActiveSlackIntegrationLocked reports whether orgID has an active Slack
// integration installed. Callers must already hold a.mu.
func (a *notificationFakeAPI) hasActiveSlackIntegrationLocked(orgID string) bool {
	for _, i := range a.integrations {
		if i.OrgID == orgID && i.Type == "slack" && i.Status == "active" {
			return true
		}
	}

	return false
}

// seedIntegration adds an installed integration directly, standing in for the
// OAuth install flow this API has no route for.
func (a *notificationFakeAPI) seedIntegration(workspaceName, teamID, orgID, orgName string) *notificationFakeIntegration {
	a.mu.Lock()
	defer a.mu.Unlock()

	i := &notificationFakeIntegration{
		ID:            a.mintID(),
		Type:          "slack",
		WorkspaceName: workspaceName,
		TeamID:        teamID,
		Status:        "active",
		OrgID:         orgID,
		OrgName:       orgName,
	}
	a.integrations[i.ID] = i

	return i
}

func (a *notificationFakeAPI) integrationEntity(i *notificationFakeIntegration) map[string]any {
	return map[string]any{
		"id": i.ID,
		"attributes": map[string]any{
			"type":           i.Type,
			"workspace_name": i.WorkspaceName,
			"team_id":        i.TeamID,
			"status":         i.Status,
			"created_at":     notificationFakeTimestamp,
			"updated_at":     notificationFakeTimestamp,
		},
		"references": map[string]any{
			"org": map[string]any{
				"id":         i.OrgID,
				"attributes": map[string]any{"name": i.OrgName},
			},
		},
	}
}

func (a *notificationFakeAPI) listIntegrations(w http.ResponseWriter, r *http.Request) {
	orgID := r.URL.Query().Get("filter[org_id]")
	typ := r.URL.Query().Get("filter[type]")

	a.mu.Lock()
	defer a.mu.Unlock()

	data := make([]map[string]any, 0, len(a.integrations))
	for _, i := range a.integrations {
		if orgID != "" && i.OrgID != orgID {
			continue
		}
		if typ != "" && i.Type != typ {
			continue
		}
		if i.Status == "revoked" {
			continue
		}
		data = append(data, a.integrationEntity(i))
	}

	notificationFakeWriteJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (a *notificationFakeAPI) getIntegration(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	i, ok := a.integrations[r.PathValue("id")]
	if !ok || i.Status == "revoked" {
		notificationFakeWriteError(w, http.StatusNotFound, "Not found.")

		return
	}

	notificationFakeWriteJSON(w, http.StatusOK, map[string]any{"data": a.integrationEntity(i)})
}

func (a *notificationFakeAPI) setIntegrationStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Data struct {
			Attributes struct {
				Status string `json:"status"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if !notificationFakeDecode(w, r, &body) {
		return
	}

	if body.Data.Attributes.Status != "active" && body.Data.Attributes.Status != "disabled" {
		notificationFakeWriteError(w, http.StatusBadRequest, `status must be "active" or "disabled"`)

		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	i, ok := a.integrations[r.PathValue("id")]
	if !ok || i.Status == "revoked" {
		notificationFakeWriteError(w, http.StatusNotFound, "Not found.")

		return
	}
	i.Status = body.Data.Attributes.Status

	notificationFakeWriteJSON(w, http.StatusOK, map[string]any{"data": a.integrationEntity(i)})
}

func (a *notificationFakeAPI) deleteIntegration(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	i, ok := a.integrations[r.PathValue("id")]
	if !ok {
		notificationFakeWriteError(w, http.StatusNotFound, "Not found.")

		return
	}
	i.Status = "revoked"
	w.WriteHeader(http.StatusNoContent)
}
