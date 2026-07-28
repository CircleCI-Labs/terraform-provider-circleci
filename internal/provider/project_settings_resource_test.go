// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	// refuseOSS models CircleCI's behaviour for a repository that is not open
	// source: the request succeeds and the setting is silently left alone.
	refuseOSS bool
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
		// The defaults a freshly followed project has.
		current: map[string]any{
			"autocancel_builds":             false,
			"build_fork_prs":                false,
			"build_prs_only":                false,
			"disable_ssh":                   false,
			"forks_receive_secret_env_vars": true,
			"oss":                           false,
			"set_github_status":             false,
			"setup_workflows":               false,
			"write_settings_requires_admin": false,
			"pr_only_branch_overrides":      []any{},
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
		f.patch(w, r)
	default:
		f.write(w, http.StatusMethodNotAllowed, map[string]string{"message": "Method Not Allowed"})
	}
}

// patch merges the request body into the held settings, the way the real API
// applies a partial update, and answers with the full settings.
func (f *fakeProjectSettingsAPI) patch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Advanced map[string]any `json:"advanced"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.write(w, http.StatusBadRequest, map[string]string{"message": "Invalid JSON body."})

		return
	}

	if len(body.Advanced) == 0 {
		// The real API rejects an update that carries no fields, so a provider
		// that sends one must fail the test rather than quietly succeed.
		f.write(w, http.StatusBadRequest, map[string]string{"message": "No JSON fields found."})

		return
	}

	f.patches = append(f.patches, body.Advanced)

	for key, value := range body.Advanced {
		if key == "oss" && f.refuseOSS {
			continue
		}
		f.current[key] = value
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
		PROnlyBranchOverrides: types.ListNull(types.StringType),
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

	// The unmanaged settings must stay null in state too. Adopting the values the
	// API reports would make them indistinguishable from settings the
	// configuration asked for, and the next apply would start writing them.
	if !state.OSS.IsNull() {
		t.Errorf("oss in state = %v, want null: it was never configured", state.OSS)
	}
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

	overrides, diags := types.ListValueFrom(t.Context(), types.StringType, []string{"main", "develop"})
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
		OSS:                        types.BoolValue(false),
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
		"oss",
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
	if got := body["oss"]; got != false {
		t.Errorf("oss sent as %v, want false", got)
	}

	branches, ok := body["pr_only_branch_overrides"].([]any)
	if !ok {
		t.Fatalf("pr_only_branch_overrides sent as %T, want a list", body["pr_only_branch_overrides"])
	}
	if len(branches) != 2 || branches[0] != "main" || branches[1] != "develop" {
		t.Errorf("pr_only_branch_overrides sent as %v, want [main develop]", branches)
	}
}

// TestProjectSettingsResourceCreateWithEmptyBranchOverrides checks that an
// explicitly empty list is still sent. Sending [] is the only way to clear the
// overrides a project already has, so it must not be treated as "unset".
func TestProjectSettingsResourceCreateWithEmptyBranchOverrides(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectSettingsAPI(t)
	api.set("pr_only_branch_overrides", []any{"main"})

	overrides, diags := types.ListValueFrom(t.Context(), types.StringType, []string{})
	if diags.HasError() {
		t.Fatalf("could not build the branch override list: %+v", diags)
	}

	plan := projectSettingsModel(testProjectSettingsSlug)
	plan.PROnlyBranchOverrides = overrides

	state, resp := createProjectSettings(t, client, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %+v", resp.Diagnostics)
	}

	body := api.onlyPatch(t)
	assertPatchKeys(t, body, "pr_only_branch_overrides")

	branches, ok := body["pr_only_branch_overrides"].([]any)
	if !ok {
		t.Fatalf("pr_only_branch_overrides sent as %T, want a list", body["pr_only_branch_overrides"])
	}
	if len(branches) != 0 {
		t.Errorf("pr_only_branch_overrides sent as %v, want an empty list", branches)
	}

	if elements := state.PROnlyBranchOverrides.Elements(); len(elements) != 0 {
		t.Errorf("pr_only_branch_overrides in state = %v, want empty", elements)
	}
}

// TestProjectSettingsResourceCreateWithNoSettings covers a resource that names
// only a project. There is nothing to write, and the API rejects a body with no
// fields, so no request must be sent.
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

// TestProjectSettingsResourceCreateFailsWhenOSSIsRefused covers CircleCI's
// silent refusal of oss for a repository that is not open source. The update
// succeeds, so the only signal is the response, and without this check the
// practitioner would see a diff that never converges.
func TestProjectSettingsResourceCreateFailsWhenOSSIsRefused(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectSettingsAPI(t)
	api.refuseOSS = true

	plan := projectSettingsModel(testProjectSettingsSlug)
	plan.OSS = types.BoolValue(true)

	_, resp := createProjectSettings(t, client, plan)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Create reported success though CircleCI did not apply oss")
	}

	if detail := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(detail, "open source") {
		t.Errorf("error detail = %q, want it to explain the open source requirement", detail)
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
	prior.OSS = types.BoolValue(false)

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
	if detail := warnings[0].Detail(); !strings.Contains(detail, "oss") {
		t.Errorf("warning detail = %q, want it to name oss", detail)
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
	for name, attribute := range schema.Attributes {
		if name == "slug" {
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
// The resource is not registered in provider.go yet, and this wrapper keeps the
// end-to-end tests working either way: once it is registered, the factory list
// already contains it and nothing is appended, so the resource is never declared
// twice.
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

// TestAccProjectSettingsResource exercises the resource through Terraform
// itself, against the fake API.
//
// Running it end to end is what proves the Optional-only design produces a
// stable plan: Terraform fails the step if the state a settings write leaves
// behind does not match the plan, or if a follow-up plan is not empty.
func TestAccProjectSettingsResource(t *testing.T) {
	api, host := startFakeProjectSettingsAPI(t)

	resource.Test(t, resource.TestCase{
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
					statecheck.ExpectKnownValue(
						"circleci_project_settings.test",
						tfjsonpath.New("oss"),
						knownvalue.Null(),
					),
				},
			},
			{
				Config: testProjectSettingsResourceConfig(host, `  auto_cancel_builds       = true
  build_prs_only           = true
  pr_only_branch_overrides = ["main"]`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project_settings.test",
						tfjsonpath.New("build_prs_only"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"circleci_project_settings.test",
						tfjsonpath.New("pr_only_branch_overrides"),
						knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("main")}),
					),
				},
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
