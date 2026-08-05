// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"strings"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// These tests back circleci_project_environment_variable
// (project_environment_variable_resource.go) with an in-process fake
// (project_environment_variable_fake_test.go) instead of a real CircleCI
// account, so they run without TF_ACC or credentials. Before this file, the
// resource had only resource.Test acceptance tests gated on CIRCLE_TOKEN.

const (
	testEnvVarProjectSlug = "circleci/org-id/project-id"
	testEnvVarName        = "MY_VAR"
)

func projectEnvVarResourceProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
`, host)
}

func projectEnvVarResourceConfig(host, slug, name, value string) string {
	return projectEnvVarResourceProviderConfig(host) + fmt.Sprintf(`
resource "circleci_project_environment_variable" "test" {
  project_slug = %q
  name         = %q
  value        = %q
}
`, slug, name, value)
}

// TestProjectEnvVarResourceUnit_CreateAndDestroy covers the create request
// body and the delete request the destroy step sends.
func TestProjectEnvVarResourceUnit_CreateAndDestroy(t *testing.T) {
	api, host := newFakeEnvVarAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectEnvVarResourceConfig(host, testEnvVarProjectSlug, testEnvVarName, "s3cr3t-value"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_project_environment_variable.test", tfjsonpath.New("name"), knownvalue.StringExact(testEnvVarName)),
					statecheck.ExpectKnownValue("circleci_project_environment_variable.test", tfjsonpath.New("value"), knownvalue.StringExact("s3cr3t-value")),
					statecheck.ExpectKnownValue("circleci_project_environment_variable.test", tfjsonpath.New("project_slug"), knownvalue.StringExact(testEnvVarProjectSlug)),
					statecheck.ExpectKnownValue("circleci_project_environment_variable.test", tfjsonpath.New("created_at"), knownvalue.StringExact("2024-01-02T03:04:05.000Z")),
				},
			},
		},
	})

	creates := api.recordedCreates()
	if len(creates) != 1 {
		t.Fatalf("create requests = %v, want exactly 1", creates)
	}
	if creates[0]["name"] != testEnvVarName || creates[0]["value"] != "s3cr3t-value" {
		t.Errorf("create body = %v, want name=%q value=%q", creates[0], testEnvVarName, "s3cr3t-value")
	}

	var sawDelete bool
	for _, req := range api.recordedRequests() {
		if req == "DELETE /api/v2/project/"+testEnvVarProjectSlug+"/envvar/"+testEnvVarName {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Errorf("no delete request seen, got %v", api.recordedRequests())
	}
}

// TestProjectEnvVarResourceUnit_Import covers the "project_slug/name" import
// ID, including a project slug that itself contains slashes — the reason
// ImportState splits from the right rather than on the first slash.
func TestProjectEnvVarResourceUnit_Import(t *testing.T) {
	_, host := newFakeEnvVarAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: projectEnvVarResourceConfig(host, testEnvVarProjectSlug, testEnvVarName, "s3cr3t-value")},
			{
				ResourceName: "circleci_project_environment_variable.test",
				ImportState:  true,
				// The value can never be verified: the API never discloses it, so
				// the resource has nothing to compare the prior state's value
				// against on import.
				ImportStateVerify:                    true,
				ImportStateVerifyIgnore:              []string{"value"},
				ImportStateVerifyIdentifierAttribute: "name",
				ImportStateId:                        testEnvVarProjectSlug + "/" + testEnvVarName,
			},
		},
	})
}

// TestProjectEnvVarResourceUnit_ImportWarnsValueIsUnset proves ImportState
// (project_environment_variable_resource.go) tells the practitioner what
// TestProjectEnvVarResourceUnit_Import's ImportStateVerifyIgnore only
// documents in a comment: the value cannot be read back, so it must be
// supplied from the configuration before plan or apply can proceed.
func TestProjectEnvVarResourceUnit_ImportWarnsValueIsUnset(t *testing.T) {
	t.Parallel()

	schema := projectEnvVarResourceSchemaForTest(t)
	r := &projectEnvironmentVariableResource{}
	resp := &fwresource.ImportStateResponse{
		State: projectEnvVarResourceStateForTest(t, schema, projectEnvVarModel("", "", "")),
	}

	r.ImportState(t.Context(), fwresource.ImportStateRequest{ID: testEnvVarProjectSlug + "/" + testEnvVarName}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("ImportState diagnostics: %+v", resp.Diagnostics)
	}

	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("ImportState produced no warning that value cannot be read back")
	}

	var sawValueWarning bool
	for _, d := range resp.Diagnostics.Warnings() {
		if strings.Contains(d.Summary(), "cannot be read") {
			sawValueWarning = true
		}
	}
	if !sawValueWarning {
		t.Errorf("ImportState warnings = %+v, want one about value being unreadable", resp.Diagnostics.Warnings())
	}
}

// TestProjectEnvVarResourceUnit_ImportRejectsMalformedID covers the guard in
// ImportState (project_environment_variable_resource.go) directly: an ID with
// no slash, or one that is all separator, must fail cleanly rather than slice
// out of bounds.
func TestProjectEnvVarResourceUnit_ImportRejectsMalformedID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
	}{
		{name: "no slash", id: "just-a-name"},
		{name: "empty", id: ""},
		{name: "trailing slash", id: testEnvVarProjectSlug + "/"},
		{name: "leading slash", id: "/" + testEnvVarName},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			schema := projectEnvVarResourceSchemaForTest(t)
			r := &projectEnvironmentVariableResource{}
			resp := &fwresource.ImportStateResponse{
				State: projectEnvVarResourceStateForTest(t, schema, projectEnvVarModel("", "", "")),
			}

			assertNoPanic(t, func() {
				r.ImportState(t.Context(), fwresource.ImportStateRequest{ID: tt.id}, resp)
			})

			if !resp.Diagnostics.HasError() {
				t.Fatalf("ImportState(%q) reported no error, want one", tt.id)
			}
		})
	}
}

// TestProjectEnvVarResourceUnit_ReadMaskedValuePreservesPriorValue is the
// regression test for requirement 5: the API never returns the real value, so
// Read must leave state.Value exactly as it was rather than overwriting it
// with the masked value the fake (and the real API) reports.
func TestProjectEnvVarResourceUnit_ReadMaskedValuePreservesPriorValue(t *testing.T) {
	t.Parallel()

	api, envSvc := newFakeEnvVarClient(t)
	api.seed(testEnvVarProjectSlug, testEnvVarName, "the-real-value", "2024-01-02T03:04:05.000Z")

	schema := projectEnvVarResourceSchemaForTest(t)
	priorState := projectEnvVarResourceStateForTest(t, schema, projectEnvVarModel(testEnvVarProjectSlug, testEnvVarName, "the-real-value"))

	r := &projectEnvironmentVariableResource{client: envSvc}
	resp := &fwresource.ReadResponse{State: priorState}
	r.Read(t.Context(), fwresource.ReadRequest{State: priorState}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %+v", resp.Diagnostics)
	}

	var got projectEnvironmentVariableResourceModel
	if diags := resp.State.Get(t.Context(), &got); diags.HasError() {
		t.Fatalf("could not read the state Read produced: %+v", diags)
	}

	if got.Value.ValueString() != "the-real-value" {
		t.Errorf("Value after Read = %q, want %q (the API's masked value must never overwrite it)", got.Value.ValueString(), "the-real-value")
	}
	if got.CreatedAt.ValueString() != "2024-01-02T03:04:05.000Z" {
		t.Errorf("CreatedAt after Read = %q, want %q", got.CreatedAt.ValueString(), "2024-01-02T03:04:05.000Z")
	}
}

// TestProjectEnvVarResourceUnit_ReadMissingRemovesFromState checks that a 404
// drops the resource from state, so the next plan offers a clean recreate —
// unlike circleci_project's Read, which has no such handling (see
// TestProjectResourceUnit_DriftMissingProjectErrorsInsteadOfRecreating).
func TestProjectEnvVarResourceUnit_ReadMissingRemovesFromState(t *testing.T) {
	t.Parallel()

	api, envSvc := newFakeEnvVarClient(t)
	api.setMissing(testEnvVarProjectSlug, testEnvVarName, true)

	schema := projectEnvVarResourceSchemaForTest(t)
	priorState := projectEnvVarResourceStateForTest(t, schema, projectEnvVarModel(testEnvVarProjectSlug, testEnvVarName, "the-real-value"))

	r := &projectEnvironmentVariableResource{client: envSvc}
	resp := &fwresource.ReadResponse{State: priorState}
	r.Read(t.Context(), fwresource.ReadRequest{State: priorState}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %+v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("Read left the resource in state though the environment variable is gone")
	}
}

// TestProjectEnvVarResourceUnit_ReadErrorMapping checks that a non-404
// failure surfaces as a diagnostic rather than a panic.
func TestProjectEnvVarResourceUnit_ReadErrorMapping(t *testing.T) {
	t.Parallel()

	// project_environment_variable_resource.go's Read special-cases a 404 as
	// drift (see TestProjectEnvVarResourceUnit_ReadMissingRemovesFromState), so
	// exercising the *error* diagnostic path needs a different failure: force
	// a 400 for this variable instead. (Not a 5xx: internal/httpcl retries those
	// with backoff, which would make this test slow for no benefit — a 4xx is
	// not retried.)
	api, envSvc := newFakeEnvVarClient(t)
	api.setForceStatus(testEnvVarProjectSlug, testEnvVarName, 400)

	schema := projectEnvVarResourceSchemaForTest(t)
	priorState := projectEnvVarResourceStateForTest(t, schema, projectEnvVarModel(testEnvVarProjectSlug, testEnvVarName, "v"))

	r := &projectEnvironmentVariableResource{client: envSvc}
	resp := &fwresource.ReadResponse{State: priorState}

	assertNoPanic(t, func() {
		r.Read(t.Context(), fwresource.ReadRequest{State: priorState}, resp)
	})

	if !resp.Diagnostics.HasError() {
		t.Fatal("Read reported no error for a 500 response")
	}
}

// --- test helpers ---

func projectEnvVarResourceSchemaForTest(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	(&projectEnvironmentVariableResource{}).Schema(t.Context(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema diagnostics: %+v", resp.Diagnostics)
	}

	return resp.Schema
}

func projectEnvVarResourceStateForTest(t *testing.T, schema rschema.Schema, model projectEnvironmentVariableResourceModel) tfsdk.State {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a state value: %+v", diags)
	}

	return state
}

// projectEnvVarModel builds a prior-state model for the tests that drive the
// resource's Go methods directly.
//
// created_at is deliberately always empty here: this builds a *prior* state for
// tests that drive Read directly, and the tests that care what Read populates it
// with (TestProjectEnvVarResourceUnit_ReadMaskedValuePreservesPriorValue) assert
// on the value the fake's Get response reports, not on this placeholder.
func projectEnvVarModel(slug, name, value string) projectEnvironmentVariableResourceModel {
	return projectEnvironmentVariableResourceModel{
		ProjectSlug: types.StringValue(slug),
		Name:        types.StringValue(name),
		Value:       types.StringValue(value),
		CreatedAt:   types.StringValue(""),
	}
}

// newFakeEnvVarClient starts the fake and returns it alongside a
// circleci.Client pointed at it, matching how provider.go constructs the real
// one.
func newFakeEnvVarClient(t *testing.T) (*fakeEnvVarAPI, *circleci.Client) {
	t.Helper()

	api, host := newFakeEnvVarAPI(t)

	return api, circleci.New(circleci.Config{Host: host, Token: "fake"})
}
