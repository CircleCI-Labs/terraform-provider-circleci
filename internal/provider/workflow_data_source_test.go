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

func TestWorkflowDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewWorkflowDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	if !resp.Schema.Attributes["id"].IsRequired() {
		t.Error("id is not required, but it is the lookup key")
	}
}

func TestAccWorkflowDataSource(t *testing.T) {
	_, host := newMockObservabilityAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: observabilityProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: observabilityProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_workflow" "test" {
  id = %[1]q
}
`, testWorkflowID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_workflow.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact("build-and-test"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_workflow.test",
						tfjsonpath.New("status"),
						knownvalue.StringExact("running"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_workflow.test",
						tfjsonpath.New("stopped_at"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_workflow.test",
						tfjsonpath.New("pipeline_run_id"),
						knownvalue.StringExact(testRunID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_workflow.test",
						tfjsonpath.New("max_auto_reruns"),
						knownvalue.Null(),
					),
				},
			},
		},
	})
}

func TestAccWorkflowDataSource_availableOnServer(t *testing.T) {
	_, host := newMockObservabilityAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: observabilityProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: observabilityProviderConfig(host, "server") + fmt.Sprintf(`
data "circleci_workflow" "test" {
  id = %[1]q
}
`, testWorkflowID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_workflow.test",
						tfjsonpath.New("status"),
						knownvalue.StringExact("running"),
					),
				},
			},
		},
	})
}
