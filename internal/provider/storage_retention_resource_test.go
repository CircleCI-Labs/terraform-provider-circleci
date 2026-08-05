// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	sdkresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"terraform-provider-circleci/internal/circleci"
)

// Why this file cannot drive Create/Read/Update through the ordinary
// resource.UnitTest + fake-httptest-server harness used everywhere else in
// this package (see e.g. organization_contacts_resource_test.go):
//
// Every other private-route resource in this provider is reachable by
// pointing the `host` provider attribute at a fake server, because those
// routes share Client.Host with the ordinary v2/v3 API (see the comment on
// organizationContactsRoute). circleci_storage_retention does not: it calls
// GetPrivate/PutPrivate, which always dial Client.PrivateHost, a value with
// deliberately no provider attribute — see internal/circleci/private.go. So
// there is no way to point a `terraform` configuration at a fake for this
// resource's CRUD without either adding that attribute (explicitly out of
// scope) or letting a test dial the real private origin (unacceptable: that
// would be a real, credentialed request to CircleCI from every `go test`
// run).
//
// The tests below split along exactly that line:
//   - Cloud-gating tests use the real resource.UnitTest harness, because the
//     gate fails inside ModifyPlan/Create before any client method is ever
//     called — no network is reachable either way, gated or not, so it is
//     safe to prove that with the ordinary harness.
//   - Everything that needs a real Create/Read/Update round trip constructs
//     *storageRetentionResource directly with circleci.New(circleci.Config{
//     PrivateHost: fake.URL}) and calls its methods directly, the same
//     approach TestCloudOnlyModifyPlanAllowsDestroy and
//     project_settings_resource_test.go's createProjectSettings already use
//     for cases the standard harness cannot reach.

const testStorageRetentionOrgID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

// fakeStorageRetentionAPI is a stateful stand-in for the private
// storage-retention-controls route, deliberately shaped like the one in
// internal/circleci/storage_retention_test.go: it clamps instead of rejecting,
// and answers a PUT with 204 and no body.
type fakeStorageRetentionAPI struct {
	server *httptest.Server

	mu      sync.Mutex
	limits  circleci.StorageRetentionLimits
	current circleci.StorageRetentionControls
	gets    int
	puts    []map[string]any
}

func newFakeStorageRetentionAPI(
	t *testing.T, limits circleci.StorageRetentionLimits, initial circleci.StorageRetentionControls,
) *fakeStorageRetentionAPI {
	t.Helper()

	api := &fakeStorageRetentionAPI{limits: limits, current: initial}
	api.server = httptest.NewServer(http.HandlerFunc(api.handle))
	t.Cleanup(api.server.Close)

	return api
}

func (a *fakeStorageRetentionAPI) handle(w http.ResponseWriter, r *http.Request) {
	wantPath := "/private/orgs/" + testStorageRetentionOrgID + "/storage-retention-controls"
	if r.URL.Path != wantPath {
		http.NotFound(w, r)

		return
	}

	switch r.Method {
	case http.MethodGet:
		a.mu.Lock()
		defer a.mu.Unlock()

		a.gets++

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(circleci.StorageRetention{Controls: a.current, Limits: a.limits})

	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)

			return
		}

		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		var requested circleci.StorageRetentionControls
		if err := json.Unmarshal(body, &requested); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		a.mu.Lock()
		a.puts = append(a.puts, raw)
		a.current = circleci.StorageRetentionControls{
			CacheDays:     clampToStorageRetentionBound(requested.CacheDays, a.limits.Cache),
			WorkspaceDays: clampToStorageRetentionBound(requested.WorkspaceDays, a.limits.Workspace),
			ArtifactDays:  clampToStorageRetentionBound(requested.ArtifactDays, a.limits.Artifact),
		}
		a.mu.Unlock()

		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func clampToStorageRetentionBound(v int64, bound circleci.StorageRetentionBound) int64 {
	if v < bound.Min {
		return bound.Min
	}

	if v > bound.Max {
		return bound.Max
	}

	return v
}

