// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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
	//
	// WRITABLE IN ONE DIRECTION ONLY ON A STANDALONE ORGANIZATION. Setting it to
	// false is accepted everywhere. Setting it to TRUE is refused on a project
	// whose slug's VCS segment is "circleci" — measured over the network against
	// the live API on three separate standalone organizations (one backed by the
	// GitHub App, one by GitLab, and one repo-less organization created for the
	// probe and owned by the calling token):
	//
	//	PATCH /api/v2/project/circleci/<org>/<project>/settings
	//	      {"advanced":{"forks_receive_secret_env_vars":true}}
	//	→ 403  {"message":"Permission denied."}
	//
	// The same request against a classic organization ("gh/<org>/<repo>", also
	// spelled "github/...") answers 200 — measured on two separate GitHub OAuth
	// organizations. It is not a role problem: the probe organization was created
	// by the token making the request, and the refusal is identical there.
	//
	// Two details make this worse than a plain rejection, and are why
	// UpdateProjectSettings refuses to send it rather than letting the route
	// answer:
	//
	//   - It is refused even when the value is already true. A no-op write of the
	//     project's own current value still answers 403, so this is a rejection of
	//     the field's true value and not a state transition check.
	//   - The 403 is NOT atomic. Every other field in the same body is applied
	//     first and keeps its new value — measured: a body carrying
	//     {"forks_receive_secret_env_vars":true,"autocancel_builds":true} answered
	//     403 and left autocancel_builds true. That is the opposite of OSS below,
	//     where the 400 is a schema rejection and nothing at all is written.
	//
	// A project's default value is true (see the default snapshot in
	// internal/provider/project_fake_test.go), so on a standalone organization the
	// setting is effectively a one-way switch: it can be turned off and never
	// turned back on through this route.
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
	// BuildPrsOnly is enabled. A non-empty value sent replaces the existing list.
	//
	// It is a pointer to a slice, not a plain slice, so that nil (omit the field,
	// leave the list alone) stays distinguishable from a pointer to an empty
	// slice (send []). A plain slice with omitempty could not express the
	// difference.
	//
	// CLEARING THE LIST IS NOT POSSIBLE ON THIS ROUTE. Sending [] is accepted
	// with HTTP 200 and changes nothing. The service fronting this route decodes
	// the v2 body into a plain []string and copies it into the v1.1 body it
	// forwards, where the field carries `omitempty` — so a zero-length slice is
	// dropped before the request that would have written it is made, and the
	// underlying setting is left exactly as it was. There is no other value that
	// means "no overrides": the field is a comma-joined string by the time it
	// reaches v1.1.
	//
	// The response to the PATCH is a fresh read, so the old list comes straight
	// back. UpdateProjectSettings turns that into an explicit error rather than
	// letting a caller store a value CircleCI did not accept — see the comment
	// there. This is a missing API capability, not something the client can work
	// around; the [] is still sent so that the guard stops firing by itself if
	// the route is ever fixed.
	PROnlyBranchOverrides *[]string `json:"pr_only_branch_overrides,omitempty"`
}

// IsEmpty reports whether no setting is set, meaning an update would carry an
// empty advanced object and change nothing. Callers skip the request in that
// case, because a request that cannot change anything is not worth making.
//
// Note that `{"advanced":{}}` is *not* rejected by the API — it answers 200 with
// the project's current settings, and the service's own handler tests assert
// exactly that. The 400 "No JSON fields found." belongs to a body that is empty
// at the top level (`{}`), which this client never sends because the envelope
// always carries an "advanced" key. Skipping the request is therefore an
// optimisation, not a workaround for a rejection.
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

// checkProjectSettingsPathSegments rejects a vcsType, orgName or projectName
// that is exactly "." or ".." — see isDotSegment in project.go — before any of
// the three reaches RouteParams. RouteParams percent-escapes each value as a
// route parameter, but escaping does not touch a segment made only of dots, so
// an unrejected one would put a literal "./" or "../" into the request path.
//
// Unlike projectSlugPath, checkoutKeyProjectPath and insightsSlugPath, this
// takes three already-split arguments rather than a single slug, because
// GetProjectSettings and UpdateProjectSettings address a project by its three
// parts directly and pass them through RouteParams individually.
func checkProjectSettingsPathSegments(vcsType, orgName, projectName string) error {
	for _, segment := range []string{vcsType, orgName, projectName} {
		if isDotSegment(segment) {
			return fmt.Errorf(
				"circleci: project settings vcs type, org name and project name must not be %q", segment,
			)
		}
	}

	return nil
}

// GetProjectSettings reads the advanced settings of a project, addressed by the
// three parts of its slug ("gh", "acme", "repo").
//
// A project that does not exist, or one whose settings the token may not read,
// yields an error satisfying IsNotFound.
func (c *Client) GetProjectSettings(ctx context.Context, vcsType, orgName, projectName string) (*ProjectSettings, error) {
	if err := checkProjectSettingsPathSegments(vcsType, orgName, projectName); err != nil {
		return nil, err
	}

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
	if err := checkProjectSettingsPathSegments(vcsType, orgName, projectName); err != nil {
		return nil, err
	}

	var envelope projectSettingsEnvelope

	if err := checkForkSecretsEnableSupported(vcsType, settings); err != nil {
		return nil, err
	}

	err := c.PatchV2(
		ctx,
		projectSettingsRoute,
		projectSettingsEnvelope{Advanced: settings},
		&envelope,
		RouteParams(vcsType, orgName, projectName),
	)
	if err != nil {
		return nil, annotateForkSecretsDenial(settings, err)
	}

	updated := envelope.Advanced

	if err := checkBranchOverridesCleared(settings, updated); err != nil {
		return nil, err
	}

	return &updated, nil
}

