// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"terraform-provider-circleci/internal/circleci"
)

// This file is the real-API counterpart to deploy_data_sources_test.go and
// its per-type siblings, which are entirely [FAKE]-backed against an
// in-process mock. Before the fixture data this file reads existed, every
// claim about the *populated* shape of a deploy environment, component or
// version — the versions array, non-sentinel pipeline/workflow/job ids,
// release_count's List-vs-Get split, and component-name's substring
// matching — was either [FAKE] only or [NET] against a different,
// non-fixture organization (see the doc comments on
// circleci.DeployComponent and its ReleaseCount/DeployComponentVersion
// fields). This file is the first coverage against fixture data seeded
// specifically for that purpose.
//
// # Deploy/release fixture data
//
// Real deploy/release markers were seeded [NET] on 2026-08-22 in two
// organizations, using CircleCI's own documented mechanism: a pipeline job
// running `circleci run release log` (single-shot) or `circleci run release
// plan` + `circleci run release update` (two-step). There is no create route
// for any of this — see deploy_environment.go and deploy_component.go — so a
// real pipeline run is the only way to produce it.
//
//   - gh-oauth-cci-1 / project-1 (CIRCLECI_TEST_GH_OAUTH_ORG_ID /
//     _PROJECT_ID — the same writable fixture project pipeline, trigger and
//     webhook tests already use), branch "deploy-fixture-seed".
//   - gh-app-cci-1 / test-repo (CIRCLECI_TEST_GH_APP_ORG_ID / _PROJECT_ID),
//     branch "deploy-fixture-seed".
//
// Both branches, and their .circleci/config.yml, are meant to persist as
// fixtures and are not cleaned up by any test — deploy environments and
// components have no delete route to clean them up WITH even if that were
// wanted. Each organization now carries:
//
//   - Environments "tf-fixture-staging" and "tf-fixture-production".
//   - Component "tf-fixture-widget", deployed to both environments, across
//     more than one version, via a real workflow (so its versions carry real
//     pipeline/workflow/job ids and job numbers, not the all-zero sentinel).
//   - Component "tf-fixture-widget-plus", a substring-match sibling of
//     "tf-fixture-widget" ("tf-fixture-widget" is a substring of its name),
//     seeded to confirm the substring-match fact against real fixture data
//     rather than only a different organization.
//   - Component "tf-fixture-planned", seeded via the `release plan` +
//     `release update` two-step form rather than `release log`, so both
//     documented marker mechanisms are exercised.
//
// gh-app-cci-1 additionally carries the *first* marker either organization
// ever received, deliberately logged with neither --component-name nor
// --environment-name: [NET] it created environment "default" and component
// "test-repo" (this project's own name) exactly as CircleCI's documentation
// says. That default only held because it was the very first marker —
// project-1's config.yml (gh-oauth-cci-1) demonstrates the opposite case:
// once more than one environment or component already exists, omitting
// either flag is rejected ("Multiple environments/components found... Please
// specify target X"), not silently defaulted. Both organizations'
// .circleci/config.yml carry this same finding as a comment, since it is not
// otherwise re-testable — an organization only has a "first ever" marker
// once.
//
// [NET] Also discovered while seeding: `circleci run release log` only
// persists its FIRST call within a single job. Later `release log` calls in
// the same job print a success-looking "LOGGED" message (with stale-looking
// values) but create no additional component or version record — even for a
// brand new component name never logged before. `release plan` +
// `release update` was not affected by this, but every marker in both
// fixture repos' config.yml still gets its own job, to be safe and to keep
// each one independently re-triggerable.
//
// # What remains unconfirmed against fixture data
//
// Labels and archived_at have no write path anywhere in this API family —
// no create, update, delete, and no CLI flag for either (`circleci run
// release log/plan/update` has no --label or --archive equivalent) — so
// fixture data cannot exercise them. They stay [FAKE]-only; see
// deploy_data_sources_test.go's mock. The all-zero UUID sentinel
// (circleci.ZeroUUID) is similarly not reproduced here: every version this
// file's fixture data ever produces is created by a real pipeline job, which
// always carries real pipeline/workflow/job ids. Whether the sentinel occurs
// for a version recorded some other way remains [FAKE]-only too.
//
// # The enabled-vs-unused question
//
// Before this fixture data existed, every organization this suite could
// reach answered `GET .../deploy/environments` and `.../deploy/components`
// with the same empty 200, making "deploys was never enabled for this org"
// indistinguishable from "enabled but unused" (see
// testDeployEmptyOrganizationID's comment in deploy_data_sources_test.go).
// With one organization now genuinely populated and others still genuinely
// untouched, that comparison became possible for the first time. It changed
// nothing: [NET] the untouched organizations still answer exactly the same
// empty 200 — same status, same body shape, same `x-route` response header
// (checked directly against the API, not just through this provider) as
// before any organization was seeded. TestAccDeployEnvironmentsDataSourceNet_UnseededOrgStaysEmpty
// below pins that this remains true for a real, live, still-untouched
// organization, now that a controlled comparison is possible. There is
// still no signal, anywhere in this route family, that distinguishes the two
// cases.

