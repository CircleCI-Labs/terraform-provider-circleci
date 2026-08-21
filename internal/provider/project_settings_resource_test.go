// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// Project settings are served by v2 on both Cloud and Server, so these tests run
// against an in-process stand-in for the API rather than a real installation.
//
// The property most of them exist for is narrow and easy to regress: a setting
// the configuration does not mention must not appear in the PATCH body at all.
// Sending it would write a value nobody asked the provider to manage, so the
// assertions are on the exact JSON the fake API receives rather than on state.

// testProjectSettingsSlug is the project the fake API serves settings for.
const testProjectSettingsSlug = "github/acme/repo"

// fakeProjectSettingsAPI is an in-memory stand-in for
// /api/v2/project/{provider}/{org}/{project}/settings.
//
// The settings it holds are a raw map rather than circleci.ProjectSettings, so
// that a request body can be inspected key by key: the point of these tests is
// which keys were sent, which a typed struct with omitempty would hide.
type fakeProjectSettingsAPI struct {
	t *testing.T

	mu      sync.Mutex
	current map[string]any
	// patches records the "advanced" object of every PATCH received, in order.
	patches []map[string]any
	// requests records "METHOD path" for every request received.
	requests []string
	// notFound makes every request answer 404, to test drift handling.
	notFound bool
}

// newFakeProjectSettingsAPI starts a fake settings API and returns it alongside
// a client pointed at it.
func newFakeProjectSettingsAPI(t *testing.T) (*fakeProjectSettingsAPI, *circleci.Client) {
	t.Helper()

	api, host := startFakeProjectSettingsAPI(t)

	return api, circleci.New(circleci.Config{Host: host, Token: "fake"})
}

// startFakeProjectSettingsAPI starts a fake settings API and returns it
// alongside its origin, for tests that configure the provider by host.
func startFakeProjectSettingsAPI(t *testing.T) (*fakeProjectSettingsAPI, string) {
	t.Helper()

	api := &fakeProjectSettingsAPI{
		t: t,
		// The defaults a freshly followed private project has, as
		// defaultFakeProjectSettings in project_fake_test.go documents: a live GET
		// reports set_github_status and setup_workflows true,
		// forks_receive_secret_env_vars true on a private project, and
		// pr_only_branch_overrides holding the default branch.
		current: map[string]any{
			"autocancel_builds":             false,
			"build_fork_prs":                false,
			"build_prs_only":                false,
			"disable_ssh":                   false,
			"forks_receive_secret_env_vars": true,
			"oss":                           false,
			"set_github_status":             true,
			"setup_workflows":               true,
			"write_settings_requires_admin": false,
			"pr_only_branch_overrides":      []any{"main"},
		},
	}

	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	return api, srv.URL
}

func (f *fakeProjectSettingsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.requests = append(f.requests, r.Method+" "+r.URL.Path)

	// /api/v2/project/{provider}/{organization}/{project}/settings
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	valid := len(parts) == 7 &&
		parts[0] == "api" && parts[1] == "v2" && parts[2] == "project" && parts[6] == "settings"

	if f.notFound || !valid {
		f.write(w, http.StatusNotFound, map[string]string{"message": "Project not found"})

		return
	}

	switch r.Method {
	case http.MethodGet:
		f.write(w, http.StatusOK, map[string]any{"advanced": f.current})
	case http.MethodPatch:
		// parts[3] is the slug's VCS segment, which is what decides whether
		// enabling forks_receive_secret_env_vars is refused. See
		// standaloneForkSecretsDenied in project_fake_test.go.
		f.patch(w, r, parts[3])
	default:
		f.write(w, http.StatusMethodNotAllowed, map[string]string{"message": "Method Not Allowed"})
	}
}

// patch merges the request body into the held settings, the way the real API
// applies a partial update, and answers with the full settings.
//
// Validation and merge semantics both live in project_fake_test.go, shared with
// the other settings fake so the two cannot drift on what "the API" means: see
// settingsPatchBody for which bodies are rejected outright, and
// applySettingsPatchForSlug for the two things the real route does to
// pr_only_branch_overrides and for the one field it writes around and then
// refuses with 403.
func (f *fakeProjectSettingsAPI) patch(w http.ResponseWriter, r *http.Request, vcsType string) {
	var raw map[string]any

	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		f.write(w, http.StatusBadRequest, map[string]string{"message": "Invalid JSON body."})

		return
	}

	advanced, message, accepted := settingsPatchBody(raw)
	if advanced != nil {
		// Recorded even when rejected, so a test can assert on what was sent.
		f.patches = append(f.patches, advanced)
	}
	if !accepted {
		f.write(w, http.StatusBadRequest, map[string]string{"message": message})

		return
	}

	if applySettingsPatchForSlug(vcsType, f.current, advanced) {
		// Every other setting in the body is already written. See
		// applySettingsPatchForSlug in project_fake_test.go.
		f.write(w, http.StatusForbidden, map[string]string{"message": "Permission denied."})

		return
	}

	f.write(w, http.StatusOK, map[string]any{"advanced": f.current})
}

func (f *fakeProjectSettingsAPI) write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		f.t.Errorf("fake project settings API could not encode a response: %v", err)
	}
}

// set changes a held setting behind Terraform's back, to test drift handling.
func (f *fakeProjectSettingsAPI) set(key string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.current[key] = value
}

// recordedPatches returns the bodies received so far.
func (f *fakeProjectSettingsAPI) recordedPatches() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]map[string]any(nil), f.patches...)
}

// recordedRequests returns every request received so far.
func (f *fakeProjectSettingsAPI) recordedRequests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.requests...)
}

// onlyPatch returns the single PATCH body received, failing when the provider
// sent a different number of updates.
func (f *fakeProjectSettingsAPI) onlyPatch(t *testing.T) map[string]any {
	t.Helper()

	patches := f.recordedPatches()
	if len(patches) != 1 {
		t.Fatalf("the provider sent %d PATCH requests, want exactly 1: %v", len(patches), patches)
	}

	return patches[0]
}

