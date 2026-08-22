// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// This file is the only [NET] coverage for circleci_ios_signing_certificate
// and circleci_ios_signing_config: every other test in this package (see
// ios_signing_fake_test.go) drives the resources against an in-memory fake,
// because this family shipped from a specification and had never been run
// against a real CircleCI installation. These two tests close that gap.
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