// deployFixture* name the entities seeded above. They are the same in both
// fixture organizations, so every test below that needs one just needs to
// know which organization's static fixture id to resolve first.
const (
	deployFixtureWidgetComponent     = "tf-fixture-widget"
	deployFixtureWidgetPlusComponent = "tf-fixture-widget-plus"
	deployFixturePlannedComponent    = "tf-fixture-planned"
	deployFixtureStagingEnv          = "tf-fixture-staging"
	deployFixtureProductionEnv       = "tf-fixture-production"
)

// testDeployGHAppOrgID returns the UUID of the GitHub App organization
// carrying seeded deploy/release fixture data. Static, like
// testGithubOrgID: this specific organization's fixture data is what these
// tests need, regardless of which integration CIRCLECI_TEST_VCS_TYPE
// otherwise names as active.
func testDeployGHAppOrgID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GH_APP_ORG_ID",
		"UUID of the GitHub App organization carrying seeded deploy/release fixture data (see this file's header comment)")
}

// testDeployClient builds a real circleci.Client from CIRCLE_TOKEN, for
// resolving a fixture's id directly (bypassing Terraform) before building the
// HCL that reads it through the data source under test. testAccPreCheck is
// called first so this skips exactly like every other helper here when no
// token is configured.
func testDeployClient(t *testing.T) *circleci.Client {
	t.Helper()
	testAccPreCheck(t)

	return circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})
}

// mustFindDeployEnvironmentID resolves the id of a fixture environment by its
// exact name, fatal-ing (not erroring) when it is missing: every caller needs
// this id to build its test config, so a missing fixture should stop the
// test immediately with a message pointing at why, rather than proceed into a
// confusing empty-config failure.
func mustFindDeployEnvironmentID(t *testing.T, client *circleci.Client, orgID, exactName string) string {
	t.Helper()

	envs, err := client.DeployEnvironments().List(context.Background(), orgID)
	if err != nil {
		t.Fatalf("listing deploy environments in organization %s to resolve fixture %q: %v", orgID, exactName, err)
	}

	for _, env := range envs {
		if env.Name == exactName {
			return env.ID
		}
	}

	t.Fatalf("no deploy environment named %q found in organization %s; is the deploy-fixture-seed branch's "+
		"pipeline still green there? See this file's header comment.", exactName, orgID)

	return ""
}

// mustFindDeployComponentID is mustFindDeployEnvironmentID's counterpart for
// components. It lists with name=exactName rather than unfiltered: that
// exercises the real List route's (substring) filter as a side effect, and
// still requires an exact match below because the filter can return more than
// one component (see deployComponentAttributes' doc comment).
func mustFindDeployComponentID(t *testing.T, client *circleci.Client, orgID, exactName string) string {
	t.Helper()

	components, err := client.DeployComponents().List(context.Background(), orgID, "", exactName)
	if err != nil {
		t.Fatalf("listing deploy components in organization %s to resolve fixture %q: %v", orgID, exactName, err)
	}

	for _, component := range components {
		if component.Name == exactName {
			return component.ID
		}
	}

	t.Fatalf("no deploy component named %q found in organization %s; is the deploy-fixture-seed branch's "+
		"pipeline still green there? See this file's header comment.", exactName, orgID)

	return ""
}

