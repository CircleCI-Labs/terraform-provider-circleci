// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccNotificationPreferencesResource_User manages a sparse set of a
// user's preference toggles and checks that the full catalog is still
// reported through the computed preferences attribute.
func TestAccNotificationPreferencesResource_User(t *testing.T) {
	api := newNotificationFakeAPI(t)

	prefID := api.seedPreference("user", "Email Status", "email", true)
	api.seedPreference("user", "Slack Status", "slack", true) // unmanaged row

	config := func(enabled bool) string {
		return orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_preferences" "test" {
  scope = "user"
  updates = {
    %q = %t
  }
}
`, prefID, enabled)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_notification_preferences.test", "updates."+prefID, "false"),
					resource.TestCheckResourceAttr("circleci_notification_preferences.test", "preferences.#", "2"),
				),
			},
			{
				Config: config(true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_notification_preferences.test", "updates."+prefID, "true"),
				),
			},
		},
	})

	if got := len(api.requestsEndingIn("POST", "/preferences")); got != 2 {
		t.Errorf("update requests = %d, want 2 (one per apply)", got)
	}
}

// TestAccNotificationPreferencesResource_NoUpdates configures no toggles at
// all, which must still populate the computed preferences catalog without
// writing anything.
func TestAccNotificationPreferencesResource_NoUpdates(t *testing.T) {
	api := newNotificationFakeAPI(t)
	api.seedPreference("user", "Email Status", "email", true)

	config := orbProviderConfig(api.URL()) + `
resource "circleci_notification_preferences" "test" {
  scope = "user"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("circleci_notification_preferences.test", "preferences.#", "1"),
				resource.TestCheckNoResourceAttr("circleci_notification_preferences.test", "updates"),
			),
		}},
	})

	if got := len(api.requestsEndingIn("POST", "/preferences")); got != 0 {
		t.Errorf("update requests = %d, want 0: no toggle was configured, so nothing should be written", got)
	}
}

// TestAccNotificationPreferencesResource_Project covers the project scope,
// which requires project_id and org_id both in the config and as references
// on the bulk-update request.
func TestAccNotificationPreferencesResource_Project(t *testing.T) {
	api := newNotificationFakeAPI(t)
	prefID := api.seedPreference("project", "Slack Status", "slack", true)

	const projectID = "77777777-7777-7777-7777-777777777777"

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_preferences" "test" {
  scope      = "project"
  project_id = %q
  org_id     = %q
  updates = {
    %q = false
  }
}
`, projectID, notificationTestOrgID, prefID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("circleci_notification_preferences.test", "updates."+prefID, "false"),
			),
		}},
	})
}

// TestAccNotificationPreferencesResource_ImportUser proves import round-trips
// for the user scope: the preference row is seeded directly into the fake,
// bypassing Terraform entirely, standing in for the always-present catalog row
// this resource never creates. ImportStatePersist is required so the second
// step plans against the imported state rather than against whatever a prior
// apply left behind (there is none here, which is itself the point: unlike
// most resources, nothing has to be applied before this can be imported).
func TestAccNotificationPreferencesResource_ImportUser(t *testing.T) {
	api := newNotificationFakeAPI(t)
	prefID := api.seedPreference("user", "Email Status", "email", true)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_preferences" "test" {
  scope = "user"
  updates = {
    %q = true
  }
}
`, prefID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "circleci_notification_preferences.test",
				ImportState:        true,
				ImportStateId:      "user",
				ImportStatePersist: true,
				Config:             config,
			},
			{
				// updates is left null by ImportState on purpose -- see its doc
				// comment -- exactly like every toggle on
				// circleci_organization_settings. So the first plan after import
				// shows updates going from null to the configured map: a real,
				// visible, one-time diff, but an Update (this attribute carries no
				// RequiresReplace), never a Replace. preferences needs no such
				// allowance: it is Computed, and the Read that follows import
				// populates it from the API like any other attribute, so it is
				// identical before and after.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_notification_preferences.test", plancheck.ResourceActionUpdate,
						),
					},
				},
			},
		},
	})
}

