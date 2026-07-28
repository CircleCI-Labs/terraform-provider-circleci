// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccIOSSigningCertificateDataSource covers the singular lookup by id,
// and that it reports no more than the API itself can: there is no attribute
// for the certificate's content or password anywhere in the schema.
func TestAccIOSSigningCertificateDataSource(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	config := iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "cert" {
  organization_id      = %[1]q
  file_name             = "distribution.p12"
  certificate_blob      = "cDEy...secret-content..."
  certificate_password  = "secret"
}

data "circleci_ios_signing_certificate" "test" {
  id = circleci_ios_signing_certificate.cert.id
}
`, iosSigningTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPair(
					"data.circleci_ios_signing_certificate.test", "id",
					"circleci_ios_signing_certificate.cert", "id",
				),
				resource.TestCheckResourceAttr("data.circleci_ios_signing_certificate.test", "organization_id", iosSigningTestOrgID),
				resource.TestCheckResourceAttr("data.circleci_ios_signing_certificate.test", "file_name", "distribution.p12"),
				resource.TestCheckResourceAttr("data.circleci_ios_signing_certificate.test", "cert_type", "distribution"),
				resource.TestCheckNoResourceAttr("data.circleci_ios_signing_certificate.test", "certificate_blob"),
				resource.TestCheckNoResourceAttr("data.circleci_ios_signing_certificate.test", "certificate_password"),
				iosSigningCheckNoSecretEcho("data.circleci_ios_signing_certificate.test", "cDEy...secret-content...", "secret"),
			),
		}},
	})
}

// TestAccIOSSigningCertificatesDataSource covers the plural, org-scoped
// listing, including that it reports an empty (not null) list for an
// organization with no certificates.
func TestAccIOSSigningCertificatesDataSource(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	t.Run("empty organization", func(t *testing.T) {
		config := iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
data "circleci_ios_signing_certificates" "test" {
  organization_id = %q
}
`, iosSigningTestOrgID)

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config: config,
				Check:  resource.TestCheckResourceAttr("data.circleci_ios_signing_certificates.test", "certificates.#", "0"),
			}},
		})
	})

	t.Run("with certificates", func(t *testing.T) {
		config := iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "cert" {
  organization_id      = %[1]q
  file_name             = "distribution.p12"
  certificate_blob      = "cDEy...content..."
  certificate_password  = "secret"
}

data "circleci_ios_signing_certificates" "test" {
  organization_id = %[1]q
  depends_on      = [circleci_ios_signing_certificate.cert]
}
`, iosSigningTestOrgID)

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.circleci_ios_signing_certificates.test", "certificates.#", "1"),
					resource.TestCheckResourceAttr("data.circleci_ios_signing_certificates.test", "certificates.0.file_name", "distribution.p12"),
				),
			}},
		})
	})
}

// TestAccIOSSigningConfigsDataSource covers the plural, org-scoped listing of
// signing configurations -- the only way to read one back at all, since there
// is no GET .../signing/configs/{id} route.
func TestAccIOSSigningConfigsDataSource(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	config := iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "cert" {
  organization_id      = %[1]q
  file_name             = "distribution.p12"
  certificate_blob      = "cDEy...content..."
  certificate_password  = "secret"
}

resource "circleci_ios_signing_config" "cfg" {
  organization_id = %[1]q
  name            = "release-config"
  certificate_id  = circleci_ios_signing_certificate.cert.id

  provisioning_profiles = [
    {
      file_name = "release.mobileprovision"
      blob      = "cGxpc3Q...profile..."
    },
  ]
}

data "circleci_ios_signing_configs" "test" {
  organization_id = %[1]q
  depends_on      = [circleci_ios_signing_config.cfg]
}
`, iosSigningTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.circleci_ios_signing_configs.test", "configs.#", "1"),
				resource.TestCheckResourceAttr("data.circleci_ios_signing_configs.test", "configs.0.name", "release-config"),
				resource.TestCheckResourceAttr("data.circleci_ios_signing_configs.test", "configs.0.provisioning_profiles.0.file_name", "release.mobileprovision"),
				resource.TestCheckNoResourceAttr("data.circleci_ios_signing_configs.test", "configs.0.provisioning_profiles.0.blob"),
				iosSigningCheckNoSecretEcho("data.circleci_ios_signing_configs.test", "cGxpc3Q...profile..."),
			),
		}},
	})
}