// deployDataSourceState returns the resource state for a data source address,
// erroring (not fatal-ing) so callers can return it from a resource.TestStep's
// Check function the way terraform-plugin-testing expects.
func deployDataSourceState(s *terraform.State, address string) (*terraform.ResourceState, error) {
	rs, ok := s.RootModule().Resources[address]
	if !ok {
		return nil, fmt.Errorf("resource %s not found in state", address)
	}

	return rs, nil
}

// flatmapListStrings reads one string field out of every element of a
// list-nested attribute, from a resource's flatmap state — e.g.
// flatmapListStrings(rs, "environments", "name") reads every
// environments.<i>.name value. Used instead of a fixed tfjsonpath index
// throughout this file because none of these lists' ordering (nor, for the
// organization-scoped ones, their exact membership — a fixture organization
// could in principle gain unrelated entries over time) is part of the
// contract under test; only "is this named entity present" is.
func flatmapListStrings(rs *terraform.ResourceState, listAttr, fieldAttr string) []string {
	count, _ := strconv.Atoi(rs.Primary.Attributes[listAttr+".#"])

	values := make([]string, 0, count)
	for i := 0; i < count; i++ {
		values = append(values, rs.Primary.Attributes[fmt.Sprintf("%s.%d.%s", listAttr, i, fieldAttr)])
	}

	return values
}

// containsString reports whether want is present in got, for the presence
// assertions throughout this file.
func containsString(got []string, want string) bool {
	for _, v := range got {
		if v == want {
			return true
		}
	}

	return false
}

// TestAccDeployEnvironmentsDataSourceNet_GHOAuth lists the GitHub OAuth
// fixture organization's deploy environments and confirms both seeded
// environments are present. It does not assert the list's exact size: this
// is a real, live, shared organization, and other entries could in principle
// appear there over time.
func TestAccDeployEnvironmentsDataSourceNet_GHOAuth(t *testing.T) {
	orgID := testGithubOrgID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_deploy_environments" "net_test" {
  org_id = %[1]q
}
`, orgID),
			Check: func(s *terraform.State) error {
				rs, err := deployDataSourceState(s, "data.circleci_deploy_environments.net_test")
				if err != nil {
					return err
				}

				names := flatmapListStrings(rs, "environments", "name")
				for _, want := range []string{deployFixtureStagingEnv, deployFixtureProductionEnv} {
					if !containsString(names, want) {
						return fmt.Errorf("organization %s's deploy environments = %v, want it to include %q "+
							"(seeded by the deploy-fixture-seed branch; see this file's header comment)",
							orgID, names, want)
					}
				}

				return nil
			},
		}},
	})
}

// TestAccDeployEnvironmentDataSourceNet_GHOAuth reads one seeded environment
// by id (resolved directly through the client, not hardcoded) through the
// singular data source.
func TestAccDeployEnvironmentDataSourceNet_GHOAuth(t *testing.T) {
	orgID := testGithubOrgID(t)
	client := testDeployClient(t)
	envID := mustFindDeployEnvironmentID(t, client, orgID, deployFixtureStagingEnv)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_deploy_environment" "net_test" {
  id = %[1]q
}
`, envID),
			Check: func(s *terraform.State) error {
				rs, err := deployDataSourceState(s, "data.circleci_deploy_environment.net_test")
				if err != nil {
					return err
				}

				if got := rs.Primary.Attributes["name"]; got != deployFixtureStagingEnv {
					return fmt.Errorf("name = %q, want %q", got, deployFixtureStagingEnv)
				}
				if got := rs.Primary.Attributes["created_at"]; got == "" {
					return fmt.Errorf("created_at is empty, want a real RFC 3339 timestamp from the API")
				}

				return nil
			},
		}},
	})
}

