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

func testAccDeployComponentsConfig(host, deployment string) string {
	return deployProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_deploy_components" "test" {
  organization_id = %[1]q
}
`, testDeployOrganizationID)
}

func TestDeployComponentsDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewDeployComponentsDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// The organization is the scope of the listing, but it is accepted as either
	// `organization_id` or `org_id` while the former is deprecated, so both are
	// Optional and the config validator requires exactly one. See
	// org_id_deprecation.go.
	for _, name := range []string{"organization_id", "org_id"} {
		if !resp.Schema.Attributes[name].IsOptional() {
			t.Errorf("attribute %q is not optional, but one of the pair must be settable", name)
		}
	}
	if !resp.Schema.Attributes["project_id"].IsOptional() {
		t.Error("project_id is not optional, but it is a filter")
	}
	if !resp.Schema.Attributes["components"].IsComputed() {
		t.Error("components is not computed, but it is entirely API-derived")
	}
}

func TestAccDeployComponentsDataSource(t *testing.T) {
	_, host := newMockDeployAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: deployProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDeployComponentsConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_components.test",
						tfjsonpath.New("components"),
						knownvalue.ListSizeExact(1),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_components.test",
						tfjsonpath.New("components").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("release-agent"),
					),
					// 0, not the component's true release count: [NET] this data
					// source's underlying route (the plural list) has been observed to
					// always answer release_count: 0. See TestAccDeployComponentDataSource
					// for the same component read through the singular data source, where
					// the real count comes back. Don't "fix" this expectation to a
					// plausible-looking non-zero value; that would be re-encoding the
					// belief this test exists to prevent.
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_components.test",
						tfjsonpath.New("components").AtSliceIndex(0).AtMapKey("release_count"),
						knownvalue.Int64Exact(0),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_components.test",
						tfjsonpath.New("components").AtSliceIndex(0).AtMapKey("archived_at"),
						knownvalue.Null(),
					),
				},
			},
		},
	})
}

// TestAccDeployComponentsDataSource_emptyOrg is the components equivalent of
// TestAccDeployEnvironmentsDataSource_emptyOrg: an org that has never used
// deploy/release tracking answers [NET] with HTTP 200 and an empty items
// array, which must read as an empty (non-null) list rather than an error.
func TestAccDeployComponentsDataSource_emptyOrg(t *testing.T) {
	_, host := newMockDeployAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: deployProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: deployProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_deploy_components" "test" {
  organization_id = %[1]q
}
`, testDeployEmptyOrganizationID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_components.test",
						tfjsonpath.New("components"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
		},
	})
}

func TestAccDeployComponentsDataSource_serverDeployment(t *testing.T) {
	_, host := newMockDeployAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: deployProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccDeployComponentsConfig(host, "server"),
			ExpectError: regexp.MustCompile(`circleci_deploy_components requires CircleCI Cloud`),
		}},
	})
}
