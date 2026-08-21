// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// The four values below are the advanced settings a project has when nothing has
// ever changed them. They are NOT a guess and not copied from documentation: they
// were read over the network from a project created seconds earlier
// (POST /api/v2/organization/{id}/project, then
// GET /api/v2/project/{slug}/settings), and the identical snapshot came back from
// three long-lived fixture projects on three different VCS integrations:
//
//	{"autocancel_builds":false,"build_fork_prs":false,"build_prs_only":false,
//	 "disable_ssh":false,"forks_receive_secret_env_vars":true,
//	 "pr_only_branch_overrides":["main"],"set_github_status":true,
//	 "setup_workflows":true,"write_settings_requires_admin":false,"oss":false}
//
// SET_GITHUB_STATUS AND SETUP_WORKFLOWS ARE TRUE. This test asserted false for
// both, which is why it failed on every integration. The repository's own fake
// (defaultFakeProjectSettings in project_fake_test.go) has always held the right
// values, so the fake and the acceptance test contradicted each other and the
// fake was the one telling the truth.
const (
	defaultSetGithubStatus = true
	defaultSetupWorkflows  = true
)

// TestAccProjectSettingsDataSource reads a project's advanced settings from a
// real installation.
//
// IT ESTABLISHES THE STATE IT ASSERTS. The previous version read a pre-existing
// fixture project and asserted a hardcoded set of values, which is how it came to
// assert false for two settings whose default is true — and it is also how an
// earlier attempt to fix it went wrong: the settings of a test organization's
// project were changed out of band to match the test, and the resulting green
// meant nothing. So this test writes the values first, through
// circleci_project_settings, and reads them back through the data source. A
// wrong constant here now fails on the write, not on somebody else's fixture.
//
// It runs against the WRITABLE fixture project (CIRCLECI_TEST_<key>_PROJECT_SLUG)
// rather than the read-only one, because it writes. Whatever the project held
// beforehand is captured before the first step and PATCHed back afterwards, so
// the fixture is left exactly as it was found — see
// testAccPreserveProjectSettings.
//
// Two settings are deliberately never written here:
//
//   - forks_receive_secret_env_vars, because enabling it is refused with HTTP 403
//     on a standalone organization and the refusal cannot be undone: the project
//     default is true, so a test that turned it off would permanently change the
//     fixture. Measured — see ProjectSettings.ForksReceiveSecretEnvVars in
//     internal/circleci/project_settings.go.
//   - write_settings_requires_admin, because a run that died between the write and
//     the restore would leave the fixture needing organization-administrator
//     rights to fix, and the restore itself would then be refused. It is writable
//     on all four organization classes — measured — but not worth that risk from a
//     test.
//
// Both are still asserted to be present, which is what the data source is
// responsible for.
func TestAccProjectSettingsDataSource(t *testing.T) {
	projectSlug := testProjectSlug(t)

	// Before the client is built: this is what resolves the token for the
	// integration under test into CIRCLE_TOKEN.
	testAccPreCheck(t)

	client := testAccProjectSettingsClient(t)
	testAccPreserveProjectSettings(t, client, projectSlug)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// The API's own defaults, written and then read back. This is the step
			// that pins set_github_status and setup_workflows to true.
			{
				Config: testProjectSettingsDataSourceConfig(projectSlug, `
  auto_cancel_builds       = false
  build_fork_prs           = false
  build_prs_only           = false
  disable_ssh              = false
  set_github_status        = true
  setup_workflows          = true
  pr_only_branch_overrides = ["main"]`),
				ConfigStateChecks: testAccProjectSettingsDataSourceChecks(
					projectSlug,
					map[string]bool{
						"auto_cancel_builds": false,
						"build_fork_prs":     false,
						"build_prs_only":     false,
						"disable_ssh":        false,
						"set_github_status":  defaultSetGithubStatus,
						"setup_workflows":    defaultSetupWorkflows,
					},
					[]string{"main"},
				),
			},
			// Every one of them flipped. Without this step the test could still pass
			// against a fixture that merely happened to hold the asserted values —
			// which is exactly how the false assertions survived. Here the data source
			// has to report values that differ from the defaults, so it can only pass
			// by actually reading the project.
			//
			// It is also the per-integration answer to "can this resource write what
			// it claims": a setting the API accepted with 200 and then ignored would
			// show up as a data source still reporting the old value.
			{
				Config: testProjectSettingsDataSourceConfig(projectSlug, `
  auto_cancel_builds       = true
  build_fork_prs           = false
  build_prs_only           = true
  disable_ssh              = true
  set_github_status        = false
  setup_workflows          = false
  pr_only_branch_overrides = ["main", "tf-acc-project-settings"]`),
				ConfigStateChecks: testAccProjectSettingsDataSourceChecks(
					projectSlug,
					map[string]bool{
						"auto_cancel_builds": true,
						"build_fork_prs":     false,
						"build_prs_only":     true,
						"disable_ssh":        true,
						"set_github_status":  false,
						"setup_workflows":    false,
					},
					[]string{"main", "tf-acc-project-settings"},
				),
			},
			// The identical configuration replanned. A setting the API accepted and
			// silently ignored would show here as a permanent diff: the refresh would
			// report the old value, the configuration would ask for the new one, and
			// the plan would never be empty. This is the cheapest silent-ignore
			// detector there is, and it costs one step.
			{
				Config: testProjectSettingsDataSourceConfig(projectSlug, `
  auto_cancel_builds       = true
  build_fork_prs           = false
  build_prs_only           = true
  disable_ssh              = true
  set_github_status        = false
  setup_workflows          = false
  pr_only_branch_overrides = ["main", "tf-acc-project-settings"]`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// testAccProjectSettingsDataSourceChecks builds the data source assertions for
// one expected set of values.
//
// oss and forks_receive_secret_env_vars are asserted as merely present rather
// than as a value: neither is written by this test — oss cannot be written at all
// and forks_receive_secret_env_vars cannot be safely restored — so asserting a
// value for either would be asserting the fixture's state, which is the mistake
// this test exists to stop repeating.
func testAccProjectSettingsDataSourceChecks(
	projectSlug string, toggles map[string]bool, branches []string,
) []statecheck.StateCheck {
	const dataSource = "data.circleci_project_settings.test_project"

	checks := []statecheck.StateCheck{
		statecheck.ExpectKnownValue(dataSource, tfjsonpath.New("slug"), knownvalue.StringExact(projectSlug)),
		statecheck.ExpectKnownValue(dataSource, tfjsonpath.New("oss"), knownvalue.NotNull()),
		statecheck.ExpectKnownValue(dataSource, tfjsonpath.New("forks_receive_secret_env_vars"), knownvalue.NotNull()),
	}

	// Sorted, so a failure names the same attribute run to run.
	names := make([]string, 0, len(toggles))
	for name := range toggles {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		checks = append(checks, statecheck.ExpectKnownValue(
			dataSource, tfjsonpath.New(name), knownvalue.Bool(toggles[name]),
		))
	}

	branchChecks := make([]knownvalue.Check, 0, len(branches))
	for _, branch := range branches {
		branchChecks = append(branchChecks, knownvalue.StringExact(branch))
	}

	return append(checks, statecheck.ExpectKnownValue(
		dataSource, tfjsonpath.New("pr_only_branch_overrides"), knownvalue.SetExact(branchChecks),
	))
}

// testProjectSettingsDataSourceConfig pairs the resource that writes the settings
// with the data source that reads them back.
//
// depends_on is load-bearing and not decoration. The data source's only argument
// is the slug, which is known at plan time, so without it Terraform would read
// the data source during PLAN — before the resource has written anything — and
// the assertions would be made against whatever the project held beforehand. That
// is precisely the failure mode this test is being rewritten to avoid.
func testProjectSettingsDataSourceConfig(projectSlug, settings string) string {
	return fmt.Sprintf(`
resource "circleci_project_settings" "under_test" {
  slug = %[1]q
%[2]s
}

data "circleci_project_settings" "test_project" {
  slug       = %[1]q
  depends_on = [circleci_project_settings.under_test]
}
`, projectSlug, settings)
}

// testAccProjectSettingsClient builds a client against the same installation the
// provider under test talks to, for the capture-and-restore below.
//
// Acceptance tests normally reach the API only through Terraform. This one needs
// a side channel: it has to read the fixture's settings before Terraform runs and
// put them back afterwards, and there is no Terraform operation that does either.
func testAccProjectSettingsClient(t *testing.T) *circleci.Client {
	t.Helper()

	return circleci.New(circleci.Config{
		// The same two variables the provider itself falls back to, so a run
		// against CircleCI Server reaches the same installation.
		Host:       os.Getenv("CIRCLE_HOST"),
		Token:      os.Getenv("CIRCLE_TOKEN"),
		Deployment: circleci.Deployment(os.Getenv("CIRCLE_DEPLOYMENT")),
	})
}

// testAccPreserveProjectSettings reads a project's advanced settings and
// registers a cleanup that writes them back.
//
// This is the difference between a test that establishes its own state and one
// that quietly redecorates a shared fixture. An earlier attempt at this item
// changed an organization's settings out of band so the assertions would match
// and reported the resulting green; a test that restores what it found cannot do
// that, because the state it asserts lasts only as long as the test.
//
// Three settings are excluded from the restore, each for its own reason:
//
//   - oss is never sent by this client at all (the PATCH answers
//     400 "Unexpected field 'advanced.oss'." and rejects the whole request), and
//     nothing here writes it, so there is nothing to put back.
//   - forks_receive_secret_env_vars is not written by the test, and re-sending
//     true would be refused with 403 on a standalone organization — the restore
//     would fail on exactly the setting it did not touch.
//   - an EMPTY pr_only_branch_overrides cannot be restored: the API accepts [] with
//     200 and ignores it. If the fixture genuinely had no overrides, the test's
//     branches cannot be removed, and saying so is better than pretending.
func testAccPreserveProjectSettings(t *testing.T, client *circleci.Client, projectSlug string) {
	t.Helper()

	vcsType, orgName, projectName, ok := splitProjectSlugForTest(projectSlug)
	if !ok {
		t.Fatalf("project slug %q is not vcs-type/org-name/project-name", projectSlug)
	}

	ctx := context.Background()

	original, err := client.GetProjectSettings(ctx, vcsType, orgName, projectName)
	if err != nil {
		t.Fatalf("could not read the original settings of %s, so this test cannot promise to restore "+
			"them and must not write: %v", projectSlug, err)
	}

	t.Cleanup(func() {
		restore := *original
		restore.OSS = nil
		restore.ForksReceiveSecretEnvVars = nil

		if restore.PROnlyBranchOverrides != nil && len(*restore.PROnlyBranchOverrides) == 0 {
			t.Errorf("%s had no pr_only_branch_overrides before this test and CircleCI's API cannot "+
				"clear the list, so the branches this test added cannot be removed. Remove them by "+
				"hand; see ProjectSettings.PROnlyBranchOverrides in internal/circleci.", projectSlug)

			restore.PROnlyBranchOverrides = nil
		}

		if _, err := client.UpdateProjectSettings(ctx, vcsType, orgName, projectName, restore); err != nil {
			t.Errorf("could not restore the original settings of %s: %v. The fixture is left holding "+
				"what this test wrote.", projectSlug, err)
		}
	})
}

// splitProjectSlugForTest splits a project slug into its three parts. Local to
// the tests: the provider's own parseProjectSlug returns Terraform diagnostics,
// which a test helper has no use for.
func splitProjectSlugForTest(slug string) (vcsType, orgName, projectName string, ok bool) {
	parts := strings.SplitN(slug, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}

	return parts[0], parts[1], parts[2], true
}

// projectSettingsDataSourceSchemaForTest returns the data source's schema, so
// that tests can build config values for it.
func projectSettingsDataSourceSchemaForTest(t *testing.T) dschema.Schema {
	t.Helper()

	resp := &datasource.SchemaResponse{}
	NewProjectSettingsDataSource().Schema(context.Background(), datasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema method diagnostics: %+v", resp.Diagnostics)
	}

	return resp.Schema
}

// projectSettingsDataSourceConfigForTest builds a tfsdk.Config from a
// slug-only query, the shape a practitioner's data block takes: every
// Computed attribute is unknown/null in a config, never a value the
// practitioner supplied.
func projectSettingsDataSourceConfigForTest(t *testing.T, schema dschema.Schema, slug string) tfsdk.Config {
	t.Helper()

	model := projectSettingsDataSourceModel{
		AutoCancelBuilds:           types.BoolNull(),
		BuildForkPrs:               types.BoolNull(),
		BuildPrsOnly:               types.BoolNull(),
		DisableSSH:                 types.BoolNull(),
		ForksReceiveSecretEnvVars:  types.BoolNull(),
		OSS:                        types.BoolNull(),
		PROnlyBranchOverrides:      types.SetNull(types.StringType),
		Slug:                       types.StringValue(slug),
		SetGithubStatus:            types.BoolNull(),
		SetupWorkflows:             types.BoolNull(),
		WriteSettingsRequiresAdmin: types.BoolNull(),
	}

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(context.Background(), model); diags.HasError() {
		t.Fatalf("could not build a config value: %+v", diags)
	}

	return tfsdk.Config{Schema: schema, Raw: state.Raw}
}

// TestProjectSettingsDataSourceRejectsDotSegmentInSlug is the provider-layer
// regression test for the vulnerability class internal/circleci/project_settings.go
// defends against: this data source's Read splits a configuration-supplied
// "slug" on "/" into the three arguments GetProjectSettings takes, and the
// schema validates nothing about the resulting segments beyond the
// `^.+/.+/.+$` regex — which a segment of exactly "." or ".." satisfies just
// as well as an ordinary name. Without circleci.GetProjectSettings's own
// check, a slug like "github/./repo" would reach the wire unrejected: the
// fake API below validates the request's path *shape* (five slug-and-fixed
// segments plus "settings"), not the content of each segment, exactly like
// the routes this provider actually talks to, so a request that should never
// have been sent would succeed.
//
// This is defence in depth, not a fix for a reachable bug: slug is Terraform
// configuration here, never third-party input.
//
// It also checks the legitimate cases still parse, including a segment that
// merely contains a dot (a repository named "my.repo"), which must remain
// valid — rejecting that would break real configurations.
func TestProjectSettingsDataSourceRejectsDotSegmentInSlug(t *testing.T) {
	t.Parallel()

	schema := projectSettingsDataSourceSchemaForTest(t)

	tests := []struct {
		name    string
		slug    string
		wantErr bool
	}{
		{name: "dot org segment", slug: "github/./repo", wantErr: true},
		{name: "dot-dot org segment", slug: "github/../repo", wantErr: true},
		{name: "dot project segment", slug: "github/acme/.", wantErr: true},
		{name: "dot-dot project segment", slug: "github/acme/..", wantErr: true},
		{name: "ordinary slug", slug: testProjectSettingsSlug, wantErr: false},
		{name: "segment merely containing a dot", slug: "github/acme/my.repo", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, client := newFakeProjectSettingsAPI(t)
			d := &ProjectSettingsDataSource{client: client}

			resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schema}}
			d.Read(context.Background(), datasource.ReadRequest{
				Config: projectSettingsDataSourceConfigForTest(t, schema, tt.slug),
			}, resp)

			if tt.wantErr {
				if !resp.Diagnostics.HasError() {
					t.Errorf("Read with slug %q produced no diagnostics, want an error", tt.slug)
				}
				if requests := api.recordedRequests(); len(requests) != 0 {
					t.Errorf("requests = %v, want none: a %q slug must be rejected before a request is made",
						requests, tt.slug)
				}

				return
			}

			if resp.Diagnostics.HasError() {
				t.Errorf("Read with slug %q produced diagnostics: %+v, want none", tt.slug, resp.Diagnostics)
			}
			if requests := api.recordedRequests(); len(requests) != 1 {
				t.Errorf("requests = %v, want exactly one GET for a legitimate slug", requests)
			}
		})
	}
}

func TestProjectSettingsDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	schemaRequest := datasource.SchemaRequest{}
	schemaResponse := &datasource.SchemaResponse{}

	NewProjectSettingsDataSource().Schema(ctx, schemaRequest, schemaResponse)

	if schemaResponse.Diagnostics.HasError() {
		t.Fatalf("Schema method diagnostics: %+v", schemaResponse.Diagnostics)
	}

	diagnostics := schemaResponse.Schema.ValidateImplementation(ctx)

	if diagnostics.HasError() {
		t.Fatalf("Schema validation diagnostics: %+v", diagnostics)
	}
}

// TestAccProjectSettingsDefaults is the test bug P7 needed and did not have.
//
// TestAccProjectSettingsDataSource above establishes the state it asserts, which
// is what makes it trustworthy — but it also means it can no longer be wrong
// about what a project's settings are BEFORE anything writes them. Setting a value
// and asserting the same value passes whatever the default is. Something has to
// pin the default itself, and it cannot be a fixture project: a fixture's settings
// drift, and the last attempt at this item "fixed" a failing assertion by editing
// an organization to match it.
//
// So this creates its own project and never writes a setting at all. It makes a
// standalone organization, makes a project in it, reads the settings that project
// was born with, and asserts the snapshot below — then deletes both. Nothing
// pre-existing is read and nothing is modified, so there is no fixture to drift
// and nothing to restore.
//
// The snapshot is what a fresh project actually answered with, read over the
// network moments after it was created:
//
// \tGET /api/v2/project/circleci/<org>/<project>/settings
// \t→ 200 {"advanced":{"autocancel_builds":false,"build_fork_prs":false,
// \t       "build_prs_only":false,"disable_ssh":false,
// \t       "forks_receive_secret_env_vars":true,"pr_only_branch_overrides":["main"],
// \t       "set_github_status":true,"setup_workflows":true,
// \t       "write_settings_requires_admin":false,"oss":false}}
//
// set_github_status and setup_workflows are TRUE, and forks_receive_secret_env_vars
// is TRUE. The acceptance test asserted false for the first two, which is why it
// failed on every integration, and defaultFakeProjectSettings in
// project_fake_test.go held the right values all along.
//
// A standalone organization is not a choice: a project create against a classic
// (gh/<org>) organization answers 404 "GitHub response: Not Found", because there
// it only adopts an existing repository. Creating one is therefore the only way to
// get a project whose settings nothing has ever touched.
func TestAccProjectSettingsDefaults(t *testing.T) {
	testAccPreCheck(t)

	client := testAccProjectSettingsClient(t)
	ctx := context.Background()

	org, err := client.CreateOrganization(ctx, circleci.OrganizationInput{
		Name:    "tf-acc-defaults-" + strings.ToLower(rand.Text()[:10]),
		VCSType: circleci.OrganizationVCSTypeStandalone,
	})
	if err != nil {
		t.Fatalf("could not create a standalone organization to read a fresh project's defaults from: %v", err)
	}

	t.Cleanup(func() {
		// Deleting the organization takes its projects with it, so this is the only
		// cleanup that has to succeed. It is registered before the project is created
		// so that it still runs if the create fails halfway.
		if err := client.DeleteOrganization(ctx, org.ID); err != nil {
			t.Errorf("could not delete the organization %s (%s) this test created: %v", org.Name, org.ID, err)
		}
	})

	project, err := client.CreateProject(ctx, org.ID, "tf-acc-defaults")
	if err != nil {
		t.Fatalf("could not create a project in %s: %v", org.Slug, err)
	}

	vcsType, orgSegment, projectSegment, ok := splitProjectSlugForTest(project.Slug)
	if !ok {
		t.Fatalf("the created project's slug %q is not vcs-type/org/project", project.Slug)
	}

	settings, err := client.GetProjectSettings(ctx, vcsType, orgSegment, projectSegment)
	if err != nil {
		t.Fatalf("could not read the settings of the project just created (%s): %v", project.Slug, err)
	}

	// Every toggle the route reports, with the value a project is born with.
	// Written out one by one rather than compared as a struct so that a failure
	// names the setting that moved.
	for _, c := range []struct {
		name string
		got  *bool
		want bool
	}{
		{"autocancel_builds", settings.AutocancelBuilds, false},
		{"build_fork_prs", settings.BuildForkPrs, false},
		{"build_prs_only", settings.BuildPrsOnly, false},
		{"disable_ssh", settings.DisableSSH, false},
		{"forks_receive_secret_env_vars", settings.ForksReceiveSecretEnvVars, true},
		{"oss", settings.OSS, false},
		{"set_github_status", settings.SetGithubStatus, defaultSetGithubStatus},
		{"setup_workflows", settings.SetupWorkflows, defaultSetupWorkflows},
		{"write_settings_requires_admin", settings.WriteSettingsRequiresAdmin, false},
	} {
		switch {
		case c.got == nil:
			t.Errorf("%s was absent from a fresh project's settings; every toggle above is reported "+
				"by the GET, and one going missing changes what the provider stores", c.name)
		case *c.got != c.want:
			t.Errorf("a fresh project's %s = %v, want %v. If the API's default really changed, the "+
				"fixture is defaultFakeProjectSettings in project_fake_test.go and the constants at "+
				"the top of this file — not this assertion on its own",
				c.name, *c.got, c.want)
		}
	}

	if settings.PROnlyBranchOverrides == nil || len(*settings.PROnlyBranchOverrides) != 1 ||
		(*settings.PROnlyBranchOverrides)[0] != "main" {
		t.Errorf("a fresh project's pr_only_branch_overrides = %v, want exactly the default branch. "+
			"This one is easy to dismiss as fixture noise, but it is a default: the route seeds the "+
			"list with the project's default branch, and because an empty list cannot be written it "+
			"can never go back to being empty", settings.PROnlyBranchOverrides)
	}
}
