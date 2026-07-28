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

func testAccGroupDataSourceConfig(host, deployment, groupID string) string {
	return testAccGroupProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_group" "test" {
  organization_id = %[1]q
  id              = %[2]q
}
`, testGroupOrgID, groupID)
}

func TestGroupDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewGroupDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// A group id is only unique within an organization, so both are inputs.
	for _, name := range []string{"id", "organization_id"} {
		attribute, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("schema is missing the %q attribute", name)
		}
		if !attribute.IsRequired() {
			t.Errorf("attribute %q is not required, but the lookup needs it", name)
		}
	}
}

func TestAccGroupDataSource(t *testing.T) {
	api, host := newMockGroupAPI(t)
	groupID := api.seed(testGroupOrgID, "platform", "Platform team")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupDataSourceConfig(host, "cloud", groupID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_group.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact(groupID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_group.test",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(testGroupOrgID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_group.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact("platform"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_group.test",
						tfjsonpath.New("description"),
						knownvalue.StringExact("Platform team"),
					),
				},
			},
		},
	})
}

func TestAccGroupDataSource_serverDeployment(t *testing.T) {
	api, host := newMockGroupAPI(t)
	groupID := api.seed(testGroupOrgID, "server-group", "")

	// Groups need a `circleci` type (standalone) organization. A CircleCI Server
	// installation is always a `github` type organization, so deployment =
	// "server" must be rejected with an explanatory error rather than attempting
	// a request the API would refuse.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccGroupDataSourceConfig(host, "server", groupID),
			ExpectError: regexp.MustCompile(`circleci_group requires a standalone CircleCI organization`),
		}},
	})
}

func TestAccGroupDataSource_notFound(t *testing.T) {
	_, host := newMockGroupAPI(t)

	// Unlike the resource, a data source has no state to drop: a missing group
	// has to be reported as an error. The API answers 403 "Permission denied."
	// rather than 404, and gives the same answer for a group in another
	// organization or a token without access.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccGroupDataSourceConfig(host, "cloud", "00000000-0000-0000-0000-000000009999"),
				ExpectError: regexp.MustCompile(`Unable to read CircleCI group`),
			},
		},
	})
}