// TestAccDeployComponentsDataSourceNet_SubstringMatch_GHOAuth confirms
// [NET], against fixture data seeded for exactly this, that component-name
// filtering is a substring match rather than equality: filtering by the
// exact name "tf-fixture-widget" also returns "tf-fixture-widget-plus".
// See circleci.DeployComponentService.List's doc comment, previously
// confirmed only against a different, non-fixture organization.
func TestAccDeployComponentsDataSourceNet_SubstringMatch_GHOAuth(t *testing.T) {
	orgID := testGithubOrgID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_deploy_components" "net_test" {
  org_id = %[1]q
  name   = %[2]q
}
`, orgID, deployFixtureWidgetComponent),
			Check: func(s *terraform.State) error {
				rs, err := deployDataSourceState(s, "data.circleci_deploy_components.net_test")
				if err != nil {
					return err
				}

				names := flatmapListStrings(rs, "components", "name")
				for _, want := range []string{deployFixtureWidgetComponent, deployFixtureWidgetPlusComponent} {
					if !containsString(names, want) {
						return fmt.Errorf("filtering by name=%q returned %v, want it to include %q too — "+
							"component-name is documented (and was [NET]-confirmed elsewhere) to be a "+
							"substring match, not an equality filter",
							deployFixtureWidgetComponent, names, want)
					}
				}

				return nil
			},
		}},
	})
}

// TestAccDeployComponentsDataSourceNet_ReleaseCountAlwaysZero_GHOAuth is one
// half of confirming, against fixture data, the release_count fact recorded
// on circleci.DeployComponent's doc comment: the plural List route always
// answers release_count: 0. TestAccDeployComponentDataSourceNet_GHOAuth below
// is the other half — the singular Get, for the very same component,
// answering a non-zero count.
func TestAccDeployComponentsDataSourceNet_ReleaseCountAlwaysZero_GHOAuth(t *testing.T) {
	orgID := testGithubOrgID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_deploy_components" "net_test" {
  org_id = %[1]q
  name   = %[2]q
}
`, orgID, deployFixtureWidgetComponent),
			Check: func(s *terraform.State) error {
				rs, err := deployDataSourceState(s, "data.circleci_deploy_components.net_test")
				if err != nil {
					return err
				}

				count, _ := strconv.Atoi(rs.Primary.Attributes["components.#"])
				for i := 0; i < count; i++ {
					name := rs.Primary.Attributes[fmt.Sprintf("components.%d.name", i)]
					if name != deployFixtureWidgetComponent {
						continue
					}

					releaseCount := rs.Primary.Attributes[fmt.Sprintf("components.%d.release_count", i)]
					if releaseCount != "0" {
						return fmt.Errorf("circleci_deploy_components reported release_count=%s for %q, want "+
							"\"0\" — this component has real release history (see "+
							"TestAccDeployComponentDataSourceNet_GHOAuth), so a non-zero value here would mean "+
							"the List route stopped always answering 0 and this provider's schema warning about "+
							"it is now stale", releaseCount, deployFixtureWidgetComponent)
					}

					return nil
				}

				return fmt.Errorf("component %q not found in circleci_deploy_components' result", deployFixtureWidgetComponent)
			},
		}},
	})
}

