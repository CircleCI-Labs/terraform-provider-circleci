// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// This used to read a pre-existing fixture webhook (CIRCLECI_TEST_<key>_WEBHOOK_ID
// / _WEBHOOK_NAME / _WEBHOOK_URL), none of which any CI job's environment block
// sets, so it never ran in CI. It now creates its own scratch webhook — the
// same resource TestAccWebhookResource creates — so the only fixture it needs
// is testProjectID, which every CI job does set.
func TestAccWebhookDataSource(t *testing.T) {
	name := "tf-acc-webhook-ds-" + rand.Text()
	projectId := testProjectID(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create webhook resource first, then read with data source
			{
				Config: testAccWebhookDataSourceConfig(name, projectId),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_webhook.test_webhook_data",
						tfjsonpath.New("name"),
						knownvalue.StringExact(name),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_webhook.test_webhook_data",
						tfjsonpath.New("url"),
						knownvalue.StringExact("https://example.com/webhook"),
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
						knownvalue.SetExact([]knownvalue.Check{
							knownvalue.StringExact("workflow-completed"),
						}),
					),
					// The data source must resolve to the same webhook the config
					// created, not merely one with the same name.
					statecheck.CompareValuePairs(
						"circleci_webhook.test_webhook",
						tfjsonpath.New("id"),
						"data.circleci_webhook.test_webhook_data",
						tfjsonpath.New("id"),
						compare.ValuesSame(),
					),
				},
			},
		},
	})
}

func testAccWebhookDataSourceConfig(name, scopeId string) string {
	return fmt.Sprintf(`
resource "circleci_webhook" "test_webhook" {
  name           = %[1]q
  url            = "https://example.com/webhook"
  verify_tls     = true
  signing_secret = "secret"
  scope_id       = %[2]q
  scope_type     = "project"
  events         = ["workflow-completed"]
}

data "circleci_webhook" "test_webhook_data" {
  id = circleci_webhook.test_webhook.id
}
`, name, scopeId)
}
