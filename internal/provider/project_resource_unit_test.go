// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
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

	"terraform-provider-circleci/internal/circleci"
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
						knownvalue.SetExact([]knownvalue.Check{
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
						knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("main")}),
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

// TestProjectResourceUnit_RejectsEmptyBranchOverrides is circleci_project's half
// of the plan-time guard added alongside circleci_project_settings' identical
// one: `pr_only_branch_overrides = []` is rejected before anything is sent,
// because the API answers it with HTTP 200 and silently keeps the branches
// already configured rather than clearing them — live-confirmed behaviour, see
// noClearingBranchOverridesValidator (project_settings_resource.go).
func TestProjectResourceUnit_RejectsEmptyBranchOverrides(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      projectResourceConfig(host, "my-repo", `  pr_only_branch_overrides = []`),
				ExpectError: regexp.MustCompile(`(?s)Cannot clear pr_only_branch_overrides.*does not support clearing`),
			},
		},
	})

	if requests := api.recordedRequests(); len(requests) != 0 {
		t.Errorf("got requests %v, want none: the configuration never passed validation", requests)
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
						knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("develop")}),
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

// TestProjectResourceUnit_CreateOnAClassicOrgExplainsAMissingRepository covers
// the diagnostic half of BUG P4.
//
// A practitioner pointing circleci_project at a classic, VCS-backed organization
// with a name that has no repository behind it used to get exactly this, and
// nothing else:
//
//	Could not create CircleCI project, unexpected error: POST
//	/api/v2/organization/<uuid>/project: 404 Not Found
//
// — observed over the network. Not even the API's own "GitHub response: Not
// Found" survived, because Create passed err.Error() through instead of
// circleci.Detail(err), and nothing said that a missing repository is what a 404
// means here or that adoption is the only thing this route does on a classic
// organization.
func TestProjectResourceUnit_CreateOnAClassicOrgExplainsAMissingRepository(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")
	api.setMissingRepository(true)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectResourceConfig(host, "no-such-repo", ""),
				// One word, so Terraform's line wrapping cannot break the match.
				// TestProjectCreateFailureDetail below asserts the whole message.
				ExpectError: regexp.MustCompile(`ADOPT`),
			},
		},
	})

	// And nothing was created or followed on the way out.
	if followed := api.followedCalls(); len(followed) != 0 {
		t.Errorf("follow calls = %v, want none: the create failed", followed)
	}
}

// TestProjectResourceUnit_CreateOnAClassicOrgExplainsAnAlreadyAdoptedRepository
// is TestProjectResourceUnit_CreateOnAClassicOrgExplainsAMissingRepository's
// counterpart for the OTHER failure a practitioner on a classic organization
// actually hits: adopting a repository that is already a CircleCI project.
//
// The fake models this the same way the real API answers it (see
// fakeProjectAPI.handleCreate): a second create for a name it already holds a
// classic project under answers 409 with the API's own message, which says
// nothing about import. Unlike the 404 case, "ADOPT" alone would not
// distinguish this diagnostic from the missing-repository one above, so this
// asserts on "terraform import" instead — the one phrase that is unique to
// the 409 hint and is also the actual instruction a practitioner needs.
func TestProjectResourceUnit_CreateOnAClassicOrgExplainsAnAlreadyAdoptedRepository(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")

	// Two resources naming the SAME repository, with the second depending on
	// the first, in one apply: Terraform then creates "first" (which succeeds
	// and adopts the repository), then "second" (which conflicts with it) --
	// deterministically, rather than leaving the two independent and letting
	// Terraform choose an order. A second, separate resource.UnitTest call
	// would not reproduce this at all: resource.UnitTest destroys everything
	// it created before returning, so a repeat create in a following run
	// would find the repository unadopted again and simply succeed.
	config := projectResourceProviderConfig(host) + fmt.Sprintf(`
resource "circleci_project" "first" {
  name            = %[1]q
  organization_id = %[2]q
}

resource "circleci_project" "second" {
  name            = %[1]q
  organization_id = %[2]q
  depends_on      = [circleci_project.first]
}
`, "already-adopted", testProjectResourceOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				// One word, so terraform's own line-wrapping (which turns an
				// ordinary space into a newline at a column width it chooses)
				// cannot split it -- see the comment on the (?s) regex a few
				// tests down for where a two-word version of exactly this
				// mistake was caught live. TestProjectCreateFailureDetail
				// below asserts the whole message.
				ExpectError: regexp.MustCompile(`\bimport\b`),
			},
		},
	})

	// Exactly one follow call resulted from the whole apply -- from
	// circleci_project.first's successful adopt. followedCalls is an
	// append-only history, unlike the fake's live project map, so this is
	// still observable after resource.UnitTest's own automatic destroy of
	// whatever ended up in state (circleci_project.first) has already run.
	// The conflicting circleci_project.second must not have added a second
	// entry.
	if followed := api.followedCalls(); len(followed) != 1 {
		t.Errorf("follow calls = %v, want exactly 1 (from the first, successful adopt)", followed)
	}
}