// assertPatchKeys checks that a request body carries exactly the named settings,
// which is the assertion the omission behaviour hangs on.
func assertPatchKeys(t *testing.T, body map[string]any, want ...string) {
	t.Helper()

	got := make([]string, 0, len(body))
	for key := range body {
		got = append(got, key)
	}

	sort.Strings(got)
	sort.Strings(want)

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("PATCH body carried settings %v, want exactly %v", got, want)
	}
}

// projectSettingsSchema returns the resource schema, so that tests can build
// plan and state values for it.
func projectSettingsSchema(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	NewProjectSettingsResource().Schema(t.Context(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema method diagnostics: %+v", resp.Diagnostics)
	}

	return resp.Schema
}

// projectSettingsState builds a state value holding model. Plan, prior state and
// the empty state a resource writes into are all the same shape, so the same
// helper serves all three.
func projectSettingsState(t *testing.T, schema rschema.Schema, model projectSettingsResourceModel) tfsdk.State {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a state value: %+v", diags)
	}

	return state
}

// projectSettingsModel returns a model with every setting unmanaged, which is
// what a configuration that names only the slug produces.
func projectSettingsModel(slug string) projectSettingsResourceModel {
	return projectSettingsResourceModel{
		Slug:                  types.StringValue(slug),
		PROnlyBranchOverrides: types.SetNull(types.StringType),
	}
}

// createProjectSettings drives Create for a plan and returns the resulting state
// and diagnostics.
func createProjectSettings(t *testing.T, client *circleci.Client, plan projectSettingsResourceModel) (projectSettingsResourceModel, *fwresource.CreateResponse) {
	t.Helper()

	ctx := t.Context()
	schema := projectSettingsSchema(t)
	r := &projectSettingsResource{client: client}

	resp := &fwresource.CreateResponse{State: projectSettingsState(t, schema, projectSettingsModel(""))}
	r.Create(ctx, fwresource.CreateRequest{Plan: tfsdk.Plan{
		Schema: schema,
		Raw:    projectSettingsState(t, schema, plan).Raw,
	}}, resp)

	var got projectSettingsResourceModel
	if !resp.State.Raw.IsNull() {
		if diags := resp.State.Get(ctx, &got); diags.HasError() {
			t.Fatalf("could not read the state Create produced: %+v", diags)
		}
	}

	return got, resp
}

// TestProjectSettingsResourceCreateOmitsUnsetSettings is the core correctness
// test for this resource.
//
// A setting the configuration leaves out must not be sent. Were the attributes
// Optional+Computed, or were the payload built from ValueBool rather than from
// the null check, every unmentioned setting would arrive as false and switch off
// whatever the project had enabled.
func TestProjectSettingsResourceCreateOmitsUnsetSettings(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectSettingsAPI(t)

	plan := projectSettingsModel(testProjectSettingsSlug)
	plan.AutoCancelBuilds = types.BoolValue(true)

	state, resp := createProjectSettings(t, client, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %+v", resp.Diagnostics)
	}

	body := api.onlyPatch(t)
	assertPatchKeys(t, body, "autocancel_builds")

	if got := body["autocancel_builds"]; got != true {
		t.Errorf("autocancel_builds sent as %v, want true", got)
	}

	// oss is the one exception to the rule below: it is Computed-only because the
	// API rejects it on write, so it always reports what CircleCI holds and
	// adopting it can never turn into a write.
	if state.OSS.IsNull() || state.OSS.ValueBool() != false {
		t.Errorf("oss in state = %v, want false as the fake API reports it", state.OSS)
	}

	// The unmanaged settings must stay null in state too. Adopting the values the
	// API reports would make them indistinguishable from settings the
	// configuration asked for, and the next apply would start writing them.
	if !state.ForksReceiveSecretEnvVars.IsNull() {
		t.Errorf("forks_receive_secret_env_vars in state = %v, want null", state.ForksReceiveSecretEnvVars)
	}
	if !state.PROnlyBranchOverrides.IsNull() {
		t.Errorf("pr_only_branch_overrides in state = %v, want null", state.PROnlyBranchOverrides)
	}
	if state.AutoCancelBuilds.ValueBool() != true {
		t.Errorf("auto_cancel_builds in state = %v, want true", state.AutoCancelBuilds)
	}
}

// TestProjectSettingsResourceCreateSendsEveryConfiguredSetting is the other half
// of the same property: a setting that is configured must be sent, including the
// ones added on top of what circleci_project exposes.
func TestProjectSettingsResourceCreateSendsEveryConfiguredSetting(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectSettingsAPI(t)

	overrides, diags := types.SetValueFrom(t.Context(), types.StringType, []string{"main", "develop"})
	if diags.HasError() {
		t.Fatalf("could not build the branch override list: %+v", diags)
	}

	plan := projectSettingsResourceModel{
		Slug:                       types.StringValue(testProjectSettingsSlug),
		AutoCancelBuilds:           types.BoolValue(true),
		BuildForkPrs:               types.BoolValue(true),
		BuildPrsOnly:               types.BoolValue(true),
		DisableSSH:                 types.BoolValue(true),
		ForksReceiveSecretEnvVars:  types.BoolValue(false),
		SetGithubStatus:            types.BoolValue(true),
		SetupWorkflows:             types.BoolValue(true),
		WriteSettingsRequiresAdmin: types.BoolValue(true),
		PROnlyBranchOverrides:      overrides,
	}

	_, resp := createProjectSettings(t, client, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %+v", resp.Diagnostics)
	}

	body := api.onlyPatch(t)
	assertPatchKeys(t, body,
		"autocancel_builds",
		"build_fork_prs",
		"build_prs_only",
		"disable_ssh",
		"forks_receive_secret_env_vars",
		"set_github_status",
		"setup_workflows",
		"write_settings_requires_admin",
		"pr_only_branch_overrides",
	)

	// build_prs_only is the setting the circleci-sdk-go AdvanceSettings struct
	// cannot carry, so it is worth asserting on directly.
	if got := body["build_prs_only"]; got != true {
		t.Errorf("build_prs_only sent as %v, want true", got)
	}

	// A false value must survive the encoding: omitempty on a *bool omits only a
	// nil pointer, not a pointer to false.
	if got := body["forks_receive_secret_env_vars"]; got != false {
		t.Errorf("forks_receive_secret_env_vars sent as %v, want false", got)
	}

	branches, ok := body["pr_only_branch_overrides"].([]any)
	if !ok {
		t.Fatalf("pr_only_branch_overrides sent as %T, want a list", body["pr_only_branch_overrides"])
	}
	if len(branches) != 2 || branches[0] != "main" || branches[1] != "develop" {
		t.Errorf("pr_only_branch_overrides sent as %v, want [main develop]", branches)
	}
}

