// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	sdkresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"terraform-provider-circleci/internal/circleci"
)

const cloudOnlyServerProvider = `
provider "circleci" {
  host       = "https://circleci.example.com"
  key        = "fake"
  deployment = "server"
}
`

// TestAccCloudOnlyResourcesFailAtPlanNotApply pins the failure to plan time.
//
// requireCloud is called from CRUD, which alone would let `terraform plan`
// succeed and report a create, then fail during apply. For infrastructure as
// code that is the wrong end of the pipeline: a plan check in CI would pass, and
// the error would land mid-apply, possibly after other resources had already
// changed. ModifyPlan moves it earlier.
func TestAccCloudOnlyResourcesFailAtPlanNotApply(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		config   string
		typeName string
	}{
		{
			name:     "orb namespace",
			typeName: "circleci_orb_namespace",
			config: `
resource "circleci_orb_namespace" "test" {
  organization_id = "00000000-1111-2222-3333-444444444444"
  name            = "example-ns"
}`,
		},
		{
			name:     "orb",
			typeName: "circleci_orb",
			config: `
resource "circleci_orb" "test" {
  namespace_id = "11111111-2222-3333-4444-555555555555"
  name         = "example"
}`,
		},
		{
			name:     "organization settings",
			typeName: "circleci_organization_settings",
			config: `
resource "circleci_organization_settings" "test" {
  organization_id  = "00000000-1111-2222-3333-444444444444"
  enable_ai_agents = false
}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sdkresource.UnitTest(t, sdkresource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []sdkresource.TestStep{{
					Config: cloudOnlyServerProvider + tc.config,
					// PlanOnly proves the error comes from planning: the host is
					// unreachable, so an apply-time check would fail for the wrong
					// reason and this assertion would not be meaningful.
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.typeName + ` requires CircleCI Cloud`),
				}},
			})
		})
	}
}

// TestCloudOnlyModifyPlanAllowsDestroy is the escape hatch.
//
// If ModifyPlan rejected every plan including a destroy, a resource created on
// Cloud and then left in state after switching to deployment = "server" would be
// permanently unmanageable: it could be neither updated nor removed. Terraform
// signals a destroy with a null plan, which must pass through silently.
func TestCloudOnlyModifyPlanAllowsDestroy(t *testing.T) {
	t.Parallel()

	serverClient := circleci.New(circleci.Config{
		Host:       "https://circleci.example.com",
		Token:      "fake",
		Deployment: circleci.DeploymentServer,
	})

	// A null plan is how Terraform represents a destroy.
	destroyPlan := tfsdk.Plan{
		Raw:    tftypes.NewValue(tftypes.Object{}, nil),
		Schema: rschema.Schema{},
	}

	cases := map[string]resource.ResourceWithModifyPlan{
		"orb namespace":         &orbNamespaceResource{client: serverClient},
		"orb":                   &orbResource{client: serverClient},
		"orb version":           &orbVersionResource{client: serverClient},
		"organization settings": &organizationSettingsResource{client: serverClient},
	}

	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var resp resource.ModifyPlanResponse
			res.ModifyPlan(t.Context(), resource.ModifyPlanRequest{Plan: destroyPlan}, &resp)

			if resp.Diagnostics.HasError() {
				t.Errorf(
					"ModifyPlan rejected a destroy on a server deployment: %v\n"+
						"A resource stranded by a deployment change must stay removable.",
					resp.Diagnostics.Errors(),
				)
			}
		})
	}
}

// TestCloudOnlyModifyPlanIsSilentBeforeConfigure guards the other no-op case:
// ModifyPlan can run before Configure has supplied a client.
func TestCloudOnlyModifyPlanIsSilentBeforeConfigure(t *testing.T) {
	t.Parallel()

	nonDestroyPlan := tfsdk.Plan{
		Raw:    tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{}),
		Schema: rschema.Schema{},
	}

	var resp resource.ModifyPlanResponse
	(&orbNamespaceResource{}).ModifyPlan(t.Context(),
		resource.ModifyPlanRequest{Plan: nonDestroyPlan}, &resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("ModifyPlan errored with no configured client: %v", resp.Diagnostics.Errors())
	}
}