// TestProjectResourceUnit_CreateOrphanedByFollowFailureNamesTheSlug drives BUG
// P8's fix through the actual resource, not just projectCreateFailureDetail in
// isolation: a create whose v2 call succeeds but whose v1.1 follow call does
// not (see the fake's setFailFollowStatus, and followProject's own doc
// comment in internal/circleci/project.go for the live measurement this
// models) must fail with a diagnostic that names the project's slug and
// points at `terraform import`, rather than leaving the practitioner with no
// way to find the project CircleCI is now tracking.
func TestProjectResourceUnit_CreateOrphanedByFollowFailureNamesTheSlug(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")
	api.setFailFollowStatus(http.StatusBadRequest)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectResourceConfig(host, "no-commits-yet", ""),
				// (?s), and "import" alone rather than the two-word phrase
				// "terraform import": terraform's own line-wrapping can turn
				// an ordinary space into a newline (see
				// TestAccGithubProjectResourceRepoNotFound's own comment on
				// this, in project_resource_test.go, for where that was
				// caught live). The slug the fake's v2 create call already
				// returned, and the instruction to act on it.
				// TestProjectCreateFailureDetail asserts the whole message.
				ExpectError: regexp.MustCompile(`(?s)gh/AcmeOrg/no-commits-yet.*\bimport\b`),
			},
		},
	})

	// The v2 create call did happen (that is the whole scenario), but no
	// follow ever succeeded.
	api.mu.Lock()
	projectCount := len(api.projects)
	api.mu.Unlock()

	if projectCount != 1 {
		t.Errorf("fake ended up holding %d project(s), want exactly 1 (the v2 create succeeded even "+
			"though the follow after it did not)", projectCount)
	}
	if followed := api.followedCalls(); len(followed) != 0 {
		t.Errorf("follow calls = %v, want none: every one was made to fail", followed)
	}
}

// TestProjectCreateFailureDetail asserts the whole diagnostic, as a pure
// function, away from Terraform's line wrapping.
//
// The error it feeds in is a real one: the fake answers the create the way the
// API does, so what is under test is the rendering rather than a hand-built
// error value that might not resemble one.
func TestProjectCreateFailureDetail(t *testing.T) {
	t.Parallel()

	t.Run("a 404 explains the classic-organization precondition", func(t *testing.T) {
		t.Parallel()

		api, client := newFakeProjectClient(t)
		api.setMissingRepository(true)

		_, err := client.CreateProject(t.Context(), testProjectResourceOrgID, "no-such-repo")
		if err == nil {
			t.Fatal("CreateProject succeeded; the fake was asked to answer 404")
		}

		detail := projectCreateFailureDetail("no-such-repo", nil, err)

		for _, want := range []string{
			// The API's own message, which err.Error() alone dropped.
			"GitHub response: Not Found",
			// Both behaviours, so the message is true whichever class the
			// organization turns out to be.
			"standalone",
			"repository-less project",
			"can only ADOPT a repository that already exists",
			// The name, so the practitioner knows which repository to look for.
			`"no-such-repo"`,
		} {
			if !strings.Contains(detail, want) {
				t.Errorf("the diagnostic does not mention %q; got:\n%s", want, detail)
			}
		}
	})

	t.Run("a non-404 gets the message without the hint", func(t *testing.T) {
		t.Parallel()

		api, client := newFakeProjectClient(t)
		api.setFailSettingsStatus(http.StatusInternalServerError)

		// A 500 from the settings route, reused here purely as a non-404 API
		// error: the hint is about a missing repository and would be noise on
		// anything but a 404.
		_, err := client.GetProjectSettings(t.Context(), "gh", "AcmeOrg", "my-repo")
		if err == nil {
			t.Fatal("GetProjectSettings succeeded; the fake was asked to answer 500")
		}

		detail := projectCreateFailureDetail("my-repo", nil, err)

		if strings.Contains(detail, "ADOPT") {
			t.Errorf("a non-404 error carried the missing-repository hint, which does not apply to "+
				"it; got:\n%s", detail)
		}
		if !strings.Contains(detail, "forced failure for test") {
			t.Errorf("the API's own message did not reach the diagnostic; got:\n%s", detail)
		}
	})

	t.Run("a 409 explains adopting an already-adopted repository, and points at import", func(t *testing.T) {
		t.Parallel()

		_, client := newFakeProjectClient(t)

		// First adopt succeeds; the second, of the same name, is the conflict
		// under test -- see fakeProjectAPI.handleCreate's alreadyAdopted branch.
		if _, err := client.CreateProject(t.Context(), testProjectResourceOrgID, "already-adopted"); err != nil {
			t.Fatalf("first CreateProject failed, want it to succeed so the second can conflict: %v", err)
		}

		_, err := client.CreateProject(t.Context(), testProjectResourceOrgID, "already-adopted")
		if err == nil {
			t.Fatal("second CreateProject succeeded; the fake was asked to answer 409 for a repeat name")
		}
		if !circleci.IsConflict(err) {
			t.Fatalf("second CreateProject error = %v, want one satisfying circleci.IsConflict", err)
		}

		detail := projectCreateFailureDetail("already-adopted", nil, err)

		for _, want := range []string{
			// The API's own message, which err.Error() alone drops the same way
			// it did for the 404 case.
			"Cannot create project since a project with the same name already exists",
			// The actual instruction, not just an acknowledgement that a
			// conflict happened.
			"terraform import",
			// The name, so the practitioner knows which project's slug to look up.
			`"already-adopted"`,
		} {
			if !strings.Contains(detail, want) {
				t.Errorf("the diagnostic does not mention %q; got:\n%s", want, detail)
			}
		}

		// The 404 hint is about a missing repository, which is the opposite
		// problem, and must not show up here. "GitHub response: Not Found" is
		// unique to that hint's lead-in; both hints legitimately say
		// "ADOPT" on its own, since that is the shared explanation for why
		// this route behaves the way it does at all.
		if strings.Contains(detail, "GitHub response: Not Found") {
			t.Errorf("the 409 diagnostic carried the missing-repository hint, which does not apply to "+
				"it; got:\n%s", detail)
		}
	})

	t.Run("an orphaned create names the project's slug and points at import", func(t *testing.T) {
		t.Parallel()

		api, client := newFakeProjectClient(t)
		api.setFailFollowStatus(http.StatusBadRequest)

		project, err := client.CreateProject(t.Context(), testProjectResourceOrgID, "no-commits-yet")
		if err == nil {
			t.Fatal("CreateProject succeeded; the fake was asked to fail the follow call")
		}
		if project == nil {
			t.Fatal("CreateProject returned a nil project; TestCreateProjectReturnsTheProjectWhenOnlyFollowFails " +
				"(internal/circleci) covers this directly, but this test needs it non-nil to render the " +
				"diagnostic at all")
		}

		detail := projectCreateFailureDetail("no-commits-yet", project, err)

		for _, want := range []string{
			// The project's own slug, not just its name: this is the one piece
			// of information the practitioner cannot reconstruct themselves
			// without it, on the classic organizations where the slug happens
			// to be predictable from the name -- and CAN reconstruct it on a
			// standalone one, where it is not.
			project.Slug,
			"terraform import",
			// Says the project already exists rather than only that
			// something went wrong, and says why the call answered an error
			// in the first place.
			"created this project",
			"Branch not found",
		} {
			if !strings.Contains(detail, want) {
				t.Errorf("the diagnostic does not mention %q; got:\n%s", want, detail)
			}
		}
	})
}