// TestProjectSettingsResourceCreateWithEmptyBranchOverrides used to document a
// defect live-verified against the real API: `pr_only_branch_overrides = []`
// reached apply, got a 200 back, and silently kept the branches already
// configured, so Terraform failed with "Provider produced inconsistent result
// after apply" — a message naming no attribute and no cause.
//
// That is now rejected at plan time by noClearingBranchOverridesValidator
// (project_settings_resource.go), before anything is sent, which is exactly the
// resolution this test's previous comment asked for. See
// TestProjectSettingsResourceUnit_RejectsEmptyBranchOverrides for the replacement
// assertion, and internal/circleci/project_settings.go's
// ErrCannotClearBranchOverrides for the client-level guard this backs up: that
// guard still exists as a defence in depth for any caller that reaches
// UpdateProjectSettings directly, bypassing the schema validator entirely.
func TestProjectSettingsResourceUnit_RejectsEmptyBranchOverrides(t *testing.T) {
	api, host := startFakeProjectSettingsAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: projectSettingsProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testProjectSettingsResourceConfig(host, `  pr_only_branch_overrides = []`),
				ExpectError: regexp.MustCompile(
					`(?s)Cannot clear pr_only_branch_overrides.*does not support clearing`,
				),
			},
		},
	})

	if requests := api.recordedRequests(); len(requests) != 0 {
		t.Errorf("got requests %v, want none: the configuration never passed validation", requests)
	}
}

// TestProjectSettingsResourceCreateWithNoSettings covers a resource that names
// only a project. There is nothing to write, so no request must be sent.
//
// Note that `{"advanced":{}}` is not expected to be *rejected* — the API answers
// 200 with the current settings. That is expected rather than observed. Skipping the
// request is an optimisation, and the assertion is on the requests made rather
// than on a status code, so it holds either way.
func TestProjectSettingsResourceCreateWithNoSettings(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectSettingsAPI(t)

	_, resp := createProjectSettings(t, client, projectSettingsModel(testProjectSettingsSlug))
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %+v", resp.Diagnostics)
	}

	if patches := api.recordedPatches(); len(patches) != 0 {
		t.Errorf("the provider sent %d PATCH requests for a resource with no settings, want 0: %v", len(patches), patches)
	}

	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("Create produced no warning for a resource that manages no settings")
	}
}

// TestProjectSettingsResourceCreateRejectsBadSlug checks the slug guard. The
// validator catches this at plan time, but Create must not build a request from
// a slug it cannot split either.
func TestProjectSettingsResourceCreateRejectsBadSlug(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectSettingsAPI(t)

	plan := projectSettingsModel("github/acme")
	plan.AutoCancelBuilds = types.BoolValue(true)

	_, resp := createProjectSettings(t, client, plan)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Create accepted a two-segment slug, want an error")
	}

	if requests := api.recordedRequests(); len(requests) != 0 {
		t.Errorf("the provider made %v, want no request at all for an invalid slug", requests)
	}
}

// TestProjectSettingsResourceCreateNeverSendsOSS is the regression test for the
// bug that broke every settings update against the real API.
//
// oss is read-only on v2: the PATCH answers 400 "Unexpected field 'advanced.oss'."
// and rejects the whole request, so a single unwritable field failed every write.
// The fake now behaves the same way, so a payload carrying oss fails this test
// twice over: on the key assertion and on the resulting error.
//
// oss is set here through the model directly, which a configuration can no longer
// do at all now that the attribute is Computed-only. That is the point: even a
// state or model value must not reach the wire.
func TestProjectSettingsResourceCreateNeverSendsOSS(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectSettingsAPI(t)

	plan := projectSettingsModel(testProjectSettingsSlug)
	plan.OSS = types.BoolValue(true)
	plan.AutoCancelBuilds = types.BoolValue(true)

	state, resp := createProjectSettings(t, client, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %+v", resp.Diagnostics)
	}

	body := api.onlyPatch(t)
	if _, ok := body["oss"]; ok {
		t.Errorf("PATCH body carried oss (%v); it is read-only and the API rejects the whole request for it", body["oss"])
	}
	assertPatchKeys(t, body, "autocancel_builds")

	// State reports what CircleCI holds, not what the model asked for.
	if state.OSS.ValueBool() != false {
		t.Errorf("oss in state = %v, want false as the API reports it", state.OSS)
	}
}

// TestProjectSettingsResourceCreateOSSOnlyWritesNothing covers the settings object
// that holds nothing but oss. Because oss is never marshalled, such an update
// would send an empty body and earn a 400 "No JSON fields found.", so
// ProjectSettings.IsEmpty must count it as empty and no request must be made.
func TestProjectSettingsResourceCreateOSSOnlyWritesNothing(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectSettingsAPI(t)

	plan := projectSettingsModel(testProjectSettingsSlug)
	plan.OSS = types.BoolValue(true)

	_, resp := createProjectSettings(t, client, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %+v", resp.Diagnostics)
	}

	if patches := api.recordedPatches(); len(patches) != 0 {
		t.Errorf("the provider sent %d PATCH requests for a resource whose only value is oss, want 0: %v", len(patches), patches)
	}
}

