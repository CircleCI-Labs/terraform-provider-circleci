// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// These tests cover the write-only value pair (environment_variable_write_only.go)
// on both environment variable resources, and the timestamp-based drift
// detection on the context one.
//
// Every case that puts `value_wo` in a configuration declares a minimum
// Terraform version: write-only attributes are a 1.11 feature, and without the
// check the failure on an older CLI is an opaque "Unsupported argument" rather
// than a skip.

// writeOnlySupported gates a test case on a Terraform CLI that understands
// write-only attributes.
func writeOnlySupported() []tfversion.TerraformVersionCheck {
	return []tfversion.TerraformVersionCheck{
		tfversion.SkipBelow(tfversion.Version1_11_0),
	}
}

// --- fake accessors -----------------------------------------------------------
//
// These read what the fake actually received. Both fakes store the value
// straight out of the request body, so asserting on the stored value is
// asserting on the request — which is the point: `value` and `value_wo` must
// produce byte-identical requests.

// storedContextEnvVarValue returns the value the fake context API currently
// holds, i.e. the value carried by the most recent PUT body.
func (a *contextFakeAPI) storedContextEnvVarValue(contextID, name string) string {
	a.mu.Lock()
	defer a.mu.Unlock()

	fakeCtx, ok := a.contexts[contextID]
	if !ok {
		return ""
	}

	envVar, ok := fakeCtx.envVars[name]
	if !ok {
		return ""
	}

	return envVar.value
}

// snapshotStoredValue reads the stored value as a step check, which runs while
// the resource still exists. Reading it after resource.UnitTest returns would
// always find "", because the harness destroys everything on the way out.
func snapshotStoredValue(api *contextFakeAPI, into *string) func(*terraform.State) error {
	return func(*terraform.State) error {
		*into = api.storedContextEnvVarValue(contextEnvVarUnitContextID, "API_KEY")

		return nil
	}
}

// rotateEnvVarOutsideTerraform overwrites the variable's value and bumps its
// updated_at directly, bypassing the PUT route. That is what "changed outside
// Terraform" means here: the provider never sees the write, so it never records
// the resulting timestamp, and only the API's copy moves forward.
//
// The name, value and timestamp are fixed rather than threaded through as
// parameters that would never vary.
func rotateEnvVarOutsideTerraform(api *contextFakeAPI) {
	api.mu.Lock()
	defer api.mu.Unlock()

	envVar := api.contexts[contextEnvVarUnitContextID].envVars["API_KEY"]
	envVar.value = "rotated-in-the-ui"
	envVar.updatedAt = "2024-07-01T00:00:00.000Z"
}

// contextEnvVarWrites returns just the PUT request lines, so a test can assert
// how many times the provider wrote and to which route.
func (a *contextFakeAPI) contextEnvVarWrites() []string {
	var writes []string

	for _, req := range a.recorded() {
		if req == "PUT /api/v2/context/"+contextEnvVarUnitContextID+"/environment-variable/API_KEY" {
			writes = append(writes, req)
		}
	}

	return writes
}

// --- configurations -----------------------------------------------------------

// contextEnvVarWriteOnlyConfig builds a context environment variable managed
// through `value_wo`. It is otherwise identical to
// contextEnvVarResourceUnitConfig, which is what makes the two comparable.
func contextEnvVarWriteOnlyConfig(host, value string, version int) string {
	return contextFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_context_environment_variable" "test" {
  context_id       = %[1]q
  name             = "API_KEY"
  value_wo         = %[2]q
  value_wo_version = %[3]d
}
`, contextEnvVarUnitContextID, value, version)
}

func projectEnvVarWriteOnlyConfig(host, value string, version int) string {
	return projectEnvVarResourceProviderConfig(host) + fmt.Sprintf(`
