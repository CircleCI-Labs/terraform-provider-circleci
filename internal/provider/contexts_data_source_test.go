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

func testAccContextsDataSourceConfig(host, deployment string) string {
	return pluralProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_contexts" "test" {
  organization_id = %[1]q
}
`, testPluralOrgID)
}

func TestContextsDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewContextsDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	organizationID, ok := resp.Schema.Attributes["organization_id"]
	if !ok {
		t.Fatal("schema is missing the organization_id attribute")
	}
	if !organizationID.IsRequired() {
		t.Error("organization_id is not required, but it is the scope of the listing")
	}

	contexts, ok := resp.Schema.Attributes["contexts"]
	if !ok {
		t.Fatal("schema is missing the contexts attribute")
	}
	if !contexts.IsComputed() {
		t.Error("contexts is not computed, but it is entirely API-derived")
	}
}

func TestAccContextsDataSource(t *testing.T) {
	api, host := newPluralAPI(t)

	// One context per page, so the data source only sees all three if it drains
	// the pagination rather than reading the first page.
	api.pageSize = 1
	api.seedContext(testPluralOrgID, "c1", "build", "2024-01-18T02:16:55Z")
	api.seedContext(testPluralOrgID, "c2", "deploy", "2024-02-18T02:16:55Z")
	api.seedContext(testPluralOrgID, "c3", "release", "2024-03-18T02:16:55Z")

	// Another organization's contexts must not leak into the result.
	api.seedContext("99999999-9999-9999-9999-999999999999", "other", "elsewhere", "2024-04-18T02:16:55Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccContextsDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_contexts.test",
						tfjsonpath.New("contexts"),
						knownvalue.ListSizeExact(3),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_contexts.test",
						tfjsonpath.New("contexts").AtSliceIndex(0).AtMapKey("id"),
						knownvalue.StringExact("c1"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_contexts.test",
						tfjsonpath.New("contexts").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("build"),
					),
					// The timestamp is recorded exactly as the API sent it.
					statecheck.ExpectKnownValue(
						"data.circleci_contexts.test",
						tfjsonpath.New("contexts").AtSliceIndex(0).AtMapKey("created_at"),
						knownvalue.StringExact("2024-01-18T02:16:55Z"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_contexts.test",
						tfjsonpath.New("contexts").AtSliceIndex(2).AtMapKey("name"),
						knownvalue.StringExact("release"),
					),
				},
			},
		},
	})
}

// TestAccContextsDataSource_serverDeployment covers the deployment the other
// plural data sources cannot serve: contexts are v2 on both, so `server` must
// work rather than be rejected.
func TestAccContextsDataSource_serverDeployment(t *testing.T) {
	api, host := newPluralAPI(t)

	api.seedContext(testPluralOrgID, "c1", "build", "2024-01-18T02:16:55Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccContextsDataSourceConfig(host, "server"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_contexts.test",
						tfjsonpath.New("contexts"),
						knownvalue.ListSizeExact(1),
					),
				},
			},
		},
	})
}

func TestAccContextsDataSource_empty(t *testing.T) {
	_, host := newPluralAPI(t)

	// An organization with no contexts yields an empty list rather than null, so
	// that for_each and length() keep working.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccContextsDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_contexts.test",
						tfjsonpath.New("contexts"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
		},
	})
}

func TestAccContextsDataSource_apiError(t *testing.T) {
	api, host := newPluralAPI(t)

	// The server's own message must reach the diagnostic, rather than being
	// flattened to a status code.
	api.fail(403, "Permission denied.")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccContextsDataSourceConfig(host, "cloud"),
			ExpectError: regexp.MustCompile(`(?s)Unable to list CircleCI contexts.*Permission denied\.`),
		}},
	})
}
