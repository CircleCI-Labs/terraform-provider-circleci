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

func testAccDeployEnvironmentConfig(host, deployment string) string {
	return deployProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_deploy_environment" "test" {
  id = %[1]q
}
`, testDeployEnvironmentID)
}

func TestDeployEnvironmentDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewDeployEnvironmentDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

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

func TestAccDeployEnvironmentDataSource(t *testing.T) {
	_, host := newMockDeployAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: deployProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDeployEnvironmentConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_environment.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact("prod-app"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_environment.test",
						tfjsonpath.New("description"),
						knownvalue.StringExact("Production environment"),
					),
				},
			},
		},
	})
}

func TestAccDeployEnvironmentDataSource_notFound(t *testing.T) {
	_, host := newMockDeployAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: deployProviderFactories,
		Steps: []resource.TestStep{{
			Config: deployProviderConfig(host, "cloud") + `
data "circleci_deploy_environment" "test" {
  id = "00000000-0000-0000-0000-000000000000"
}
`,
			ExpectError: regexp.MustCompile(`Unable to read CircleCI deploy environment`),
		}},
	})
}

func TestAccDeployEnvironmentDataSource_serverDeployment(t *testing.T) {
	_, host := newMockDeployAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: deployProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccDeployEnvironmentConfig(host, "server"),
			ExpectError: regexp.MustCompile(`circleci_deploy_environment requires CircleCI Cloud`),
		}},
	})
}