func (a *fakeStorageRetentionAPI) getCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.gets
}

func (a *fakeStorageRetentionAPI) lastPut(t *testing.T) map[string]any {
	t.Helper()

	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.puts) == 0 {
		t.Fatal("expected a PUT, got none")
	}

	return a.puts[len(a.puts)-1]
}

func defaultStorageRetentionTestLimits() circleci.StorageRetentionLimits {
	return circleci.StorageRetentionLimits{
		Cache:     circleci.StorageRetentionBound{Min: 1, Max: 15},
		Workspace: circleci.StorageRetentionBound{Min: 1, Max: 15},
		Artifact:  circleci.StorageRetentionBound{Min: 1, Max: 30},
	}
}

// storageRetentionSchema returns the resource schema, so tests can build plan
// and state values for it.
func storageRetentionSchema(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	NewStorageRetentionResource().Schema(t.Context(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema method diagnostics: %+v", resp.Diagnostics)
	}

	return resp.Schema
}

// storageRetentionState builds a state value holding model. Plan, prior state
// and the empty state a resource writes into are all the same shape, so the
// same helper serves all three.
func storageRetentionState(t *testing.T, schema rschema.Schema, model storageRetentionResourceModel) tfsdk.State {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a state value: %+v", diags)
	}

	return state
}

// storageRetentionPlanModel is a configured plan with the bound attributes
// unknown, matching what Terraform actually sends into Create/Update for
// Computed attributes the configuration never sets.
func storageRetentionPlanModel(cache, workspace, artifact int64) storageRetentionResourceModel {
	return storageRetentionResourceModel{
		OrgID:                     types.StringValue(testStorageRetentionOrgID),
		CacheRetentionDays:        types.Int64Value(cache),
		CacheRetentionDaysMin:     types.Int64Unknown(),
		CacheRetentionDaysMax:     types.Int64Unknown(),
		WorkspaceRetentionDays:    types.Int64Value(workspace),
		WorkspaceRetentionDaysMin: types.Int64Unknown(),
		WorkspaceRetentionDaysMax: types.Int64Unknown(),
		ArtifactRetentionDays:     types.Int64Value(artifact),
		ArtifactRetentionDaysMin:  types.Int64Unknown(),
		ArtifactRetentionDaysMax:  types.Int64Unknown(),
	}
}

// createStorageRetention drives Create for a plan and returns the resulting
// state and response.
func createStorageRetention(
	t *testing.T, client *circleci.Client, plan storageRetentionResourceModel,
) (storageRetentionResourceModel, *fwresource.CreateResponse) {
	t.Helper()

	ctx := t.Context()
	schema := storageRetentionSchema(t)
	r := &storageRetentionResource{client: client}

	resp := &fwresource.CreateResponse{
		State: storageRetentionState(t, schema, storageRetentionResourceModel{OrgID: types.StringNull()}),
	}
	r.Create(ctx, fwresource.CreateRequest{Plan: tfsdk.Plan{
		Schema: schema,
		Raw:    storageRetentionState(t, schema, plan).Raw,
	}}, resp)

	var got storageRetentionResourceModel
	if !resp.State.Raw.IsNull() {
		if diags := resp.State.Get(ctx, &got); diags.HasError() {
			t.Fatalf("could not read the state Create produced: %+v", diags)
		}
	}

	return got, resp
}

// updateStorageRetention drives Update for a plan against prior and returns
// the resulting state and response.
func updateStorageRetention(
	t *testing.T, client *circleci.Client, prior, plan storageRetentionResourceModel,
) (storageRetentionResourceModel, *fwresource.UpdateResponse) {
	t.Helper()

	ctx := t.Context()
	schema := storageRetentionSchema(t)
	r := &storageRetentionResource{client: client}

	resp := &fwresource.UpdateResponse{State: storageRetentionState(t, schema, prior)}
	r.Update(ctx, fwresource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schema, Raw: storageRetentionState(t, schema, plan).Raw},
		State: storageRetentionState(t, schema, prior),
	}, resp)

	var got storageRetentionResourceModel
	if !resp.State.Raw.IsNull() {
		if diags := resp.State.Get(ctx, &got); diags.HasError() {
			t.Fatalf("could not read the state Update produced: %+v", diags)
		}
	}

	return got, resp
}