// TestProjectSettingsResourceReadKeepsUnmanagedSettingsNull checks that a
// refresh adopts drift for the managed settings only.
func TestProjectSettingsResourceReadKeepsUnmanagedSettingsNull(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectSettingsAPI(t)
	api.set("disable_ssh", true)
	api.set("setup_workflows", true)

	ctx := t.Context()
	schema := projectSettingsSchema(t)

	prior := projectSettingsModel(testProjectSettingsSlug)
	prior.DisableSSH = types.BoolValue(false)

	priorState := projectSettingsState(t, schema, prior)
	r := &projectSettingsResource{client: client}

	resp := &fwresource.ReadResponse{State: priorState}
	r.Read(ctx, fwresource.ReadRequest{State: priorState}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %+v", resp.Diagnostics)
	}

	var got projectSettingsResourceModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("could not read the state Read produced: %+v", diags)
	}

	// Managed, and changed behind Terraform's back: the drift must show up.
	if got.DisableSSH.ValueBool() != true {
		t.Errorf("disable_ssh in state = %v, want true so that the drift is planned away", got.DisableSSH)
	}

	// Unmanaged, and also true remotely: it must stay null.
	if !got.SetupWorkflows.IsNull() {
		t.Errorf("setup_workflows in state = %v, want null: it is not managed here", got.SetupWorkflows)
	}
}

// TestProjectSettingsResourceReadDropsMissingProject checks that a deleted
// project takes its settings resource out of state rather than failing forever.
func TestProjectSettingsResourceReadDropsMissingProject(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectSettingsAPI(t)
	api.notFound = true

	ctx := t.Context()
	schema := projectSettingsSchema(t)
	priorState := projectSettingsState(t, schema, projectSettingsModel(testProjectSettingsSlug))

	r := &projectSettingsResource{client: client}
	resp := &fwresource.ReadResponse{State: priorState}
	r.Read(ctx, fwresource.ReadRequest{State: priorState}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %+v", resp.Diagnostics)
	}

	if !resp.State.Raw.IsNull() {
		t.Error("Read left the resource in state though the project is gone")
	}
}

// TestProjectSettingsResourceUpdateOmitsUnsetSettings repeats the core property
// for the update path, and checks that dropping a setting from the configuration
// warns rather than silently abandoning it.
func TestProjectSettingsResourceUpdateOmitsUnsetSettings(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectSettingsAPI(t)

	ctx := t.Context()
	schema := projectSettingsSchema(t)

	// Previously managed: two settings.
	prior := projectSettingsModel(testProjectSettingsSlug)
	prior.AutoCancelBuilds = types.BoolValue(true)
	prior.DisableSSH = types.BoolValue(false)

	// Now managed: only one of them, with a new value.
	plan := projectSettingsModel(testProjectSettingsSlug)
	plan.AutoCancelBuilds = types.BoolValue(false)

	priorState := projectSettingsState(t, schema, prior)
	planState := projectSettingsState(t, schema, plan)

	r := &projectSettingsResource{client: client}
	resp := &fwresource.UpdateResponse{State: priorState}
	r.Update(ctx, fwresource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schema, Raw: planState.Raw},
		State: priorState,
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Update diagnostics: %+v", resp.Diagnostics)
	}

	body := api.onlyPatch(t)
	assertPatchKeys(t, body, "autocancel_builds")

	if got := body["autocancel_builds"]; got != false {
		t.Errorf("autocancel_builds sent as %v, want false", got)
	}

	warnings := resp.Diagnostics.Warnings()
	if len(warnings) == 0 {
		t.Fatal("Update produced no warning for the setting dropped from the configuration")
	}
	if detail := warnings[0].Detail(); !strings.Contains(detail, "disable_ssh") {
		t.Errorf("warning detail = %q, want it to name disable_ssh", detail)
	}
}

// TestProjectSettingsResourceDeleteMakesNoRequest documents the deliberate no-op
// Delete: CircleCI cannot delete or reset a project's settings, so destroying
// this resource only stops tracking them.
func TestProjectSettingsResourceDeleteMakesNoRequest(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectSettingsAPI(t)

	r := &projectSettingsResource{client: client}
	resp := &fwresource.DeleteResponse{}
	r.Delete(t.Context(), fwresource.DeleteRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete diagnostics: %+v", resp.Diagnostics)
	}

	if requests := api.recordedRequests(); len(requests) != 0 {
		t.Errorf("Delete made %v, want no request: settings cannot be deleted", requests)
	}

	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("Delete produced no warning that the settings were left in place")
	}
}

// TestProjectSettingsResourceImportState covers the import id, which is the
// project slug.
func TestProjectSettingsResourceImportState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "slug", id: "github/acme/repo"},
		{name: "circleci org slug", id: "circleci/abc-123/repo"},
		{name: "two segments", id: "github/acme", wantErr: true},
		{name: "four segments", id: "github/acme/repo/extra", wantErr: true},
		{name: "empty", id: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			schema := projectSettingsSchema(t)
			r := &projectSettingsResource{}

			resp := &fwresource.ImportStateResponse{
				State: projectSettingsState(t, schema, projectSettingsModel("")),
			}
			r.ImportState(ctx, fwresource.ImportStateRequest{ID: tt.id}, resp)

			if tt.wantErr {
				if !resp.Diagnostics.HasError() {
					t.Fatalf("ImportState(%q) reported no error, want one", tt.id)
				}

				return
			}

			if resp.Diagnostics.HasError() {
				t.Fatalf("ImportState(%q) diagnostics: %+v", tt.id, resp.Diagnostics)
			}

			var got projectSettingsResourceModel
			if diags := resp.State.Get(ctx, &got); diags.HasError() {
				t.Fatalf("could not read the imported state: %+v", diags)
			}

			if got.Slug.ValueString() != tt.id {
				t.Errorf("imported slug = %q, want %q", got.Slug.ValueString(), tt.id)
			}

			// The settings themselves stay null so that the first plan after an
			// import shows exactly what the configuration asks to manage.
			if !got.AutoCancelBuilds.IsNull() {
				t.Errorf("auto_cancel_builds after import = %v, want null", got.AutoCancelBuilds)
			}
		})
	}
}

func TestProjectSettingsResourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	schema := projectSettingsSchema(t)

	if diags := schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("Schema validation diagnostics: %+v", diags)
	}

	// Optional+Computed is the mistake this resource exists to avoid, so it is
	// worth asserting on rather than only describing in a comment.
	//
	// oss is the deliberate exception, and it is the opposite mistake that matters
	// there: it must never be Optional, because the API rejects the field on write
	// and rejects the whole request with it.
	oss, ok := schema.Attributes["oss"]
	if !ok {
		t.Fatal("the schema has no oss attribute")
	}
	if oss.IsOptional() {
		t.Error("oss is Optional; it is read-only on the API, which answers " +
			"400 \"Unexpected field 'advanced.oss'.\" and rejects the whole request when it is sent")
	}
	if !oss.IsComputed() {
		t.Error("oss is not Computed; it is read from the API and must be reported in state")
	}

	for name, attribute := range schema.Attributes {
		if name == "slug" || name == "oss" {
			continue
		}
		if attribute.IsComputed() {
			t.Errorf("attribute %q is Computed; settings must be Optional-only so that an "+
				"unmanaged setting stays null in state", name)
		}
		if !attribute.IsOptional() {
			t.Errorf("attribute %q is not Optional", name)
		}
	}
}

// projectSettingsTestProvider registers circleci_project_settings for the tests
// below.
//
// The resource IS registered in provider.go — Resources there lists
// NewProjectSettingsResource — so this wrapper appends nothing today. It stays
// because it costs one type and makes these tests independent of that
// registration: the loop finds the existing factory and returns the list
// unchanged, so the resource can never be declared twice.
type projectSettingsTestProvider struct {
	fwprovider.Provider
}

func (p projectSettingsTestProvider) Resources(ctx context.Context) []func() fwresource.Resource {
	factories := p.Provider.Resources(ctx)
	for _, factory := range factories {
		if _, ok := factory().(*projectSettingsResource); ok {
			return factories
		}
	}

	return append(factories, NewProjectSettingsResource)
}

// projectSettingsProviderFactories serves the provider with
// circleci_project_settings registered.
var projectSettingsProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"circleci": providerserver.NewProtocol6WithError(projectSettingsTestProvider{New("test")()}),
}

func testProjectSettingsResourceConfig(host, settings string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %[1]q
  key  = "fake"
}

resource "circleci_project_settings" "test" {
  slug = %[2]q
%[3]s
}
`, host, testProjectSettingsSlug, settings)
}

// TestProjectSettingsResourceRejectsConfiguredOSS proves oss cannot be set from a
// configuration at all now that it is Computed-only. Terraform itself refuses,
// before the provider is asked to do anything.
func TestProjectSettingsResourceRejectsConfiguredOSS(t *testing.T) {
	_, host := startFakeProjectSettingsAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: projectSettingsProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testProjectSettingsResourceConfig(host, `  oss = true`),
				ExpectError: regexp.MustCompile(`(?s)Invalid Configuration for Read-Only Attribute.*oss`),
			},
		},
	})
}

// TestProjectSettingsResourceRequiresExplicitForkSecrets covers the one dangerous
// default that omitting unset settings exposes.
//
// forks_receive_secret_env_vars is true on a private project when it was never
// set, so a configuration that turns fork builds on without mentioning it would
// hand the project's secrets to anyone who can open a pull request. CircleCI gates
// that exposure on both settings, so the validator fires for exactly that pair —
// and at validate time, before an apply can write anything.
func TestProjectSettingsResourceRequiresExplicitForkSecrets(t *testing.T) {
	_, host := startFakeProjectSettingsAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: projectSettingsProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testProjectSettingsResourceConfig(host, `  build_fork_prs = true`),
				ExpectError: regexp.MustCompile(`(?s)forks_receive_secret_env_vars.*would receive the project's secrets`),
			},
		},
	})
}

// TestProjectSettingsResourceExplicitForkSecretsIsAccepted is the other half: the
// same configuration with the setting named is fine, whichever value it names, and
// nothing warns about the pair once it is explicit.
func TestProjectSettingsResourceExplicitForkSecretsIsAccepted(t *testing.T) {
	api, host := startFakeProjectSettingsAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: projectSettingsProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testProjectSettingsResourceConfig(host, `  build_fork_prs                = true
  forks_receive_secret_env_vars = false`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project_settings.test",
						tfjsonpath.New("forks_receive_secret_env_vars"),
						knownvalue.Bool(false),
					),
				},
			},
		},
	})

	body := api.onlyPatch(t)
	assertPatchKeys(t, body, "build_fork_prs", "forks_receive_secret_env_vars")

	if got := body["forks_receive_secret_env_vars"]; got != false {
		t.Errorf("forks_receive_secret_env_vars sent as %v, want false", got)
	}
}

