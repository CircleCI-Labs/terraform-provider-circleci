// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccNotificationIntegrationStatusResource_Adopt adopts an integration
// installed outside Terraform and toggles its status in place.
func TestAccNotificationIntegrationStatusResource_Adopt(t *testing.T) {
	api := newNotificationFakeAPI(t)

	i := api.seedIntegration("acme-corp", "T-ACME", notificationTestOrgID, "Acme Corp")

	config := func(status string) string {
		return orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_integration_status" "test" {
  id     = %q
  status = %q
}
`, i.ID, status)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("disabled"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_notification_integration_status.test", "status", "disabled"),
					resource.TestCheckResourceAttr("circleci_notification_integration_status.test", "workspace_name", "acme-corp"),
					resource.TestCheckResourceAttr("circleci_notification_integration_status.test", "org_name", "Acme Corp"),
				),
			},
			{
				Config: config("active"),
				Check:  resource.TestCheckResourceAttr("circleci_notification_integration_status.test", "status", "active"),
			},
		},
	})

	if got := len(api.requestsFor("POST", "/set-status")); got != 2 {
		t.Errorf("set-status requests = %d, want 2 (create adopts+sets once, update sets again)", got)
	}
	if got := len(api.requestsFor("DELETE", "/integrations/")); got != 0 {
		t.Errorf("delete requests = %d, want 0: destroying this resource must not revoke the integration", got)
	}
}

// TestAccNotificationIntegrationStatusResource_DestroyDoesNotRevoke asserts
// that the fake integration is still active (not revoked) after Terraform
// destroys the resource.
func TestAccNotificationIntegrationStatusResource_DestroyDoesNotRevoke(t *testing.T) {
	api := newNotificationFakeAPI(t)

	i := api.seedIntegration("acme-corp", "T-ACME", notificationTestOrgID, "Acme Corp")

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_integration_status" "test" {
  id     = %q
  status = "active"
}
`, i.ID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				Config: orbProviderConfig(api.URL()),
			},
		},
	})

	api.mu.Lock()
	defer api.mu.Unlock()
	if got := api.integrations[i.ID].Status; got != "active" {
		t.Errorf("integration status after destroy = %q, want %q: destroy must not revoke it", got, "active")
	}
}

// TestAccNotificationIntegrationStatusResource_ImportRoundTrips proves import
// round-trips cleanly: status is Required rather than Computed on the schema
// (see the ImportState doc comment on why), but the Read that follows import
// fills it in from the API along with every other attribute, so a
// configuration matching the integration's actual status plans no change at
// all -- not the one-time update/replace this package's other import tests
// found for the ios-signing and otel-exporter resources.
//
// The integration is seeded directly into the fake, the same way every other
// test in this file adopts one "installed outside Terraform" (there is no API
// route to install one at all -- see the resource's own doc comment).
// ImportStatePersist makes the second step plan against the imported state
// rather than against whatever a previous step left behind.
func TestAccNotificationIntegrationStatusResource_ImportRoundTrips(t *testing.T) {
	api := newNotificationFakeAPI(t)

	i := api.seedIntegration("acme-corp", "T-ACME", notificationTestOrgID, "Acme Corp")

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_integration_status" "test" {
  id     = %q
  status = "active"
}
`, i.ID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "circleci_notification_integration_status.test",
				ImportState:        true,
				ImportStateId:      i.ID,
				ImportStatePersist: true,
				Config:             config,
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_notification_integration_status.test", plancheck.ResourceActionNoop,
						),
					},
				},
			},
		},
	})
}

// TestAccNotificationIntegrationStatusResource_DriftRecreates drops the
// resource from state when the underlying integration is gone (revoked or
// deleted outside Terraform).
func TestAccNotificationIntegrationStatusResource_DriftRecreates(t *testing.T) {
	api := newNotificationFakeAPI(t)

	i := api.seedIntegration("acme-corp", "T-ACME", notificationTestOrgID, "Acme Corp")

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_integration_status" "test" {
  id     = %q
  status = "active"
}
`, i.ID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()

					api.integrations[i.ID].Status = "revoked"
				},
				Config:             config,
				ExpectNonEmptyPlan: true,
				PlanOnly:           true,
			},
		},
	})
}