// TestAccDeployComponentDataSourceNet_GHOAuth reads the seeded
// "tf-fixture-widget" component by id through the singular data source and
// confirms, against real fixture data:
//
//   - release_count is the true, non-zero count (the other half of the fact
//     TestAccDeployComponentsDataSourceNet_ReleaseCountAlwaysZero_GHOAuth
//     confirms the List side of).
//   - The versions array is actually populated.
//   - At least one version carries real (non-null) run_id, workflow_id and
//     job_id — i.e. was not left as circleci.ZeroUUID by
//     zeroUUIDToNull — because it was recorded by a real pipeline job.
func TestAccDeployComponentDataSourceNet_GHOAuth(t *testing.T) {
	orgID := testGithubOrgID(t)
	client := testDeployClient(t)
	componentID := mustFindDeployComponentID(t, client, orgID, deployFixtureWidgetComponent)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_deploy_component" "net_test" {
  id = %[1]q
}
`, componentID),
			Check: func(s *terraform.State) error {
				rs, err := deployDataSourceState(s, "data.circleci_deploy_component.net_test")
				if err != nil {
					return err
				}

				if got := rs.Primary.Attributes["name"]; got != deployFixtureWidgetComponent {
					return fmt.Errorf("name = %q, want %q", got, deployFixtureWidgetComponent)
				}

				releaseCount, convErr := strconv.Atoi(rs.Primary.Attributes["release_count"])
				if convErr != nil || releaseCount <= 0 {
					return fmt.Errorf("release_count = %q, want a positive integer from the singular Get route "+
						"(the plural List route is the one documented — and separately confirmed by "+
						"TestAccDeployComponentsDataSourceNet_ReleaseCountAlwaysZero_GHOAuth — to always answer 0)",
						rs.Primary.Attributes["release_count"])
				}

				versionCount, _ := strconv.Atoi(rs.Primary.Attributes["versions.#"])
				if versionCount == 0 {
					return fmt.Errorf("versions.# = 0, want at least one recorded version for a component " +
						"deployed by the deploy-fixture-seed branch")
				}

				foundRealIDs := false
				for i := 0; i < versionCount; i++ {
					prefix := fmt.Sprintf("versions.%d.", i)
					runID := rs.Primary.Attributes[prefix+"run_id"]
					workflowID := rs.Primary.Attributes[prefix+"workflow_id"]
					jobID := rs.Primary.Attributes[prefix+"job_id"]

					if runID != "" && workflowID != "" && jobID != "" {
						foundRealIDs = true

						break
					}
				}

				if !foundRealIDs {
					return fmt.Errorf("no version among the %d returned carried a non-null run_id, workflow_id "+
						"and job_id; every version this fixture seeds is recorded by a real pipeline job, so "+
						"all-null (or circleci.ZeroUUID, which zeroUUIDToNull would have turned into this same "+
						"all-null shape) here would be a regression, not the documented sentinel case",
						versionCount)
				}

				return nil
			},
		}},
	})
}

// TestAccDeploySettingsDataSourceNet reads deploy settings for the active
// integration's writable fixture project. Unlike every other test in this
// file, this does not depend on the deploy-fixture-seed branch: [NET] this
// route answers the same `{}` whether or not a project's organization has
// ever used deploy markers (see this file's header comment on the
// enabled-vs-unused question) — so it needs no seeded fixture data, only a
// project that is a real, followed CircleCI project, which testProjectID
// already is for every configured integration.
func TestAccDeploySettingsDataSourceNet(t *testing.T) {
	projectID := testProjectID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_deploy_settings" "net_test" {
  project_id = %[1]q
}
`, projectID),
			Check: func(s *terraform.State) error {
				rs, err := deployDataSourceState(s, "data.circleci_deploy_settings.net_test")
				if err != nil {
					return err
				}

				if got := rs.Primary.Attributes["project_id"]; got != projectID {
					return fmt.Errorf("project_id = %q, want %q", got, projectID)
				}

				return nil
			},
		}},
	})
}