// TestAccProjectSettingsResource exercises the resource through Terraform
// itself, against the fake API.
//
// Running it end to end is what proves the Optional-only design produces a
// stable plan: Terraform fails the step if the state a settings write leaves
// behind does not match the plan, or if a follow-up plan is not empty.
func TestAccProjectSettingsResource(t *testing.T) {
	api, host := startFakeProjectSettingsAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: projectSettingsProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testProjectSettingsResourceConfig(host, `  auto_cancel_builds = true`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project_settings.test",
						tfjsonpath.New("auto_cancel_builds"),
						knownvalue.Bool(true),
					),
					// Never configured, so never adopted.
					statecheck.ExpectKnownValue(
						"circleci_project_settings.test",
						tfjsonpath.New("forks_receive_secret_env_vars"),
						knownvalue.Null(),
					),
					// oss is the exception: Computed-only, so it reports what
					// CircleCI holds. It is safe to adopt because it can never be
					// written back.
					statecheck.ExpectKnownValue(
						"circleci_project_settings.test",
						tfjsonpath.New("oss"),
						knownvalue.Bool(false),
					),
				},
			},
			{
				// Two branches, not one: the fake answers with them in a different
				// order (see reorderedLikeTheAPI in webhook_resource_fake_test.go), the
				// way the real API does, so this step is what proves
				// pr_only_branch_overrides being a Set rather than a List. With a single
				// branch there is no order to get wrong and the step passes either way.
				Config: testProjectSettingsResourceConfig(host, `  auto_cancel_builds       = true
  build_prs_only           = true
  pr_only_branch_overrides = ["main", "develop"]`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project_settings.test",
						tfjsonpath.New("build_prs_only"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"circleci_project_settings.test",
						tfjsonpath.New("pr_only_branch_overrides"),
						knownvalue.SetExact([]knownvalue.Check{
							knownvalue.StringExact("main"),
							knownvalue.StringExact("develop"),
						}),
					),
				},
			},
			{
				// The identical configuration, replanned: the plan must be empty.
				//
				// This is the shape of test the permanent diff needed and did not have.
				// While pr_only_branch_overrides was a List, Terraform compared the
				// configured order against the order CircleCI answered with and planned a
				// change on every run, for ever, with nothing to apply.
				Config: testProjectSettingsResourceConfig(host, `  auto_cancel_builds       = true
  build_prs_only           = true
  pr_only_branch_overrides = ["main", "develop"]`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			{
				ResourceName:  "circleci_project_settings.test",
				ImportState:   true,
				ImportStateId: testProjectSettingsSlug,
				// The import deliberately leaves the settings null, so the
				// imported state cannot match the state built by the step above.
				ImportStateVerify: false,
			},
		},
	})

	// Whatever Terraform did, it must never have written a setting the
	// configuration did not name.
	for _, body := range api.recordedPatches() {
		for key := range body {
			switch key {
			case "autocancel_builds", "build_prs_only", "pr_only_branch_overrides":
			default:
				t.Errorf("the provider wrote %q, which no configuration in this test set", key)
			}
		}
	}
}

// TestProjectSettingsResourceUnit_ImportRoundTrips is the genuine round-trip
// proof that TestAccProjectSettingsResource's own import step disclaims:
// that step imports into a state built by a config managing three settings, so
// it can never match (import always leaves every setting null) and disables
// ImportStateVerify rather than hide that mismatch behind an ignore list.
//
// The honest round trip this resource supports is narrower: a configuration
// that manages *nothing* (only `slug`) is exactly what `terraform
// plan -generate-config-out` would produce from an import, because every
// setting attribute is null and Optional-only, so a generated config omits
// them. Applying that trivial config, importing the same slug into a fresh
// state, and replanning the same trivial config must all agree — which is
// what this test proves, with no ImportStateVerifyIgnore at all.
func TestProjectSettingsResourceUnit_ImportRoundTrips(t *testing.T) {
	_, host := startFakeProjectSettingsAPI(t)

	trivialConfig := testProjectSettingsResourceConfig(host, "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: projectSettingsProviderFactories,
		Steps: []resource.TestStep{
			{
				// Manages no setting at all: Create only tracks the slug.
				Config: trivialConfig,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project_settings.test",
						tfjsonpath.New("auto_cancel_builds"),
						knownvalue.Null(),
					),
				},
			},
			{
				// A fresh state, imported by slug alone — no ignores, because a
				// config this trivial is exactly what import itself produces.
				//
				// This resource has no "id" attribute (slug is what identifies it,
				// hence the RequiresReplace on slug rather than on some separate
				// id), so the identifier attribute must be named explicitly.
				ResourceName:                         "circleci_project_settings.test",
				ImportState:                          true,
				ImportStateId:                        testProjectSettingsSlug,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "slug",
			},
			{
				// The same trivial config, replanned against the freshly imported
				// state: the plan must be empty, which is the whole point of a
				// round trip.
				Config:             trivialConfig,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// testProjectSettingsSlugFor builds a resource config for an arbitrary slug, so
// the standalone-organization tests below can use a "circleci/…" slug while every
// other test here keeps using testProjectSettingsSlug.
func testProjectSettingsResourceConfigForSlug(host, slug, settings string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %[1]q
  key  = "fake"
}

resource "circleci_project_settings" "test" {
  slug = %[2]q
%[3]s
}
`, host, slug, settings)
}

// testStandaloneProjectSettingsSlug is a project in a standalone
// (CircleCI-native) organization. Both segments are the 22-character identifiers
// the real API uses for such a project — the settings route rejects organization
// and project *names* there ("Invalid project slug", measured), so a realistic
// slug matters for anything that parses one.
const testStandaloneProjectSettingsSlug = "circleci/T4ByWwp8uucrumRKY8wEmp/B1bv3rjTroryDXCWBp32Y6"

// TestProjectSettingsResourceRejectsEnablingForkSecretsOnStandalone is the
// plan-time half of the guard on the route's second unwritable field.
//
// Measured over the network, not through a fake: on a project whose slug's VCS
// segment is "circleci",
//
//	PATCH /api/v2/project/circleci/<org>/<project>/settings
//	      {"advanced":{"forks_receive_secret_env_vars":true}}
//	→ 403  {"message":"Permission denied."}
//
// on all three standalone organizations probed — GitHub App backed, GitLab
// backed, and a repo-less organization created by the token making the request —
// and it is refused even when the setting is already true. The identical body
// against a classic organization answers 200.
//
// The step must fail during PLAN, before any request. The route applies every
// other field in the body before answering 403, so an apply that reaches the wire
// changes the project and then reports failure, and a failed Create writes no
// state — the changes are left behind untracked.
func TestProjectSettingsResourceRejectsEnablingForkSecretsOnStandalone(t *testing.T) {
	api, host := startFakeProjectSettingsAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: projectSettingsProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testProjectSettingsResourceConfigForSlug(host, testStandaloneProjectSettingsSlug,
					`  forks_receive_secret_env_vars = true`),
				// PlanOnly is the assertion, not a shortcut: with the config
				// validator removed, circleci.UpdateProjectSettings still refuses the
				// write, so an apply-shaped step would still error and pass. Only a
				// plan-only step distinguishes "rejected before Terraform commits to
				// anything" from "rejected halfway through an apply".
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)forks_receive_secret_env_vars.*standalone organization`),
			},
		},
	})

	if requests := api.recordedRequests(); len(requests) != 0 {
		t.Errorf("the provider made %d requests: %v. The configuration must be rejected at plan time, "+
			"because this route writes the other settings in the same body before answering 403",
			len(requests), requests)
	}
}

