// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccWebhookDataSource(t *testing.T) {
	name := testWebhookName(t)
	url := testWebhookURL(t)
	projectId := testProjectID(t)
	webhookId := testWebhookID(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create webhook resource first, then read with data source
			{
				Config: testAccWebhookDataSourceConfig(webhookId),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_webhook.test_webhook_data",
						tfjsonpath.New("name"),
						knownvalue.StringExact(name),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_webhook.test_webhook_data",
						tfjsonpath.New("url"),
						knownvalue.StringExact(url),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_webhook.test_webhook_data",
						tfjsonpath.New("verify_tls"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_webhook.test_webhook_data",
						tfjsonpath.New("scope_id"),
						knownvalue.StringExact(projectId),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_webhook.test_webhook_data",
						tfjsonpath.New("scope_type"),
						knownvalue.StringExact("project"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_webhook.test_webhook_data",
						tfjsonpath.New("events"),
						knownvalue.ListExact([]knownvalue.Check{
							knownvalue.StringExact("workflow-completed"),
						}),
					),
				},
			},
		},
	})
}

func testAccWebhookDataSourceConfig(id string) string {
	return fmt.Sprintf(`
data "circleci_webhook" "test_webhook_data" {
  id = %[1]q
}
`, id)
}
