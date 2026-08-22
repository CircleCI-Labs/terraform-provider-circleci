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

// TestAccNotificationChannelConfigResource_ProjectEmailTargetNeverEchoed
// covers the one combination the real API [NET] was confirmed to handle
// differently from every other: a project-scoped, channel_type = "email"
// config accepts a target on create but never returns one afterwards -- not
// on the create response, not on a subsequent get, not in a list. Without
// applyNotificationChannelConfig's "leave target alone when the API sends
// none back" behaviour, this shows up as a plan that is never empty: every
// refresh nulls state's target to "", and the next plan wants to set it back
// to what the configuration says, forever. resource.Test's built-in
// post-apply plan check is what actually catches that, with no assertion of
// its own needed here.
func TestAccNotificationChannelConfigResource_ProjectEmailTargetNeverEchoed(t *testing.T) {
	api := newNotificationFakeAPI(t)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "project"
  channel_type = "email"
  target       = "team@example.com"
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
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "target", "team@example.com"),
				),
				// The bug this guards manifests as a non-empty plan on the
				// implicit post-apply refresh below, not as an error from this
				// step -- resource.Test performs that check on every step
				// unless ExpectNonEmptyPlan is set, which it deliberately is
				// not here.
			},
		},
	})
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

// TestAccNotificationChannelConfigResource_ImportRoundTrips proves import
// round-trips cleanly, unlike the ios-signing and otel-exporter resources:
// every attribute here comes back from a read (see applyNotificationChannelConfig),
// there is no unreadable secret and no RequiresReplace attribute whose value
// import cannot recover, so the plan right after import is genuinely empty
// once the configuration matches what CircleCI reports -- not a one-time
// update or replacement.
//
// The channel config is seeded directly into the fake, bypassing Terraform
// Create entirely, to stand in for one that already exists and was never
// created by this Terraform run. ImportStatePersist makes the second step
// plan against the imported state rather than against whatever a previous
// step left behind.
func TestAccNotificationChannelConfigResource_ImportRoundTrips(t *testing.T) {
	api := newNotificationFakeAPI(t)

	const id = "66666666-6666-6666-6666-666666666666"
	api.channelCfgs[id] = &notificationFakeChannelConfig{
		ID:          id,
		Scope:       "user",
		ChannelType: "email",
		Target:      "me@example.com",
		IsEnabled:   true,
		OrgID:       notificationTestOrgID,
		UserID:      "77777777-7777-7777-7777-777777777777",
	}

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
			{
				ResourceName:       "circleci_notification_channel_config.test",
				ImportState:        true,
				ImportStateId:      id,
				ImportStatePersist: true,
				Config:             config,
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_notification_channel_config.test", plancheck.ResourceActionNoop,
						),
					},
				},
			},
		},
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