// TestProjectResourceUnit_DestroyOfAStandaloneProjectUsesTheReportedSlug is the
// provider half of the BUG P6 investigation.
//
// The concern was that Delete addresses the project by name, which for a
// standalone project the API rejects with 400 "Invalid project slug". It does
// not: it sends state's slug, which is whatever create reported. Confirmed
// end to end against the live API — `terraform apply` then `terraform destroy`
// against a standalone organization deleted the project (a following GET
// answered 404) and emptied state, and the acceptance tests in
// project_resource_test.go now exercise the same path on every run.
//
// This test is what keeps the property visible in CI, and it only means anything
// because the fake's standalone slug is now the shape the API actually returns —
// two opaque fragments, see fakeStandaloneProjectFragment. Against the old fake,
// whose standalone slug ended in the project's own id, a Delete that rebuilt the
// slug from the name would have looked almost identical.
func TestProjectResourceUnit_DestroyOfAStandaloneProjectUsesTheReportedSlug(t *testing.T) {
	api, host := newFakeProjectAPI(t, "standalone")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: projectResourceConfig(host, "my-repo", "")},
		},
	})

	want := "DELETE /api/v2/project/" + fakeStandaloneOrgSlug + "/" + fakeStandaloneProjectFragment

	var sawDelete bool
	for _, req := range api.recordedRequests() {
		if req == want {
			sawDelete = true
		}
		if req == "DELETE /api/v2/project/"+fakeStandaloneOrgSlug+"/my-repo" {
			t.Errorf("the provider addressed the delete by project NAME (%s); the API answers "+
				"400 \"Invalid project slug\" for that form on a standalone project", req)
		}
	}
	if !sawDelete {
		t.Errorf("no %q seen, got %v", want, api.recordedRequests())
	}
}

