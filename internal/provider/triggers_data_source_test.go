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

func testAccTriggersDataSourceConfig(host, deployment string) string {
	return pluralProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_triggers" "test" {
  project_id  = %[1]q
  pipeline_id = %[2]q
}
`, testPluralProjectID, testPluralPipelineID)
}

func TestTriggersDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewTriggersDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// Both ids are required: a trigger id is only meaningful within a pipeline
	// definition, which is only meaningful within a project.
	for _, name := range []string{"project_id", "pipeline_id"} {
		attribute, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("schema is missing the %q attribute", name)
		}
		if !attribute.IsRequired() {
			t.Errorf("%s is not required, but it is part of the scope of the listing", name)
		}
	}

	triggers, ok := resp.Schema.Attributes["triggers"]
	if !ok {
		t.Fatal("schema is missing the triggers attribute")
	}
	if !triggers.IsComputed() {
		t.Error("triggers is not computed, but it is entirely API-derived")
	}
}

func TestAccTriggersDataSource(t *testing.T) {
	api, host := newPluralAPI(t)

	api.seedRepoTrigger(testPluralProjectID, testPluralPipelineID, "t1", "acme-bot", "push",
		"github_app.push", "acme/api", "123456")
	api.seedScheduledTrigger(testPluralProjectID, testPluralPipelineID, "t2", "nightly",
		"0 0 * * *", "a1b2c3", map[string]any{
			"deploy":   true,
			"replicas": 3,
			"env":      "prod",
		})

	// Another definition's triggers must not leak into the result.
	api.seedRepoTrigger(testPluralProjectID, "ffffffff-ffff-ffff-ffff-ffffffffffff", "other", "elsewhere",
		"push", "github_app.push", "acme/other", "999")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccTriggersDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers"),
						knownvalue.ListSizeExact(2),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(0).AtMapKey("event_source_provider"),
						knownvalue.StringExact("github_app"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(0).AtMapKey("event_source_repository_name"),
						knownvalue.StringExact("acme/api"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(0).AtMapKey("event_source_repository_external_id"),
						knownvalue.StringExact("123456"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(0).AtMapKey("event_preset"),
						knownvalue.StringExact("github_app.push"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(0).AtMapKey("checkout_ref"),
						knownvalue.StringExact("main"),
					),
					// The API omits `disabled` for an enabled trigger, which must be
					// reported as false rather than left unknown.
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(0).AtMapKey("disabled"),
						knownvalue.Bool(false),
					),
					// A trigger with no parameters reports null, so that "sets none"
					// stays distinguishable from "sets an empty one".
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(0).AtMapKey("parameters"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(1).AtMapKey("disabled"),
						knownvalue.Bool(true),
					),
					// The schedule is flattened, and attribution_actor is an object
					// carrying an id rather than a bare string on read.
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(1).AtMapKey("event_source_schedule_cron_expression"),
						knownvalue.StringExact("0 0 * * *"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(1).AtMapKey("event_source_schedule_attribution_actor"),
						knownvalue.StringExact("a1b2c3"),
					),
					// Parameters arrive as arbitrary JSON and must render without
					// scientific notation or Go syntax.
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(1).AtMapKey("parameters"),
						knownvalue.MapExact(map[string]knownvalue.Check{
							"deploy":   knownvalue.StringExact("true"),
							"replicas": knownvalue.StringExact("3"),
							"env":      knownvalue.StringExact("prod"),
						}),
					),
				},
			},
		},
	})
}

func TestAccTriggersDataSource_webhookProvider(t *testing.T) {
	api, host := newPluralAPI(t)

	// A webhook trigger's inbound URL and sender must both surface; the repository
	// and schedule fields stay empty for it.
	api.mu.Lock()
	api.triggers[testPluralProjectID+"/"+testPluralPipelineID] = []map[string]any{{
		"id":         "t3",
		"name":       "inbound",
		"event_name": "Inbound webhook",
		"event_source": map[string]any{
			"provider": "webhook",
			"webhook": map[string]any{
				"url":    "https://example.com/private/soc/e/t3?secret=**REDACTED**",
				"sender": "inbound",
			},
		},
	}}
	api.mu.Unlock()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccTriggersDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(0).AtMapKey("event_source_webhook_url"),
						knownvalue.StringExact("https://example.com/private/soc/e/t3?secret=**REDACTED**"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(0).AtMapKey("event_source_webhook_sender"),
						knownvalue.StringExact("inbound"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(0).AtMapKey("event_source_repository_name"),
						knownvalue.StringExact(""),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers").AtSliceIndex(0).AtMapKey("event_source_schedule_cron_expression"),
						knownvalue.StringExact(""),
					),
				},
			},
		},
	})
}

// TestAccTriggersDataSource_serverDeployment covers the Cloud-only gate: CircleCI
// Server does not route the trigger endpoints, so the request must be refused with
// an explanatory error rather than sent and answered with a 404 that reads as a
// missing pipeline definition.
func TestAccTriggersDataSource_serverDeployment(t *testing.T) {
	api, host := newPluralAPI(t)

	// Seeded deliberately: even with data available, `server` must be refused
	// before the request rather than succeeding by accident.
	api.seedRepoTrigger(testPluralProjectID, testPluralPipelineID, "t1", "acme-bot", "push",
		"github_app.push", "acme/api", "123456")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccTriggersDataSourceConfig(host, "server"),
			ExpectError: regexp.MustCompile(`(?s)circleci_triggers requires CircleCI Cloud.*"server"`),
		}},
	})
}

func TestAccTriggersDataSource_empty(t *testing.T) {
	_, host := newPluralAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccTriggersDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_triggers.test",
						tfjsonpath.New("triggers"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
		},
	})
}

func TestAccTriggersDataSource_apiError(t *testing.T) {
	api, host := newPluralAPI(t)

	api.fail(404, "Pipeline definition not found.")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccTriggersDataSourceConfig(host, "cloud"),
			ExpectError: regexp.MustCompile(`(?s)Unable to list CircleCI triggers.*Pipeline definition not found\.`),
		}},
	})
}
