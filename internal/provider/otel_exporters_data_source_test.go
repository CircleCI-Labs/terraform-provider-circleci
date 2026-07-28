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

	"terraform-provider-circleci/internal/circleci"
)

func TestAccOTelExportersDataSource(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	// Two exporters, one of which carries a header, so the redaction is visible.
	config := governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_otel_exporter" "grpc" {
  organization_id = %[1]q
  endpoint        = "collector-a.example.com:4317"
  protocol        = "grpc"
  headers = {
    "x-api-key" = "super-secret"
  }
}

resource "circleci_otel_exporter" "http" {
  organization_id = %[1]q
  endpoint        = "collector-b.example.com:4318"
  protocol        = "http"
  insecure        = true
}

data "circleci_otel_exporters" "all" {
  organization_id = %[1]q

  depends_on = [
    circleci_otel_exporter.grpc,
    circleci_otel_exporter.http,
  ]
}
`, testOTelOrg)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_otel_exporters.all",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(testOTelOrg),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_otel_exporters.all",
						tfjsonpath.New("exporters"),
						knownvalue.ListSizeExact(2),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_otel_exporters.all",
						tfjsonpath.New("exporters").AtSliceIndex(0).AtMapKey("endpoint"),
						knownvalue.StringExact("collector-a.example.com:4317"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_otel_exporters.all",
						tfjsonpath.New("exporters").AtSliceIndex(0).AtMapKey("protocol"),
						knownvalue.StringExact("grpc"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_otel_exporters.all",
						tfjsonpath.New("exporters").AtSliceIndex(0).AtMapKey("insecure"),
						knownvalue.Bool(false),
					),
					// The data source reports what the API reports, which for a
					// header value is always the redaction placeholder — never the
					// secret the resource configured.
					statecheck.ExpectKnownValue(
						"data.circleci_otel_exporters.all",
						tfjsonpath.New("exporters").AtSliceIndex(0).AtMapKey("headers").AtMapKey("x-api-key"),
						knownvalue.StringExact(circleci.OTelRedactedHeaderValue),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_otel_exporters.all",
						tfjsonpath.New("exporters").AtSliceIndex(0).AtMapKey("issues"),
						knownvalue.ListExact([]knownvalue.Check{}),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_otel_exporters.all",
						tfjsonpath.New("exporters").AtSliceIndex(1).AtMapKey("endpoint"),
						knownvalue.StringExact("collector-b.example.com:4318"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_otel_exporters.all",
						tfjsonpath.New("exporters").AtSliceIndex(1).AtMapKey("insecure"),
						knownvalue.Bool(true),
					),
					// An exporter with no headers reports a null map rather than an
					// empty one.
					statecheck.ExpectKnownValue(
						"data.circleci_otel_exporters.all",
						tfjsonpath.New("exporters").AtSliceIndex(1).AtMapKey("headers"),
						knownvalue.Null(),
					),
				},
			},
		},
	})
}

// TestAccOTelExportersDataSourceEmpty covers an organization with no exporters:
// the attribute must be an empty list rather than null, so configurations can
// iterate over it unconditionally.
func TestAccOTelExportersDataSourceEmpty(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: governanceProviderConfig(srv.URL) + fmt.Sprintf(`
data "circleci_otel_exporters" "all" {
  organization_id = %q
}
`, testOTelOrg),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_otel_exporters.all",
						tfjsonpath.New("exporters"),
						knownvalue.ListExact([]knownvalue.Check{}),
					),
				},
			},
		},
	})
}