// readStorageRetention drives Read against prior and returns the resulting
// state and response.
func readStorageRetention(
	t *testing.T, client *circleci.Client, prior storageRetentionResourceModel,
) (storageRetentionResourceModel, *fwresource.ReadResponse) {
	t.Helper()

	ctx := t.Context()
	schema := storageRetentionSchema(t)
	r := &storageRetentionResource{client: client}

	resp := &fwresource.ReadResponse{State: storageRetentionState(t, schema, prior)}
	r.Read(ctx, fwresource.ReadRequest{State: storageRetentionState(t, schema, prior)}, resp)

	var got storageRetentionResourceModel
	if !resp.State.Raw.IsNull() {
		if diags := resp.State.Get(ctx, &got); diags.HasError() {
			t.Fatalf("could not read the state Read produced: %+v", diags)
		}
	}

	return got, resp
}

func TestStorageRetentionResourceUnit_CreateSendsConfiguredValuesAndRefreshesBounds(t *testing.T) {
	t.Parallel()

	limits := defaultStorageRetentionTestLimits()
	api := newFakeStorageRetentionAPI(t, limits, circleci.StorageRetentionControls{})
	client := circleci.New(circleci.Config{PrivateHost: api.server.URL, Token: "fake"})

	plan := storageRetentionPlanModel(10, 7, 20)
	state, resp := createStorageRetention(t, client, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %+v", resp.Diagnostics)
	}

	if resp.Diagnostics.WarningsCount() != 0 {
		t.Errorf("expected no warnings for values inside plan bounds, got: %+v", resp.Diagnostics)
	}

	sent := api.lastPut(t)
	for key, want := range map[string]float64{
		"retention_days_cache":     10,
		"retention_days_workspace": 7,
		"retention_days_artifact":  20,
	} {
		if got := sent[key]; got != want {
			t.Errorf("PUT body %s = %v, want %v", key, got, want)
		}
	}

	if state.CacheRetentionDaysMin.ValueInt64() != limits.Cache.Min ||
		state.CacheRetentionDaysMax.ValueInt64() != limits.Cache.Max {
		t.Errorf("cache bounds = [%d, %d], want [%d, %d]",
			state.CacheRetentionDaysMin.ValueInt64(), state.CacheRetentionDaysMax.ValueInt64(),
			limits.Cache.Min, limits.Cache.Max)
	}

	if state.ArtifactRetentionDays.ValueInt64() != 20 {
		t.Errorf("artifact_retention_days = %d, want 20", state.ArtifactRetentionDays.ValueInt64())
	}
}

// TestStorageRetentionResourceUnit_CreateWarnsWhenClamped is the scenario
// warnClampedStorageRetention exists for: a value outside the plan's bounds is
// accepted, not rejected, so the only way to learn the value actually in
// effect differs from the one configured is to read it back and compare.
func TestStorageRetentionResourceUnit_CreateWarnsWhenClamped(t *testing.T) {
	t.Parallel()

	api := newFakeStorageRetentionAPI(t, defaultStorageRetentionTestLimits(), circleci.StorageRetentionControls{})
	client := circleci.New(circleci.Config{PrivateHost: api.server.URL, Token: "fake"})

	// Cache's plan max is 15; ask for far more.
	plan := storageRetentionPlanModel(1000, 7, 20)
	state, resp := createStorageRetention(t, client, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %+v", resp.Diagnostics)
	}

	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("expected a warning about a clamped value, got none")
	}

	found := false

	for _, d := range resp.Diagnostics.Warnings() {
		if regexp.MustCompile(`cache_retention_days.*requested 1000.*stored 15`).MatchString(d.Detail()) {
			found = true
		}
	}

	if !found {
		t.Errorf("no warning mentioned the clamped cache value; got: %+v", resp.Diagnostics.Warnings())
	}

	// State must hold what CircleCI actually stored, not what was requested —
	// otherwise the next plan would show no difference from a configuration
	// that can never actually be satisfied.
	if state.CacheRetentionDays.ValueInt64() != 15 {
		t.Errorf("cache_retention_days in state = %d, want 15 (the clamped value)", state.CacheRetentionDays.ValueInt64())
	}
}

