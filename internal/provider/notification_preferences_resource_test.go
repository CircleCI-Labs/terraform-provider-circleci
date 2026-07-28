// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
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
