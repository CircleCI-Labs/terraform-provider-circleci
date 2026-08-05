// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const iosSigningTestOrgID = "22222222-2222-2222-2222-222222222222"

// TestAccIOSSigningCertificateResource covers the create/read/delete cycle,
// and is the load-bearing test for two of the security decisions documented on
// the resource:
//
//  1. certificate_blob and certificate_password are never echoed into any
//     other attribute (checked explicitly below), and
//  2. a second `terraform plan` right after `terraform apply` is empty --
//     the "write-only-content trap": if Read tried to clear or recompute
//     either write-only attribute from the API's answer (which has nothing to
//     say about either), every subsequent plan would show a permanent diff.
func TestAccIOSSigningCertificateResource(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	const (
		blob     = "cDEyLWJhc2U2NC1jb250ZW50LWRpc3RyaWJ1dGlvbg=="
		password = "s3cret-password"
	)

	config := fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "test" {
  organization_id      = %q
  file_name            = "distribution.p12"
  certificate_blob     = %q
  certificate_password = %q
}
`, iosSigningTestOrgID, blob, password)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: iosSigningProviderConfig(api.URL()) + config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("circleci_ios_signing_certificate.test", "id"),
					resource.TestCheckResourceAttr("circleci_ios_signing_certificate.test", "organization_id", iosSigningTestOrgID),
					resource.TestCheckResourceAttr("circleci_ios_signing_certificate.test", "file_name", "distribution.p12"),
					resource.TestCheckResourceAttr("circleci_ios_signing_certificate.test", "certificate_blob", blob),
					resource.TestCheckResourceAttr("circleci_ios_signing_certificate.test", "certificate_password", password),
					// cert_type is server-derived, never accepted as input.
					resource.TestCheckResourceAttr("circleci_ios_signing_certificate.test", "cert_type", "distribution"),
					resource.TestCheckResourceAttrSet("circleci_ios_signing_certificate.test", "fingerprint"),
					resource.TestCheckResourceAttr("circleci_ios_signing_certificate.test", "created_at", iosSigningFakeCreatedAt),
					resource.TestCheckResourceAttr("circleci_ios_signing_certificate.test", "expires_at", iosSigningFakeExpiresAt),
					iosSigningCheckNoSecretEcho("circleci_ios_signing_certificate.test", blob, password),
				),
			},
			// A second plan with no configuration change must report no changes at
			// all, despite certificate_blob and certificate_password having nothing
			// the API can refresh them from.
			{
				Config:   iosSigningProviderConfig(api.URL()) + config,
				PlanOnly: true,
			},
			{
				ResourceName:      "circleci_ios_signing_certificate.test",
				ImportState:       true,
				ImportStateVerify: true,
				// certificate_blob and certificate_password cannot be recovered on
				// import: the API never returns either.
				ImportStateVerifyIgnore: []string{"certificate_blob", "certificate_password"},
			},
		},
	})

	if got := api.certs; len(got) != 0 {
		t.Errorf("certs remaining after destroy = %d, want 0", len(got))
	}
}

// TestAccIOSSigningCertificateResource_ImportForcesReplacement proves, against
// a real plan rather than by reading the plan modifier's source, the
// consequence documented on ImportState and on the resource's documentation
// page: importing a certificate and then supplying its content via
// certificate_blob/certificate_password (the state-backed path) plans a
// replacement on the very first apply after import, not a quiet no-op.
//
// The certificate is seeded directly into the fake, bypassing Terraform
// entirely, to stand in for one that already exists in the organization and
// was never created by this Terraform run -- exactly the case import is for.
// ImportStatePersist is required to observe this: a bare ImportState step
// imports into a throwaway working directory and discards it, so a Config
// step afterwards would still be planning against whatever state the
// *previous* step left behind rather than against the imported state.
func TestAccIOSSigningCertificateResource_ImportForcesReplacement(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	const (
		blob     = "cDEyLWJhc2U2NC1jb250ZW50LWRpc3RyaWJ1dGlvbg=="
		password = "s3cret-password"
		id       = "00000001-1111-2222-3333-444444444444"
	)

	api.certs[id] = &iosSigningFakeCert{
		ID:        id,
		OrgID:     iosSigningTestOrgID,
		FileName:  "distribution.p12",
		Blob:      blob,
		Password:  password,
		CertType:  "distribution",
		CreatedAt: iosSigningFakeCreatedAt,
		ExpiresAt: iosSigningFakeExpiresAt,
	}

	config := iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "test" {
  organization_id      = %q
  file_name            = "distribution.p12"
  certificate_blob     = %q
  certificate_password = %q
}
`, iosSigningTestOrgID, blob, password)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "circleci_ios_signing_certificate.test",
				ImportState:        true,
				ImportStateId:      id,
				ImportStatePersist: true,
				Config:             config,
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_ios_signing_certificate.test", plancheck.ResourceActionReplace,
						),
					},
				},
			},
		},
	})
}