func TestStorageRetentionResourceUnit_ReadRefreshesBounds(t *testing.T) {
	t.Parallel()

	limits := defaultStorageRetentionTestLimits()
	api := newFakeStorageRetentionAPI(t, limits, circleci.StorageRetentionControls{
		CacheDays: 12, WorkspaceDays: 5, ArtifactDays: 25,
	})
	client := circleci.New(circleci.Config{PrivateHost: api.server.URL, Token: "fake"})

	prior := storageRetentionPlanModel(12, 5, 25)
	state, resp := readStorageRetention(t, client, prior)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %+v", resp.Diagnostics)
	}

	if state.WorkspaceRetentionDaysMax.ValueInt64() != limits.Workspace.Max {
		t.Errorf("workspace_retention_days_max = %d, want %d",
			state.WorkspaceRetentionDaysMax.ValueInt64(), limits.Workspace.Max)
	}

	if api.getCount() != 1 {
		t.Errorf("expected exactly 1 GET, got %d", api.getCount())
	}
}

func TestStorageRetentionResourceUnit_ReadDropsMissingOrganization(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	client := circleci.New(circleci.Config{PrivateHost: srv.URL, Token: "fake"})

	prior := storageRetentionPlanModel(10, 10, 10)
	_, resp := readStorageRetention(t, client, prior)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %+v", resp.Diagnostics)
	}

	if !resp.State.Raw.IsNull() {
		t.Error("expected the resource to be removed from state for a 404, but state is not null")
	}
}

