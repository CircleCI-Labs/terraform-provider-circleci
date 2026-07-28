// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccIOSSigningConfigResource covers the create/read/delete cycle for a
// signing configuration paired with a certificate, and -- like
// TestAccIOSSigningCertificateResource -- proves that a second plan after
// apply is empty and that the profile's blob is never echoed into a readable
// attribute, even though there is no GET .../signing/configs/{id} route at
// all for Read to call.
func TestAccIOSSigningConfigResource(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	const profileBlob = "bW9iaWxlcHJvdmlzaW9uLWNvbnRlbnQ="

	config := iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "cert" {
  organization_id      = %[1]q
  file_name             = "distribution.p12"
  certificate_blob      = "cDEy...cert..."
  certificate_password  = "secret"
}

resource "circleci_ios_signing_config" "test" {
  organization_id = %[1]q
  name            = "release-config"
  certificate_id  = circleci_ios_signing_certificate.cert.id

  provisioning_profiles = [
    {
      file_name = "release.mobileprovision"
      blob      = %[2]q
    },
  ]
}
`, iosSigningTestOrgID, profileBlob)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("circleci_ios_signing_config.test", "id"),
					resource.TestCheckResourceAttr("circleci_ios_signing_config.test", "name", "release-config"),
					resource.TestCheckResourceAttrPair(
						"circleci_ios_signing_config.test", "certificate_id",
						"circleci_ios_signing_certificate.cert", "id",
					),
					resource.TestCheckResourceAttr("circleci_ios_signing_config.test", "certificate_file_name", "distribution.p12"),
					resource.TestCheckResourceAttr("circleci_ios_signing_config.test", "certificate_type", "distribution"),
					resource.TestCheckResourceAttr("circleci_ios_signing_config.test", "provisioning_profiles.0.file_name", "release.mobileprovision"),
					resource.TestCheckResourceAttr("circleci_ios_signing_config.test", "provisioning_profiles.0.blob", profileBlob),
					iosSigningCheckNoSecretEcho("circleci_ios_signing_config.test", profileBlob),
				),
			},
			// The write-only-content trap, for provisioning_profiles[*].blob: Read
			// has no GET .../signing/configs/{id} route to call at all (only a list),
			// and the list answer's profile shape reports just file_name, so this
			// plan must still come back empty.
			{
				Config:   config,
				PlanOnly: true,
			},
			{
				ResourceName: "circleci_ios_signing_config.test",
				ImportState:  true,
				// The import id is "<organization_id>/<config_id>": there is no route
				// to resolve a signing config by id alone.
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					res, ok := state.RootModule().Resources["circleci_ios_signing_config.test"]
					if !ok {
						return "", fmt.Errorf("resource not found in state")
					}

					return iosSigningTestOrgID + "/" + res.Primary.Attributes["id"], nil
				},
				ImportStateVerify: true,
				// blob cannot be recovered on import, and provisioning_profiles is a
				// nested list, so the whole list is excluded rather than one field of it.
				ImportStateVerifyIgnore: []string{"provisioning_profiles"},
			},
		},
	})

	if got := api.configs; len(got) != 0 {
		t.Errorf("configs remaining after destroy = %d, want 0", len(got))
	}
}

// TestAccIOSSigningConfigResource_RejectsInvalidName guards the name
// validator, which mirrors signingConfigNameRE in circleci/the API.
func TestAccIOSSigningConfigResource_RejectsInvalidName(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	config := iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "cert" {
  organization_id      = %[1]q
  file_name             = "distribution.p12"
  certificate_blob      = "cDEy...cert..."
  certificate_password  = "secret"
}

resource "circleci_ios_signing_config" "test" {
  organization_id = %[1]q
  name            = "not a valid name!"
  certificate_id  = circleci_ios_signing_certificate.cert.id

  provisioning_profiles = [
    {
      file_name = "release.mobileprovision"
      blob      = "profile-blob"
    },
  ]
}
`, iosSigningTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`letters, numbers and hyphens`),
		}},
	})
}
