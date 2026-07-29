// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// These tests back circleci_project (project_resource.go) with an in-process
// fake (project_fake_test.go) instead of a real CircleCI account, so they run
// without TF_ACC or credentials. Before this file, the resource had only
// resource.Test acceptance tests gated on CIRCLE_TOKEN, so its Create, Read,
// Update and Delete methods ran in CI only when a fixture account was
// configured.
//
// Most scenarios below drive the resource through resource.UnitTest with a
// real Terraform configuration rather than by hand-constructing a plan,
// because this resource's toggles are Optional+Computed: Terraform core
// plans an omitted Optional+Computed attribute as *unknown* when the resource
// is being created (there is no prior state to fall back to), not as null.
// That distinction turns out to matter a great deal here — see
// TestProjectResourceUnit_CreateClassicOrgAppliesSettings.

func projectResourceProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
`, host)
}

func projectResourceConfig(host, name, extraAttrs string) string {
	return projectResourceProviderConfig(host) + fmt.Sprintf(`
resource "circleci_project" "test" {
  name            = %[1]q
  organization_id = %[2]q
%[3]s
}
`, name, testProjectResourceOrgID, extraAttrs)
}

const testProjectResourceOrgID = "org-11111111"

// TestProjectResourceUnit_CreateClassicOrgAppliesSettings covers the classic
// (GitHub/Bitbucket) organization branch of project creation: the v2 create
// call must be followed by a v1.1 follow call (known bug history item 3), and
// every configured setting must reach the settings PATCH unquoted (bug history
// item 1).
//
// It also used to document a bug found alongside those: every
// "if !plan.X.IsNull()" guard in Create was dead code, because an
// Optional+Computed attribute the configuration omits is *unknown* at Create
// time rather than null. IsNull() is false for an unknown value, so every guard
// was taken whatever the configuration said, and ValueBoolPointer() on an
// unknown value yields a pointer to false — so every toggle was written as
// false on every create. That has been fixed: the guards now check IsUnknown()
// too (projectSettingRequest), an unconfigured toggle is left out of the
// request, and CircleCI's own default applies. The assertion at the end of this
// test is what holds that fix in place.
func TestProjectResourceUnit_CreateClassicOrgAppliesSettings(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectResourceConfig(host, "my-repo", `
  build_prs_only           = true
  pr_only_branch_overrides = ["main", "release/1.x"]
`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("slug"), knownvalue.StringExact("gh/AcmeOrg/my-repo")),
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("organization_name"), knownvalue.StringExact("AcmeOrg")),
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("build_prs_only"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(
						"circleci_project.test",
						tfjsonpath.New("pr_only_branch_overrides"),
						knownvalue.ListExact([]knownvalue.Check{
							knownvalue.StringExact("main"),
							knownvalue.StringExact("release/1.x"),
						}),
					),
				},
			},
		},
	})

	// --- bug history item 3: the v1.1 follow call, for a classic organization ---
	followed := api.followedCalls()
	if len(followed) != 1 {
		t.Fatalf("follow calls = %v, want exactly 1 for a classic organization", followed)
	}
	if followed[0] != "github/AcmeOrg/my-repo" {
		t.Errorf("follow call = %q, want %q", followed[0], "github/AcmeOrg/my-repo")
	}

	requests := api.recordedRequests()
	var sawCreate bool
	for _, req := range requests {
		if req == "POST /api/v2/organization/"+testProjectResourceOrgID+"/project" {
			sawCreate = true
		}
	}
	if !sawCreate {
		t.Errorf("no create request seen, got %v", requests)
	}

	// --- bug history item 1: branch names must reach the wire unquoted ---
	body := api.onlyPatch(t)
	branches, ok := body["pr_only_branch_overrides"].([]any)
	if !ok {
		t.Fatalf("pr_only_branch_overrides sent as %T, want a list", body["pr_only_branch_overrides"])
	}
	if len(branches) != 2 || branches[0] != "main" || branches[1] != "release/1.x" {
		t.Errorf(`pr_only_branch_overrides sent as %v, want ["main" "release/1.x"] unquoted`, branches)
	}

	// --- the dead-code bug described above, now fixed ---
	//
	// This configuration says nothing about forks_receive_secret_env_vars, so the
	// setting must be absent from the request body entirely. It used to be sent as
	// false, which is what the unreachable "else" branch and this assertion once
	// documented. Sending anything at all here would decide a security-relevant
	// setting on the practitioner's behalf; leaving it out lets CircleCI apply its
	// own default, which is true on a private project.
	if got, ok := body["forks_receive_secret_env_vars"]; ok {
		t.Errorf(
			"forks_receive_secret_env_vars sent as %v for a configuration that never mentions it, "+
				"want it absent from the body so CircleCI's own default applies",
			got,
		)
	}

	// Only the two configured settings, and never oss: it is read-only, and the
	// API rejects the whole request when it is present.
	assertPatchKeys(t, body, "build_prs_only", "pr_only_branch_overrides")
}

// TestProjectResourceUnit_CreateNeverSendsOSS is the regression test for the bug
// that broke every project create against the real API.
//
// oss is read-only on v2. The settings PATCH answers
// 400 "Unexpected field 'advanced.oss'." and rejects the *whole* request, so
// creating a project always failed at the settings step even though the project
// itself had been created. Every mocked test passed regardless, because the fake
// accepted the field; it now rejects it the way the real API does, so a
// regression fails here twice: on the key assertion and on the create itself.
func TestProjectResourceUnit_CreateNeverSendsOSS(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectResourceConfig(host, "my-repo", `
  auto_cancel_builds            = true
  build_fork_prs                = true
  forks_receive_secret_env_vars = false
