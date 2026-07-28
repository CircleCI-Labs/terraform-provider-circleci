// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccPipelineDataSource(t *testing.T) {
	projectID := testProjectID(t)
	pipelineID := testPipelineID(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Read testing
			{
				Config: testPipelineDataSourceConfig(pipelineID, projectID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline.test_pipeline",
						tfjsonpath.New("id"),
						knownvalue.StringExact(pipelineID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline.test_pipeline",
						tfjsonpath.New("project_id"),
						knownvalue.StringExact(projectID),
					),
				},
			},
		},
	})
}

func testPipelineDataSourceConfig(pipelineID, projectID string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = "https://circleci.com/api/v2"
}

data "circleci_pipeline" "test_pipeline" {
  id         = %[1]q
  project_id = %[2]q
}
`, pipelineID, projectID)
}
