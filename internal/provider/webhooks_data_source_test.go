// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func testAccWebhooksDataSourceConfig(host, deployment string) string {
	return pluralProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_webhooks" "test" {
  project_id = %[1]q
}
`, testPluralProjectID)
}

func TestWebhooksDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewWebhooksDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	projectID, ok := resp.Schema.Attributes["project_id"]
	if !ok {
		t.Fatal("schema is missing the project_id attribute")
	}
	if !projectID.IsRequired() {
		t.Error("project_id is not required, but it is the scope of the listing")
	}

	webhooks, ok := resp.Schema.Attributes["webhooks"]
	if !ok {
		t.Fatal("schema is missing the webhooks attribute")
	}
	if !webhooks.IsComputed() {
		t.Error("webhooks is not computed, but it is entirely API-derived")
	}
}

func TestAccWebhooksDataSource(t *testing.T) {
	api, host := newPluralAPI(t)

	// One webhook per page, so the result only covers both if the pagination is
	// drained. One has a signing secret and one does not, because that is the only
	// fact about the secret a read can establish.
	api.pageSize = 1
	api.seedWebhook(testPluralProjectID, "w1", "on-workflow", "https://hooks.example.com/one",
		[]string{"workflow-completed", "job-completed"}, true, true)
	api.seedWebhook(testPluralProjectID, "w2", "on-job", "https://hooks.example.com/two",
		[]string{"job-completed"}, false, false)

	// Another project's webhooks must not leak into the result.
	api.seedWebhook("99999999-9999-9999-9999-999999999999", "other", "elsewhere",
		"https://hooks.example.com/other", []string{"job-completed"}, true, false)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccWebhooksDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_webhooks.test",
						tfjsonpath.New("webhooks"),
						knownvalue.ListSizeExact(2),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_webhooks.test",
						tfjsonpath.New("webhooks").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("on-workflow"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_webhooks.test",
						tfjsonpath.New("webhooks").AtSliceIndex(0).AtMapKey("url"),
						knownvalue.StringExact("https://hooks.example.com/one"),
					),
					// The nested scope object is flattened, matching circleci_webhook.
					statecheck.ExpectKnownValue(
						"data.circleci_webhooks.test",
						tfjsonpath.New("webhooks").AtSliceIndex(0).AtMapKey("scope_id"),
						knownvalue.StringExact(testPluralProjectID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_webhooks.test",
						tfjsonpath.New("webhooks").AtSliceIndex(0).AtMapKey("scope_type"),
						knownvalue.StringExact("project"),
					),
					// Event names keep their hyphens: only JSON keys are converted to
					// snake_case by the API, not values.
					statecheck.ExpectKnownValue(
						"data.circleci_webhooks.test",
						tfjsonpath.New("webhooks").AtSliceIndex(0).AtMapKey("events"),
						knownvalue.ListExact([]knownvalue.Check{
							knownvalue.StringExact("workflow-completed"),
							knownvalue.StringExact("job-completed"),
						}),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_webhooks.test",
						tfjsonpath.New("webhooks").AtSliceIndex(0).AtMapKey("verify_tls"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_webhooks.test",
						tfjsonpath.New("webhooks").AtSliceIndex(0).AtMapKey("has_signing_secret"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_webhooks.test",
						tfjsonpath.New("webhooks").AtSliceIndex(0).AtMapKey("created_at"),
						knownvalue.StringExact("2014-02-11T22:40:37Z"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_webhooks.test",
						tfjsonpath.New("webhooks").AtSliceIndex(1).AtMapKey("verify_tls"),
						knownvalue.Bool(false),
					),
					// A webhook with no signing secret reads back "" from the API,
					// which must report as false rather than as a masked secret.
					statecheck.ExpectKnownValue(
						"data.circleci_webhooks.test",
						tfjsonpath.New("webhooks").AtSliceIndex(1).AtMapKey("has_signing_secret"),
						knownvalue.Bool(false),
					),
				},
			},
		},
	})
}

// TestAccWebhooksDataSource_serverDeployment covers CircleCI Server: webhooks are
// v2 on both deployments, so this must work rather than be rejected.
func TestAccWebhooksDataSource_serverDeployment(t *testing.T) {
	api, host := newPluralAPI(t)

	api.seedWebhook(testPluralProjectID, "w1", "on-job", "https://hooks.example.com/one",
		[]string{"job-completed"}, true, false)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccWebhooksDataSourceConfig(host, "server"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_webhooks.test",
						tfjsonpath.New("webhooks"),
						knownvalue.ListSizeExact(1),
					),
				},
			},
		},
	})
}

func TestAccWebhooksDataSource_empty(t *testing.T) {
	_, host := newPluralAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccWebhooksDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_webhooks.test",
						tfjsonpath.New("webhooks"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
		},
	})
}

func TestAccWebhooksDataSource_apiError(t *testing.T) {
	api, host := newPluralAPI(t)

	api.fail(400, "Invalid scope parameters")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccWebhooksDataSourceConfig(host, "cloud"),
			ExpectError: regexp.MustCompile(`(?s)Unable to list CircleCI webhooks.*Invalid scope parameters`),
		}},
	})
}
