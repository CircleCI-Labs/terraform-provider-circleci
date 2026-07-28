// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccNotificationTypesRequireCloud checks that every notification type
// fails with an explicit, actionable message when the provider is configured
// for CircleCI Server, which does not route /api/v3.
func TestAccNotificationTypesRequireCloud(t *testing.T) {
	wantError := regexp.MustCompile(`(?s)requires CircleCI Cloud.*v3.*"server".*circleci\.example\.com`)

	tests := []struct {
		name   string
		config string
	}{
		{
			name: "circleci_notification_channel_config resource",
			config: `
resource "circleci_notification_channel_config" "test" {
  scope        = "user"
  channel_type = "email"
  target       = "me@example.com"
  is_enabled   = true
  org_id       = "11111111-1111-1111-1111-111111111111"
}
`,
		},
		{
			name: "circleci_notification_channel_config data source",
			config: `
data "circleci_notification_channel_config" "test" {
  id = "22222222-2222-2222-2222-222222222222"
}
`,
		},
		{
			name: "circleci_notification_channel_configs data source",
			config: `
data "circleci_notification_channel_configs" "test" {
  scope = "user"
}
`,
		},
		{
			name: "circleci_notification_preferences resource",
			config: `
resource "circleci_notification_preferences" "test" {
  scope = "user"
}
`,
		},
		{
			name: "circleci_notification_integrations data source",
			config: `
data "circleci_notification_integrations" "test" {}
`,
		},
		{
			name: "circleci_notification_integration_status resource",
			config: `
resource "circleci_notification_integration_status" "test" {
  id     = "33333333-3333-3333-3333-333333333333"
  status = "active"
}
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      orbServerProviderConfig() + tt.config,
					ExpectError: wantError,
				}},
			})
		})
	}
}
