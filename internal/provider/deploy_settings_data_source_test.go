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

func testAccDeploySettingsConfig(host, deployment string) string {
	return deployProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_deploy_settings" "test" {
  project_id = %[1]q
}
`, testDeployProjectID)
}

func TestDeploySettingsDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewDeploySettingsDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	if !resp.Schema.Attributes["project_id"].IsRequired() {
		t.Error("project_id is not required, but it is the lookup key")
	}
	for _, name := range []string{"rollback_pipeline_definition_id", "deploy_pipeline_definition_id"} {
		if resp.Schema.Attributes[name].IsComputed() != true {
			t.Errorf("%s is not computed, but it is entirely API-derived (this data source is read-only)", name)
		}
	}
}

func TestAccDeploySettingsDataSource(t *testing.T) {
	_, host := newMockDeployAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: deployProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDeploySettingsConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_settings.test",
						tfjsonpath.New("rollback_pipeline_definition_id"),
						knownvalue.StringExact("1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"),
					),
					// The API omits deploy_pipeline_definition_id entirely when it is
					// not configured, which must read as null.
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_settings.test",
						tfjsonpath.New("deploy_pipeline_definition_id"),
						knownvalue.Null(),
					),
				},
			},
		},
	})
}

func TestAccDeploySettingsDataSource_serverDeployment(t *testing.T) {
	_, host := newMockDeployAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: deployProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccDeploySettingsConfig(host, "server"),
			ExpectError: regexp.MustCompile(`circleci_deploy_settings requires CircleCI Cloud`),
		}},
	})
}
