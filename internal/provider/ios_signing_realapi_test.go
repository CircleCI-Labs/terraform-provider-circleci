// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"terraform-provider-circleci/internal/circleci"
)

// This file is the only [NET] coverage for circleci_ios_signing_certificate
// and circleci_ios_signing_config: every other test in this package (see
// ios_signing_fake_test.go) drives the resources against an in-memory fake,
// because this family shipped from a specification and had never been run
// against a real CircleCI installation. These three tests close that gap.
//
// The first two cover create/read/import/destroy for each resource in
// isolation. TestAccIOSSigningCertificateRotation_RealAPI covers what those
// two, run separately, cannot: rotating a certificate that a signing config
// still references, as one compound Terraform apply. Both resources are
// entirely RequiresReplace (there is no update route for either), so a
// rotation is a destroy-and-create of the certificate *and* a destroy-and-
// create of the config that names its id, in the same plan -- and
// DeleteSigningCertificate answers 409 while a config still references the
// certificate (see its doc comment below). Terraform's default replace
// ordering destroys a dependent resource before the thing it depends on, so
// the config is destroyed before the certificate it references, and that 409
// is never reached; TestAccIOSSigningCertificateRotation_RealAPI is the real-
// API proof that this actually happens, not just a description of why it
// should.
//
// Every certificate and provisioning profile here is generated locally by
// shelling out to openssl -- never a real Apple-issued signing identity, and
// nothing capable of signing a real iOS build. TestAccIOSSigningCertificateResource_RealAPI
// and TestAccIOSSigningConfigResource_RealAPI both skip, with a named reason,
// when openssl is not on PATH, when CIRCLE_TOKEN is unset (testAccPreCheck),
// or when no primary test organization is configured (testOrgID) -- so the
// suite stays green without a provisioned account, exactly like every other
// TestAcc test in this package.
//
// What was verified by hand against the real API before this file existed,
// and is pinned here so it stays true: uploading a self-signed certificate
// whose Subject Common Name starts "iPhone Developer: " is classified
// cert_type="development"; the reported fingerprint is a 40-character lowercase
// hex SHA-1 digest with no separators (see the correction in
// signing_certificate_test.go -- the fixture used to show "AA:BB:CC:DD", which
// is not the real shape); cert_blob and cert_password never appear in the
// GET response; re-uploading identical certificate bytes under a different
// file_name upserts onto the first upload's id and leaves file_name
// unchanged; a certificate still referenced by a signing configuration
// answers 409 "certificate is in use by one or more signing configurations"
// on DELETE; and a genuinely CMS-signed provisioning profile naming the
// certificate's own DER bytes in DeveloperCertificates is accepted by
// POST .../signing/configs and cross-checked successfully.

// iosSigningRealArtifacts holds a throwaway, locally generated certificate
// (and, optionally, a provisioning profile built to authorize it) for the
// [NET] tests below.
type iosSigningRealArtifacts struct {
	CertBlobBase64    string
	CertPassword      string
	ProfileBlobBase64 string // set only when generated withProfile.
}