`),
				ConfigStateChecks: []statecheck.StateCheck{
					// oss is still reported, read from the API into state.
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("oss"), knownvalue.Bool(false)),
				},
			},
		},
	})

	body := api.onlyPatch(t)
	if got, ok := body["oss"]; ok {
		t.Errorf("PATCH body carried oss (%v); it is read-only and the API rejects the whole request for it", got)
	}
	assertPatchKeys(t, body, "autocancel_builds", "build_fork_prs", "forks_receive_secret_env_vars")
}

// TestProjectResourceUnit_CreateRejectsConfiguredOSS proves oss cannot be set from
// a configuration at all: it is Computed-only, so Terraform itself refuses before
// the provider is asked to write anything.
func TestProjectResourceUnit_CreateRejectsConfiguredOSS(t *testing.T) {
	_, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      projectResourceConfig(host, "my-repo", `  oss = true`),
				ExpectError: regexp.MustCompile(`(?s)Invalid Configuration for Read-Only Attribute.*oss`),
			},
		},
	})
}

// TestProjectResourceUnit_CreateOmitsUnconfiguredToggles is the core regression
// test for the Optional+Computed guard bug.
//
// An omitted toggle must be absent from the request body rather than sent as
// false, and a configured one must be sent with the value asked for — including an
// explicit false, which is indistinguishable from "unset" once it reaches a plain
// bool. set_github_status is the setting that makes the difference visible: the
// fake defaults it to true, the way CircleCI does, so an unconfigured project must
// read back true instead of the false the old code forced.
func TestProjectResourceUnit_CreateOmitsUnconfiguredToggles(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectResourceConfig(host, "my-repo", `
  auto_cancel_builds = true
  disable_ssh        = false
`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("auto_cancel_builds"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("disable_ssh"), knownvalue.Bool(false)),
					// Never configured, so never sent: state reports CircleCI's own
					// defaults, read back from the settings response.
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("set_github_status"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("setup_workflows"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("forks_receive_secret_env_vars"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(
						"circleci_project.test",
						tfjsonpath.New("pr_only_branch_overrides"),
						knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("main")}),
					),
				},
			},
		},
	})

	body := api.onlyPatch(t)

	// Exactly the two configured settings, and nothing else. Every other key would
	// be a value nobody asked the provider to decide.
	assertPatchKeys(t, body, "autocancel_builds", "disable_ssh")

	if got := body["autocancel_builds"]; got != true {
		t.Errorf("autocancel_builds sent as %v, want true", got)
	}

	// An explicitly configured false must survive: it is a value the practitioner
	// chose, not an absence.
	if got, ok := body["disable_ssh"]; !ok || got != false {
		t.Errorf("disable_ssh sent as %v (present: %t), want false", got, ok)
	}
}

// TestProjectResourceUnit_CreateWithNoSettingsWritesNothing covers a project
// created with no settings at all. There is nothing to write, and the API rejects
// a body with no fields ("No JSON fields found."), so no PATCH must be sent — and
// state must still hold a known value for every Computed toggle, read from the
// project's existing settings.
func TestProjectResourceUnit_CreateWithNoSettingsWritesNothing(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectResourceConfig(host, "my-repo", ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("set_github_status"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("auto_cancel_builds"), knownvalue.Bool(false)),
				},
			},
		},
	})

	if patches := api.recordedPatches(); len(patches) != 0 {
		t.Errorf("the provider sent %d settings PATCH requests for a project that configures none, want 0: %v", len(patches), patches)
	}
}

// TestProjectResourceUnit_RequiresExplicitForkSecrets covers the one dangerous
// default that omitting unset settings exposes, on circleci_project.
//
// forks_receive_secret_env_vars is true on a private project when it was never
// set, so enabling fork builds without naming it would hand the project's secrets
// to anyone who can open a pull request. The validator fires at validate time, so
// the project is never even created.
func TestProjectResourceUnit_RequiresExplicitForkSecrets(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      projectResourceConfig(host, "my-repo", `  build_fork_prs = true`),
				ExpectError: regexp.MustCompile(`(?s)forks_receive_secret_env_vars.*would receive the project's secrets`),
			},
		},
	})

	if requests := api.recordedRequests(); len(requests) != 0 {
		t.Errorf("the provider made %v, want none: the configuration never passed validation", requests)
	}
}

