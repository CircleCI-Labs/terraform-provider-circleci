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

func testAccGroupsDataSourceConfig(host, deployment string) string {
	return testAccGroupProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_groups" "test" {
  organization_id = %[1]q
}
`, testGroupOrgID)
}

func TestGroupsDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewGroupsDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	organizationID, ok := resp.Schema.Attributes["organization_id"]
	if !ok {
		t.Fatal("schema is missing the organization_id attribute")
	}
	if !organizationID.IsRequired() {
		t.Error("organization_id is not required, but it is the scope of the listing")
	}

	groups, ok := resp.Schema.Attributes["groups"]
	if !ok {
		t.Fatal("schema is missing the groups attribute")
	}
	if !groups.IsComputed() {
		t.Error("groups is not computed, but it is entirely API-derived")
	}
}

func TestAccGroupsDataSource(t *testing.T) {
	api, host := newMockGroupAPI(t)

	// One group per page, so the data source only sees all three if it drains
	// the pagination rather than reading the first page.
	api.pageSize = 1
	api.seed(testGroupOrgID, "alpha", "First group")
	api.seed(testGroupOrgID, "beta", "Second group")
	api.seed(testGroupOrgID, "gamma", "")

	// Another organization's groups must not leak into the result.
	api.seed("99999999-9999-9999-9999-999999999999", "other-org-group", "Elsewhere")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupsDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_groups.test",
						tfjsonpath.New("groups"),
						knownvalue.ListSizeExact(3),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_groups.test",
						tfjsonpath.New("groups").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("alpha"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_groups.test",
						tfjsonpath.New("groups").AtSliceIndex(1).AtMapKey("description"),
						knownvalue.StringExact("Second group"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_groups.test",
						tfjsonpath.New("groups").AtSliceIndex(2).AtMapKey("name"),
						knownvalue.StringExact("gamma"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_groups.test",
						tfjsonpath.New("groups").AtSliceIndex(2).AtMapKey("description"),
						knownvalue.StringExact(""),
					),
				},
			},
		},
	})
}

func TestAccGroupsDataSource_serverDeployment(t *testing.T) {
	_, host := newMockGroupAPI(t)

	// Groups need a `circleci` type (standalone) organization. A CircleCI Server
	// installation is always a `github` type organization, so deployment =
	// "server" must be rejected with an explanatory error rather than attempting
	// a request the API would refuse.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccGroupsDataSourceConfig(host, "server"),
			ExpectError: regexp.MustCompile(`circleci_groups requires a standalone CircleCI organization`),
		}},
	})
}

func TestAccGroupsDataSource_empty(t *testing.T) {
	_, host := newMockGroupAPI(t)

	// An organization with no groups yields an empty list rather than null, so
	// that for_each and length() keep working.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupsDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_groups.test",
						tfjsonpath.New("groups"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
		},
	})
}