// TestAccDeployEnvironmentsDataSourceNet_UnseededOrgStaysEmpty pins, against
// a real, live, genuinely-untouched organization, that seeding deploy data
// elsewhere changed nothing about this one: it still answers an empty list,
// exactly as every organization did before any of them had deploy data. See
// this file's header comment ("The enabled-vs-unused question") for the
// fuller [NET] comparison (status code, body shape and response headers,
// checked directly against the API) this only partially re-proves through
// the provider itself.
//
// GL_CLOUD is used rather than a GH_* key specifically because it is not one
// of the two organizations this file seeds, so a future change that
// accidentally started seeding it too would be caught here.
func TestAccDeployEnvironmentsDataSourceNet_UnseededOrgStaysEmpty(t *testing.T) {
	orgID := testAccEnv(t, "CIRCLECI_TEST_GL_CLOUD_ORG_ID", "UUID of the GitLab Cloud test organization, which this workstream never seeded deploy data into")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_deploy_environments" "net_test" {
  org_id = %[1]q
}
`, orgID),
			Check: func(s *terraform.State) error {
				rs, err := deployDataSourceState(s, "data.circleci_deploy_environments.net_test")
				if err != nil {
					return err
				}

				if got := rs.Primary.Attributes["environments.#"]; got != "0" {
					return fmt.Errorf("environments.# = %s, want \"0\" for an organization this workstream "+
						"never seeded — if this now has entries, either something else started using deploys "+
						"on gitlab-test, or this organization is no longer the untouched control it is meant "+
						"to be", got)
				}

				return nil
			},
		}},
	})
}

// TestAccDeployComponentsDataSourceNet_SubstringMatch_GHApp is
// TestAccDeployComponentsDataSourceNet_SubstringMatch_GHOAuth's counterpart on
// the standalone (GitHub App) organization class, confirming the
// substring-match fact is not specific to a classic, VCS-backed organization.
func TestAccDeployComponentsDataSourceNet_SubstringMatch_GHApp(t *testing.T) {
	orgID := testDeployGHAppOrgID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_deploy_components" "net_test" {
  org_id = %[1]q
  name   = %[2]q
}
`, orgID, deployFixtureWidgetComponent),
			Check: func(s *terraform.State) error {
				rs, err := deployDataSourceState(s, "data.circleci_deploy_components.net_test")
				if err != nil {
					return err
				}

				names := flatmapListStrings(rs, "components", "name")
				for _, want := range []string{deployFixtureWidgetComponent, deployFixtureWidgetPlusComponent} {
					if !containsString(names, want) {
						return fmt.Errorf("filtering by name=%q in the GitHub App organization returned %v, "+
							"want it to include %q too", deployFixtureWidgetComponent, names, want)
					}
				}

				return nil
			},
		}},
	})
}

// TestAccDeployComponentDataSourceNet_DefaultedComponentIsProjectName_GHApp
// confirms a permanent consequence of gh-app-cci-1's very first deploy
// marker (see this file's header comment): a component named after the
// project itself ("test-repo") now exists there, because
// --component-name was omitted on that first call and there was nothing yet
// for CircleCI to disambiguate against. Unlike the moment it was created,
// this component's continued existence is a stable, permanently-repeatable
// fact, which is what this test actually pins.
func TestAccDeployComponentDataSourceNet_DefaultedComponentIsProjectName_GHApp(t *testing.T) {
	orgID := testDeployGHAppOrgID(t)
	client := testDeployClient(t)
	componentID := mustFindDeployComponentID(t, client, orgID, "test-repo")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_deploy_component" "net_test" {
  id = %[1]q
}
`, componentID),
			Check: func(s *terraform.State) error {
				rs, err := deployDataSourceState(s, "data.circleci_deploy_component.net_test")
				if err != nil {
					return err
				}

				if got := rs.Primary.Attributes["name"]; got != "test-repo" {
					return fmt.Errorf("name = %q, want %q", got, "test-repo")
				}

				return nil
			},
		}},
	})
}

// TestAccDeployEnvironmentDataSourceNet_DefaultedEnvironmentIsDefault_GHApp
// is TestAccDeployComponentDataSourceNet_DefaultedComponentIsProjectName_GHApp's
// counterpart for the environment side of the same first marker: an
// environment literally named "default" now permanently exists in this
// organization.
func TestAccDeployEnvironmentDataSourceNet_DefaultedEnvironmentIsDefault_GHApp(t *testing.T) {
	orgID := testDeployGHAppOrgID(t)
	client := testDeployClient(t)
	envID := mustFindDeployEnvironmentID(t, client, orgID, "default")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_deploy_environment" "net_test" {
  id = %[1]q
}
`, envID),
			Check: func(s *terraform.State) error {
				rs, err := deployDataSourceState(s, "data.circleci_deploy_environment.net_test")
				if err != nil {
					return err
				}

				if got := rs.Primary.Attributes["name"]; got != "default" {
					return fmt.Errorf("name = %q, want %q", got, "default")
				}

				return nil
			},
		}},
	})
}
