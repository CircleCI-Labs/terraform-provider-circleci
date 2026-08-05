// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
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

// TestNotificationChannelConfigsDataSourceUnit_UserScopeRejectsOrgID covers the
// plan-time guard on scope = "user" combined with org_id.
//
// ListNotificationChannelConfigs always sends filter[org_id] when the
// attribute is set, but a user-scoped listing takes no organization argument
// at all: the fake API's listChannelConfigs filters project-scoped rows by
// org_id but never checks it for user-scoped ones, matching the real API.
// Setting org_id there would silently do nothing rather than scope the
// result, so it must be refused before the request is ever sent.
func TestNotificationChannelConfigsDataSourceUnit_UserScopeRejectsOrgID(t *testing.T) {
	api := newNotificationFakeAPI(t)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
data "circleci_notification_channel_configs" "test" {
  scope  = "user"
  org_id = %q
}
`, notificationTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`org_id is not honored for scope = "user"`),
		}},
	})

	if requests := api.requestsFor("GET", "/notification/channel-configs"); len(requests) != 0 {
		t.Errorf("the provider listed channel configs despite the invalid combination: %v", requests)
	}
}

// TestNotificationChannelConfigsDataSourceUnit_UserScopeWithoutOrgID is the
// control: scope = "user" alone, with no org_id, must still work — the guard
// must not reject the ordinary case.
func TestNotificationChannelConfigsDataSourceUnit_UserScopeWithoutOrgID(t *testing.T) {
	api := newNotificationFakeAPI(t)

	api.mu.Lock()
	api.channelCfgs["existing"] = &notificationFakeChannelConfig{
		ID:          "existing",
		Scope:       "user",
		ChannelType: "email",
		Target:      "me@example.com",
		IsEnabled:   true,
		OrgID:       notificationTestOrgID,
		UserID:      notificationFakeCallerUserID,
	}
	api.mu.Unlock()

	config := orbProviderConfig(api.URL()) + `
data "circleci_notification_channel_configs" "test" {
  scope = "user"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.circleci_notification_channel_configs.test", "channel_configs.#", "1"),
			),
		}},
	})
}