// TestStorageRetentionResourceUnit_ImportStateRoundTripsWithRead proves the
// round trip `terraform import` actually drives: ImportState sets only
// org_id (see storageRetentionResource.ImportState), and the framework's own
// post-import refresh then calls Read against that bare state. This asserts
// the combination reproduces every attribute Create left behind — the three
// configured retention values and all six bound attributes — which is the
// condition that makes the plan following an import empty. There is no
// secret or write-only attribute on this resource to complicate that: every
// attribute the API can report, Read reports.
func TestStorageRetentionResourceUnit_ImportStateRoundTripsWithRead(t *testing.T) {
	t.Parallel()

	limits := defaultStorageRetentionTestLimits()
	api := newFakeStorageRetentionAPI(t, limits, circleci.StorageRetentionControls{})
	client := circleci.New(circleci.Config{PrivateHost: api.server.URL, Token: "fake"})

	created, createResp := createStorageRetention(t, client, storageRetentionPlanModel(10, 7, 20))
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %+v", createResp.Diagnostics)
	}

	schema := storageRetentionSchema(t)
	r := &storageRetentionResource{client: client}

	// ImportState only ever receives a bare id and an empty (all-null-but-typed)
	// state to write into, the same as a real `terraform import` call.
	importResp := &fwresource.ImportStateResponse{
		State: storageRetentionState(t, schema, storageRetentionResourceModel{OrgID: types.StringNull()}),
	}
	r.ImportState(t.Context(), fwresource.ImportStateRequest{ID: testStorageRetentionOrgID}, importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState diagnostics: %+v", importResp.Diagnostics)
	}

	readResp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema}}
	r.Read(t.Context(), fwresource.ReadRequest{State: importResp.State}, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %+v", readResp.Diagnostics)
	}

	var imported storageRetentionResourceModel
	if diags := readResp.State.Get(t.Context(), &imported); diags.HasError() {
		t.Fatalf("could not read the state Import+Read produced: %+v", diags)
	}

	if imported.OrgID.ValueString() != testStorageRetentionOrgID {
		t.Errorf("imported org_id = %q, want %q", imported.OrgID.ValueString(), testStorageRetentionOrgID)
	}
	if imported.CacheRetentionDays.ValueInt64() != created.CacheRetentionDays.ValueInt64() {
		t.Errorf("imported cache_retention_days = %d, want %d (the value Create produced)",
			imported.CacheRetentionDays.ValueInt64(), created.CacheRetentionDays.ValueInt64())
	}
	if imported.WorkspaceRetentionDays.ValueInt64() != created.WorkspaceRetentionDays.ValueInt64() {
		t.Errorf("imported workspace_retention_days = %d, want %d",
			imported.WorkspaceRetentionDays.ValueInt64(), created.WorkspaceRetentionDays.ValueInt64())
	}
	if imported.ArtifactRetentionDays.ValueInt64() != created.ArtifactRetentionDays.ValueInt64() {
		t.Errorf("imported artifact_retention_days = %d, want %d",
			imported.ArtifactRetentionDays.ValueInt64(), created.ArtifactRetentionDays.ValueInt64())
	}
	if imported.CacheRetentionDaysMin.ValueInt64() != created.CacheRetentionDaysMin.ValueInt64() ||
		imported.CacheRetentionDaysMax.ValueInt64() != created.CacheRetentionDaysMax.ValueInt64() {
		t.Errorf("imported cache bounds = [%d, %d], want [%d, %d]",
			imported.CacheRetentionDaysMin.ValueInt64(), imported.CacheRetentionDaysMax.ValueInt64(),
			created.CacheRetentionDaysMin.ValueInt64(), created.CacheRetentionDaysMax.ValueInt64())
	}
	if imported.WorkspaceRetentionDaysMin.ValueInt64() != created.WorkspaceRetentionDaysMin.ValueInt64() ||
		imported.WorkspaceRetentionDaysMax.ValueInt64() != created.WorkspaceRetentionDaysMax.ValueInt64() {
		t.Errorf("imported workspace bounds = [%d, %d], want [%d, %d]",
			imported.WorkspaceRetentionDaysMin.ValueInt64(), imported.WorkspaceRetentionDaysMax.ValueInt64(),
			created.WorkspaceRetentionDaysMin.ValueInt64(), created.WorkspaceRetentionDaysMax.ValueInt64())
	}
	if imported.ArtifactRetentionDaysMin.ValueInt64() != created.ArtifactRetentionDaysMin.ValueInt64() ||
		imported.ArtifactRetentionDaysMax.ValueInt64() != created.ArtifactRetentionDaysMax.ValueInt64() {
		t.Errorf("imported artifact bounds = [%d, %d], want [%d, %d]",
			imported.ArtifactRetentionDaysMin.ValueInt64(), imported.ArtifactRetentionDaysMax.ValueInt64(),
			created.ArtifactRetentionDaysMin.ValueInt64(), created.ArtifactRetentionDaysMax.ValueInt64())
	}
}