// generateIOSSigningRealArtifacts shells out to openssl to build a throwaway
// self-signed certificate with the given Subject Common Name, and -- when
// withProfile is true -- a CMS-signed (.mobileprovision-shaped) provisioning
// profile whose DeveloperCertificates array names that certificate's own DER
// bytes, so CircleCI's cross-check between a signing configuration's profile
// and its certificate has something real to match against.
//
// It skips the calling test, naming the reason, when openssl is not on PATH:
// there is no pure-Go PKCS#12 encoder in this module's dependency graph (see
// the comment on iosSigningFakeCertType for why the fake does not need one),
// so this is the only way to produce upload-able certificate bytes without a
// new third-party dependency.
func generateIOSSigningRealArtifacts(t *testing.T, commonName string, withProfile bool) iosSigningRealArtifacts {
	t.Helper()

	opensslPath, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not found on PATH: required to generate a throwaway self-signed " +
			"certificate for this [NET] acceptance test; see this file's package comment")
	}

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.pem")
	certPath := filepath.Join(dir, "cert.pem")
	p12Path := filepath.Join(dir, "cert.p12")

	run := func(args ...string) {
		out, err := exec.Command(opensslPath, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("openssl %v: %v\n%s", args, err, out)
		}
	}

	run("req", "-x509", "-newkey", "rsa:2048", "-keyout", keyPath, "-out", certPath,
		"-days", "30", "-nodes", "-subj", "/CN="+commonName)

	passwordBytes := make([]byte, 16)
	if _, err := rand.Read(passwordBytes); err != nil {
		t.Fatalf("generating a random certificate password: %v", err)
	}
	password := hex.EncodeToString(passwordBytes)

	run("pkcs12", "-export", "-inkey", keyPath, "-in", certPath, "-out", p12Path,
		"-passout", "pass:"+password)

	p12, err := os.ReadFile(p12Path)
	if err != nil {
		t.Fatalf("reading generated .p12: %v", err)
	}

	artifacts := iosSigningRealArtifacts{
		CertBlobBase64: base64.StdEncoding.EncodeToString(p12),
		CertPassword:   password,
	}

	if withProfile {
		derPath := filepath.Join(dir, "cert.der")
		run("x509", "-in", certPath, "-outform", "DER", "-out", derPath)

		der, err := os.ReadFile(derPath)
		if err != nil {
			t.Fatalf("reading generated DER certificate: %v", err)
		}

		plistPath := filepath.Join(dir, "profile.plist")
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Name</key>
	<string>terraform-provider-circleci acceptance test</string>
	<key>UUID</key>
	<string>%s</string>
	<key>TeamIdentifier</key>
	<array><string>ACCTEST0000</string></array>
	<key>DeveloperCertificates</key>
	<array><data>%s</data></array>
</dict>
</plist>`, uuid.NewString(), base64.StdEncoding.EncodeToString(der))

		if err := os.WriteFile(plistPath, []byte(plist), 0o600); err != nil {
			t.Fatalf("writing generated plist: %v", err)
		}

		provisionPath := filepath.Join(dir, "profile.mobileprovision")
		run("smime", "-sign", "-signer", certPath, "-inkey", keyPath, "-certfile", certPath,
			"-in", plistPath, "-out", provisionPath, "-outform", "DER", "-nodetach")

		provision, err := os.ReadFile(provisionPath)
		if err != nil {
			t.Fatalf("reading generated provisioning profile: %v", err)
		}
		artifacts.ProfileBlobBase64 = base64.StdEncoding.EncodeToString(provision)
	}

	return artifacts
}

// iosSigningFingerprintPattern is the real, measured shape of a reported
// fingerprint: 40 lowercase hex characters, no colons. See the package
// comment above -- the pre-existing FAKE test fixtures showed
// "AA:BB:CC:DD:...", which this test would have caught immediately.
var iosSigningFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// TestAccIOSSigningCertificateResource_RealAPI is the [NET] counterpart to
// TestAccIOSSigningCertificateResource: same create/no-op-plan/import
// sequence, but against the real CircleCI API, with a genuinely uploaded,
// self-signed, throwaway certificate rather than the fake's stand-in bytes.
func TestAccIOSSigningCertificateResource_RealAPI(t *testing.T) {
	testAccPreCheck(t)
	orgID := testOrgID(t)

	artifacts := generateIOSSigningRealArtifacts(t, "iPhone Developer: tf-provider-circleci acctest (ACCTEST1)", false)

	config := fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "test" {
  organization_id      = %q
  file_name            = "acctest-realapi.p12"
  certificate_blob     = %q
  certificate_password = %q
}
`, orgID, artifacts.CertBlobBase64, artifacts.CertPassword)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("circleci_ios_signing_certificate.test", "id"),
					resource.TestCheckResourceAttr("circleci_ios_signing_certificate.test", "organization_id", orgID),
					// The real classifier, not the fake's marker-matching stand-in: a
					// genuine X.509 Subject Common Name starting "iPhone Developer: " is
					// classified "development".
					resource.TestCheckResourceAttr("circleci_ios_signing_certificate.test", "cert_type", "development"),
					resource.TestCheckResourceAttrWith("circleci_ios_signing_certificate.test", "fingerprint",
						func(value string) error {
							if !iosSigningFingerprintPattern.MatchString(value) {
								return fmt.Errorf("fingerprint = %q, want 40 lowercase hex characters with no "+
									"separators (measured against the real API; the fake used to assume "+
									"colon-separated uppercase)", value)
							}

							return nil
						}),
					resource.TestCheckResourceAttrSet("circleci_ios_signing_certificate.test", "created_at"),
					resource.TestCheckResourceAttrSet("circleci_ios_signing_certificate.test", "expires_at"),
					iosSigningCheckNoSecretEcho("circleci_ios_signing_certificate.test",
						artifacts.CertBlobBase64, artifacts.CertPassword),
				),
			},
			// Real proof of the write-only-content trap this whole family exists to
			// avoid: certificate_blob and certificate_password have nothing the real
			// API can refresh them from, and this plan must still be empty.
			{
				Config:   config,
				PlanOnly: true,
			},
			{
				ResourceName:            "circleci_ios_signing_certificate.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"certificate_blob", "certificate_password"},
			},
		},
	})
}

