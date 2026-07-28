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

func auditLogConfigsDataSourceConfig(host string) string {
	return auditLogConfigProviderConfig(host) + fmt.Sprintf(`
data "circleci_audit_log_configs" "test" {
  organization_id = %q
}
`, testAuditLogConfigOrg)
}

func TestAccAuditLogConfigsDataSource(t *testing.T) {
	api := &auditLogConfigAPI{
		configs: []map[string]any{
			{
				"id":          "00000000-0000-0000-0000-000000000001",
				"org_id":      testAuditLogConfigOrg,
				"target_type": "S3",
				"is_disabled": false,
				"config": map[string]any{
					"arn":         "arn:aws:iam::123456789012:role/circleci-audit-logs",
					"region":      "us-east-1",
					"bucket_name": "acme-audit-logs",
				},
				"created_by":        "11111111-1111-1111-1111-111111111111",
				"created_at":        "2024-01-02T03:04:05Z",
				"updated_at":        "2024-01-02T03:04:05Z",
				"connection_status": "CONNECTED",
			},
			{
				"id":          "00000000-0000-0000-0000-000000000002",
				"org_id":      testAuditLogConfigOrg,
				"target_type": "S3_COMPATIBLE",
				"is_disabled": true,
				"config": map[string]any{
					"arn":         "arn:minio:iam::role/circleci-audit-logs",
					"bucket_name": "acme-audit-logs-2",
					"endpoint":    "https://minio.example.com",
				},
				"created_by":        "11111111-1111-1111-1111-111111111111",
				"created_at":        "2024-02-03T04:05:06Z",
				"updated_at":        "2024-02-03T04:05:06Z",
				"connection_status": "DISABLED",
			},
		},
	}
	srv := newAuditLogConfigServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: auditLogConfigsDataSourceConfig(srv.URL),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_audit_log_configs.test",
					tfjsonpath.New("audit_log_configs"),
					knownvalue.ListSizeExact(2),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_audit_log_configs.test",
					tfjsonpath.New("audit_log_configs").AtSliceIndex(0).AtMapKey("target_type"),
					knownvalue.StringExact("S3"),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_audit_log_configs.test",
					tfjsonpath.New("audit_log_configs").AtSliceIndex(0).AtMapKey("endpoint"),
					knownvalue.Null(),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_audit_log_configs.test",
					tfjsonpath.New("audit_log_configs").AtSliceIndex(1).AtMapKey("target_type"),
					knownvalue.StringExact("S3_COMPATIBLE"),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_audit_log_configs.test",
					tfjsonpath.New("audit_log_configs").AtSliceIndex(1).AtMapKey("connection_status"),
					knownvalue.StringExact("DISABLED"),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_audit_log_configs.test",
					tfjsonpath.New("audit_log_configs").AtSliceIndex(1).AtMapKey("region"),
					knownvalue.Null(),
				),
			},
		}},
	})
}

// TestAccAuditLogConfigsDataSource_Empty checks that an organization with no
// configs yields an empty list rather than a null one, so configurations can
// iterate over it unconditionally.
func TestAccAuditLogConfigsDataSource_Empty(t *testing.T) {
	api := &auditLogConfigAPI{}
	srv := newAuditLogConfigServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: auditLogConfigsDataSourceConfig(srv.URL),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_audit_log_configs.test",
					tfjsonpath.New("audit_log_configs"),
					knownvalue.ListSizeExact(0),
				),
			},
		}},
	})
}

// TestAccAuditLogConfigsDataSource_RequiresCloud covers the Server rejection.
// Data sources have no ModifyPlan, so this can only surface from Read.
func TestAccAuditLogConfigsDataSource_RequiresCloud(t *testing.T) {
	cfg := `
provider "circleci" {
  host       = "https://circleci.example.com"
  key        = "fake"
  deployment = "server"
}
` + fmt.Sprintf(`
data "circleci_audit_log_configs" "test" {
  organization_id = %q
}
`, testAuditLogConfigOrg)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`circleci_audit_log_configs requires CircleCI Cloud`),
		}},
	})
}
