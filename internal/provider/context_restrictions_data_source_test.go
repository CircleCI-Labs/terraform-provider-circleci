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

func testAccContextRestrictionsDataSourceConfig(host, deployment string) string {
	return pluralProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_context_restrictions" "test" {
  context_id = %[1]q
}
`, testPluralContextID)
}

func TestContextRestrictionsDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewContextRestrictionsDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	contextID, ok := resp.Schema.Attributes["context_id"]
	if !ok {
		t.Fatal("schema is missing the context_id attribute")
	}
	if !contextID.IsRequired() {
		t.Error("context_id is not required, but it is the scope of the listing")
	}

	restrictions, ok := resp.Schema.Attributes["restrictions"]
	if !ok {
		t.Fatal("schema is missing the restrictions attribute")
	}
	if !restrictions.IsComputed() {
		t.Error("restrictions is not computed, but it is entirely API-derived")
	}
}

func TestAccContextRestrictionsDataSource(t *testing.T) {
	api, host := newPluralAPI(t)

	// One of each kind, because project_id is populated only for the project one
	// and name is empty only for the expression one.
	api.seedRestriction(testPluralContextID, "r1", "acme/api", "project", "22222222-2222-2222-2222-222222222222")
	api.seedRestriction(testPluralContextID, "r2", "All members", "group", "96d25399-ab81-4499-969f-f890a383209e")
	api.seedRestriction(testPluralContextID, "r3", "", "expression", "pipeline.git.branch == \"main\"")

	// Another context's restrictions must not leak into the result.
	api.seedRestriction("ffffffff-ffff-ffff-ffff-ffffffffffff", "other", "elsewhere", "project", "other-project")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccContextRestrictionsDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_context_restrictions.test",
						tfjsonpath.New("restrictions"),
						knownvalue.ListSizeExact(3),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context_restrictions.test",
						tfjsonpath.New("restrictions").AtSliceIndex(0).AtMapKey("type"),
						knownvalue.StringExact("project"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context_restrictions.test",
						tfjsonpath.New("restrictions").AtSliceIndex(0).AtMapKey("value"),
						knownvalue.StringExact("22222222-2222-2222-2222-222222222222"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context_restrictions.test",
						tfjsonpath.New("restrictions").AtSliceIndex(0).AtMapKey("project_id"),
						knownvalue.StringExact("22222222-2222-2222-2222-222222222222"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context_restrictions.test",
						tfjsonpath.New("restrictions").AtSliceIndex(0).AtMapKey("context_id"),
						knownvalue.StringExact(testPluralContextID),
					),
					// A group restriction has no project_id at all, which must read
					// back as "" rather than repeating the restriction value.
					statecheck.ExpectKnownValue(
						"data.circleci_context_restrictions.test",
						tfjsonpath.New("restrictions").AtSliceIndex(1).AtMapKey("project_id"),
						knownvalue.StringExact(""),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context_restrictions.test",
						tfjsonpath.New("restrictions").AtSliceIndex(1).AtMapKey("name"),
						knownvalue.StringExact("All members"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context_restrictions.test",
						tfjsonpath.New("restrictions").AtSliceIndex(2).AtMapKey("type"),
						knownvalue.StringExact("expression"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context_restrictions.test",
						tfjsonpath.New("restrictions").AtSliceIndex(2).AtMapKey("name"),
						knownvalue.StringExact(""),
					),
				},
			},
		},
	})
}

// TestAccContextRestrictionsDataSource_serverDeployment covers CircleCI Server:
// restrictions are v2 on both deployments, so this must work rather than be
// rejected.
func TestAccContextRestrictionsDataSource_serverDeployment(t *testing.T) {
	api, host := newPluralAPI(t)

	api.seedRestriction(testPluralContextID, "r1", "acme/api", "project", "22222222-2222-2222-2222-222222222222")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccContextRestrictionsDataSourceConfig(host, "server"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_context_restrictions.test",
						tfjsonpath.New("restrictions"),
						knownvalue.ListSizeExact(1),
					),
				},
			},
		},
	})
}

func TestAccContextRestrictionsDataSource_empty(t *testing.T) {
	_, host := newPluralAPI(t)

	// An empty list means every group grant has been removed (locking the
	// context down to organization administrators, per CircleCI's
	// documentation) rather than an absent answer, so it must not read back
	// as null.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccContextRestrictionsDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_context_restrictions.test",
						tfjsonpath.New("restrictions"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
		},
	})
}

func TestAccContextRestrictionsDataSource_apiError(t *testing.T) {
	api, host := newPluralAPI(t)

	api.fail(404, "Context not found.")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccContextRestrictionsDataSourceConfig(host, "cloud"),
			ExpectError: regexp.MustCompile(`(?s)Unable to list restrictions for CircleCI context.*Context not found\.`),
		}},
	})
}
