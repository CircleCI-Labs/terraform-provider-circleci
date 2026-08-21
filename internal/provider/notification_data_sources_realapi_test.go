// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// This file exercises the plural notification data sources against a real
// CircleCI installation [NET]. Every other test for these data sources runs
// only against the in-memory fake.

// TestAccNotificationIntegrationsDataSource_RealAPI_NoneInstalled confirms,
// against the live API, that an organization with no Slack workspace
// installed reports an empty list rather than an error -- every disposable
// fixture organization available for this family's testing is in that state
// [NET], so this is also the only real coverage
// circleci_notification_integrations gets: there is no fixture with an
// installed integration to exercise a non-empty result against.
func TestAccNotificationIntegrationsDataSource_RealAPI_NoneInstalled(t *testing.T) {
	config := notificationRealProviderConfig() + fmt.Sprintf(`
data "circleci_notification_integrations" "test" {
  org_id = %q
}
`, testOrgID(t))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.circleci_notification_integrations.test", "integrations.#", "0"),
			),
		}},
	})
}

// TestAccNotificationChannelConfigsDataSource_RealAPI_ProjectScope creates a
// real project-scoped Slack channel config's email counterpart through the
// resource, then confirms the plural data source lists it back -- against
// live CircleCI, not the fake, covering the one filter combination
// (scope = "project") the plural listing route actually takes a project_id
// and org_id for.
func TestAccNotificationChannelConfigsDataSource_RealAPI_ProjectScope(t *testing.T) {
	config := notificationRealProviderConfig() + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "project"
  channel_type = "email"
  target       = "tf-provider-notification-list-probe@example.com"
  is_enabled   = true
  project_id   = %[1]q
  org_id       = %[2]q
}

data "circleci_notification_channel_configs" "test" {
  scope      = "project"
  project_id = %[1]q
  org_id     = %[2]q

  depends_on = [circleci_notification_channel_config.test]
}
`, testProjectID(t), testOrgID(t))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.circleci_notification_channel_configs.test", "channel_configs.#", "1"),
				resource.TestCheckResourceAttr("data.circleci_notification_channel_configs.test", "channel_configs.0.channel_type", "email"),
				resource.TestCheckResourceAttr("data.circleci_notification_channel_configs.test", "channel_configs.0.is_enabled", "true"),
				// The real API's project + email quirk (see
				// applyNotificationChannelConfig) reaches the plural listing too:
				// target is genuinely absent from CircleCI's response for this
				// combination, so the data source -- which has no prior state to
				// preserve, unlike the resource -- reports it as null. Confirmed
				// [NET]; asserting the null here pins that down as fact rather
				// than as an assumption a future change could silently invert.
				// (The attribute is present-but-null, not absent, in the legacy
				// flatmap representation TestCheckResourceAttr reads, hence "" here
				// rather than TestCheckNoResourceAttr.)
				resource.TestCheckResourceAttr("data.circleci_notification_channel_configs.test", "channel_configs.0.target", ""),
			),
		}},
	})
}