// TestProjectSettingsResourceAllowsDisablingForkSecretsOnStandalone is the other
// half: false is the value that MATTERS on a standalone organization, because the
// project default is true and turning the setting off is the security-relevant
// direction. A guard keyed on the field rather than on the value would make it
// unmanageable exactly where it is needed.
func TestProjectSettingsResourceAllowsDisablingForkSecretsOnStandalone(t *testing.T) {
	api, host := startFakeProjectSettingsAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: projectSettingsProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testProjectSettingsResourceConfigForSlug(host, testStandaloneProjectSettingsSlug,
					`  forks_receive_secret_env_vars = false`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project_settings.test",
						tfjsonpath.New("forks_receive_secret_env_vars"),
						knownvalue.Bool(false),
					),
				},
			},
		},
	})

	body := api.onlyPatch(t)
	assertPatchKeys(t, body, "forks_receive_secret_env_vars")

	if got := body["forks_receive_secret_env_vars"]; got != false {
		t.Errorf("forks_receive_secret_env_vars sent as %v, want false", got)
	}
}

// TestProjectSettingsResourceAllowsEnablingForkSecretsOnClassicOrganization keeps
// the plan-time guard from spreading to the organizations where the write works:
// measured over the network, "gh/…" and "github/…" both answer 200 for the body
// that "circleci/…" refuses.
func TestProjectSettingsResourceAllowsEnablingForkSecretsOnClassicOrganization(t *testing.T) {
	api, host := startFakeProjectSettingsAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: projectSettingsProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testProjectSettingsResourceConfigForSlug(host, testProjectSettingsSlug,
					`  forks_receive_secret_env_vars = true`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project_settings.test",
						tfjsonpath.New("forks_receive_secret_env_vars"),
						knownvalue.Bool(true),
					),
				},
			},
		},
	})

	body := api.onlyPatch(t)
	assertPatchKeys(t, body, "forks_receive_secret_env_vars")

	if got := body["forks_receive_secret_env_vars"]; got != true {
		t.Errorf("forks_receive_secret_env_vars sent as %v, want true", got)
	}
}

// TestIsStandaloneProjectSlug pins the one string comparison the plan-time guard
// turns on, including the case sensitivity the route itself has: measured,
// "circleci/…" answers 200 and "CircleCI/…" answers 404 "Project not found.", so
// a mis-cased slug is a not-found project rather than a standalone one.
func TestIsStandaloneProjectSlug(t *testing.T) {
	t.Parallel()

	cases := []struct {
		slug string
		want bool
	}{
		{testStandaloneProjectSettingsSlug, true},
		{"circleci/org/project", true},
		{"github/acme/repo", false},
		{"gh/acme/repo", false},
		{"bitbucket/acme/repo", false},
		// Case-sensitive on this route, so not standalone as far as the guard goes.
		{"CircleCI/org/project", false},
		// A repository that merely starts with the word.
		{"github/acme/circleci", false},
		{"", false},
		{"circleci", false},
	}

	for _, c := range cases {
		if got := isStandaloneProjectSlug(c.slug); got != c.want {
			t.Errorf("isStandaloneProjectSlug(%q) = %v, want %v", c.slug, got, c.want)
		}
	}
}