// TestAccIOSSigningCertificateResource_ImportWriteOnlyForcesReplacement is the
// write-only counterpart: certificate_wo_version, not certificate_blob_wo (which
// is null in state whether imported or created, so no modifier on it can ever
// fire), is what carries the RequiresReplace here, and it is just as null after
// import as certificate_blob is on the state-backed path.
func TestAccIOSSigningCertificateResource_ImportWriteOnlyForcesReplacement(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	const (
		blob     = "cDEyLWJhc2U2NC1jb250ZW50LWRpc3RyaWJ1dGlvbg=="
		password = "s3cret-password"
		id       = "00000001-1111-2222-3333-444444444444"
	)

	api.certs[id] = &iosSigningFakeCert{
		ID:        id,
		OrgID:     iosSigningTestOrgID,
		FileName:  "distribution.p12",
		Blob:      blob,
		Password:  password,
		CertType:  "distribution",
		CreatedAt: iosSigningFakeCreatedAt,
		ExpiresAt: iosSigningFakeExpiresAt,
	}

	config := iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "test" {
  organization_id         = %q
  file_name               = "distribution.p12"
  certificate_blob_wo     = %q
  certificate_password_wo = %q
  certificate_wo_version  = 1
}
`, iosSigningTestOrgID, blob, password)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "circleci_ios_signing_certificate.test",
				ImportState:        true,
				ImportStateId:      id,
				ImportStatePersist: true,
				Config:             config,
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_ios_signing_certificate.test", plancheck.ResourceActionReplace,
						),
					},
				},
			},
		},
	})
}

// TestAccIOSSigningCertificateResource_DevelopmentCertType covers the other
// cert_type value the fake can produce, so the test suite does not only ever
// exercise "distribution".
func TestAccIOSSigningCertificateResource_DevelopmentCertType(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	config := iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "test" {
  organization_id      = %q
  file_name            = "development.p12"
  certificate_blob     = "cDEyLWJhc2U2NC1jb250ZW50LURFVi1jZXJ0"
  certificate_password = "secret"
}
`, iosSigningTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.TestCheckResourceAttr(
				"circleci_ios_signing_certificate.test", "cert_type", "development"),
		}},
	})
}

// TestAccIOSSigningCertificateResource_ChangingContentReplaces asserts that
// changing certificate_blob replaces the resource (uploads a new certificate,
// deletes the old one) rather than attempting an update the API has no route
// for.
func TestAccIOSSigningCertificateResource_ChangingContentReplaces(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	config := func(blob string) string {
		return iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "test" {
  organization_id      = %q
  file_name            = "distribution.p12"
  certificate_blob      = %q
  certificate_password  = "secret"
}
`, iosSigningTestOrgID, blob)
	}

	var firstID string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("cDEy...first..."),
				Check:  iosSigningCaptureAttr("circleci_ios_signing_certificate.test", "id", &firstID),
			},
			{
				Config: config("cDEy...second..."),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(state *terraform.State) error {
						res, ok := state.RootModule().Resources["circleci_ios_signing_certificate.test"]
						if !ok {
							return fmt.Errorf("resource not found in state")
						}
						if res.Primary.Attributes["id"] == firstID {
							return fmt.Errorf("id unchanged after changing certificate_blob, want a replacement")
						}

						return nil
					},
				),
			},
		},
	})
}

// iosSigningCaptureAttr stores an attribute value so a later step can compare
// against it.
func iosSigningCaptureAttr(address, key string, into *string) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		res, ok := state.RootModule().Resources[address]
		if !ok {
			return fmt.Errorf("resource %s not found in state", address)
		}
		*into = res.Primary.Attributes[key]

		return nil
	}
}

// iosSigningCheckNoSecretEcho asserts that neither secret value configured on
// address appears in any other attribute recorded in state -- id, fingerprint,
// cert_type and the timestamps must all be independent of the credentials, not
// a derivative of them that would leak the secret into a "readable" attribute.
func iosSigningCheckNoSecretEcho(address string, secrets ...string) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		res, ok := state.RootModule().Resources[address]
		if !ok {
			return fmt.Errorf("resource %s not found in state", address)
		}

		for attr, value := range res.Primary.Attributes {
			// Configured inputs are necessarily in state — that is what Terraform
			// does with a Required attribute, and it is precisely why the docs insist
			// on an encrypted backend. What this check is actually for is a COMPUTED
			// attribute that echoes a secret back, which would leak it somewhere the
			// practitioner never asked for.
			if isIOSSigningSecretInput(attr) {
				continue
			}
			for _, secret := range secrets {
				if secret != "" && strings.Contains(value, secret) {
					return fmt.Errorf("attribute %s = %q contains a configured secret value; "+
						"a write-only credential must not be echoed into a readable attribute", attr, value)
				}
			}
		}

		return nil
	}
}

// isIOSSigningSecretInput reports whether attr is a secret the configuration
// supplies, rather than a value the provider computed.
//
// provisioning_profiles is a nested list, so its blobs appear as
// "provisioning_profiles.0.blob" and cannot be matched by name alone.
func isIOSSigningSecretInput(attr string) bool {
	switch attr {
	case "certificate_blob", "certificate_password":
		return true
	}

	return strings.HasSuffix(attr, ".blob")
}