// TestProjectResourceUnit_DestroyDoesNotOrphanAnUnreachableProject is the
// regression test for the silent orphaning BUG P6 was looking for, which turned
// out to be real but for a different reason than the slug shape.
//
// `DELETE /project/{slug}` answers 404 "Project not found" both for a project
// that is gone and for one the token may not see — measured over the network with
// two personal tokens belonging to two different accounts; the project survived
// the refused DELETE. Delete used to treat every 404 as "already gone", so
// Terraform reported a successful destroy and dropped a live project from state.
//
// The fake reproduces both halves: refuseDeleteAs404 answers the DELETE the way
// an unauthorised one is answered and keeps the project, and hiddenOrganization
// makes the organization lookup answer "Org not found." the way it does for a
// token that cannot reach it. Without deletedProjectIsReallyGone this test fails
// with no diagnostics at all — a clean, wrong success.
func TestProjectResourceUnit_DestroyDoesNotOrphanAnUnreachableProject(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectClient(t)

	api.addProject(&fakeProject{
		id:            "id",
		name:          "my-repo",
		slug:          "gh/AcmeOrg/my-repo",
		orgName:       "AcmeOrg",
		orgSlug:       "gh/AcmeOrg",
		orgID:         testProjectResourceOrgID,
		vcsURL:        "url",
		vcsProvider:   "GitHub",
		defaultBranch: "main",
	}, defaultFakeProjectSettings())

	api.setRefuseDeleteAs404(true)
	api.setOrganizationHidden(true)

	schema := projectResourceSchemaForTest(t)
	state := projectResourceStateForTest(t, schema, minimalProjectModel("gh/AcmeOrg/my-repo"))

	r := &projectResource{client: client}
	resp := &fwresource.DeleteResponse{State: state}

	r.Delete(t.Context(), fwresource.DeleteRequest{State: state}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("Delete reported success for a project the token cannot see. The DELETE answered " +
			"404, the project is still there, and Terraform will now drop it from state — a live " +
			"project left running with nothing managing it.")
	}

	if detail := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(detail, "terraform state rm") {
		t.Errorf("the diagnostic does not tell the practitioner what to do if the organization was "+
			"genuinely deleted; got: %s", detail)
	}
}

// TestProjectResourceUnit_DestroyOfAGenuinelyDeletedProjectStillSucceeds is the
// other side of the guard above, and the reason the guard checks the organization
// rather than simply failing on every 404.
//
// A project someone deleted in the CircleCI UI is the common case, and a destroy
// of it must succeed rather than making the practitioner run `terraform state
// rm`. Here the DELETE answers 404 but the organization is still reachable, so
// the 404 is taken at face value.
func TestProjectResourceUnit_DestroyOfAGenuinelyDeletedProjectStillSucceeds(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectClient(t)

	// No project seeded at all, so the DELETE answers 404 on its own — and the
	// organization route answers normally.
	api.setOrganizationHidden(false)

	schema := projectResourceSchemaForTest(t)
	state := projectResourceStateForTest(t, schema, minimalProjectModel("gh/AcmeOrg/my-repo"))

	r := &projectResource{client: client}
	resp := &fwresource.DeleteResponse{State: state}

	r.Delete(t.Context(), fwresource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete of a project that really is gone reported an error: %v\n"+
			"A project deleted outside Terraform must destroy cleanly.", resp.Diagnostics.Errors())
	}
}

// TestProjectResourceUnit_Import covers importing by slug, with NO
// ImportStateVerifyIgnore list — which is the point.
//
// This test used to carry a nine-entry ignore list: the eight boolean toggles and
// pr_only_branch_overrides, all of which ImportState left null while Create
// resolved every one of them. That list was BUG P3 written down as an
// expectation. Removing it is the regression test: with the nine attributes
// unpopulated the framework reports
//
//	ImportStateVerify attributes not equivalent … the - symbol indicates
//	attributes missing after import
//
// naming all nine. The same thing happens against the real API — see the
// acceptance tests in project_resource_test.go, which have always asked for
// ImportStateVerify with no ignore list and could not have passed.
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

// TestProjectResourceUnit_ImportPopulatesEverySetting is the positive statement
// of what an import must leave behind, and it replaces two tests that asserted
// the opposite.
//
// Those were TestProjectResourceUnit_ImportLeavesTogglesUntracked and
// TestProjectResourceUnit_ImportLeavesBranchOverridesUntracked, which between
// them pinned all nine attributes as absent after import and explained why that
// was correct. It was not: an import whose state omits nine of the resource's
// attributes fails the framework's own ImportStateVerify against the same
// project created through Create, and makes the first plan after the import
// propose a change for every setting the practitioner then writes down. That is
// BUG P3.
//
// Every value asserted here comes from defaultFakeProjectSettings, which is
// CircleCI's own default set as confirmed against a live project — including the
// two that are NOT false (set_github_status, setup_workflows) and
// pr_only_branch_overrides defaulting to the default branch rather than an empty
// list. Asserting the exact values rather than merely "not null" is what makes
// this catch an import that populates the attributes with the wrong thing.
func TestProjectResourceUnit_ImportPopulatesEverySetting(t *testing.T) {
	_, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: projectResourceConfig(host, "my-repo", "")},
			{
				ResourceName:  "circleci_project.test",
				ImportState:   true,
				ImportStateId: "gh/AcmeOrg/my-repo",
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("got %d imported instance states, want 1", len(states))
					}

					attributes := states[0].Attributes

					for attribute, want := range map[string]string{
						"auto_cancel_builds":            "false",
						"build_fork_prs":                "false",
						"build_prs_only":                "false",
						"disable_ssh":                   "false",
						"forks_receive_secret_env_vars": "true",
						"set_github_status":             "true",
						"setup_workflows":               "true",
						"write_settings_requires_admin": "false",
						// The ninth. CircleCI defaults it to the project's default
						// branch, not to an empty list.
						"pr_only_branch_overrides.#": "1",
						"pr_only_branch_overrides.0": "main",
					} {
						got, ok := attributes[attribute]
						if !ok {
							return fmt.Errorf(
								"imported state has no %s; an import must populate every attribute the "+
									"API can supply, or the first plan afterwards proposes a change for it",
								attribute,
							)
						}
						if got != want {
							return fmt.Errorf("imported state has %s = %q, want %q", attribute, got, want)
						}
					}

					// And the identifying attributes the import ID does not carry.
					for attribute, want := range map[string]string{
						"id":                "proj-1",
						"name":              "my-repo",
						"slug":              "gh/AcmeOrg/my-repo",
						"organization_name": "AcmeOrg",
						"vcs_info_provider": "GitHub",
					} {
						if got := attributes[attribute]; got != want {
							return fmt.Errorf("imported state has %s = %q, want %q", attribute, got, want)
						}
					}

					return nil
				},
			},
		},
	})
}

