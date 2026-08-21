// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
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

	versions, ok := resp.Schema.Attributes["versions"].(dsschema.ListNestedAttribute)
	if !ok {
		t.Fatalf("versions is %T, not a list of nested objects", resp.Schema.Attributes["versions"])
	}

	nested := versions.NestedObject.Attributes

	runID, ok := nested["run_id"]
	if !ok {
		t.Fatal("versions is missing the run_id attribute")
	}
	if !runID.IsComputed() {
		t.Error("run_id is not computed, but it is entirely API-derived")
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
					// 42, not the 0 that the same component's release_count reads as
					// through circleci_deploy_components (the plural list) — see that
					// data source's test. Only the singular Get, exercised here, has
					// been observed [NET] to return the true count.
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_component.test",
						tfjsonpath.New("release_count"),
						knownvalue.Int64Exact(42),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_component.test",
						tfjsonpath.New("versions"),
						knownvalue.ListSizeExact(2),
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
						tfjsonpath.New("versions").AtSliceIndex(0).AtMapKey("run_id"),
						knownvalue.Null(),
					),
					// The second version does have a run recorded, so null everywhere
					// would not pass for a correct read.
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_component.test",
						tfjsonpath.New("versions").AtSliceIndex(1).AtMapKey("run_id"),
						knownvalue.StringExact(testDeployVersionRunID),
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