// TestStorageRetentionResourceUnit_UpdateAlwaysSendsAllThreeFields guards the
// bug class named in this repository's house rules: an attribute that only
// takes effect on create (or, here, only on a changed field) silently no-ops
// on update while `terraform apply` reports success. The underlying route
// replaces all three fields in one PUT; there is no partial update, so a
// regression that tried to send only the field that changed would silently
// leave the organization's other two retention values wrong on the wire even
// though Terraform state looks correct.
func TestStorageRetentionResourceUnit_UpdateAlwaysSendsAllThreeFields(t *testing.T) {
	t.Parallel()

	api := newFakeStorageRetentionAPI(t, defaultStorageRetentionTestLimits(), circleci.StorageRetentionControls{
		CacheDays: 10, WorkspaceDays: 10, ArtifactDays: 10,
	})
	client := circleci.New(circleci.Config{PrivateHost: api.server.URL, Token: "fake"})

	prior := storageRetentionPlanModel(10, 10, 10)
	// Only cache_retention_days changes.
	plan := storageRetentionPlanModel(5, 10, 10)

	_, resp := updateStorageRetention(t, client, prior, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Update diagnostics: %+v", resp.Diagnostics)
	}

	sent := api.lastPut(t)
	for key, want := range map[string]float64{
		"retention_days_cache":     5,
		"retention_days_workspace": 10,
		"retention_days_artifact":  10,
	} {
		got, ok := sent[key]
		if !ok {
			t.Errorf("PUT body missing %q; an update that omits an unchanged field would silently no-op it. Got keys: %v", key, sent)

			continue
		}

		if got != want {
			t.Errorf("PUT body %s = %v, want %v", key, got, want)
		}
	}
}

// TestStorageRetentionResourceUnit_DeleteMakesNoAPICallAndWarns asserts the
// documented destroy behavior: removing the resource stops Terraform from
// tracking it without resetting the organization's retention values (there is
// no route that would let it), and says so with the actual values that are
// left in place.
//
// The client is pointed at an address nothing listens on, deliberately: if
// Delete is ever changed to call the API, this test fails with a connection
// error rather than silently passing.
func TestStorageRetentionResourceUnit_DeleteMakesNoAPICallAndWarns(t *testing.T) {
	t.Parallel()

	client := circleci.New(circleci.Config{PrivateHost: "http://127.0.0.1:1", Token: "fake"})
	r := &storageRetentionResource{client: client}

	schema := storageRetentionSchema(t)
	state := storageRetentionPlanModel(15, 7, 30)
	state.CacheRetentionDaysMin = types.Int64Value(1)
	state.CacheRetentionDaysMax = types.Int64Value(15)
	state.WorkspaceRetentionDaysMin = types.Int64Value(1)
	state.WorkspaceRetentionDaysMax = types.Int64Value(15)
	state.ArtifactRetentionDaysMin = types.Int64Value(1)
	state.ArtifactRetentionDaysMax = types.Int64Value(30)

	resp := &fwresource.DeleteResponse{}
	r.Delete(t.Context(), fwresource.DeleteRequest{
		State: storageRetentionState(t, schema, state),
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete diagnostics: %+v", resp.Diagnostics)
	}

	if resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("expected exactly 1 warning, got %+v", resp.Diagnostics)
	}

	detail := resp.Diagnostics.Warnings()[0].Detail()
	for _, want := range []string{"15", "7", "30"} {
		if !regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(detail) {
			t.Errorf("Delete warning does not mention %q: %s", want, detail)
		}
	}
}

