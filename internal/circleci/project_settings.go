// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"encoding/json"
)

// projectSettingsRoute is the v2 route for a project's advanced settings. The
// same path serves the read (GET) and the partial update (PATCH).
//
// Settings are addressed by the three parts of a project slug rather than by
// project id, and they exist on v2 for both CircleCI Cloud and CircleCI Server,
// so nothing here is gated on Client.IsCloud.
const projectSettingsRoute = "/project/%s/%s/%s/settings"

// ProjectSettings holds the advanced settings of a single project, as served by
// GET /api/v2/project/{provider}/{org}/{project}/settings and accepted by the
// PATCH on the same route.
//
// Every toggle is a *bool and every field is omitempty for one reason: the PATCH
// is a partial update over one shared settings record, so a field present in the
// body is written and a field absent from it is left alone. Plain bools would
// send false for every toggle the caller did not intend to change and silently
// switch off whatever the project had enabled. A nil pointer is the only way to
// say "not managed here".
//
// This mirrors project.AdvanceSettings in circleci-sdk-go, with one addition the
// SDK struct does not carry: BuildPrsOnly. The v2 schema rejects unknown fields
// ("Unknown advanced setting"), so the set below is exactly the documented one.
type ProjectSettings struct {
	// AutocancelBuilds cancels running pipelines on a branch, except the default
	// branch, when a newer pipeline starts on that branch.
	AutocancelBuilds *bool `json:"autocancel_builds,omitempty"`
	// BuildForkPrs runs builds for pull requests opened from forks.
	BuildForkPrs *bool `json:"build_fork_prs,omitempty"`
	// BuildPrsOnly builds only branches that have an open pull request
	// associated with them. PROnlyBranchOverrides lists the exceptions.
	BuildPrsOnly *bool `json:"build_prs_only,omitempty"`
	// DisableSSH stops job re-runs from offering SSH debugging access.
	DisableSSH *bool `json:"disable_ssh,omitempty"`
	// ForksReceiveSecretEnvVars runs forked pull requests with this project's
	// environment variables and secrets, and shares the build cache with forks.
	ForksReceiveSecretEnvVars *bool `json:"forks_receive_secret_env_vars,omitempty"`
	// OSS reports whether the project is treated as free and open source, which
	// grants additional credits and makes builds publicly visible.
	//
	// READ-ONLY on this API version. The settings PATCH does not accept it and
	// rejects the whole request if it is present — verified against the live API:
	//
	//	PATCH /api/v2/project/{slug}/settings  {"advanced":{"oss":false}}
	//	→ 400  {"message":"Unexpected field 'advanced.oss'."}
	//
	// The same request without oss answers 200. Because the field IS returned by
	// the GET, it looks writable, and the published API reference documents it as
	// part of the request body — but no write path exists here. Sending it made
	// every project create and settings update fail against the real API while
	// every mocked test passed, because the fake accepted the field.
	//
	// MarshalJSON below drops it unconditionally, so it cannot be sent by
	// accident. Do not add it back to the write path without confirming against a
	// live installation.
	OSS *bool `json:"oss,omitempty"`
	// SetGithubStatus reports the status of every pushed commit to GitHub's
	// status API, once per job.
	SetGithubStatus *bool `json:"set_github_status,omitempty"`
	// SetupWorkflows allows configuration outside the primary .circleci
	// directory to be triggered conditionally by a setup workflow.
	SetupWorkflows *bool `json:"setup_workflows,omitempty"`
	// WriteSettingsRequiresAdmin requires organization administrator permissions
	// to change these settings.
	WriteSettingsRequiresAdmin *bool `json:"write_settings_requires_admin,omitempty"`
	// PROnlyBranchOverrides lists branches that always build, even when
	// BuildPrsOnly is enabled. The value sent replaces the existing list.
	//
	// It is a pointer to a slice, not a plain slice, so that the three states the
	// API distinguishes stay distinguishable: nil omits the field and leaves the
	// list alone, a pointer to an empty slice sends [] and clears every override,
	// and a pointer to a populated slice replaces the list. A plain slice with
	// omitempty could not express the middle case.
	PROnlyBranchOverrides *[]string `json:"pr_only_branch_overrides,omitempty"`
}

// IsEmpty reports whether no setting is set, meaning an update would carry an
// empty advanced object and change nothing. Callers skip the request in that
// case: the API rejects a body with no fields ("No JSON fields found.").
func (s ProjectSettings) IsEmpty() bool {
	for _, field := range []*bool{
		s.AutocancelBuilds,
		s.BuildForkPrs,
		s.BuildPrsOnly,
		s.DisableSSH,
		s.ForksReceiveSecretEnvVars,
		// OSS is deliberately absent: it is never marshalled, so a settings object
		// carrying only OSS has nothing to send and must count as empty. Including
		// it here would send a body with no fields and earn a 400.
		s.SetGithubStatus,
		s.SetupWorkflows,
		s.WriteSettingsRequiresAdmin,
	} {
		if field != nil {
			return false
		}
	}

	return s.PROnlyBranchOverrides == nil
}

// projectSettingsEnvelope is the request and response body shape. Unlike the
// rest of v2, project settings are nested under a single "advanced" key, and the
// API rejects any other root field.
type projectSettingsEnvelope struct {
	Advanced ProjectSettings `json:"advanced"`
}

// MarshalJSON serialises the settings for a write, always omitting oss.
//
// oss is read-only on this API version: the settings PATCH answers
// 400 "Unexpected field 'advanced.oss'." when it is present, and rejects the
// entire request, so one unwritable field would fail every write. See the comment
// on the OSS field.
//
// This is done here rather than in each caller so that it cannot be forgotten.
// The alternative — a separate write struct — would mean two types to keep in
// step, and the read and write shapes are otherwise identical.
func (s ProjectSettings) MarshalJSON() ([]byte, error) {
	// A local alias avoids recursing into this method. The alias has no methods,
	// so encoding/json falls back to struct-tag marshalling.
	type writable ProjectSettings

	out := writable(s)
	out.OSS = nil

	return json.Marshal(out)
}

// GetProjectSettings reads the advanced settings of a project, addressed by the
// three parts of its slug ("gh", "acme", "repo").
//
// A project that does not exist, or one whose settings the token may not read,
// yields an error satisfying IsNotFound.
func (c *Client) GetProjectSettings(ctx context.Context, vcsType, orgName, projectName string) (*ProjectSettings, error) {
	var envelope projectSettingsEnvelope

	err := c.GetV2(ctx, projectSettingsRoute, &envelope, RouteParams(vcsType, orgName, projectName))
	if err != nil {
		return nil, err
	}

	settings := envelope.Advanced

	return &settings, nil
}

// UpdateProjectSettings applies a partial update to a project's advanced
// settings and returns the full settings as the API reports them afterwards.
//
// Only the non-nil fields of settings are sent, so settings a caller does not
// manage keep their current values.
func (c *Client) UpdateProjectSettings(ctx context.Context, vcsType, orgName, projectName string, settings ProjectSettings) (*ProjectSettings, error) {
	var envelope projectSettingsEnvelope

	err := c.PatchV2(
		ctx,
		projectSettingsRoute,
		projectSettingsEnvelope{Advanced: settings},
		&envelope,
		RouteParams(vcsType, orgName, projectName),
	)
	if err != nil {
		return nil, err
	}

	updated := envelope.Advanced

	return &updated, nil
}