// TestProjectResourceUnit_CreateStandaloneOrgSkipsFollow covers the other half
// of bug history item 3: a standalone (circleci/<uuid>) organization already
// follows the project as part of creating it, so the provider must not also
// send a v1.1 follow request.
func TestProjectResourceUnit_CreateStandaloneOrgSkipsFollow(t *testing.T) {
	api, host := newFakeProjectAPI(t, "standalone")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectResourceConfig(host, "my-repo", ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("vcs_info_provider"), knownvalue.StringExact("CircleCI")),
				},
			},
		},
	})

	if followed := api.followedCalls(); len(followed) != 0 {
		t.Errorf("follow calls = %v, want none for a standalone organization", followed)
	}
}

// TestProjectResourceUnit_UpdateSendsBuildPrsOnly is the regression test for known
// bug history item 4, now fixed.
//
// Update's settings payload had no BuildPrsOnly field at all, while every other
// toggle was present — so changing build_prs_only on an existing project was
// silently never sent. Update then wrote state from whatever the API reported,
// which was still the old value, so the state it saved contradicted the plan it had
// promised and Terraform failed the apply with "provider produced inconsistent
// result after apply". A user would see a confusing provider bug rather than the
// real cause, which was one missing struct field.
//
// Asserting on the request body as well as on state matters here: state agreeing
// with the plan is necessary but not sufficient, since the whole failure mode was
// state and the API disagreeing.
func TestProjectResourceUnit_UpdateSendsBuildPrsOnly(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectResourceConfig(host, "my-repo", `  build_prs_only = true`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("build_prs_only"), knownvalue.Bool(true)),
				},
			},
			{
				// Turning it off must reach the API and survive the read-back. Before
				// the fix this step failed the apply outright.
				Config: projectResourceConfig(host, "my-repo", `  build_prs_only = false`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("build_prs_only"), knownvalue.Bool(false)),
				},
			},
		},
	})

	// The last settings PATCH must carry build_prs_only:false explicitly. An absent
	// key would leave the project unchanged and is exactly the bug.
	patches := api.recordedPatches()
	if len(patches) == 0 {
		t.Fatal("no settings PATCH recorded")
	}

	last := patches[len(patches)-1]
	got, present := last["build_prs_only"]
	if !present {
		t.Errorf("final settings PATCH omitted build_prs_only (body: %v); the update would "+
			"silently not happen", last)
	} else if got != false {
		t.Errorf("final settings PATCH sent build_prs_only = %v, want false", got)
	}
}

// TestProjectResourceUnit_NameChangeForcesReplacement proves the
// RequiresReplace plan modifier on `name` is actually wired up.
//
// This is worth a dedicated test because the identical omission is a live bug on
// circleci_pipeline: its `project_id` has no RequiresReplace, so changing it plans
// an in-place update and the PATCH lands on the wrong project — see
// TestPipelineResourceUnit_ProjectIDChangeIsNotForcedReplacement. There is no API
// route that renames a project, so if this modifier were ever dropped the provider
// would silently plan an update it cannot perform.
func TestProjectResourceUnit_NameChangeForcesReplacement(t *testing.T) {
	_, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: projectResourceConfig(host, "my-repo", "")},
			{
				Config: projectResourceConfig(host, "renamed-repo", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_project.test",
							plancheck.ResourceActionDestroyBeforeCreate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact("renamed-repo"),
					),
				},
			},
		},
	})
}