// TestProjectResourceUnit_ImportRoundTrips is the "plan after import is empty"
// half of the import contract.
//
// It matters more now than it did, and for the opposite reason. Import populates
// all nine settings attributes, so this proves the thing an import is for: a
// configuration that names none of them still plans clean against the state the
// import produced. These attributes are Optional+Computed rather than
// Optional-only, so it is not obvious without a real plan that Terraform core
// keeps an unconfigured Computed attribute at its prior (now non-null) value
// rather than treating it as unknown and proposing a change; this test is what
// confirms it does.
func TestProjectResourceUnit_ImportRoundTrips(t *testing.T) {
	_, host := newFakeProjectAPI(t, "classic")

	trivialConfig := projectResourceConfig(host, "my-repo", "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: trivialConfig},
			{
				// A fresh state, imported by slug alone.
				ResourceName:  "circleci_project.test",
				ImportState:   true,
				ImportStateId: "gh/AcmeOrg/my-repo",
			},
			{
				// The same trivial config, replanned against the freshly imported
				// state: the plan must be empty.
				Config:             trivialConfig,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
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

// TestProjectResourceUnit_DriftMissingProjectIsOfferedForRecreation pins the fixed
// behaviour of the drift path.
//
// This test used to be a bug report. It was named
// "...ErrorsInsteadOfRecreating" and asserted that a project deleted outside
// Terraform failed every subsequent plan forever, with a comment explaining that a
// test asserting the *better* behaviour would fail. Two characterization tests in
// this file described that defect accurately and, between them, made it look like a
// settled limitation rather than the one-line omission it was: Read simply never
// called circleci.IsNotFound, unlike 33 sibling resources.
//
// The behaviour now matches those siblings, so the assertion is positive: after the
// project disappears, refresh drops it from state and the plan offers a recreate.
func TestProjectResourceUnit_DriftMissingProjectIsOfferedForRecreation(t *testing.T) {
	api, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: projectResourceConfig(host, "my-repo", "")},
			{
				// The project vanishes from CircleCI. Refresh must notice, drop it from
				// state, and leave a non-empty plan that creates it again — rather than
				// failing, which is what this asserted before the fix.
				PreConfig:          func() { api.setMissing(true) },
				Config:             projectResourceConfig(host, "my-repo", ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Restore it so TestCase's own destroy step succeeds.
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
		PROnlyBranchOverrides:      types.SetNull(types.StringType),
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
	badState.PROnlyBranchOverrides = types.SetValueMust(types.StringType, nil)

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

// stringSet is a small helper for building a pr_only_branch_overrides value by
// hand, the way the direct-call tests in this section need to.
func stringSet(values ...string) types.Set {
	elements := make([]attr.Value, 0, len(values))
	for _, v := range values {
		elements = append(elements, types.StringValue(v))
	}

	return types.SetValueMust(types.StringType, elements)
}

// TestProjectResourceUnit_UpdateWritesBranchOverridesFromResponseNotRequest is
// the regression test for issue #4: Update wrote pr_only_branch_overrides to
// state from the request it had just sent (projectSettings, built from the
// plan) rather than from updatedProject, the settings the API reported back —
// unlike every other field this function sets, all of which come from
// updatedProject.
//
// The bug is latent as long as the API echoes the list back unchanged, which
// is all the existing fakes ever did (reorderedLikeTheAPI only reorders, and a
// Set does not see order). So this test gives the fake a hook
// (setBranchOverridesResponse, project_fake_test.go) that makes a settings
// PATCH answer with a list that genuinely differs in content from the one
// sent — the only shape of test that can tell "state written from the
// request" apart from "state written from the response". The plan asks for
// ["yankee", "zulu"]; the fake answers the PATCH with ["alpha", "bravo"]
// regardless. Only state built from the response can end up holding
// ["alpha", "bravo"].
func TestProjectResourceUnit_UpdateWritesBranchOverridesFromResponseNotRequest(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectClient(t)

	const slug = "gh/AcmeOrg/my-repo"
	api.addProject(&fakeProject{
		id:            "id",
		name:          "my-repo",
		slug:          slug,
		orgName:       "AcmeOrg",
		orgSlug:       "gh/AcmeOrg",
		orgID:         testProjectResourceOrgID,
		vcsURL:        "url",
		vcsProvider:   "GitHub",
		defaultBranch: "main",
	}, map[string]any{
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
	})

	// The response to the PATCH the plan below is about to trigger: a list that
	// shares nothing with either the prior state or the request, so any of the
	// three appearing in the final state is unambiguous about where it came
	// from.
	api.setBranchOverridesResponse([]string{"alpha", "bravo"})

	schema := projectResourceSchemaForTest(t)

	prior := minimalProjectModel(slug)
	prior.AutoCancelBuilds = types.BoolValue(false)
	prior.BuildForkPrs = types.BoolValue(false)
	prior.BuildPrsOnly = types.BoolValue(false)
	prior.DisableSSH = types.BoolValue(false)
	prior.ForksReceiveSecretEnvVars = types.BoolValue(true)
	prior.OSS = types.BoolValue(false)
	prior.SetGithubStatus = types.BoolValue(true)
	prior.SetupWorkflows = types.BoolValue(true)
	prior.WriteSettingsRequiresAdmin = types.BoolValue(false)
	prior.PROnlyBranchOverrides = stringSet("main")

	state := projectResourceStateForTest(t, schema, prior)

	plan := prior
	plan.PROnlyBranchOverrides = stringSet("yankee", "zulu")
	planState := projectResourceStateForTest(t, schema, plan)

	r := &projectResource{client: client}
	resp := &fwresource.UpdateResponse{State: state}

	assertNoPanic(t, func() {
		r.Update(t.Context(), fwresource.UpdateRequest{
			Plan:  tfsdk.Plan{Schema: schema, Raw: planState.Raw},
			State: state,
		}, resp)
	})

	if resp.Diagnostics.HasError() {
		t.Fatalf("Update diagnostics: %+v", resp.Diagnostics)
	}

	var got projectResourceModel
	if diags := resp.State.Get(t.Context(), &got); diags.HasError() {
		t.Fatalf("could not read the resulting state: %+v", diags)
	}

	var branches []string
	if diags := got.PROnlyBranchOverrides.ElementsAs(t.Context(), &branches, false); diags.HasError() {
		t.Fatalf("could not read pr_only_branch_overrides from state: %+v", diags)
	}

	want := map[string]bool{"alpha": true, "bravo": true}
	if len(branches) != len(want) {
		t.Fatalf("pr_only_branch_overrides after Update = %v, want exactly %v (what the API reported)", branches, want)
	}
	for _, b := range branches {
		if !want[b] {
			t.Errorf(
				"pr_only_branch_overrides after Update contains %q, want only what the API's response "+
					"reported (%v); %q is either the request just sent or the prior state, not the response",
				b, want, b,
			)
		}
	}
}

// TestProjectResourceUnit_ReadDoesNotAdoptUndeclaredToggles is the regression
// test for the bug fixed alongside circleci_project_settings' own
// projectSettingRefresh: Read used to write every toggle the API reported
// straight into state with types.BoolPointerValue, whatever state already held.
//
// build_prs_only stands in for "declared" here: prior state already carries a
// known value for it, so a refresh is expected to keep tracking it.
// set_github_status stands in for "undeclared": prior state is null for it,
// simulating a project whose toggles have never been adopted — state written by
// an older provider version, or hand-edited. The fake is made to report a
// *different* value for it than any default, so adopting it would be obvious
// rather than accidentally matching by coincidence.
//
// This is deliberately NOT the post-import case, which adopts every value the
// API reports (see projectSettingImport and
// TestProjectResourceUnit_ImportPopulatesEverySetting). minimalProjectModel sets
// a non-null id, which is exactly what tells the two apart.
func TestProjectResourceUnit_ReadDoesNotAdoptUndeclaredToggles(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectClient(t)

	settings := defaultFakeProjectSettings()
	settings["set_github_status"] = false
	api.addProject(&fakeProject{
		id:            "id",
		name:          "my-repo",
		slug:          "gh/AcmeOrg/my-repo",
		orgName:       "AcmeOrg",
		orgSlug:       "gh/AcmeOrg",
		orgID:         testProjectResourceOrgID,
		vcsURL:        "url",
		vcsProvider:   "GitHub",
		defaultBranch: "main",
	}, settings)

	schema := projectResourceSchemaForTest(t)

	prior := minimalProjectModel("gh/AcmeOrg/my-repo")
	prior.BuildPrsOnly = types.BoolValue(false) // declared and already tracked
	// Every other toggle, including set_github_status, stays BoolNull() from
	// minimalProjectModel: undeclared, never adopted.
	state := projectResourceStateForTest(t, schema, prior)

	r := &projectResource{client: client}
	readResp := &fwresource.ReadResponse{State: state}

	assertNoPanic(t, func() {
		r.Read(t.Context(), fwresource.ReadRequest{State: state}, readResp)
	})
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %+v", readResp.Diagnostics)
	}

	var got projectResourceModel
	if diags := readResp.State.Get(t.Context(), &got); diags.HasError() {
		t.Fatalf("could not read the resulting state: %+v", diags)
	}

	if !got.SetGithubStatus.IsNull() {
		t.Errorf(
			"set_github_status after Read = %v, want null: an undeclared toggle must not adopt "+
				"whatever the API happens to report",
			got.SetGithubStatus,
		)
	}
	if got.BuildPrsOnly.IsNull() || got.BuildPrsOnly.ValueBool() {
		t.Errorf("build_prs_only after Read = %v, want false: a declared toggle must keep refreshing", got.BuildPrsOnly)
	}

	// And the second half of the bug: because these attributes are
	// Optional+Computed, a plan that still says nothing about set_github_status
	// carries the resulting state's value forward untouched. With it null, Update
	// must omit the key from the settings PATCH rather than reassert a value
	// nobody configured.
	planState := projectResourceStateForTest(t, schema, got)
	updateResp := &fwresource.UpdateResponse{State: state}

	assertNoPanic(t, func() {
		r.Update(t.Context(), fwresource.UpdateRequest{
			Plan:  tfsdk.Plan{Schema: schema, Raw: planState.Raw},
			State: state,
		}, updateResp)
	})
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update diagnostics: %+v", updateResp.Diagnostics)
	}

	body := api.onlyPatch(t)
	if got, ok := body["set_github_status"]; ok {
		t.Errorf(
			"settings PATCH carried set_github_status = %v; it was never configured and Read never "+
				"adopted it, so it must be absent from the request body",
			got,
		)
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

// TestProjectResourceUnit_MissingProjectIsDriftNotAnError pins the behaviour that
// replaced this file's longest-standing characterization test.
//
// That test asserted Read *errors* for a project that no longer exists, and its comment
// concluded the only problem was a diagnostics-quality one: the detail came from
// err.Error() instead of circleci.Detail(err), so the API's own "Project not found"
// message never reached the practitioner. That conclusion was wrong, and being written
// down made the real defect look understood and accepted. Erroring at all was the bug.
//
// A project someone unfollowed or deleted in the CircleCI UI is drift. Every other
// resource in this package treats it that way — 33 of them call circleci.IsNotFound and
// remove the resource from state, so Terraform proposes a recreate. project_resource.go
// was the sole exception, which meant `terraform plan` failed outright, with an
// unparsed HTTP status line, on the most widely used resource here.
func TestProjectResourceUnit_MissingProjectIsDriftNotAnError(t *testing.T) {
	t.Parallel()

	// No project seeded, so a Read for a slug that never existed answers 404.
	_, client := newFakeProjectClient(t)

	schema := projectResourceSchemaForTest(t)
	state := projectResourceStateForTest(t, schema, minimalProjectModel("gh/Nobody/nothing"))

	r := &projectResource{client: client}
	resp := &fwresource.ReadResponse{State: state}

	assertNoPanic(t, func() {
		r.Read(t.Context(), fwresource.ReadRequest{State: state}, resp)
	})

	if resp.Diagnostics.HasError() {
		t.Fatalf(
			"Read of a nonexistent project reported an error: %v\n"+
				"A missing project is drift: Read must remove it from state so the next plan "+
				"proposes a recreate.",
			resp.Diagnostics.Errors(),
		)
	}

	if !resp.State.Raw.IsNull() {
		t.Error("Read left the resource in state; a project that no longer exists must be removed from it")
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

// projectResourceConfigWithOrgAttr renders a project using whichever of the two
// organization attribute names is asked for.
func projectResourceConfigWithOrgAttr(host, attr string) string {
	return projectResourceProviderConfig(host) + fmt.Sprintf(`
resource "circleci_project" "test" {
  name = "my-repo"
  %s   = %q
}
`, attr, testProjectResourceOrgID)
}

// TestProjectResourceUnit_SwitchingOrgAttributeDoesNotReplace is the test the
// organization_id -> org_id deprecation lives or dies by.
//
// organization_id carried RequiresReplace, because there is no API route that moves
// a project between organizations. Introducing org_id naively — as a second
// Optional attribute — would mean that a practitioner following our own deprecation
// advice removes organization_id from their configuration, Terraform plans it as
// null, sees a change, and destroys and recreates the project. Deleting a project
// takes its build history with it. The "non-breaking" migration would have been more
// destructive than the breaking rename it was meant to avoid.
//
// Two things prevent that, and this test is what proves they work together:
// Computed retains the prior value instead of planning null, and
// RequiresReplaceIfConfigured skips replacement when the configuration value is
// null while still replacing on a genuine organization change.
//
// The assertion is a no-op plan, not merely "not a replacement". Anything else —
// an in-place update, a drift diff — would mean the two names are not truly
// interchangeable and practitioners would see churn on upgrade.
func TestProjectResourceUnit_SwitchingOrgAttributeDoesNotReplace(t *testing.T) {
	_, host := newFakeProjectAPI(t, "classic")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// The state a practitioner already has today.
				Config: projectResourceConfigWithOrgAttr(host, "organization_id"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("organization_id"), knownvalue.StringExact(testProjectResourceOrgID)),
					// Both are populated, so the value is available under either name
					// before any migration happens.
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("org_id"), knownvalue.StringExact(testProjectResourceOrgID)),
				},
			},
			{
				// The migration: same organization, new attribute name.
				Config: projectResourceConfigWithOrgAttr(host, "org_id"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_project.test", plancheck.ResourceActionNoop),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("org_id"), knownvalue.StringExact(testProjectResourceOrgID)),
					statecheck.ExpectKnownValue("circleci_project.test", tfjsonpath.New("organization_id"), knownvalue.StringExact(testProjectResourceOrgID)),
				},
			},
			{
				// And back again, so the deprecation is not a one-way door.
				Config: projectResourceConfigWithOrgAttr(host, "organization_id"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_project.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
}

// TestProjectResourceUnit_ChangingOrgStillReplaces is the other half: the safety
// above must not have disabled replacement for a real organization change. There is
// no route that moves a project, so this has to be a destroy and create.
func TestProjectResourceUnit_ChangingOrgStillReplaces(t *testing.T) {
	_, host := newFakeProjectAPI(t, "classic")

	other := projectResourceProviderConfig(host) + `
resource "circleci_project" "test" {
  name   = "my-repo"
  org_id = "org-99999999"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: projectResourceConfigWithOrgAttr(host, "org_id")},
			{
				Config: other,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_project.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			},
		},
	})
}

// TestProjectResourceUnit_RequiresExactlyOneOrgAttribute covers the two ways of
// getting it wrong: both names, or neither.
func TestProjectResourceUnit_RequiresExactlyOneOrgAttribute(t *testing.T) {
	_, host := newFakeProjectAPI(t, "classic")

	both := projectResourceProviderConfig(host) + fmt.Sprintf(`
resource "circleci_project" "test" {
  name            = "my-repo"
  organization_id = %[1]q
  org_id          = %[1]q
}
`, testProjectResourceOrgID)

	neither := projectResourceProviderConfig(host) + `
resource "circleci_project" "test" {
  name = "my-repo"
}
`

	for name, config := range map[string]string{"both set": both, "neither set": neither} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      config,
					ExpectError: regexp.MustCompile(`(?s)Invalid Attribute Combination|Missing Attribute Configuration`),
				}},
			})
		})
	}
}

// TestProjectResourceUnit_CreateWritesStateWhenSettingsFetchFails is a
// regression test for issue #37.
//
// Once CreateProject succeeds, the project exists at CircleCI and is
// followed: unlike circleci_orb_version or circleci_orb, a retry after this
// point does not collide with anything (CreateProject on a name that is
// already taken is its own, differently-shaped failure), but it does leave
// the project running with nothing in Terraform tracking it until the
// practitioner notices and imports it by hand. Because every toggle here is
// Computed, Create still needs a settings call after CreateProject even when
// the configuration sets none of them (see the comment above
// newProjectSettings in Create) — that call is what this test makes fail.
//
// Before the fix, that failure returned before resp.State.Set, so state was
// left completely empty.
func TestProjectResourceUnit_CreateWritesStateWhenSettingsFetchFails(t *testing.T) {
	t.Parallel()

	api, client := newFakeProjectClient(t)
	api.setFailSettingsStatus(http.StatusInternalServerError)

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
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}

	r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{
		Schema: schema,
		Raw:    projectResourceStateForTest(t, schema, plan).Raw,
	}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("Create returned no diagnostics for a failed settings fetch, want one")
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("Create left state empty after CreateProject succeeded; the project now exists " +
			"and is followed at CircleCI with nothing in Terraform tracking it")
	}

	var out projectResourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if out.Id.ValueString() == "" {
		t.Error("state id is empty, want the id CreateProject returned")
	}
	if out.Slug.ValueString() == "" {
		t.Error("state slug is empty, want the slug CreateProject returned")
	}

	var creates int
	for _, req := range api.recordedRequests() {
		if req == "POST /api/v2/organization/"+testProjectResourceOrgID+"/project" {
			creates++
		}
	}
	if creates != 1 {
		t.Errorf("project create requests = %d, want exactly 1: the failure was in reading settings "+
			"back, not in creating the project, and create must not be retried on that account", creates)
	}
}
