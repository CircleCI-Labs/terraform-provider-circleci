// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
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

// TestAccIOSSigningConfigResource_ImportForcesReplacement is
// circleci_ios_signing_config's counterpart to
// TestAccIOSSigningCertificateResource_ImportForcesReplacement: proof, against
// a real plan, that importing a signing configuration and then supplying
// provisioning_profiles (the only way to have a usable resource, since the list
// is Required to have at least one entry) plans a replacement on the very
// first apply after import, not a quiet no-op. See this resource's
// ImportState doc comment.
//
// Both the certificate and the configuration are seeded directly into the
// fake, bypassing Terraform entirely, standing in for objects that already
// exist in the organization and were never created by this Terraform run.
func TestAccIOSSigningConfigResource_ImportForcesReplacement(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	const (
		certID      = "00000001-1111-2222-3333-444444444444"
		configID    = "00000002-1111-2222-3333-444444444444"
		profileBlob = "bW9iaWxlcHJvdmlzaW9uLWNvbnRlbnQ="
	)

	api.certs[certID] = &iosSigningFakeCert{
		ID: certID, OrgID: iosSigningTestOrgID, FileName: "distribution.p12",
		Blob: "cDEy...cert...", Password: "secret", CertType: "distribution",
		CreatedAt: iosSigningFakeCreatedAt, ExpiresAt: iosSigningFakeExpiresAt,
	}
	api.configs[configID] = &iosSigningFakeConfig{
		ID: configID, OrgID: iosSigningTestOrgID, Name: "release-config", CertID: certID,
		Profiles: []iosSigningFakeProfile{{FileName: "release.mobileprovision", Blob: profileBlob}},
	}

	config := iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_config" "test" {
  organization_id = %[1]q
  name            = "release-config"
  certificate_id  = %[2]q

  provisioning_profiles = [
    {
      file_name = "release.mobileprovision"
      blob      = %[3]q
    },
  ]
}
`, iosSigningTestOrgID, certID, profileBlob)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "circleci_ios_signing_config.test",
				ImportState:        true,
				ImportStateId:      iosSigningTestOrgID + "/" + configID,
				ImportStatePersist: true,
				Config:             config,
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_ios_signing_config.test", plancheck.ResourceActionReplace,
						),
					},
				},
			},
		},
	})
}

// TestAccIOSSigningConfigResource_ImportWriteOnlyForcesReplacement is the
// write-only counterpart, using provisioning_profiles_wo and
// provisioning_profiles_wo_version instead. provisioning_profiles_wo is null in
// state whether imported or created, so no modifier on it can ever fire;
// provisioning_profiles_wo_version is what carries RequiresReplace, and it is
// just as null after import as provisioning_profiles is on the state-backed
// path.
func TestAccIOSSigningConfigResource_ImportWriteOnlyForcesReplacement(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	const (
		certID      = "00000001-1111-2222-3333-444444444444"
		configID    = "00000002-1111-2222-3333-444444444444"
		profileBlob = "bW9iaWxlcHJvdmlzaW9uLWNvbnRlbnQ="
	)

	api.certs[certID] = &iosSigningFakeCert{
		ID: certID, OrgID: iosSigningTestOrgID, FileName: "distribution.p12",
		Blob: "cDEy...cert...", Password: "secret", CertType: "distribution",
		CreatedAt: iosSigningFakeCreatedAt, ExpiresAt: iosSigningFakeExpiresAt,
	}
	api.configs[configID] = &iosSigningFakeConfig{
		ID: configID, OrgID: iosSigningTestOrgID, Name: "release-config", CertID: certID,
		Profiles: []iosSigningFakeProfile{{FileName: "release.mobileprovision", Blob: profileBlob}},
	}

	config := iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_config" "test" {
  organization_id = %[1]q
  name            = "release-config"
  certificate_id  = %[2]q

  provisioning_profiles_wo = [
    {
      file_name = "release.mobileprovision"
      blob      = %[3]q
    },
  ]
  provisioning_profiles_wo_version = 1
}
`, iosSigningTestOrgID, certID, profileBlob)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "circleci_ios_signing_config.test",
				ImportState:        true,
				ImportStateId:      iosSigningTestOrgID + "/" + configID,
				ImportStatePersist: true,
				Config:             config,
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_ios_signing_config.test", plancheck.ResourceActionReplace,
						),
					},
				},
			},
		},
	})
}

// TestAccIOSSigningConfigResource_RejectsInvalidName guards the name
// validator, which mirrors the pattern the real API enforces.
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