// item 1's regression test on the update path specifically (branchOverrides
// itself is already covered unit-by-unit in project_branch_overrides_test.go;
// this proves the resource's Update method actually uses it).
func TestProjectResourceUnit_UpdateSendsUnquotedBranchOverrides(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: projectResourceConfig(host, "my-repo", "")},
			{
				// "develop" rather than "main" on purpose: the fake defaults the
				// overrides to the default branch, the way the real API does, so
				// asking for ["main"] would plan no change at all and send nothing.
				Config: projectResourceConfig(host, "my-repo", `  pr_only_branch_overrides = ["develop"]`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project.test",
						tfjsonpath.New("pr_only_branch_overrides"),
						knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("develop")}),
					),
				},
			},
		},
	})

	patches := api.recordedPatches()
	if len(patches) == 0 {
		t.Fatal("no settings PATCH requests were sent")
	}

	last := patches[len(patches)-1]
	branches, ok := last["pr_only_branch_overrides"].([]any)
	if !ok || len(branches) != 1 || branches[0] != "develop" {
		t.Errorf(`last PATCH pr_only_branch_overrides = %v, want ["develop"] unquoted`, last["pr_only_branch_overrides"])
	}
}

// TestProjectResourceUnit_Destroy checks that destroying the resource issues
// the v2 delete call against the project's slug.
func TestProjectResourceUnit_Destroy(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: projectResourceConfig(host, "my-repo", "")},
		},
	})

	var sawDelete bool
	for _, req := range api.recordedRequests() {
		if req == "DELETE /api/v2/project/gh/AcmeOrg/my-repo" {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Errorf("no delete request seen, got %v", api.recordedRequests())
	}
}

// TestProjectResourceUnit_Import covers importing by slug, which is all
// ImportState sets (project_resource.go's ImportState).
func TestProjectResourceUnit_Import(t *testing.T) {
	_, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: projectResourceConfig(host, "my-repo", "")},
			{
				ResourceName:      "circleci_project.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateId:     "gh/AcmeOrg/my-repo",
			},
		},
	})
}

// TestProjectResourceUnit_ImportMalformedSlugFailsCleanly imports an ID that
// is not a valid slug. ImportState itself (project_resource.go's ImportState)
// performs no validation at all — it hands req.ID straight to
// SetAttribute(slug) — so it is the framework-triggered Read that follows
// import which must catch this via parseProjectSlug, rather than panicking on
// an unguarded strings.Split (bug history item 2).
func TestProjectResourceUnit_ImportMalformedSlugFailsCleanly(t *testing.T) {
	_, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: projectResourceConfig(host, "my-repo", "")},
			{
				// The error comes from circleci.GetProject's own segment-count
				// guard (projectSlugPath), not from this package's
				// parseProjectSlug: project_resource.go's Read calls GetProject
				// before it ever calls parseProjectSlug, so a malformed slug never
				// reaches the latter. Either way, the result is a clean
				// diagnostic rather than the historical panic.
				ResourceName:  "circleci_project.test",
				ImportState:   true,
				ImportStateId: "not-a-valid-slug",
				ExpectError:   regexp.MustCompile(`expected three segments`),
			},
		},
	})
}

