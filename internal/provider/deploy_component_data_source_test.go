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

func testAccDeployComponentConfig(host, deployment string) string {
	return deployProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_deploy_component" "test" {
  id = %[1]q
}
`, testDeployComponentID)
}

func TestDeployComponentDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewDeployComponentDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	if !resp.Schema.Attributes["id"].IsRequired() {
		t.Error("id is not required, but it is the lookup key")
	}
	if !resp.Schema.Attributes["versions"].IsComputed() {
		t.Error("versions is not computed, but it is entirely API-derived")
	}
}

func TestAccDeployComponentDataSource(t *testing.T) {
	_, host := newMockDeployAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: deployProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDeployComponentConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_component.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact("release-agent"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_component.test",
						tfjsonpath.New("versions"),
						knownvalue.ListSizeExact(1),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_component.test",
						tfjsonpath.New("versions").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("1.2.3"),
					),
					// The all-zero UUID the API sends for an unrecorded
					// pipeline/workflow/job association must read as null, not as
					// that literal sentinel value.
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_component.test",
						tfjsonpath.New("versions").AtSliceIndex(0).AtMapKey("pipeline_id"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_component.test",
						tfjsonpath.New("versions").AtSliceIndex(0).AtMapKey("job_number"),
						knownvalue.Null(),
					),
				},
			},
		},
	})
}

func TestAccDeployComponentDataSource_serverDeployment(t *testing.T) {
	_, host := newMockDeployAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: deployProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccDeployComponentConfig(host, "server"),
			ExpectError: regexp.MustCompile(`circleci_deploy_component requires CircleCI Cloud`),
		}},
	})
}
