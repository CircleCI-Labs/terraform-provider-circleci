// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccNotificationChannelConfigsDataSource lists channel configs for a
// project scope, seeded outside Terraform.
func TestAccNotificationChannelConfigsDataSource(t *testing.T) {
	api := newNotificationFakeAPI(t)

	const projectID = "66666666-6666-6666-6666-666666666666"

	api.mu.Lock()
	for _, target := range []string{"a@example.com", "b@example.com"} {
		cc := &notificationFakeChannelConfig{
			ID:          api.mintID(),
			Scope:       "project",
			ChannelType: "email",
			Target:      target,
			IsEnabled:   true,
			ProjectID:   projectID,
			OrgID:       notificationTestOrgID,
		}
		api.channelCfgs[cc.ID] = cc
	}
	api.mu.Unlock()

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
data "circleci_notification_channel_configs" "test" {
  scope      = "project"
  project_id = %q
  org_id     = %q
}
`, projectID, notificationTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.circleci_notification_channel_configs.test", "channel_configs.#", "2"),
			),
		}},
	})
}