resource "circleci_project_environment_variable" "test" {
  project_slug     = %[1]q
  name             = %[2]q
  value_wo         = %[3]q
  value_wo_version = %[4]d
}
`, testEnvVarProjectSlug, testEnvVarName, value, version)
}

// --- both names reach the same request ----------------------------------------

// TestContextEnvVarWriteOnly_SameRequestAsValue proves `value` and `value_wo`
// are two spellings of one argument: the same secret, written the same way, to
// the same route. The two paths are run against separate fakes and their
// observations compared, so a difference in either the request line or the
// value that arrived fails the test.
func TestContextEnvVarWriteOnly_SameRequestAsValue(t *testing.T) {
	const secret = "s3cr3t"

	observed := map[string]struct {
		writes []string
		stored string
	}{}

	for name, config := range map[string]func(host string) string{
		"value":    func(host string) string { return contextEnvVarResourceUnitConfig(host, secret) },
		"value_wo": func(host string) string { return contextEnvVarWriteOnlyConfig(host, secret, 1) },
	} {
		t.Run(name, func(t *testing.T) {
			api, host := newContextFakeAPI(t)
			api.seedContext(contextEnvVarUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")

			var stored string

			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   writeOnlySupported(),
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config: config(host),
						Check:  snapshotStoredValue(api, &stored),
						ConfigStateChecks: []statecheck.StateCheck{
							// Whichever name was used, the write-only attribute is never
							// persisted; the framework nulls it in plan and state.
							statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("value_wo"), knownvalue.Null()),
						},
					},
				},
			})

			observed[name] = struct {
				writes []string
				stored string
			}{writes: api.contextEnvVarWrites(), stored: stored}
		})
	}

	if len(observed["value"].writes) != 1 {
		t.Fatalf("value path made %d writes, want 1: %v", len(observed["value"].writes), observed["value"].writes)
	}
	if observed["value"].stored != secret {
		t.Errorf("value path wrote %q, want %q", observed["value"].stored, secret)
	}
	if fmt.Sprint(observed["value_wo"]) != fmt.Sprint(observed["value"]) {
		t.Errorf("value_wo reached the API differently from value:\n  value_wo: %v\n  value:    %v", observed["value_wo"], observed["value"])
	}
}

// TestProjectEnvVarWriteOnly_SameRequestAsValue is the same proof for the
// project resource, where the fake records the create bodies verbatim.
func TestProjectEnvVarWriteOnly_SameRequestAsValue(t *testing.T) {
	const secret = "s3cr3t-value"

	observed := map[string][]map[string]string{}

	for name, config := range map[string]func(host string) string{
		"value": func(host string) string {
			return projectEnvVarResourceConfig(host, testEnvVarProjectSlug, testEnvVarName, secret)
		},
		"value_wo": func(host string) string { return projectEnvVarWriteOnlyConfig(host, secret, 1) },
	} {
		t.Run(name, func(t *testing.T) {
			api, host := newFakeEnvVarAPI(t)

			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   writeOnlySupported(),
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config: config(host),
						ConfigStateChecks: []statecheck.StateCheck{
							statecheck.ExpectKnownValue("circleci_project_environment_variable.test", tfjsonpath.New("value_wo"), knownvalue.Null()),
						},
					},
				},
			})

			observed[name] = api.recordedCreates()
		})
	}

	want := []map[string]string{{"name": testEnvVarName, "value": secret}}
	if fmt.Sprint(observed["value"]) != fmt.Sprint(want) {
		t.Fatalf("value path create bodies = %v, want %v", observed["value"], want)
	}
	if fmt.Sprint(observed["value_wo"]) != fmt.Sprint(observed["value"]) {
		t.Errorf("value_wo reached the API differently from value:\n  value_wo: %v\n  value:    %v", observed["value_wo"], observed["value"])
	}
}

// --- rotation -----------------------------------------------------------------

// TestContextEnvVarWriteOnly_RotationNeedsAVersionBump covers both halves of
// the version contract: a changed `value_wo` alone is invisible to Terraform
// and is therefore NOT sent, and bumping `value_wo_version` is what sends it.
//
// The middle step is the one worth having. It fails if the resource ever grows
// something that leaks the value into state, because then the value change
// alone would produce a diff and the version would be pointless.
func TestContextEnvVarWriteOnly_RotationNeedsAVersionBump(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextEnvVarUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")

	var stored string

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextEnvVarWriteOnlyConfig(host, "s3cr3t", 1),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("value_wo_version"), knownvalue.Int64Exact(1)),
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("updated_at"), knownvalue.StringExact("2024-06-01T00:00:00.000Z")),
				},
			},
			{
				// New value, same version: no diff, so no request.
				Config:   contextEnvVarWriteOnlyConfig(host, "rotated", 1),
				PlanOnly: true,
			},
			{
				Config: contextEnvVarWriteOnlyConfig(host, "rotated", 2),
				Check:  snapshotStoredValue(api, &stored),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						// In place, not replaced: the PUT route is an upsert.
						plancheck.ExpectResourceAction("circleci_context_environment_variable.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("value_wo_version"), knownvalue.Int64Exact(2)),
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("updated_at"), knownvalue.StringExact("2024-06-02T00:00:00.000Z")),
				},
			},
		},
	})

	if stored != "rotated" {
		t.Errorf("value at the API after rotating = %q, want %q", stored, "rotated")
	}
	if writes := api.contextEnvVarWrites(); len(writes) != 2 {
		t.Errorf("writes = %d, want 2 (create and the version bump; the unbumped change must not be sent)", len(writes))
	}
}

// TestProjectEnvVarWriteOnly_RotationReplaces covers the same rotation on the
// project resource, where it must replace rather than update: the API has no
// route that overwrites a project environment variable, so `value_wo_version`
// carries RequiresReplace on this resource and not on the context one.
func TestProjectEnvVarWriteOnly_RotationReplaces(t *testing.T) {
	api, host := newFakeEnvVarAPI(t)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: projectEnvVarWriteOnlyConfig(host, "first", 1)},
			{
				Config:   projectEnvVarWriteOnlyConfig(host, "second", 1),
				PlanOnly: true,
			},
			{
				Config: projectEnvVarWriteOnlyConfig(host, "second", 2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_project_environment_variable.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			},
		},
	})

	creates := api.recordedCreates()
	if len(creates) != 2 {
		t.Fatalf("create bodies = %v, want 2 (the unbumped change must not be sent)", creates)
	}
	if creates[1]["value"] != "second" {
		t.Errorf("second create sent %q, want %q", creates[1]["value"], "second")
	}
}

// --- exactly one of ----------------------------------------------------------

// TestEnvVarWriteOnly_ExactlyOneValue covers envVarValueConfigValidator and the
// AlsoRequires/AtLeast validators on the version, on both resources: neither
// name set, both set, a version with no value, and a version below 1.
func TestEnvVarWriteOnly_ExactlyOneValue(t *testing.T) {
	contextBody := func(body string) string {
		return `