// TestAccIOSSigningConfigResource_RealAPI is the [NET] counterpart to
// TestAccIOSSigningConfigResource. The provisioning profile is a genuine
// CMS-signed (.mobileprovision-shaped) structure naming the certificate's own
// DER bytes in DeveloperCertificates, so this exercises the real API's
// documented cross-check between a profile and its certificate, not just the
// "at least one profile" count the fake enforces from cert_type.
func TestAccIOSSigningConfigResource_RealAPI(t *testing.T) {
	testAccPreCheck(t)
	orgID := testOrgID(t)

	artifacts := generateIOSSigningRealArtifacts(
		t, "iPhone Developer: tf-provider-circleci acctest (ACCTEST2)", true,
	)

	config := fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "cert" {
  organization_id      = %[1]q
  file_name            = "acctest-realapi-config.p12"
  certificate_blob     = %[2]q
  certificate_password = %[3]q
}

resource "circleci_ios_signing_config" "test" {
  organization_id = %[1]q
  name            = "acctest-realapi-config"
  certificate_id  = circleci_ios_signing_certificate.cert.id

  provisioning_profiles = [
    {
      file_name = "acctest-realapi.mobileprovision"
      blob      = %[4]q
    },
  ]
}
`, orgID, artifacts.CertBlobBase64, artifacts.CertPassword, artifacts.ProfileBlobBase64)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("circleci_ios_signing_config.test", "id"),
					resource.TestCheckResourceAttrPair(
						"circleci_ios_signing_config.test", "certificate_id",
						"circleci_ios_signing_certificate.cert", "id",
					),
					resource.TestCheckResourceAttr("circleci_ios_signing_config.test", "certificate_file_name", "acctest-realapi-config.p12"),
					resource.TestCheckResourceAttr("circleci_ios_signing_config.test", "certificate_type", "development"),
					resource.TestCheckResourceAttr("circleci_ios_signing_config.test", "provisioning_profiles.0.file_name", "acctest-realapi.mobileprovision"),
					iosSigningCheckNoSecretEcho("circleci_ios_signing_config.test", artifacts.ProfileBlobBase64),
				),
			},
			// GetSigningConfig has no single-entity route at all -- only a list --
			// so this is the real-API proof that Read still produces an empty plan
			// despite hydrating entirely from a list match.
			{
				Config:   config,
				PlanOnly: true,
			},
			{
				ResourceName: "circleci_ios_signing_config.test",
				ImportState:  true,
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					res, ok := state.RootModule().Resources["circleci_ios_signing_config.test"]
					if !ok {
						return "", fmt.Errorf("resource not found in state")
					}

					return orgID + "/" + res.Primary.Attributes["id"], nil
				},
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"provisioning_profiles"},
			},
		},
	})
}

// TestAccIOSSigningCertificateRotation_RealAPI is the compound [NET] scenario
// TestAccIOSSigningCertificateResource_RealAPI and
// TestAccIOSSigningConfigResource_RealAPI cannot reach on their own: rotating
// a certificate that a circleci_ios_signing_config still references, driven
// through one ordinary Terraform apply rather than assembled by hand.
//
// The two certificates generated here must be genuinely different key
// material, not the same bytes twice: CreateSigningCertificate upserts on
// (organization, fingerprint), so re-uploading identical bytes under a new
// file_name would silently return the first upload's id and certificate_id
// would never actually change -- the test would report a pass having rotated
// nothing. generateIOSSigningRealArtifacts is called twice with different
// Subject Common Names specifically so each run generates its own RSA key
// pair; the explicit fingerprint-inequality assertion below is what turns a
// silent no-op into a loud failure if that ever stopped being true.
//
// Both resources are entirely RequiresReplace -- certificate_blob and
// certificate_password force a new circleci_ios_signing_certificate, and
// certificate_id forces a new circleci_ios_signing_config, because neither
// resource has an update route (see both resources' Update methods). So this
// rotation plans a replace of both in the same apply. DeleteSigningCertificate
// answers 409 while any config still references the certificate (see the doc
// comment on that method), which means the destroy order across the two
// resources matters: destroying the old certificate before the old config is
// deleted would hit that 409. It doesn't happen, because certificate_id also
// makes the config depend on the certificate, and Terraform's default
// (non-create_before_destroy) replace ordering destroys a dependent resource
// before the resource it depends on -- so the old config is destroyed first,
// and the 409 is never reached. This test is what proves that ordering holds
// against the real API, rather than only against the fake's model of it (see
// the 409 case in ios_signing_fake_test.go's deleteCertificate).
func TestAccIOSSigningCertificateRotation_RealAPI(t *testing.T) {
	testAccPreCheck(t)
	orgID := testOrgID(t)

	// Subject Common Name is capped at 64 characters by the X.509 ASN.1
	// PrintableString it's encoded into -- openssl req rejects anything longer
	// with "string too long" rather than truncating -- so these stay short.
	before := generateIOSSigningRealArtifacts(t, "iPhone Developer: tf-provider-circleci acctest (ROTATE1)", true)
	after := generateIOSSigningRealArtifacts(t, "iPhone Developer: tf-provider-circleci acctest (ROTATE2)", true)

	if before.CertBlobBase64 == after.CertBlobBase64 {
		t.Fatal("the two generated certificates are byte-identical -- this rotation test would upsert " +
			"onto a single id and exercise nothing; see the doc comment above")
	}

	config := func(artifacts iosSigningRealArtifacts, certFileName, profileFileName string) string {
		return fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "cert" {
  organization_id      = %[1]q
  file_name            = %[2]q
  certificate_blob     = %[3]q
  certificate_password = %[4]q
}

resource "circleci_ios_signing_config" "test" {
  organization_id = %[1]q
  name            = "acctest-rotation-config"
  certificate_id  = circleci_ios_signing_certificate.cert.id

  provisioning_profiles = [
    {
      file_name = %[5]q
      blob      = %[6]q
    },
  ]
}
`, orgID, certFileName, artifacts.CertBlobBase64, artifacts.CertPassword, profileFileName, artifacts.ProfileBlobBase64)
	}

	var beforeCertID, beforeConfigID string

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(before, "acctest-rotation-before.p12", "acctest-rotation-before.mobileprovision"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("circleci_ios_signing_certificate.cert", "id"),
					resource.TestCheckResourceAttrSet("circleci_ios_signing_config.test", "id"),
					resource.TestCheckResourceAttrPair(
						"circleci_ios_signing_config.test", "certificate_id",
						"circleci_ios_signing_certificate.cert", "id",
					),
					func(s *terraform.State) error {
						cert, ok := s.RootModule().Resources["circleci_ios_signing_certificate.cert"]
						if !ok {
							return fmt.Errorf("circleci_ios_signing_certificate.cert not found in state")
						}
						cfg, ok := s.RootModule().Resources["circleci_ios_signing_config.test"]
						if !ok {
							return fmt.Errorf("circleci_ios_signing_config.test not found in state")
						}
						beforeCertID = cert.Primary.ID
						beforeConfigID = cfg.Primary.ID

						return nil
					},
				),
			},
			// The rotation itself: genuinely different key material and a
			// differently-authorized provisioning profile, which must plan (and
			// successfully apply) a replace of both resources in one step.
			{
				Config: config(after, "acctest-rotation-after.p12", "acctest-rotation-after.mobileprovision"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_ios_signing_certificate.cert", plancheck.ResourceActionReplace),
						plancheck.ExpectResourceAction("circleci_ios_signing_config.test", plancheck.ResourceActionReplace),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(
						"circleci_ios_signing_config.test", "certificate_id",
						"circleci_ios_signing_certificate.cert", "id",
					),
					resource.TestCheckResourceAttrWith("circleci_ios_signing_certificate.cert", "id", func(value string) error {
						if value == beforeCertID {
							return fmt.Errorf("certificate id %q is unchanged after rotation -- the replace did not happen", value)
						}

						return nil
					}),
					resource.TestCheckResourceAttrWith("circleci_ios_signing_config.test", "id", func(value string) error {
						if value == beforeConfigID {
							return fmt.Errorf("config id %q is unchanged after rotation -- the replace did not happen", value)
						}

						return nil
					}),
					// The load-bearing assertion: had the old certificate been
					// destroyed before the old config that referenced it, this whole
					// apply would have failed with a 409 rather than reaching this
					// Check at all. Reaching here already proves the ordering: this
					// additionally confirms the old certificate is actually gone
					// (not merely dropped from Terraform state), by asking the real
					// API directly rather than trusting a 2xx from a step that
					// completed.
					func(*terraform.State) error {
						client := circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})

						_, err := client.GetSigningCertificate(t.Context(), beforeCertID)
						if !circleci.IsNotFound(err) {
							return fmt.Errorf(
								"GetSigningCertificate(%s) (the pre-rotation certificate) = %v, want a not-found "+
									"error -- the rotation's destroy of the old certificate either did not happen "+
									"or the id is somehow still live", beforeCertID, err,
							)
						}

						return nil
					},
				),
			},
		},
	})
}

