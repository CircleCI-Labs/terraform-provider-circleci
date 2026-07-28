// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccNotificationChannelConfigDataSource looks up a channel config seeded
// outside Terraform, by id.
func TestAccNotificationChannelConfigDataSource(t *testing.T) {
	api := newNotificationFakeAPI(t)

	api.mu.Lock()
	cc := &notificationFakeChannelConfig{
		ID:          api.mintID(),
		Scope:       "user",
		ChannelType: "email",
		Target:      "seeded@example.com",
		IsEnabled:   true,
		OrgID:       notificationTestOrgID,
		UserID:      notificationFakeCallerUserID,
	}
	api.channelCfgs[cc.ID] = cc
	api.mu.Unlock()

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
data "circleci_notification_channel_config" "test" {
  id = %q
}
`, cc.ID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.circleci_notification_channel_config.test", "target", "seeded@example.com"),
				resource.TestCheckResourceAttr("data.circleci_notification_channel_config.test", "channel_type", "email"),
				resource.TestCheckResourceAttr("data.circleci_notification_channel_config.test", "scope", "user"),
				resource.TestCheckResourceAttr("data.circleci_notification_channel_config.test", "user_id", notificationFakeCallerUserID),
			),
		}},
	})
}
