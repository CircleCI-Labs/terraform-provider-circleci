// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// This file is the only [NET] coverage circleci_notification_integration_status
// has, and it is deliberately narrow: every disposable fixture organization
// available to this suite (see the four listed in this package's other
// realapi files) has no Slack workspace installed -- GET
// /api/v3/notification/integrations?filter[org_id]=<fixture> answers
// {"data":[]} for all of them. There is no disposable fixture with a
// CONNECTED integration to create, update or delete against: the real,
// non-disposable organizations that do have one are exactly the ones this
// suite must never mutate, since a bug in a test here would be a real change
// to a real team's Slack notifications, not a throwaway one.
//
// So what this file establishes [NET] is exactly what the write path does
// against the one situation every fixture can actually offer: no integration
// at all. TestAccNotificationIntegrationStatusResource_RealAPI_NoIntegrationInstalled
// covers that honestly. Everything past it --
// SetNotificationIntegrationStatus actually flipping a real integration's
// status, and DeleteNotificationIntegration actually revoking one -- remains
// UNVALIDATED against the real API. The fake-driven tests in
// notification_integration_status_resource_test.go are the only coverage
// those paths have, and TestAccNotificationIntegrationsDataSource_RealAPI_NoneInstalled
// (notification_data_sources_realapi_test.go) is the only other [NET] contact
// this family's integration routes get: a read, not a write.

// TestAccNotificationIntegrationStatusResource_RealAPI_NoIntegrationInstalled
// proves, against the live API, what Create does when the id it is given
// does not name an installed integration: GetNotificationIntegration (the
// first call Create makes, to read the integration it is meant to adopt) is
// a plain 404 "Resource does not exist or unauthorized", which surfaces as
// this resource's ordinary "Unable to read" error -- not a distinct
// "integration not found" message, and not silently treated as success. A
// random, well-formed UUID is used for id specifically because it cannot
// coincide with a real integration on any organization: this test does not
// even reach the point of naming an organization, since
// GetNotificationIntegration's route is a bare
// /notification/integrations/{id} with no org-scoping to get through first.
func TestAccNotificationIntegrationStatusResource_RealAPI_NoIntegrationInstalled(t *testing.T) {
	nonExistentID := uuid.NewString()

	config := notificationRealProviderConfig() + fmt.Sprintf(`
resource "circleci_notification_integration_status" "test" {
  id     = %q
  status = "active"
}
`, nonExistentID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ExpectError: regexp.MustCompile(
				`(?s)Unable to read CircleCI notification integration.*Resource does not exist or unauthorized`,
			),
		}},
	})
}