resource "circleci_context_environment_variable" "test" {
  context_id = "` + contextEnvVarUnitContextID + `"
  name       = "API_KEY"
` + body + `
}
`
	}
	projectBody := func(body string) string {
		return `
resource "circleci_project_environment_variable" "test" {
  project_slug = "` + testEnvVarProjectSlug + `"
  name         = "` + testEnvVarName + `"
` + body + `
}
`
	}

	// Both halves of ExactlyOneOf report the same detail under different titles
	// ("Missing Attribute Configuration" for none, "Invalid Attribute
	// Combination" for both), so the detail is what these match on.
	exactlyOne := regexp.MustCompile(`Exactly one of these attributes must be configured: \[value,value_wo\]`)

	tests := map[string]struct {
		body  func(string) string
		error *regexp.Regexp
	}{
		"context neither set": {
			body:  func(string) string { return contextBody(``) },
			error: exactlyOne,
		},
		"context both set": {
			body: func(string) string {
				return contextBody("  value = \"a\"\n  value_wo = \"b\"\n  value_wo_version = 1")
			},
			error: exactlyOne,
		},
		"context version without value_wo": {
			body:  func(string) string { return contextBody("  value = \"a\"\n  value_wo_version = 1") },
			error: regexp.MustCompile(`Invalid Attribute Combination`),
		},
		"context value_wo without a version": {
			body:  func(string) string { return contextBody("  value_wo = \"a\"") },
			error: regexp.MustCompile(`Invalid Attribute Combination`),
		},
		"context version below one": {
			body:  func(string) string { return contextBody("  value_wo = \"a\"\n  value_wo_version = 0") },
			error: regexp.MustCompile(`Invalid Attribute Value`),
		},
		"project neither set": {
			body:  func(string) string { return projectBody(``) },
			error: exactlyOne,
		},
		"project both set": {
			body: func(string) string {
				return projectBody("  value = \"a\"\n  value_wo = \"b\"\n  value_wo_version = 1")
			},
			error: exactlyOne,
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			api, host := newContextFakeAPI(t)
			api.seedContext(contextEnvVarUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")

			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   writeOnlySupported(),
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config:      contextFakeProviderConfig(host) + testCase.body(host),
						PlanOnly:    true,
						ExpectError: testCase.error,
					},
				},
			})
		})
	}
}

// --- the guard --------------------------------------------------------------

// TestEnvVarWriteOnly_GuardsAgainstAMissingWriteOnlyValue drives Create and
// Update directly, because a plan cannot reach this state: the ExactlyOneOf
// validator rejects a configuration with neither value at validation time. The
// guard exists for the gap after that — a write-only value that resolved to
// nothing by apply — where the alternative is a request that overwrites a live
// secret with an empty string.
func TestEnvVarWriteOnly_GuardsAgainstAMissingWriteOnlyValue(t *testing.T) {
	t.Parallel()

	// value and value_wo both null, value_wo_version set: the shape of a resource
	// already managed through the write-only path whose value has gone missing.
	contextModel := contextEnvironmentVariableResourceModel{
		ContextId:      types.StringValue(contextEnvVarUnitContextID),
		Name:           types.StringValue("API_KEY"),
		ValueWOVersion: types.Int64Value(1),
	}
	projectModel := projectEnvironmentVariableResourceModel{
		ProjectSlug:    types.StringValue(testEnvVarProjectSlug),
		Name:           types.StringValue(testEnvVarName),
		ValueWOVersion: types.Int64Value(1),
	}

	contextSchema := contextEnvVarResourceSchemaForTest(t)
	projectSchema := projectEnvVarResourceSchemaForTest(t)

	tests := map[string]func(t *testing.T) fwresource.CreateResponse{
		"context create": func(t *testing.T) fwresource.CreateResponse {
			config := configForTest(t, contextSchema, contextModel)
			resp := fwresource.CreateResponse{State: tfsdk.State{Schema: contextSchema}}
			(&contextEnvironmentVariableResource{}).Create(t.Context(), fwresource.CreateRequest{
				Config: config,
				Plan:   tfsdk.Plan{Schema: contextSchema, Raw: config.Raw},
			}, &resp)

			return resp
		},
		"project create": func(t *testing.T) fwresource.CreateResponse {
			config := configForTest(t, projectSchema, projectModel)
			resp := fwresource.CreateResponse{State: tfsdk.State{Schema: projectSchema}}
			(&projectEnvironmentVariableResource{}).Create(t.Context(), fwresource.CreateRequest{
				Config: config,
				Plan:   tfsdk.Plan{Schema: projectSchema, Raw: config.Raw},
			}, &resp)

			return resp
		},
	}

	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			resp := run(t)

			if !resp.Diagnostics.HasError() {
				t.Fatal("no error reported; a request with no value would have been sent")
			}
			if !regexp.MustCompile(`value_wo_version`).MatchString(fmt.Sprint(resp.Diagnostics)) {
				t.Errorf("error does not explain the write-only path: %+v", resp.Diagnostics)
			}
		})
	}

	// Update, on the resource that has one. Same shape, same guard.
	config := configForTest(t, contextSchema, contextModel)
	updateResp := fwresource.UpdateResponse{State: tfsdk.State{Schema: contextSchema}}
	(&contextEnvironmentVariableResource{}).Update(t.Context(), fwresource.UpdateRequest{
		Config: config,
		Plan:   tfsdk.Plan{Schema: contextSchema, Raw: config.Raw},
		State:  tfsdk.State{Schema: contextSchema, Raw: config.Raw},
	}, &updateResp)

	if !updateResp.Diagnostics.HasError() {
		t.Error("Update reported no error; a request with no value would have been sent")
	}
}

// --- drift detection --------------------------------------------------------

// TestContextEnvVarDrift_TimestampComparison covers detectContextEnvVarDrift
// directly, which is where the subtlety lives: which timestamp is compared with
// which, and what happens when there is nothing to compare.
func TestContextEnvVarDrift_TimestampComparison(t *testing.T) {
	t.Parallel()

	const (
		ours   = "2024-06-01T00:00:00.000Z"
		newer  = "2024-07-01T00:00:00.000Z"
		older  = "2024-05-01T00:00:00.000Z"
		secret = "s3cr3t"
	)

	tests := map[string]struct {
		state            contextEnvironmentVariableResourceModel
		remote           string
		wantValue        types.String
		wantVersion      types.Int64
		wantUpdatedAt    string
		wantRemoteUpdate string
	}{
		"first read after create is not drift": {
			state:            contextEnvironmentVariableResourceModel{Value: types.StringValue(secret), UpdatedAt: types.StringValue(ours)},
			remote:           ours,
			wantValue:        types.StringValue(secret),
			wantUpdatedAt:    ours,
			wantRemoteUpdate: ours,
		},
		"import adopts the remote timestamp": {
			state:            contextEnvironmentVariableResourceModel{Value: types.StringNull(), UpdatedAt: types.StringNull()},
			remote:           newer,
			wantValue:        types.StringNull(),
			wantUpdatedAt:    newer,
			wantRemoteUpdate: newer,
		},
		"newer remote clears value": {
			state:            contextEnvironmentVariableResourceModel{Value: types.StringValue(secret), UpdatedAt: types.StringValue(ours)},
			remote:           newer,
			wantValue:        types.StringNull(),
			wantUpdatedAt:    ours,
			wantRemoteUpdate: newer,
		},
		"newer remote clears the version on the write-only path": {
			state: contextEnvironmentVariableResourceModel{
				Value:          types.StringNull(),
				ValueWOVersion: types.Int64Value(3),
				UpdatedAt:      types.StringValue(ours),
			},
			remote:           newer,
			wantValue:        types.StringNull(),
			wantVersion:      types.Int64Null(),
			wantUpdatedAt:    ours,
			wantRemoteUpdate: newer,
		},
		"older remote is not drift": {
			state:            contextEnvironmentVariableResourceModel{Value: types.StringValue(secret), UpdatedAt: types.StringValue(ours)},
			remote:           older,
			wantValue:        types.StringValue(secret),
			wantUpdatedAt:    ours,
			wantRemoteUpdate: older,
		},
		"a different spelling of the same instant is not drift": {
			state:            contextEnvironmentVariableResourceModel{Value: types.StringValue(secret), UpdatedAt: types.StringValue(ours)},
			remote:           "2024-06-01T02:00:00+02:00",
			wantValue:        types.StringValue(secret),
			wantUpdatedAt:    ours,
			wantRemoteUpdate: "2024-06-01T02:00:00+02:00",
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			state := testCase.state
			detectContextEnvVarDrift(&state, testCase.remote)

			if !state.Value.Equal(testCase.wantValue) {
				t.Errorf("value = %v, want %v", state.Value, testCase.wantValue)
			}
			if !state.ValueWOVersion.Equal(testCase.wantVersion) {
				t.Errorf("value_wo_version = %v, want %v", state.ValueWOVersion, testCase.wantVersion)
			}
			if state.UpdatedAt.ValueString() != testCase.wantUpdatedAt {
				t.Errorf("updated_at = %v, want %q", state.UpdatedAt, testCase.wantUpdatedAt)
			}
			if state.RemoteUpdatedAt.ValueString() != testCase.wantRemoteUpdate {
				t.Errorf("remote_updated_at = %v, want %q", state.RemoteUpdatedAt, testCase.wantRemoteUpdate)
			}
		})
	}
}

// TestContextEnvVarDrift_ValueChangedOutsideTerraform is the end-to-end proof:
// somebody rotates the secret in the CircleCI UI, and the next plan offers to
// put the configured value back.
//
// The middle PlanOnly step is load-bearing: it is the first read after create,
// and it must be silent. A drift check that compared the API's timestamp against
// the *previous* API timestamp rather than against our own write would report
// drift here, on a resource nobody has touched.
func TestContextEnvVarDrift_ValueChangedOutsideTerraform(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextEnvVarUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")

	var stored string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextEnvVarResourceUnitConfig(host, "s3cr3t"),
				ConfigStateChecks: []statecheck.StateCheck{
					// Both timestamps come from our own write, so the refresh at the
					// start of the next step has nothing to report.
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("updated_at"), knownvalue.StringExact("2024-06-01T00:00:00.000Z")),
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("remote_updated_at"), knownvalue.StringExact("2024-06-01T00:00:00.000Z")),
				},
			},
			{
				// No external change: the first read after create must be quiet.
				Config:   contextEnvVarResourceUnitConfig(host, "s3cr3t"),
				PlanOnly: true,
			},
			{
				PreConfig:          func() { rotateEnvVarOutsideTerraform(api) },
				Config:             contextEnvVarResourceUnitConfig(host, "s3cr3t"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Applying re-asserts the configured value and re-pins both timestamps.
				Config: contextEnvVarResourceUnitConfig(host, "s3cr3t"),
				Check:  snapshotStoredValue(api, &stored),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						// Updated, not replaced: the value is rewritten in place.
						plancheck.ExpectResourceAction("circleci_context_environment_variable.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("value"), knownvalue.StringExact("s3cr3t")),
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("updated_at"), knownvalue.StringExact("2024-06-02T00:00:00.000Z")),
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("remote_updated_at"), knownvalue.StringExact("2024-06-02T00:00:00.000Z")),
				},
			},
		},
	})

	if stored != "s3cr3t" {
		t.Errorf("value at the API after re-asserting = %q, want %q", stored, "s3cr3t")
	}
}

// TestContextEnvVarDrift_WriteOnlyPath proves drift detection works on the
// write-only path too, where there is no `value` in state to clear: the
// persisted `value_wo_version` is cleared instead, and the apply reads the
// value back out of configuration.
func TestContextEnvVarDrift_WriteOnlyPath(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextEnvVarUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")

	var stored string

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: contextEnvVarWriteOnlyConfig(host, "s3cr3t", 1)},
			{
				Config:   contextEnvVarWriteOnlyConfig(host, "s3cr3t", 1),
				PlanOnly: true,
			},
			{
				PreConfig:          func() { rotateEnvVarOutsideTerraform(api) },
				Config:             contextEnvVarWriteOnlyConfig(host, "s3cr3t", 1),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: contextEnvVarWriteOnlyConfig(host, "s3cr3t", 1),
				Check:  snapshotStoredValue(api, &stored),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("value_wo_version"), knownvalue.Int64Exact(1)),
				},
			},
		},
	})

	if stored != "s3cr3t" {
		t.Errorf("value at the API after re-asserting = %q, want %q", stored, "s3cr3t")
	}
}

// --- helpers ----------------------------------------------------------------

func contextEnvVarResourceSchemaForTest(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	(&contextEnvironmentVariableResource{}).Schema(t.Context(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema diagnostics: %+v", resp.Diagnostics)
	}

	return resp.Schema
}

// configForTest builds a tfsdk.Config from a model, for the tests that drive a
// resource's Go methods without a Terraform CLI. It goes via tfsdk.State
// because only State and Plan expose Set; the raw value is the same either way.
func configForTest[M any](t *testing.T, schema rschema.Schema, model M) tfsdk.Config {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a config value: %+v", diags)
	}

	return tfsdk.Config{Schema: schema, Raw: state.Raw}
}