// TestAccProjectSettingsResourceLive drives circleci_project_settings against a
// real installation, on a project this test creates and destroys.
//
// Every other end-to-end test of this resource runs against the fake in this
// file, which means nothing here had ever confirmed the resource can write what
// its schema claims. This does, for every setting the API accepts, including the
// two the fake-backed tests avoid:
//
//   - write_settings_requires_admin, which is writable on all four organization
//     classes (measured) but is dangerous to flip on a shared fixture: a run that
//     died between setting it and putting it back could leave the project needing
//     organization-administrator rights that the restore itself would then be
//     refused. On a project this test owns and deletes, there is nothing to strand.
//   - forks_receive_secret_env_vars set to FALSE, which is a one-way change on a
//     standalone organization — measured, enabling it again answers 403. Again
//     safe only because the project is discarded.
//
// A self-created project also means a known starting state — the defaults
// TestAccProjectSettingsDefaults pins — so "the API accepted this and changed
// nothing" is distinguishable from "it was already that value". A setting the
// route accepted with 200 and silently ignored would fail the state check on the
// step that wrote it, and again on the plan-only step that follows.
//
// It runs on a standalone organization because that is the only class where a
// project can be created at all: the same create against a classic (gh/<org>)
// organization answers 404 "GitHub response: Not Found", since there it only
// adopts an existing repository. The four-class coverage comes from
// TestAccProjectSettingsDataSource, which drives this same resource against the
// per-integration fixture projects.
func TestAccProjectSettingsResourceLive(t *testing.T) {
	testAccPreCheck(t)

	client := testAccProjectSettingsClient(t)
	ctx := context.Background()

	org, err := client.CreateOrganization(ctx, circleci.OrganizationInput{
		Name:    "tf-acc-settings-" + strings.ToLower(rand.Text()[:10]),
		VCSType: circleci.OrganizationVCSTypeStandalone,
	})
	if err != nil {
		t.Fatalf("could not create a standalone organization to own the test project: %v", err)
	}

	t.Cleanup(func() {
		if err := client.DeleteOrganization(ctx, org.ID); err != nil {
			t.Errorf("could not delete the organization %s (%s) this test created: %v", org.Name, org.ID, err)
		}
	})

	project, err := client.CreateProject(ctx, org.ID, "tf-acc-settings")
	if err != nil {
		t.Fatalf("could not create a project in %s: %v", org.Slug, err)
	}

	// Everything the resource claims it can write, all at once and all away from
	// the default. build_fork_prs and forks_receive_secret_env_vars are named
	// together because explicitForkSecretsValidator requires it, and false is the
	// only value forks_receive_secret_env_vars can take here.
	const managed = `
  auto_cancel_builds            = true
  build_fork_prs                = true
  build_prs_only                = true
  disable_ssh                   = true
  forks_receive_secret_env_vars = false
  set_github_status             = false
  setup_workflows               = false
  write_settings_requires_admin = true
  pr_only_branch_overrides      = ["main", "release", "tf-acc-live"]`

	config := testAccProjectSettingsLiveConfig(project.Slug, managed)
	const target = "circleci_project_settings.live"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(target, tfjsonpath.New("auto_cancel_builds"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(target, tfjsonpath.New("build_fork_prs"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(target, tfjsonpath.New("build_prs_only"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(target, tfjsonpath.New("disable_ssh"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(target, tfjsonpath.New("forks_receive_secret_env_vars"), knownvalue.Bool(false)),
					// Both of these start out TRUE on a fresh project, so a route that
					// ignored the write would leave them true and fail here. That is the
					// same pair the data source acceptance test used to assert backwards.
					statecheck.ExpectKnownValue(target, tfjsonpath.New("set_github_status"), knownvalue.Bool(false)),
					statecheck.ExpectKnownValue(target, tfjsonpath.New("setup_workflows"), knownvalue.Bool(false)),
					statecheck.ExpectKnownValue(target, tfjsonpath.New("write_settings_requires_admin"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(target, tfjsonpath.New("pr_only_branch_overrides"),
						knownvalue.SetExact([]knownvalue.Check{
							knownvalue.StringExact("main"),
							knownvalue.StringExact("release"),
							knownvalue.StringExact("tf-acc-live"),
						})),
					// Read-only, and the API reports it whatever the configuration says.
					statecheck.ExpectKnownValue(target, tfjsonpath.New("oss"), knownvalue.NotNull()),
				},
			},
			{
				// Refreshed and replanned. A setting accepted with 200 and then
				// ignored shows up here as a plan that never empties: the refresh
				// reports the old value and the configuration asks for the new one.
				// Three of these are order-sensitive lists on the wire, which is the
				// other thing this step catches.
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			{
				// Written back to the defaults, which proves the writes go both ways
				// rather than only away from the default. write_settings_requires_admin
				// going back to false is the one that would fail if the token had been
				// locked out by the step above.
				Config: testAccProjectSettingsLiveConfig(project.Slug, `
  auto_cancel_builds            = false
  build_fork_prs                = false
  forks_receive_secret_env_vars = false
  build_prs_only                = false
  disable_ssh                   = false
  set_github_status             = true
  setup_workflows               = true
  write_settings_requires_admin = false
  pr_only_branch_overrides      = ["main"]`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(target, tfjsonpath.New("set_github_status"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(target, tfjsonpath.New("setup_workflows"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(target, tfjsonpath.New("write_settings_requires_admin"), knownvalue.Bool(false)),
					statecheck.ExpectKnownValue(target, tfjsonpath.New("pr_only_branch_overrides"),
						knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("main")})),
				},
			},
		},
	})
}

// TestAccProjectSettingsResourceCannotEnableForkSecretsLive is the live proof of
// the one thing the plan-time guard asserts about the API.
//
// It creates a standalone organization's project — where the default is already
// true — and asks for true. If CircleCI ever starts accepting that write, this is
// the test that says so, and the guard in
// noEnablingForkSecretsOnStandaloneValidator should then be removed. Until then
// the configuration must be rejected during PLAN, because the route applies the
// rest of the body before answering 403.
func TestAccProjectSettingsResourceCannotEnableForkSecretsLive(t *testing.T) {
	testAccPreCheck(t)

	client := testAccProjectSettingsClient(t)
	ctx := context.Background()

	org, err := client.CreateOrganization(ctx, circleci.OrganizationInput{
		Name:    "tf-acc-forksec-" + strings.ToLower(rand.Text()[:10]),
		VCSType: circleci.OrganizationVCSTypeStandalone,
	})
	if err != nil {
		t.Fatalf("could not create a standalone organization: %v", err)
	}

	t.Cleanup(func() {
		if err := client.DeleteOrganization(ctx, org.ID); err != nil {
			t.Errorf("could not delete the organization %s (%s) this test created: %v", org.Name, org.ID, err)
		}
	})

	project, err := client.CreateProject(ctx, org.ID, "tf-acc-forksec")
	if err != nil {
		t.Fatalf("could not create a project in %s: %v", org.Slug, err)
	}

	// The API's side of the same claim, made directly so that a failure here
	// distinguishes "the provider guard is wrong" from "the API changed".
	enabled := true

	vcsType, orgSegment, projectSegment, ok := splitProjectSlugForTest(project.Slug)
	if !ok {
		t.Fatalf("the created project's slug %q is not vcs-type/org/project", project.Slug)
	}

	if _, err := client.UpdateProjectSettings(ctx, vcsType, orgSegment, projectSegment,
		circleci.ProjectSettings{ForksReceiveSecretEnvVars: &enabled}); err == nil {
		t.Error("enabling forks_receive_secret_env_vars on a standalone organization's project " +
			"succeeded. The API used to answer 403 \"Permission denied.\" for this, even when the " +
			"setting was already true; if that has changed, remove the pre-flight guard in " +
			"circleci.UpdateProjectSettings and noEnablingForkSecretsOnStandaloneValidator.")
	} else if !errors.Is(err, circleci.ErrCannotEnableForkSecrets) {
		t.Errorf("error = %v, want ErrCannotEnableForkSecrets from the pre-flight guard", err)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectSettingsLiveConfig(project.Slug,
					`  forks_receive_secret_env_vars = true`),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)forks_receive_secret_env_vars.*standalone organization`),
			},
		},
	})
}

// testAccProjectSettingsLiveConfig builds a configuration against a real
// installation: no host and no key, so the provider resolves both the way it does
// in production.
func testAccProjectSettingsLiveConfig(slug, settings string) string {
	return fmt.Sprintf(`
resource "circleci_project_settings" "live" {
  slug = %[1]q
%[2]s
}
`, slug, settings)
}
