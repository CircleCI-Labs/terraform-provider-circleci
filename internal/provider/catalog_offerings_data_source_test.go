// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"regexp"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func testAccCatalogOfferingsConfig(host, deployment string) string {
	return discoveryProviderConfig(host, deployment) + `
data "circleci_catalog_offerings" "test" {}
`
}

func TestCatalogOfferingsDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewCatalogOfferingsDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// The catalog is scoped entirely by the configured token, so there is nothing
	// for a practitioner to supply and every attribute must be computed.
	for name, attr := range resp.Schema.Attributes {
		if attr.IsRequired() || attr.IsOptional() {
			t.Errorf("%s is configurable, but the catalog takes no arguments", name)
		}
		if !attr.IsComputed() {
			t.Errorf("%s is not computed, but it is entirely API-derived", name)
		}
	}

	for _, name := range []string{"linux", "windows", "macos", "deprecated", "resource_classes"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("schema is missing the %s attribute", name)
		}
	}
}

func TestAccCatalogOfferingsDataSource(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCatalogOfferingsConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					// Each platform is a map keyed by resource class name, so a
					// configuration can index one directly.
					statecheck.ExpectKnownValue(
						"data.circleci_catalog_offerings.test",
						tfjsonpath.New("linux").AtMapKey("medium"),
						knownvalue.ListExact([]knownvalue.Check{
							knownvalue.StringExact("ubuntu-2404:current"),
							knownvalue.StringExact("ubuntu-2204:current"),
						}),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_catalog_offerings.test",
						tfjsonpath.New("windows").AtMapKey("windows.medium"),
						knownvalue.ListSizeExact(1),
					),
					// An unentitled platform is an empty map, not null, so lookup() and
					// keys() keep working against it.
					statecheck.ExpectKnownValue(
						"data.circleci_catalog_offerings.test",
						tfjsonpath.New("macos"),
						knownvalue.MapSizeExact(0),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_catalog_offerings.test",
						tfjsonpath.New("deprecated").AtMapKey("ubuntu-2004"),
						knownvalue.ListSizeExact(1),
					),
					// The flat list is sorted, deduplicated, and excludes the
					// deprecated-only class so a contains() check does not pass it.
					statecheck.ExpectKnownValue(
						"data.circleci_catalog_offerings.test",
						tfjsonpath.New("resource_classes"),
						knownvalue.ListExact([]knownvalue.Check{
							knownvalue.StringExact("large"),
							knownvalue.StringExact("medium"),
							knownvalue.StringExact("windows.medium"),
						}),
					),
				},
			},
		},
	})
}

func TestAccCatalogOfferingsDataSource_serverDeployment(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	// The catalog is served by v3, which CircleCI Server does not route. That must
	// be an explicit error rather than the HTTP 404 the request would otherwise
	// produce, which is indistinguishable from a missing resource.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccCatalogOfferingsConfig(host, "server"),
			ExpectError: regexp.MustCompile(`circleci_catalog_offerings requires CircleCI Cloud`),
		}},
	})
}

func TestAccCatalogOfferingsDataSource_validatesResourceClass(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	// The reason this data source exists: a precondition can reject an unavailable
	// resource class at plan time rather than at job runtime.
	config := testAccCatalogOfferingsConfig(host, "cloud") + `
output "medium_is_available" {
  value = contains(data.circleci_catalog_offerings.test.resource_classes, "medium")
}

output "made_up_is_available" {
  value = contains(data.circleci_catalog_offerings.test.resource_classes, "gigantic")
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckOutput("medium_is_available", "true"),
					resource.TestCheckOutput("made_up_is_available", "false"),
				),
			},
		},
	})
}
