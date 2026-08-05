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

func TestJobDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewJobDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	for _, name := range []string{"project_slug", "job_number"} {
		if !resp.Schema.Attributes[name].IsRequired() {
			t.Errorf("%s is not required, but it is the lookup key", name)
		}
	}
}

func TestAccJobDataSource(t *testing.T) {
	_, host := newMockObservabilityAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: observabilityProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: observabilityProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_job" "test" {
  project_slug = %[1]q
  job_number   = %[2]d
}
`, testRunProjectSlug, testJobNumber),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_job.test",
						tfjsonpath.New("status"),
						knownvalue.StringExact("success"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_job.test",
						tfjsonpath.New("resource_class"),
						knownvalue.StringExact("medium"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_job.test",
						tfjsonpath.New("organization_name"),
						knownvalue.StringExact("CircleCI-Public"),
					),
					// duration_ms is absent from the mock response, so it must read
					// as null rather than 0.
					statecheck.ExpectKnownValue(
						"data.circleci_job.test",
						tfjsonpath.New("duration_ms"),
						knownvalue.Null(),
					),
				},
			},
		},
	})
}

func TestAccJobDataSource_availableOnServer(t *testing.T) {
	_, host := newMockObservabilityAPI(t)

	// Jobs by project slug and number are served by a backend that is
	// deployed on both CircleCI Cloud and CircleCI Server.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: observabilityProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: observabilityProviderConfig(host, "server") + fmt.Sprintf(`
data "circleci_job" "test" {
  project_slug = %[1]q
  job_number   = %[2]d
}
`, testRunProjectSlug, testJobNumber),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_job.test",
						tfjsonpath.New("status"),
						knownvalue.StringExact("success"),
					),
				},
			},
		},
	})
}