// TestProjectResourceUnit_DriftMissingProjectErrorsInsteadOfRecreating is a bug
// report, not a demonstration of correct behaviour.
//
// checkout_key_resource.go and project_settings_resource.go both drop a
// resource from state on a 404 read (via circleci.IsNotFound), which lets
// Terraform plan a clean recreate. project_resource.go's Read
// (project_resource.go:338-345) does not: it maps every GetProject error,
// 404 included, to resp.Diagnostics.AddError. So a circleci_project whose
// project was deleted outside Terraform fails every plan and apply forever
// instead of being offered for recreation.
//
// This test asserts the actual (arguably wrong) current behaviour, because
// that is what a test in this suite must do — a test asserting the *better*
// behaviour would fail. See the task's bug report for the fix this documents
// the need for.
func TestProjectResourceUnit_DriftMissingProjectErrorsInsteadOfRecreating(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: projectResourceConfig(host, "my-repo", "")},
			{
				PreConfig:   func() { api.setMissing(true) },
				Config:      projectResourceConfig(host, "my-repo", ""),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Unable to Read CircleCI project`),
			},
			{
				// Restore the project so TestCase's own destroy step succeeds.
				PreConfig: func() { api.setMissing(false) },
				Config:    projectResourceConfig(host, "my-repo", ""),
			},
		},
	})
}

// --- direct-call tests ---
//
// These drive the resource's Go methods directly rather than through
// Terraform, for the cases that do not depend on how Terraform core plans an
// Optional+Computed attribute: reading or updating from a hand-built prior
// state, where every field is known (state never holds unknown values), and
// Create's use of the API's own (attacker- or bug-controlled) response rather
// than anything the practitioner configured.

func projectResourceSchemaForTest(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	(&projectResource{}).Schema(t.Context(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema diagnostics: %+v", resp.Diagnostics)
	}

	return resp.Schema
}

func projectResourceStateForTest(t *testing.T, schema rschema.Schema, model projectResourceModel) tfsdk.State {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a state value: %+v", diags)
	}

	return state
}

// minimalProjectModel returns a model for the tests that drive the resource's Go
// methods directly. The name is fixed because those tests assert on slug parsing
// and guard behaviour, where the project name is not the variable under test.
func minimalProjectModel(slug string) projectResourceModel {
	return projectResourceModel{
		OrganizationId:             types.StringValue(testProjectResourceOrgID),
		Name:                       types.StringValue("my-repo"),
		Slug:                       types.StringValue(slug),
		Id:                         types.StringValue("id"),
		OrganizationName:           types.StringValue("org-name"),
		OrganizationSlug:           types.StringValue("org-slug"),
		VcsInfoUrl:                 types.StringValue("url"),
		VcsInfoProvider:            types.StringValue("GitHub"),
		VcsInfoDefaultBranch:       types.StringValue("main"),
		AutoCancelBuilds:           types.BoolNull(),
		BuildForkPrs:               types.BoolNull(),
		BuildPrsOnly:               types.BoolNull(),
		DisableSSH:                 types.BoolNull(),
		ForksReceiveSecretEnvVars:  types.BoolNull(),
		OSS:                        types.BoolNull(),
		SetGithubStatus:            types.BoolNull(),
		SetupWorkflows:             types.BoolNull(),
		WriteSettingsRequiresAdmin: types.BoolNull(),
		PROnlyBranchOverrides:      types.ListNull(types.StringType),
	}
}

// TestProjectResourceUnit_CreateGuardsMalformedSlugFromAPI covers bug history
// item 2 on the Create path: if the API ever answered with a slug that does
// not have exactly three segments, parseProjectSlug must turn that into a
// diagnostic rather than let the historical unguarded strings.Split panic the
// provider process.
func TestProjectResourceUnit_CreateGuardsMalformedSlugFromAPI(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectClient(t)
	api.setForceSlug("not-a-valid-slug")

	schema := projectResourceSchemaForTest(t)
	plan := minimalProjectModel("")
	plan.Id = types.StringUnknown()
	plan.Slug = types.StringUnknown()
	plan.OrganizationName = types.StringUnknown()
	plan.OrganizationSlug = types.StringUnknown()
	plan.VcsInfoUrl = types.StringUnknown()
	plan.VcsInfoProvider = types.StringUnknown()
	plan.VcsInfoDefaultBranch = types.StringUnknown()

	r := &projectResource{client: client}
	resp := &fwresource.CreateResponse{State: projectResourceStateForTest(t, schema, minimalProjectModel("placeholder"))}

	assertNoPanic(t, func() {
		r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{
			Schema: schema,
			Raw:    projectResourceStateForTest(t, schema, plan).Raw,
		}}, resp)
	})

	if !resp.Diagnostics.HasError() {
		t.Fatal("Create accepted a malformed slug from the API, want an error")
	}
}

// TestProjectResourceUnit_ReadRejectsBadSlug covers the Read-side guard
// (project_resource.go:358) directly: a state slug the resource itself would
// never have written (only possible via a bad import or manual state edit)
// must fail cleanly with no request sent.
func TestProjectResourceUnit_ReadRejectsBadSlug(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectClient(t)
	schema := projectResourceSchemaForTest(t)
	state := projectResourceStateForTest(t, schema, minimalProjectModel("two/segments"))

	r := &projectResource{client: client}
	resp := &fwresource.ReadResponse{State: state}

	assertNoPanic(t, func() {
		r.Read(t.Context(), fwresource.ReadRequest{State: state}, resp)
	})

	if !resp.Diagnostics.HasError() {
		t.Fatal("Read accepted a two-segment slug, want an error")
	}
	if requests := api.recordedRequests(); len(requests) != 0 {
		t.Errorf("Read made %v, want no request for an invalid slug", requests)
	}
}

// TestProjectResourceUnit_UpdateRejectsBadSlug is the Update-side counterpart
// (project_resource.go:432).
func TestProjectResourceUnit_UpdateRejectsBadSlug(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectClient(t)
	schema := projectResourceSchemaForTest(t)

	badState := minimalProjectModel("two/segments")
	badState.AutoCancelBuilds = types.BoolValue(false)
	badState.BuildForkPrs = types.BoolValue(false)
	badState.BuildPrsOnly = types.BoolValue(false)
	badState.DisableSSH = types.BoolValue(false)
	badState.ForksReceiveSecretEnvVars = types.BoolValue(true)
	badState.OSS = types.BoolValue(false)
	badState.SetGithubStatus = types.BoolValue(false)
	badState.SetupWorkflows = types.BoolValue(false)
	badState.WriteSettingsRequiresAdmin = types.BoolValue(false)
	badState.PROnlyBranchOverrides = types.ListValueMust(types.StringType, nil)

	state := projectResourceStateForTest(t, schema, badState)
	plan := badState
	plan.AutoCancelBuilds = types.BoolValue(true)
	planState := projectResourceStateForTest(t, schema, plan)

	r := &projectResource{client: client}
	resp := &fwresource.UpdateResponse{State: state}

	assertNoPanic(t, func() {
		r.Update(t.Context(), fwresource.UpdateRequest{
			Plan:  tfsdk.Plan{Schema: schema, Raw: planState.Raw},
			State: state,
		}, resp)
	})

	if !resp.Diagnostics.HasError() {
		t.Fatal("Update accepted a two-segment slug, want an error")
	}
	if requests := api.recordedRequests(); len(requests) != 0 {
		t.Errorf("Update made %v, want no request for an invalid slug", requests)
	}
}

// assertNoPanic runs fn and fails the test with the recovered value if it
// panics, rather than letting the panic crash the whole test binary — which
// is exactly what bug history item 2 used to do to the provider process.
func assertNoPanic(t *testing.T, fn func()) {
	t.Helper()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked: %v", r)
		}
	}()

	fn()
}

// TestProjectResourceUnit_ErrorMapping checks that a non-404 API failure
// surfaces as a diagnostic rather than propagating a raw error or panicking.
//
// It also documents a smaller finding alongside the drift bug above:
// project_resource.go's Read (and Create, Update and Delete) builds the
// diagnostic detail from err.Error() directly, rather than from
// circleci.Detail(err) the way project_settings_resource.go and
// environment_variables_data_source.go do. err.Error() on an HTTP error is
// only the status line ("GET ...: 404 Not Found"); circleci.Detail(err) would
// have included the API's own "Project not found" body message. Nothing
// panics and nothing is silently swallowed, so this is a UX/diagnostics
// quality gap rather than a correctness bug — CircleCI's error message is
// simply not shown.
func TestProjectResourceUnit_ErrorMapping(t *testing.T) {
	t.Parallel()

	// No project seeded, and missingProject is unset, so a Read for a slug
	// that never existed answers a plain 404 ("Project not found").
	_, client := newFakeProjectClient(t)

	schema := projectResourceSchemaForTest(t)
	state := projectResourceStateForTest(t, schema, minimalProjectModel("gh/Nobody/nothing"))

	r := &projectResource{client: client}
	resp := &fwresource.ReadResponse{State: state}

	assertNoPanic(t, func() {
		r.Read(t.Context(), fwresource.ReadRequest{State: state}, resp)
	})

	if !resp.Diagnostics.HasError() {
		t.Fatal("Read against a nonexistent project reported no error")
	}

	detail := resp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(detail, "404") {
		t.Errorf("error detail = %q, want it to at least mention the HTTP status", detail)
	}
	// This is the finding, not an assertion of good behaviour: the API's own
	// "Project not found" message never makes it into the diagnostic, because
	// Read uses err.Error() instead of circleci.Detail(err). If that is ever
	// fixed, this should become a positive assertion that the message *is*
	// present.
	if strings.Contains(detail, "Project not found") {
		t.Errorf(
			"error detail = %q unexpectedly contains the API's message; project_resource.go's Read has "+
				"apparently started using circleci.Detail(err) — update this test (and its comment) to assert "+
				"that positively instead",
			detail,
		)
	}
}

// projectResourceSchemaValidate is a light sanity check that the schema this
// file exercises by hand is internally consistent.
func TestProjectResourceUnit_Schema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	schema := projectResourceSchemaForTest(t)
	if diags := schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("Schema validation diagnostics: %+v", diags)
	}
}
