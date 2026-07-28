// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
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
