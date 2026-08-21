// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// This file exercises circleci_notification_channel_config against a real
// CircleCI installation [NET] -- everything else in this family is tested
// only against the in-memory fake in notification_fake_test.go. It skips
// (via testAccPreCheck/testOrgID/testProjectID) rather than failing when no
// acceptance-test fixture is configured, exactly like every other TestAcc
// test in this package.
//
// notificationRealProviderConfig deliberately does not point at a fake: it
// is the same "provider circleci { host = ... }" block
// testContextDataSourceConfig and friends use to reach the real API.
func notificationRealProviderConfig() string {
	return `
provider "circleci" {
  host = "https://circleci.com/api/v2"
}
`
}

// TestAccNotificationChannelConfigResource_RealAPI_UserScope proves the full
// create/update/destroy cycle against the real API for a user-scoped email
// config: the one combination confirmed [NET] to echo target back on every
// read, so an empty plan after apply is a genuine assertion here, not
// something the fake could get wrong in the same direction as the code.
func TestAccNotificationChannelConfigResource_RealAPI_UserScope(t *testing.T) {
	config := func(target string, enabled bool) string {
		return notificationRealProviderConfig() + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "user"
  channel_type = "email"
  target       = %q
  is_enabled   = %t
  org_id       = %q
}
`, target, enabled, testOrgID(t))
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("tf-provider-notification-probe@example.com", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "target", "tf-provider-notification-probe@example.com"),
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "is_enabled", "true"),
					resource.TestCheckResourceAttrSet("circleci_notification_channel_config.test", "user_id"),
					resource.TestCheckNoResourceAttr("circleci_notification_channel_config.test", "channel_name"),
				),
			},
			{
				// A genuine in-place update, not a replace: id must be stable.
				Config: config("tf-provider-notification-probe-2@example.com", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "target", "tf-provider-notification-probe-2@example.com"),
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "is_enabled", "false"),
				),
			},
			{
				ResourceName:      "circleci_notification_channel_config.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccNotificationChannelConfigResource_RealAPI_ProjectScopeEmail is the
// real-API counterpart of
// TestAccNotificationChannelConfigResource_ProjectEmailTargetNeverEchoed: it
// proves against live CircleCI, not just the fake, that a project-scoped
// email config's target is accepted on create but never echoed back on any
// subsequent read -- and that applyNotificationChannelConfig's
// preserve-rather-than-null handling keeps the plan empty regardless. Without
// that handling this fails exactly the way the fake-driven test does when the
// fix is reverted: "Provider produced inconsistent result after apply".
func TestAccNotificationChannelConfigResource_RealAPI_ProjectScopeEmail(t *testing.T) {
	config := notificationRealProviderConfig() + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "project"
  channel_type = "email"
  target       = "tf-provider-notification-probe@example.com"
  is_enabled   = true
  project_id   = %q
  org_id       = %q
}
`, testProjectID(t), testOrgID(t))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "target", "tf-provider-notification-probe@example.com"),
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "scope", "project"),
				),
				// The regression this guards is a non-empty (or outright
				// inconsistent-result) plan on the implicit post-apply refresh,
				// which resource.Test checks on its own.
			},
		},
	})
}