// TestIOSSigningCertificateDeleteWhileReferenced_RealAPI is the automated
// [NET] proof of the fact this file's package comment says was, until now,
// only "verified by hand": DeleteSigningCertificate answers 409 while a
// circleci_ios_signing_config still references the certificate. It is also
// the reason TestAccIOSSigningCertificateRotation_RealAPI's ordering matters
// -- that 409 is exactly what a wrong destroy order would hit.
//
// This bypasses Terraform and the provider entirely, driving circleci.Client
// straight against the real API, because the 409 is the *service's* own
// referential-integrity check, not anything the provider computes. Terraform
// never lets a practitioner hit it (its dependency graph destroys the
// referencing config first, whenever certificate_id is what ties the two
// together -- the only way to create the pair), so reproducing the 409 means
// deliberately deleting in the wrong order, which there is no supported way
// to do through the resources themselves.
func TestIOSSigningCertificateDeleteWhileReferenced_RealAPI(t *testing.T) {
	testAccPreCheck(t)
	orgID := testOrgID(t)
	ctx := t.Context()

	client := circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})

	artifacts := generateIOSSigningRealArtifacts(t, "iPhone Developer: tf-provider-circleci acctest (DELREF)", true)

	cert, err := client.CreateSigningCertificate(ctx, circleci.CreateSigningCertificateRequest{
		OrganizationID: orgID,
		FileName:       "acctest-delref.p12",
		CertBlob:       artifacts.CertBlobBase64,
		CertPassword:   artifacts.CertPassword,
	})
	if err != nil {
		t.Fatalf("CreateSigningCertificate: %v", err)
	}
	// Cleanup runs even if an assertion below fails partway, in the order the
	// real API requires: the config (if it exists) before the certificate it
	// references, exactly the ordering this whole test is about.
	var cfg *circleci.SigningConfig
	t.Cleanup(func() {
		// context.Background(), not ctx: t.Context() is already canceled by the
		// time Cleanup funcs run, and this cleanup has to make real DELETE calls
		// after that point.
		cleanupCtx := context.Background()

		if cfg != nil {
			if err := client.DeleteSigningConfig(cleanupCtx, cfg.ID); err != nil && !circleci.IsNotFound(err) {
				t.Errorf("cleanup: DeleteSigningConfig(%s): %v", cfg.ID, err)
			}
		}
		if err := client.DeleteSigningCertificate(cleanupCtx, cert.ID); err != nil && !circleci.IsNotFound(err) {
			t.Errorf("cleanup: DeleteSigningCertificate(%s): %v", cert.ID, err)
		}
	})

	cfg, err = client.CreateSigningConfig(ctx, circleci.CreateSigningConfigRequest{
		OrganizationID: orgID,
		CertificateID:  cert.ID,
		Name:           "acctest-delref-config",
		ProvisioningProfiles: []circleci.CreateSigningProvisioningProfile{
			{FileName: "acctest-delref.mobileprovision", Blob: artifacts.ProfileBlobBase64},
		},
	})
	if err != nil {
		t.Fatalf("CreateSigningConfig: %v", err)
	}

	// The measurement: deleting the certificate while cfg still references it.
	err = client.DeleteSigningCertificate(ctx, cert.ID)
	if err == nil {
		t.Fatal("DeleteSigningCertificate succeeded while a signing config still referenced it; " +
			"want a 409 -- either the API's referential-integrity check is gone, or this certificate " +
			"was not actually still referenced")
	}
	if !circleci.IsConflict(err) {
		t.Fatalf("DeleteSigningCertificate while referenced = %v, want an HTTP 409 conflict", err)
	}

	// The certificate must still exist: a rejected delete must not have had a
	// partial effect.
	if _, err := client.GetSigningCertificate(ctx, cert.ID); err != nil {
		t.Fatalf("GetSigningCertificate(%s) after the rejected delete: %v -- the 409 should have left "+
			"the certificate untouched", cert.ID, err)
	}

	// Now delete in the order that actually works, confirming the 409 was
	// solely about the reference and not, say, a permissions problem: the
	// config first, then the certificate it no longer blocks.
	if err := client.DeleteSigningConfig(ctx, cfg.ID); err != nil {
		t.Fatalf("DeleteSigningConfig: %v", err)
	}
	cfg = nil // deleted; t.Cleanup above must not try again.

	if err := client.DeleteSigningCertificate(ctx, cert.ID); err != nil {
		t.Fatalf("DeleteSigningCertificate, after the referencing config was deleted: %v -- want success "+
			"now that nothing references it", err)
	}
}
