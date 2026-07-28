// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccOrbTypesRequireCloud checks that every orb type fails with an explicit,
// actionable message when the provider is configured for CircleCI Server.
//
// Server does not route /api/v3 to the public API service, so these types cannot
// work there. Without this check the request would return an HTTP 404, which is
// indistinguishable from a missing resource: a data source would report "not
// found" and a resource would silently drop itself from state.
func TestAccOrbTypesRequireCloud(t *testing.T) {
	// The message must name the type, say Cloud is required, and report the
	// configured deployment and host.
	wantError := regexp.MustCompile(`(?s)requires CircleCI Cloud.*v3.*"server".*circleci\.example\.com`)

	tests := []struct {
		name   string
		config string
	}{
		{
			name: "circleci_orb_namespace resource",
			config: `
resource "circleci_orb_namespace" "test" {
  name            = "acme"
  organization_id = "22222222-2222-2222-2222-222222222222"
}
`,
		},
		{
			name: "circleci_orb resource",
			config: `
resource "circleci_orb" "test" {
  namespace_id = "11111111-1111-1111-1111-111111111111"
  name         = "node"
}
`,
		},
		{
			name: "circleci_orb_version resource",
			config: `
resource "circleci_orb_version" "test" {
  orb_id  = "33333333-3333-3333-3333-333333333333"
  version = "1.0.0"
  yaml    = "version: 2.1\n"
}
`,
		},
		{
			name: "circleci_orb_namespace data source",
			config: `
data "circleci_orb_namespace" "test" {
  name = "acme"
}
`,
		},
		{
			name: "circleci_orb data source",
			config: `
data "circleci_orb" "test" {
  full_name = "acme/node"
}
`,
		},
		{
			name: "circleci_orbs data source",
			config: `
data "circleci_orbs" "test" {}
`,
		},
		{
			name: "circleci_orb_version data source",
			config: `
data "circleci_orb_version" "test" {
  orb_id  = "33333333-3333-3333-3333-333333333333"
  version = "1.0.0"
}
`,
		},
		{
			name: "circleci_orb_categories data source",
			config: `
data "circleci_orb_categories" "test" {}
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      orbServerProviderConfig() + tt.config,
					ExpectError: wantError,
				}},
			})
		})
	}
}
