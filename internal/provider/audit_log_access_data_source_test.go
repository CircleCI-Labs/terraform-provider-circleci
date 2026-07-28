// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func auditLogAccessDataSourceConfig(host string) string {
	return auditLogConfigProviderConfig(host) + fmt.Sprintf(`
data "circleci_audit_log_access" "test" {
  organization_id = %q
}
`, testAuditLogConfigOrg)
}

func TestAccAuditLogAccessDataSource_Allowed(t *testing.T) {
	api := &auditLogConfigAPI{hasAccess: true}
	srv := newAuditLogConfigServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: auditLogAccessDataSourceConfig(srv.URL),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_audit_log_access.test",
					tfjsonpath.New("has_access"),
					knownvalue.Bool(true),
				),
			},
		}},
	})

	var sawAccess bool
	for _, req := range api.recorded() {
		if req == "GET /api/v2/organizations/"+testAuditLogConfigOrg+"/audit-log/access" {
			sawAccess = true
		}
	}
	if !sawAccess {
		t.Errorf("no access request with the expected URI, got %v", api.recorded())
	}
}

func TestAccAuditLogAccessDataSource_Denied(t *testing.T) {
	api := &auditLogConfigAPI{hasAccess: false}
	srv := newAuditLogConfigServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: auditLogAccessDataSourceConfig(srv.URL),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_audit_log_access.test",
					tfjsonpath.New("has_access"),
					knownvalue.Bool(false),
				),
			},
		}},
	})
}

// TestAccAuditLogAccessDataSource_RequiresCloud covers the Server rejection.
func TestAccAuditLogAccessDataSource_RequiresCloud(t *testing.T) {
	cfg := `
provider "circleci" {
  host       = "https://circleci.example.com"
  key        = "fake"
  deployment = "server"
}
` + fmt.Sprintf(`
data "circleci_audit_log_access" "test" {
  organization_id = %q
}
`, testAuditLogConfigOrg)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`circleci_audit_log_access requires CircleCI Cloud`),
		}},
	})
}
