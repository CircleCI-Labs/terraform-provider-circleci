// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccNotificationIntegrationsDataSource lists integrations installed
// outside Terraform (there is no resource that could have created them).
func TestAccNotificationIntegrationsDataSource(t *testing.T) {
	api := newNotificationFakeAPI(t)

	i := api.seedIntegration("acme-corp", "T-ACME", notificationTestOrgID, "Acme Corp")

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
data "circleci_notification_integrations" "test" {
  org_id = %q
}
`, notificationTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.circleci_notification_integrations.test", "integrations.#", "1"),
				resource.TestCheckResourceAttr("data.circleci_notification_integrations.test", "integrations.0.id", i.ID),
				resource.TestCheckResourceAttr("data.circleci_notification_integrations.test", "integrations.0.workspace_name", "acme-corp"),
				resource.TestCheckResourceAttr("data.circleci_notification_integrations.test", "integrations.0.status", "active"),
				resource.TestCheckResourceAttr("data.circleci_notification_integrations.test", "integrations.0.org_name", "Acme Corp"),
			),
		}},
	})
}

// TestAccNotificationIntegrationsDataSource_ExcludesRevoked confirms a
// revoked integration never appears in the listing.
func TestAccNotificationIntegrationsDataSource_ExcludesRevoked(t *testing.T) {
	api := newNotificationFakeAPI(t)

	i := api.seedIntegration("gone-corp", "T-GONE", notificationTestOrgID, "Gone Corp")
	api.mu.Lock()
	i.Status = "revoked"
	api.mu.Unlock()

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
data "circleci_notification_integrations" "test" {
  org_id = %q
}
`, notificationTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check:  resource.TestCheckResourceAttr("data.circleci_notification_integrations.test", "integrations.#", "0"),
		}},
	})
}

// TestAccNotificationIntegrationsDataSource_OtherOrgIsExcluded proves the listing
// is scoped to the organization asked for.
//
// Every other test seeded a single organization, so a data source that ignored its
// org filter entirely would have passed all of them.
func TestAccNotificationIntegrationsDataSource_OtherOrgIsExcluded(t *testing.T) {
	api := newNotificationFakeAPI(t)

	const otherOrgID = "99999999-9999-9999-9999-999999999999"

	mine := api.seedIntegration("acme-corp", "T-ACME", notificationTestOrgID, "Acme Corp")
	api.seedIntegration("other-corp", "T-OTHER", otherOrgID, "Other Corp")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: orbProviderConfig(api.URL()) + fmt.Sprintf(`
data "circleci_notification_integrations" "test" {
  org_id = %q
}
`, notificationTestOrgID),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.circleci_notification_integrations.test", "integrations.#", "1"),
				resource.TestCheckResourceAttr("data.circleci_notification_integrations.test", "integrations.0.id", mine.ID),
			),
		}},
	})
}
