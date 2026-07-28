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

	if !resp.Schema.Attributes["organization_id"].IsRequired() {
		t.Error("organization_id is not required, but it is the scope of the listing")
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
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_components.test",
						tfjsonpath.New("components").AtSliceIndex(0).AtMapKey("release_count"),
						knownvalue.Int64Exact(42),
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
