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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func testAccDeployEnvironmentsConfig(host, deployment string) string {
	return deployProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_deploy_environments" "test" {
  organization_id = %[1]q
}
`, testDeployOrganizationID)
}

func TestDeployEnvironmentsDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewDeployEnvironmentsDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	if !resp.Schema.Attributes["organization_id"].IsRequired() {
		t.Error("organization_id is not required, but it is the scope of the listing")
	}
	if !resp.Schema.Attributes["environments"].IsComputed() {
		t.Error("environments is not computed, but it is entirely API-derived")
	}
}

func TestAccDeployEnvironmentsDataSource(t *testing.T) {
	api, host := newMockDeployAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: deployProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDeployEnvironmentsConfig(host, "cloud"),
				Check: func(*terraform.State) error {
					for _, req := range api.seenRequests() {
						if req == "GET /api/v2/deploy/environments?org-id="+testDeployOrganizationID {
							return nil
						}
					}

					return fmt.Errorf("no request scoped the listing by org-id; requests seen: %v", api.seenRequests())
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_environments.test",
						tfjsonpath.New("environments"),
						knownvalue.ListSizeExact(1),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_environments.test",
						tfjsonpath.New("environments").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("prod-app"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_environments.test",
						tfjsonpath.New("environments").AtSliceIndex(0).AtMapKey("labels"),
						knownvalue.MapExact(map[string]knownvalue.Check{
							"env": knownvalue.StringExact("prod"),
						}),
					),
				},
			},
		},
	})
}

func TestAccDeployEnvironmentsDataSource_serverDeployment(t *testing.T) {
	_, host := newMockDeployAPI(t)

	// the API (and the the API facade in front of it) is not
	// deployed on CircleCI Server, so deployment = "server" must be rejected
	// rather than attempting a request the Server installation cannot route.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: deployProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccDeployEnvironmentsConfig(host, "server"),
			ExpectError: regexp.MustCompile(`circleci_deploy_environments requires CircleCI Cloud`),
		}},
	})
}