// ErrCannotClearBranchOverrides reports that an attempt to remove every
// pr_only_branch_overrides entry was accepted and ignored.
//
// It exists as a sentinel so a caller can recognise this one case without
// matching on message text. See ProjectSettings.PROnlyBranchOverrides for why
// the API behaves this way.
var ErrCannotClearBranchOverrides = errors.New(
	"CircleCI's project settings API cannot clear pr_only_branch_overrides: an empty list is " +
		"accepted with HTTP 200 and silently ignored, so the previous branches are still in force",
)

// checkBranchOverridesCleared reports an error when a write asked for every
// branch override to be removed and the API kept them.
//
// The API answers a settings PATCH with a fresh read, so this compares what was
// asked for against what CircleCI actually holds afterwards. Returning an error
// is deliberate: the alternative is to hand back a value that contradicts the
// request, which a Terraform resource would either store as state that lies or
// fail on with "Provider produced inconsistent result after apply" — a message
// that says nothing about which attribute or why.
//
// A project that had no overrides to begin with is not an error: the request was
// a no-op and the end state is the one that was asked for.
func checkBranchOverridesCleared(sent, got ProjectSettings) error {
	if sent.PROnlyBranchOverrides == nil || len(*sent.PROnlyBranchOverrides) > 0 {
		return nil
	}

	if got.PROnlyBranchOverrides == nil || len(*got.PROnlyBranchOverrides) == 0 {
		return nil
	}

	return fmt.Errorf(
		"%w (it still reports %d: %s). Remove pr_only_branch_overrides from the configuration to stop "+
			"managing it, or set it to the branches that should always build",
		ErrCannotClearBranchOverrides,
		len(*got.PROnlyBranchOverrides),
		strings.Join(*got.PROnlyBranchOverrides, ", "),
	)
}

// StandaloneSlugVCSType is the VCS segment of a project or organization slug
// belonging to a CircleCI-native ("standalone") organization: "circleci", as in
// "circleci/<org-identifier>/<project-identifier>".
//
// The segment is case-sensitive on this route — measured over the network:
// "circleci/..." answers 200 and "CircleCI/..." answers
// 404 "Project not found." — so comparisons against it are exact rather than
// case-insensitive.
const StandaloneSlugVCSType = OrganizationVCSTypeStandalone

// ErrCannotEnableForkSecrets reports that a write asked to enable
// forks_receive_secret_env_vars on a standalone organization's project, which
// the settings route refuses.
//
// It exists as a sentinel so a caller can recognise this one case without
// matching on message text, the same way ErrCannotClearBranchOverrides does. See
// ProjectSettings.ForksReceiveSecretEnvVars for the measurements behind it.
var ErrCannotEnableForkSecrets = errors.New(
	"CircleCI's project settings API refuses to enable forks_receive_secret_env_vars on a " +
		"standalone (circleci/...) organization's project: the request answers 403 " +
		"\"Permission denied.\" even when the setting is already true, and applies every other " +
		"field in the same request before failing",
)

// checkForkSecretsEnableSupported refuses, before any request is made, a write
// that would ask a standalone organization's project to enable
// forks_receive_secret_env_vars.
//
// Pre-flight rather than after the fact, which is the opposite of
// checkBranchOverridesCleared. The reason is that this particular 403 is not
// atomic: the route applies every other field in the body and only then refuses,
// so letting the request go out would change settings on a project while
// reporting failure — and a Terraform Create that fails does not write state, so
// those changes would be left behind untracked. There is no way to recover them
// afterwards, and no way to retry that succeeds. The only safe place to stop is
// before the request.
//
// The cost of being pre-flight is that this guard would have to be removed if
// CircleCI ever allows the write. That is deliberate: an error naming the
// setting and the organization class is far better than a silent partial write,
// and the sentinel above makes the guard easy to find.
func checkForkSecretsEnableSupported(vcsType string, settings ProjectSettings) error {
	if settings.ForksReceiveSecretEnvVars == nil || !*settings.ForksReceiveSecretEnvVars {
		return nil
	}

	if vcsType != StandaloneSlugVCSType {
		return nil
	}

	return fmt.Errorf(
		"%w. Set forks_receive_secret_env_vars to false, or remove it from the configuration to "+
			"leave whatever the project already has in place",
		ErrCannotEnableForkSecrets,
	)
}

// annotateForkSecretsDenial adds the one explanation an HTTP 403 from the
// settings route almost always needs.
//
// The pre-flight guard above catches the standalone case, which is the one this
// client can predict. A classic organization can still answer 403 — a token that
// is not an organization administrator on a project with
// write_settings_requires_admin enabled, for instance — and when the body
// carried forks_receive_secret_env_vars: true the bare "Permission denied."
// gives a practitioner nothing to act on. It also warns that the other settings
// in the request may already have been written, because on this route they are.
func annotateForkSecretsDenial(sent ProjectSettings, err error) error {
	if !HasStatus(err, http.StatusForbidden) {
		return err
	}

	if sent.ForksReceiveSecretEnvVars == nil || !*sent.ForksReceiveSecretEnvVars {
		return err
	}

	return fmt.Errorf(
		"%w (the request set forks_receive_secret_env_vars to true, which this route commonly "+
			"refuses; note that it applies the other settings in the same request before failing, "+
			"so they may already have been written)",
		err,
	)
}
