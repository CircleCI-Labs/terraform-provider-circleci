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

func TestWorkflowJobsDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewWorkflowJobsDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	if !resp.Schema.Attributes["workflow_id"].IsRequired() {
		t.Error("workflow_id is not required, but it is the lookup key")
	}
	if !resp.Schema.Attributes["jobs"].IsComputed() {
		t.Error("jobs is not computed, but it is entirely API-derived")
	}
}

func TestAccWorkflowJobsDataSource(t *testing.T) {
	_, host := newMockObservabilityAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: observabilityProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: observabilityProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_workflow_jobs" "test" {
  workflow_id = %[1]q
}
`, testWorkflowID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_workflow_jobs.test",
						tfjsonpath.New("jobs"),
						knownvalue.ListSizeExact(1),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_workflow_jobs.test",
						tfjsonpath.New("jobs").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("build"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_workflow_jobs.test",
						tfjsonpath.New("jobs").AtSliceIndex(0).AtMapKey("job_number"),
						knownvalue.Int64Exact(testJobNumber),
					),
				},
			},
		},
	})
}
