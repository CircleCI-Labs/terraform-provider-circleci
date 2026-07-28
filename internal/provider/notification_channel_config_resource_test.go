// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

const notificationTestOrgID = "44444444-4444-4444-4444-444444444444"

// TestAccNotificationChannelConfigResource_User covers the create/update/
// destroy cycle of a user-scoped email channel config, and asserts that
// changing target and is_enabled is a genuine in-place update.
func TestAccNotificationChannelConfigResource_User(t *testing.T) {
	api := newNotificationFakeAPI(t)

	config := func(target string, enabled bool) string {
		return orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "user"
  channel_type = "email"
  target       = %q
  is_enabled   = %t
  org_id       = %q
}
`, target, enabled, notificationTestOrgID)
	}

	var id string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("me@example.com", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "scope", "user"),
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "target", "me@example.com"),
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "is_enabled", "true"),
					resource.TestCheckResourceAttrSet("circleci_notification_channel_config.test", "user_id"),
					orbCaptureAttr("circleci_notification_channel_config.test", "id", &id),
				),
			},
			{
				Config: config("someone-else@example.com", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "target", "someone-else@example.com"),
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "is_enabled", "false"),
					orbExpectAttr("circleci_notification_channel_config.test", "id", &id),
				),
			},
		},
	})

	if got := len(api.requestsFor("POST", "/update")); got != 1 {
		t.Errorf("update requests = %d, want 1: changing target/is_enabled must be a real update, not a replace", got)
	}
	if got := len(api.requestsEndingIn("POST", "/channel-configs")); got != 1 {
		t.Errorf("create requests = %d, want 1 (only the initial create)", got)
	}
}

// TestAccNotificationChannelConfigResource_ProjectSlack covers a
// project-scoped Slack config and its resolved channel_name.
func TestAccNotificationChannelConfigResource_ProjectSlack(t *testing.T) {
	api := newNotificationFakeAPI(t)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "project"
  channel_type = "slack"
  target       = "C0123456789"
  is_enabled   = true
  project_id   = "55555555-5555-5555-5555-555555555555"
  org_id       = %q
}
`, notificationTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "channel_name", "#C0123456789"),
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "project_id", "55555555-5555-5555-5555-555555555555"),
				),
			},
		},
	})
}

// TestAccNotificationChannelConfigResource_RejectsMismatchedProjectID guards
// the scope/project_id validation: project_id must be omitted for user scope.
func TestAccNotificationChannelConfigResource_RejectsMismatchedProjectID(t *testing.T) {
	api := newNotificationFakeAPI(t)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "user"
  channel_type = "email"
  target       = "me@example.com"
  is_enabled   = true
  project_id   = "55555555-5555-5555-5555-555555555555"
  org_id       = %q
}
`, notificationTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`project_id must be omitted`),
		}},
	})
}

// TestAccNotificationChannelConfigResource_DriftRecreates drops the resource
// from state when the config is gone, rather than failing the refresh.
func TestAccNotificationChannelConfigResource_DriftRecreates(t *testing.T) {
	api := newNotificationFakeAPI(t)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "user"
  channel_type = "email"
  target       = "me@example.com"
  is_enabled   = true
  org_id       = %q
}
`, notificationTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()

					api.channelCfgs = map[string]*notificationFakeChannelConfig{}
				},
				Config:             config,
				ExpectNonEmptyPlan: true,
				PlanOnly:           true,
			},
		},
	})
}