// TestAccNotificationPreferencesResource_ImportProject is the project-scope
// counterpart, using the "<org_id>/<project_id>" import id.
func TestAccNotificationPreferencesResource_ImportProject(t *testing.T) {
	api := newNotificationFakeAPI(t)
	prefID := api.seedPreference("project", "Slack Status", "slack", true)

	const projectID = "77777777-7777-7777-7777-777777777777"

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_preferences" "test" {
  scope      = "project"
  project_id = %q
  org_id     = %q
  updates = {
    %q = false
  }
}
`, projectID, notificationTestOrgID, prefID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "circleci_notification_preferences.test",
				ImportState:        true,
				ImportStateId:      notificationTestOrgID + "/" + projectID,
				ImportStatePersist: true,
				Config:             config,
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_notification_preferences.test", plancheck.ResourceActionUpdate,
						),
					},
				},
			},
		},
	})
}

// TestAccNotificationPreferencesResource_ImportRejectsMalformedID guards the
// two shapes ImportState accepts: "user" alone, or exactly one slash for the
// project scope.
func TestAccNotificationPreferencesResource_ImportRejectsMalformedID(t *testing.T) {
	api := newNotificationFakeAPI(t)

	config := orbProviderConfig(api.URL()) + `
resource "circleci_notification_preferences" "test" {
  scope = "user"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			ResourceName:  "circleci_notification_preferences.test",
			ImportState:   true,
			ImportStateId: "not-a-recognised-id",
			Config:        config,
			ExpectError:   regexp.MustCompile(`(?s)Invalid import ID`),
		}},
	})
}

// TestAccNotificationPreferencesResource_RejectsMissingProjectID guards the
// scope validation.
func TestAccNotificationPreferencesResource_RejectsMissingProjectID(t *testing.T) {
	api := newNotificationFakeAPI(t)

	config := orbProviderConfig(api.URL()) + `
resource "circleci_notification_preferences" "test" {
  scope = "project"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`project_id and org_id are required`),
		}},
	})
}

// TestAccNotificationPreferencesResource_EnablesADisabledPreference starts from a
// preference that is already off.
//
// Every other test seeds preferences enabled, so the off-to-on direction was never
// exercised — and that is the one a practitioner actually writes a configuration
// for. It also proves the resource does not assume the API's default.
func TestAccNotificationPreferencesResource_EnablesADisabledPreference(t *testing.T) {
	api := newNotificationFakeAPI(t)

	prefID := api.seedPreference("user", "Email Status", "email", false)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_preferences" "test" {
  scope = "user"
  updates = {
    %q = true
  }
}
`, prefID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_notification_preferences.test", "updates."+prefID, "true"),
					// The computed catalog must report the new value, not the seeded one.
					resource.TestCheckResourceAttr("circleci_notification_preferences.test", "preferences.0.is_enabled", "true"),
				),
			},
			// A second plan must be empty: the API now agrees with the configuration.
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

// TestAccNotificationPreferencesResource_SectionFields covers the section
// attributes of the computed catalog.
//
// Every other test in this file seeds rows that belong to no section, so
// section_id and section_name are null in all of them — which is also exactly
// what a misspelled json tag would produce. A read-only attribute that is
// silently null forever gives no signal at all, so a row that really is in a
// section is asserted here, alongside a row that is not, so both branches of
// the nullable field are covered by the same catalog.
func TestAccNotificationPreferencesResource_SectionFields(t *testing.T) {
	api := newNotificationFakeAPI(t)

	sectioned := api.seedPreferenceInSection(
		"user", "Deploy Finished", "slack",
		"55555555-5555-5555-5555-555555555555", "Release tracking", true,
	)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_preferences" "test" {
  scope = "user"
  updates = {
    %q = false
  }
}
`, sectioned)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("circleci_notification_preferences.test", "preferences.#", "1"),
				resource.TestCheckResourceAttr(
					"circleci_notification_preferences.test",
					"preferences.0.section_id", "55555555-5555-5555-5555-555555555555",
				),
				resource.TestCheckResourceAttr(
					"circleci_notification_preferences.test",
					"preferences.0.section_name", "Release tracking",
				),
			),
		}},
	})
}

// TestAccNotificationPreferencesResource_NullSectionStaysNull is the other half:
// a row with no section must report null rather than an empty string, so a
// configuration can tell "no section" apart from "a section with no name".
func TestAccNotificationPreferencesResource_NullSectionStaysNull(t *testing.T) {
	api := newNotificationFakeAPI(t)
	api.seedPreference("user", "Email Status", "email", true)

	config := orbProviderConfig(api.URL()) + `
resource "circleci_notification_preferences" "test" {
  scope = "user"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr("circleci_notification_preferences.test", "preferences.0.section_id"),
				resource.TestCheckNoResourceAttr("circleci_notification_preferences.test", "preferences.0.section_name"),
			),
		}},
	})
}
