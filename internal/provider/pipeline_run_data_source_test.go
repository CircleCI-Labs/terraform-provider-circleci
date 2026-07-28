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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestPipelineRunDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewPipelineRunDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	for _, name := range []string{"id", "project_slug", "number"} {
		attr := resp.Schema.Attributes[name]
		if !attr.IsOptional() || !attr.IsComputed() {
			t.Errorf("%s must be optional+computed: either an input or derived from the response", name)
		}
	}
}

func TestAccPipelineRunDataSource_byID(t *testing.T) {
	api, host := newMockObservabilityAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: observabilityProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: observabilityProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_pipeline_run" "test" {
  id = %[1]q
}
`, testRunID),
				Check: func(*terraform.State) error {
					for _, req := range api.seenRequests() {
						if req == "GET /api/v2/pipeline/"+testRunID {
							return nil
						}
					}

					return fmt.Errorf("no request fetched the run by id; requests seen: %v", api.seenRequests())
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_run.test",
						tfjsonpath.New("number"),
						knownvalue.Int64Exact(testRunNumber),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_run.test",
						tfjsonpath.New("project_slug"),
						knownvalue.StringExact(testRunProjectSlug),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_run.test",
						tfjsonpath.New("vcs_branch"),
						knownvalue.StringExact("main"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_run.test",
						tfjsonpath.New("vcs_tag"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_run.test",
						tfjsonpath.New("warnings"),
						knownvalue.ListSizeExact(1),
					),
				},
			},
		},
	})
}

func TestAccPipelineRunDataSource_byProjectAndNumber(t *testing.T) {
	_, host := newMockObservabilityAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: observabilityProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: observabilityProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_pipeline_run" "test" {
  project_slug = %[1]q
  number       = %[2]d
}
`, testRunProjectSlug, testRunNumber),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_run.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact(testRunID),
					),
				},
			},
		},
	})
}

func TestAccPipelineRunDataSource_availableOnServer(t *testing.T) {
	_, host := newMockObservabilityAPI(t)

	// Pipeline runs are served by the same v2 API on both deployments, so
	// deployment = "server" must not be rejected.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: observabilityProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: observabilityProviderConfig(host, "server") + fmt.Sprintf(`
data "circleci_pipeline_run" "test" {
  id = %[1]q
}
`, testRunID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_run.test",
						tfjsonpath.New("state"),
						knownvalue.StringExact("created"),
					),
				},
			},
		},
	})
}