// TestStorageRetentionResourceUnit_DeleteSucceedsUnderServerDeployment proves
// the design decision documented on Delete: it is not gated on requireCloud,
// so a record created under `deployment = "cloud"` and then stranded in state
// by a later switch to `deployment = "server"` stays removable, matching
// projectGroupResource.Delete.
func TestStorageRetentionResourceUnit_DeleteSucceedsUnderServerDeployment(t *testing.T) {
	t.Parallel()

	serverClient := circleci.New(circleci.Config{
		Host:       "https://circleci.example.com",
		Token:      "fake",
		Deployment: circleci.DeploymentServer,
	})
	r := &storageRetentionResource{client: serverClient}

	schema := storageRetentionSchema(t)
	state := storageRetentionPlanModel(15, 7, 30)

	resp := &fwresource.DeleteResponse{}
	r.Delete(t.Context(), fwresource.DeleteRequest{
		State: storageRetentionState(t, schema, state),
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("Delete errored under a Server deployment, want it to succeed: %+v", resp.Diagnostics)
	}
}

// TestStorageRetentionResourceUnit_ModifyPlanAllowsDestroy mirrors
// TestCloudOnlyModifyPlanAllowsDestroy for this resource, which is gated in
// its own file rather than in cloud_only.go (the same choice
// otel_exporter_resource.go and organization_contacts_resource.go make).
func TestStorageRetentionResourceUnit_ModifyPlanAllowsDestroy(t *testing.T) {
	t.Parallel()

	serverClient := circleci.New(circleci.Config{
		Host:       "https://circleci.example.com",
		Token:      "fake",
		Deployment: circleci.DeploymentServer,
	})
	r := &storageRetentionResource{client: serverClient}

	destroyPlan := tfsdk.Plan{
		Raw:    tftypes.NewValue(tftypes.Object{}, nil),
		Schema: rschema.Schema{},
	}

	var resp fwresource.ModifyPlanResponse
	r.ModifyPlan(t.Context(), fwresource.ModifyPlanRequest{Plan: destroyPlan}, &resp)

	if resp.Diagnostics.HasError() {
		t.Errorf(
			"ModifyPlan rejected a destroy on a server deployment: %v\n"+
				"A resource stranded by a deployment change must stay removable.",
			resp.Diagnostics.Errors(),
		)
	}
}

// TestStorageRetentionResourceUnit_ModifyPlanIsSilentBeforeConfigure mirrors
// TestCloudOnlyModifyPlanIsSilentBeforeConfigure for this resource.
func TestStorageRetentionResourceUnit_ModifyPlanIsSilentBeforeConfigure(t *testing.T) {
	t.Parallel()

	nonDestroyPlan := tfsdk.Plan{
		Raw:    tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{}),
		Schema: rschema.Schema{},
	}

	var resp fwresource.ModifyPlanResponse
	(&storageRetentionResource{}).ModifyPlan(t.Context(), fwresource.ModifyPlanRequest{Plan: nonDestroyPlan}, &resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("ModifyPlan errored with no configured client: %v", resp.Diagnostics.Errors())
	}
}

// TestAccStorageRetention_RejectsServerDeploymentAtPlanTime uses the ordinary
// resource.UnitTest harness: this is safe with no fake API and no credentials
// because the ModifyPlan gate fails before any client method — and therefore
// any network call — is ever reached. host is an unroutable placeholder on
// purpose: if the gate is ever broken, this test must fail loudly with a
// connection error rather than silently reaching a real server.
func TestAccStorageRetention_RejectsServerDeploymentAtPlanTime(t *testing.T) {
	t.Parallel()

	cfg := fmt.Sprintf(`
provider "circleci" {
  host       = "http://127.0.0.1:1"
  key        = "fake"
  deployment = "server"
}
resource "circleci_storage_retention" "test" {
  org_id                   = %q
  cache_retention_days     = 15
  workspace_retention_days = 15
  artifact_retention_days  = 30
}
`, testStorageRetentionOrgID)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config:      cfg,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`(?s)circleci_storage_retention requires CircleCI Cloud`),
		}},
	})
}

// TestAccStorageRetention_RejectsServerDeployment is the apply-time twin of
// the plan-time test above: Create's own gate must also reject a Server
// deployment, in case ModifyPlan's gate is ever removed on its own.
func TestAccStorageRetention_RejectsServerDeployment(t *testing.T) {
	t.Parallel()

	cfg := fmt.Sprintf(`
provider "circleci" {
  host       = "http://127.0.0.1:1"
  key        = "fake"
  deployment = "server"
}
resource "circleci_storage_retention" "test" {
  org_id                   = %q
  cache_retention_days     = 15
  workspace_retention_days = 15
  artifact_retention_days  = 30
}
`, testStorageRetentionOrgID)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)circleci_storage_retention requires CircleCI Cloud`),
		}},
	})
}