// TestAccIOSSigningConfigResource_developerIDCertificateCannotHaveAConfig is the
// finding this resource's tests missed entirely.
//
// CircleCI decides whether provisioning profiles are required or forbidden from
// the referenced certificate's cert_type, and the rule has two sides: for
// distribution, development, mac-development and mac-app-distribution at least
// one profile is required, and for developer-id-application,
// developer-id-installer and mac-installer-distribution any profile at all is
// refused -- Apple's workflow has no provisioning profile for those three.
//
// This resource requires at least one profile (listvalidator.SizeAtLeast(1) plus
// iosSigningProfilesConfigValidator's ExactlyOneOf), so those three certificate
// types cannot have a circleci_ios_signing_config at all. That was invisible
// before: the client documented the opposite ("an empty list is accepted"), and
// the fake accepted any profile count against any certificate, so the whole suite
// reported this combination as working.
//
// It cannot be caught at plan time -- cert_type is derived from the uploaded
// certificate and is unknown while the certificate is being created in the same
// apply -- so the contract being pinned here is that the failure is CircleCI's,
// legible, and reaches the practitioner intact.
func TestAccIOSSigningConfigResource_developerIDCertificateCannotHaveAConfig(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	// The fake derives cert_type from a marker in the decoded blob; this one
	// decodes to a string containing DEVELOPER-ID-APPLICATION. See
	// iosSigningFakeCertTypeMarkers.
	const developerIDBlob = "REVWRUxPUEVSLUlELUFQUExJQ0FUSU9OOiBBY21l" // "DEVELOPER-ID-APPLICATION: Acme"

	config := iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "cert" {
  org_id               = %[1]q
  file_name            = "developer-id.p12"
  certificate_blob     = %[2]q
  certificate_password = "secret"
}

resource "circleci_ios_signing_config" "test" {
  org_id         = %[1]q
  name           = "direct-distribution"
  certificate_id = circleci_ios_signing_certificate.cert.id

  provisioning_profiles = [
    {
      file_name = "release.mobileprovision"
      blob      = "bW9iaWxlcHJvdmlzaW9u"
    },
  ]
}
`, iosSigningTestOrgID, developerIDBlob)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`provisioning profiles are not allowed for this certificate type`),
		}},
	})
}

// TestAccIOSSigningConfigResource_macAppDistributionCertificateWorks is the other
// side of the same rule: a certificate type outside the exempt three does require
// a profile, and pairing one with it succeeds.
//
// Without this the test above would be satisfied by a fake that refused every
// non-iOS certificate type, which is not what the service does.
func TestAccIOSSigningConfigResource_macAppDistributionCertificateWorks(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	const macAppBlob = "TUFDLUFQUC1ESVNUUklCVVRJT046IEFjbWU=" // "MAC-APP-DISTRIBUTION: Acme"

	config := iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "cert" {
  org_id               = %[1]q
  file_name            = "mac-app.p12"
  certificate_blob     = %[2]q
  certificate_password = "secret"
}

resource "circleci_ios_signing_config" "test" {
  org_id         = %[1]q
  name           = "mac-app-store"
  certificate_id = circleci_ios_signing_certificate.cert.id

  provisioning_profiles = [
    {
      file_name = "mac.provisionprofile"
      blob      = "bW9iaWxlcHJvdmlzaW9u"
    },
  ]
}
`, iosSigningTestOrgID, macAppBlob)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				// cert_type is one of the five values this provider used to claim did
				// not exist.
				resource.TestCheckResourceAttr("circleci_ios_signing_certificate.cert",
					"cert_type", "mac-app-distribution"),
				resource.TestCheckResourceAttr("circleci_ios_signing_config.test",
					"certificate_type", "mac-app-distribution"),
			),
		}},
	})
}

// TestAccIOSSigningConfigResource_duplicateNameConflicts pins that a signing
// configuration name is unique within the organization: a repeat is refused with
// a conflict rather than replacing the existing configuration. Nothing asserted
// this before, and nothing in the schema hints at it.
func TestAccIOSSigningConfigResource_duplicateNameConflicts(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	config := iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "cert" {
  org_id               = %[1]q
  file_name            = "distribution.p12"
  certificate_blob     = "cDEyLWRpc3RyaWJ1dGlvbg=="
  certificate_password = "secret"
}

resource "circleci_ios_signing_config" "first" {
  org_id         = %[1]q
  name           = "release-config"
  certificate_id = circleci_ios_signing_certificate.cert.id

  provisioning_profiles = [
    { file_name = "a.mobileprovision", blob = "YQ==" },
  ]
}

resource "circleci_ios_signing_config" "second" {
  org_id         = %[1]q
  name           = "release-config"
  certificate_id = circleci_ios_signing_certificate.cert.id

  provisioning_profiles = [
    { file_name = "b.mobileprovision", blob = "Yg==" },
  ]
}
`, iosSigningTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`(?s)signing configuration with this name already exists`),
		}},
	})
}
