// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccIOSSigningTypesRequireCloud checks that every iOS signing type fails
// with an explicit, actionable message when the provider is configured for
// CircleCI Server, following the same pattern as TestAccOrbTypesRequireCloud.
//
// CircleCI Server does not route /api/v3 to the public API service, so these
// types cannot work there. Without ModifyPlan gating (see
// ios_signing_cloud_only.go), the request would fail with an HTTP 404 at
// apply time, well past `terraform plan`.
func TestAccIOSSigningTypesRequireCloud(t *testing.T) {
	wantError := regexp.MustCompile(`(?s)requires CircleCI Cloud.*v3.*"server".*circleci\.example\.com`)

	tests := []struct {
		name   string
		config string
	}{
		{
			name: "circleci_ios_signing_certificate resource",
			config: `
resource "circleci_ios_signing_certificate" "test" {
  organization_id      = "22222222-2222-2222-2222-222222222222"
  file_name             = "distribution.p12"
  certificate_blob      = "cDEy..."
  certificate_password  = "secret"
}
`,
		},
		{
			name: "circleci_ios_signing_config resource",
			config: `
resource "circleci_ios_signing_config" "test" {
  organization_id = "22222222-2222-2222-2222-222222222222"
  name            = "release-config"
  certificate_id  = "33333333-3333-3333-3333-333333333333"

  provisioning_profiles = [
    {
      file_name = "release.mobileprovision"
      blob      = "cGxpc3Q..."
    },
  ]
}
`,
		},
		{
			name: "circleci_ios_signing_certificate data source",
			config: `
data "circleci_ios_signing_certificate" "test" {
  id = "11111111-1111-1111-1111-111111111111"
}
`,
		},
		{
			name: "circleci_ios_signing_certificates data source",
			config: `
data "circleci_ios_signing_certificates" "test" {
  organization_id = "22222222-2222-2222-2222-222222222222"
}
`,
		},
		{
			name: "circleci_ios_signing_configs data source",
			config: `
data "circleci_ios_signing_configs" "test" {
  organization_id = "22222222-2222-2222-2222-222222222222"
}
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      iosSigningServerProviderConfig() + tt.config,
					ExpectError: wantError,
				}},
			})
		})
	}
}
