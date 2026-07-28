// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestPipelineConfigDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewPipelineConfigDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	if !resp.Schema.Attributes["pipeline_run_id"].IsRequired() {
		t.Error("pipeline_run_id is not required, but it is the lookup key")
	}
}

func TestAccPipelineConfigDataSource(t *testing.T) {
	_, host := newMockObservabilityAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: observabilityProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: observabilityProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_pipeline_config" "test" {
  pipeline_run_id = %[1]q
}
`, testRunID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_config.test",
						tfjsonpath.New("source"),
						knownvalue.StringExact("callithumpian"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_config.test",
						tfjsonpath.New("compiled"),
						knownvalue.StringExact("adscititious"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_config.test",
						tfjsonpath.New("setup_config"),
						knownvalue.StringExact("mo' repos"),
					),
				},
			},
		},
	})
}

func TestAccPipelineConfigDataSource_availableOnServer(t *testing.T) {
	_, host := newMockObservabilityAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: observabilityProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: observabilityProviderConfig(host, "server") + fmt.Sprintf(`
data "circleci_pipeline_config" "test" {
  pipeline_run_id = %[1]q
}
`, testRunID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_config.test",
						tfjsonpath.New("compiled"),
						knownvalue.StringExact("adscititious"),
					),
				},
			},
		},
	})
}
