// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"sort"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// These tests cover `certificate_blob_wo` + `certificate_password_wo` +
// `certificate_wo_version` on circleci_ios_signing_certificate
// (ios_signing_write_only.go).
//
// Every case that puts a write-only attribute in a configuration declares a
// minimum Terraform version through writeOnlySupported() — shared with the
// environment variable and webhook write-only tests — because write-only
// attributes are a 1.11 feature and without the check the failure on an older CLI
// is an opaque "Unsupported argument" rather than a skip.

const (
	iosSigningWOBlob          = "cDEyLWJhc2U2NC1jb250ZW50LWRpc3RyaWJ1dGlvbg=="
	iosSigningWORotatedBlob   = "cDEyLWJhc2U2NC1jb250ZW50LXJvdGF0ZWQ="
	iosSigningWOPassword      = "s3cret-password"
	iosSigningWONewPassword   = "rotated-password"
	iosSigningWOFileName      = "distribution.p12"
	iosSigningWOAddress       = "circleci_ios_signing_certificate.test"
	iosSigningFirstFakeCertID = "00000001-1111-2222-3333-444444444444"
)

// --- fake accessors -----------------------------------------------------------
//
// The fake stores file_name, cert_blob and cert_password straight out of the
// POST body (see iosSigningFakeAPI.createCertificate), so asserting on the stored
// record is asserting on the request the provider sent — which is the point here:
// the state-backed and write-only spellings must produce identical uploads. The
// same approach is used by the environment variable write-only tests.

// iosSigningStoredCert is what the fake received on create, in a form a test can
// compare.
type iosSigningStoredCert struct {
	ID       string
	OrgID    string
	FileName string
	Blob     string
	Password string
}

// storedCertificates returns every certificate the fake currently holds, ordered
// by id so two runs are comparable.
func (a *iosSigningFakeAPI) storedCertificates() []iosSigningStoredCert {
	a.mu.Lock()
	defer a.mu.Unlock()

	stored := make([]iosSigningStoredCert, 0, len(a.certs))
	for _, c := range a.certs {
		stored = append(stored, iosSigningStoredCert{
			ID: c.ID, OrgID: c.OrgID, FileName: c.FileName, Blob: c.Blob, Password: c.Password,
		})
	}

	sort.Slice(stored, func(i, j int) bool { return stored[i].ID < stored[j].ID })

	return stored
}

// snapshotStoredCertificates copies what the fake holds as a step check, which
// runs while the certificate still exists. Reading it after resource.UnitTest
// returns would always find nothing, because the harness destroys everything on
// the way out — so the assertion would pass for a provider that uploaded an empty
// certificate, which is exactly the failure being looked for.
func snapshotStoredCertificates(api *iosSigningFakeAPI, into *[]iosSigningStoredCert) resource.TestCheckFunc {
	return func(*terraform.State) error {
		*into = api.storedCertificates()

		return nil
	}
}

// --- configurations -----------------------------------------------------------

// iosSigningWriteOnlyConfig builds a certificate managed through the write-only
// pair. It is otherwise identical to iosSigningStatefulConfig, which is what
// makes the two comparable.
func iosSigningWriteOnlyConfig(host, blob, password string, version int) string {
	return iosSigningProviderConfig(host) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "test" {
  organization_id         = %[1]q
  file_name               = %[2]q
  certificate_blob_wo     = %[3]q
  certificate_password_wo = %[4]q
  certificate_wo_version  = %[5]d
}
`, iosSigningTestOrgID, iosSigningWOFileName, blob, password, version)
}

// iosSigningStatefulConfig builds the same certificate through the state-backed
// attributes.
func iosSigningStatefulConfig(host, blob, password string) string {
	return iosSigningProviderConfig(host) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "test" {
  organization_id      = %[1]q
  file_name            = %[2]q
  certificate_blob     = %[3]q
  certificate_password = %[4]q
}
`, iosSigningTestOrgID, iosSigningWOFileName, blob, password)
}

// --- both spellings reach the same request ------------------------------------

// TestIOSSigningWriteOnly_SameRequestAsCertificateBlob proves the two spellings
// are one argument: the same `.p12` and the same password, in the same POST body,
// to the same route. The two paths run against separate fakes and the stored
// records — which the fake fills in from the request body — are compared field by
// field, so a difference in any of them fails.
func TestIOSSigningWriteOnly_SameRequestAsCertificateBlob(t *testing.T) {
	observed := map[string][]iosSigningStoredCert{}

	for name, config := range map[string]func(host string) string{
		"certificate_blob": func(host string) string {
			return iosSigningStatefulConfig(host, iosSigningWOBlob, iosSigningWOPassword)
		},
		"certificate_blob_wo": func(host string) string {
			return iosSigningWriteOnlyConfig(host, iosSigningWOBlob, iosSigningWOPassword, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			api := newIOSSigningFakeAPI(t)

			var stored []iosSigningStoredCert

			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   writeOnlySupported(),
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: config(api.URL()),
					Check:  snapshotStoredCertificates(api, &stored),
					ConfigStateChecks: []statecheck.StateCheck{
						// Whichever name was used, the write-only attributes are never
						// persisted: the framework nulls them in plan and state.
						statecheck.ExpectKnownValue(iosSigningWOAddress, tfjsonpath.New("certificate_blob_wo"), knownvalue.Null()),
						statecheck.ExpectKnownValue(iosSigningWOAddress, tfjsonpath.New("certificate_password_wo"), knownvalue.Null()),
						// And cert_type is derived from the uploaded bytes, so it doubles as
						// proof the API got a decodable certificate rather than "".
						statecheck.ExpectKnownValue(iosSigningWOAddress, tfjsonpath.New("cert_type"), knownvalue.StringExact("distribution")),
					},
				}},
			})

			observed[name] = stored
		})
	}

	want := []iosSigningStoredCert{{
		ID:       iosSigningFirstFakeCertID,
		OrgID:    iosSigningTestOrgID,
		FileName: iosSigningWOFileName,
		Blob:     iosSigningWOBlob,
		Password: iosSigningWOPassword,
	}}

	if got := fmt.Sprint(observed["certificate_blob"]); got != fmt.Sprint(want) {
		t.Fatalf("the certificate_blob path uploaded %v, want %v", got, want)
	}
	if got, statePath := fmt.Sprint(observed["certificate_blob_wo"]), fmt.Sprint(observed["certificate_blob"]); got != statePath {
		t.Errorf("certificate_blob_wo reached the API differently from certificate_blob:\n"+
			"  certificate_blob_wo: %s\n  certificate_blob:    %s", got, statePath)
	}
}

// TestIOSSigningWriteOnly_CredentialsAreNullInState covers the other half of the
// trade the documentation describes: on the write-only path neither the
// certificate nor its password nor anything derived from either is in state, while
// the version is — and a second plan is still empty, which is the "write-only
// content" trap this resource has always had to avoid.
func TestIOSSigningWriteOnly_CredentialsAreNullInState(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: iosSigningWriteOnlyConfig(api.URL(), iosSigningWOBlob, iosSigningWOPassword, 4),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(iosSigningWOAddress, tfjsonpath.New("certificate_blob_wo"), knownvalue.Null()),
					statecheck.ExpectKnownValue(iosSigningWOAddress, tfjsonpath.New("certificate_password_wo"), knownvalue.Null()),
					statecheck.ExpectKnownValue(iosSigningWOAddress, tfjsonpath.New("certificate_blob"), knownvalue.Null()),
					statecheck.ExpectKnownValue(iosSigningWOAddress, tfjsonpath.New("certificate_password"), knownvalue.Null()),
					statecheck.ExpectKnownValue(iosSigningWOAddress, tfjsonpath.New("certificate_wo_version"), knownvalue.Int64Exact(4)),
				},
			},
			{
				Config:   iosSigningWriteOnlyConfig(api.URL(), iosSigningWOBlob, iosSigningWOPassword, 4),
				PlanOnly: true,
			},
			{
				ResourceName:      iosSigningWOAddress,
				ImportState:       true,
				ImportStateVerify: true,
				// The version is configuration-only, exactly like the two state-backed
				// credentials: nothing on the API reports it.
				ImportStateVerifyIgnore: []string{"certificate_wo_version"},
			},
		},
	})
}

// --- rotation -----------------------------------------------------------------

// TestIOSSigningWriteOnly_RotationNeedsAVersionBump covers both halves of the
// version contract: a changed `certificate_blob_wo` alone is invisible to
// Terraform and is therefore not uploaded, and bumping `certificate_wo_version`
// is what uploads it.
//
// The middle step is the one worth having. It fails if the resource ever grows
// something that leaks the certificate into state, because then the change alone
// would produce a diff and the version would be pointless.
//
// The bump replaces the resource rather than updating it, which is not a
// stylistic choice: Update on this resource is a no-op because the API has no
// update route, so an in-place plan would report success having uploaded nothing.
func TestIOSSigningWriteOnly_RotationNeedsAVersionBump(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	var afterRotation []iosSigningStoredCert

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: iosSigningWriteOnlyConfig(api.URL(), iosSigningWOBlob, iosSigningWOPassword, 1),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(iosSigningWOAddress, tfjsonpath.New("id"), knownvalue.StringExact(iosSigningFirstFakeCertID)),
				},
			},
			{
				// A new certificate and a new password, same version: no diff, so no
				// upload.
				Config:   iosSigningWriteOnlyConfig(api.URL(), iosSigningWORotatedBlob, iosSigningWONewPassword, 1),
				PlanOnly: true,
			},
			{
				Config: iosSigningWriteOnlyConfig(api.URL(), iosSigningWORotatedBlob, iosSigningWONewPassword, 2),
				Check:  snapshotStoredCertificates(api, &afterRotation),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(iosSigningWOAddress, plancheck.ResourceActionReplace),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(iosSigningWOAddress, tfjsonpath.New("certificate_wo_version"), knownvalue.Int64Exact(2)),
					// A replacement, so a new certificate id.
					statecheck.ExpectKnownValue(iosSigningWOAddress, tfjsonpath.New("id"), knownvalue.StringExact("00000002-1111-2222-3333-444444444444")),
				},
			},
		},
	})

	if len(afterRotation) != 1 {
		t.Fatalf("the fake holds %d certificate(s) after the rotation, want 1 (the old one is deleted): %+v",
			len(afterRotation), afterRotation)
	}
	if afterRotation[0].Blob != iosSigningWORotatedBlob || afterRotation[0].Password != iosSigningWONewPassword {
		t.Errorf("the rotation uploaded blob = %q, password = %q, want %q / %q — a rotation that does not "+
			"reach the wire leaves the old certificate live while state claims otherwise",
			afterRotation[0].Blob, afterRotation[0].Password, iosSigningWORotatedBlob, iosSigningWONewPassword)
	}
}

// --- validation ---------------------------------------------------------------

// TestIOSSigningWriteOnly_AttributeCombinations covers
// iosSigningCertificateBlobConfigValidator and every AlsoRequires on the five
// certificate attributes.
//
// The "certificate_blob_wo without a version" case is the load-bearing one:
// AlsoRequires is one-directional, so declaring the relationship only on the
// version would let that configuration through — and it would upload once and
// then be unrotatable forever, with Terraform reporting "no changes" every time
// the certificate was edited.
func TestIOSSigningWriteOnly_AttributeCombinations(t *testing.T) {
	body := func(credentials string) string {
		return `
resource "circleci_ios_signing_certificate" "test" {
  organization_id = "` + iosSigningTestOrgID + `"
  file_name       = "` + iosSigningWOFileName + `"
` + credentials + `
}
`
	}

	// The two halves of ExactlyOneOf report the same detail under different titles
	// — "Missing Attribute Configuration" when neither is set, "Invalid Attribute
	// Combination" when both are — so the detail is what these match on. The
	// whitespace is a pattern rather than a literal because these attribute names
	// are long enough that Terraform wraps the detail onto a second line, between
	// the colon and the list.
	exactlyOneBlob := regexp.MustCompile(
		`(?s)Exactly one of these attributes must be configured:\s+\[certificate_blob,certificate_blob_wo\]`)
	combination := regexp.MustCompile(`Invalid Attribute Combination`)

	tests := map[string]struct {
		credentials string
		error       *regexp.Regexp
	}{
		"neither blob": {
			credentials: ``,
			error:       exactlyOneBlob,
		},
		"both blobs": {
			credentials: `  certificate_blob        = "a"
  certificate_password    = "b"
  certificate_blob_wo     = "c"
  certificate_password_wo = "d"
  certificate_wo_version  = 1`,
			error: exactlyOneBlob,
		},
		"certificate_blob without a password": {
			credentials: `  certificate_blob = "a"`,
			error:       combination,
		},
		"certificate_password without a blob": {
			credentials: `  certificate_password = "b"`,
			error:       combination,
		},
		// The one that matters: valid-looking, and silently unrotatable if allowed.
		"write-only pair without a version": {
			credentials: `  certificate_blob_wo     = "c"
  certificate_password_wo = "d"`,
			error: combination,
		},
		"certificate_blob_wo without a password": {
			credentials: `  certificate_blob_wo    = "c"
  certificate_wo_version = 1`,
			error: combination,
		},
		"certificate_password_wo without a blob": {
			credentials: `  certificate_password_wo = "d"
  certificate_wo_version  = 1`,
			error: combination,
		},
		"version on its own": {
			credentials: `  certificate_wo_version = 1`,
			error:       combination,
		},
		// A crossed pair: the write-only certificate with the state-backed password,
		// which is the plausible mistake when migrating one attribute at a time.
		// Rejected twice over — certificate_blob_wo requires certificate_password_wo,
		// and certificate_password requires a certificate_blob that ExactlyOneOf has
		// already ruled out.
		"crossed pair": {
			credentials: `  certificate_blob_wo    = "c"
  certificate_password   = "b"
  certificate_wo_version = 1`,
			error: combination,
		},
		"version below one": {
			credentials: `  certificate_blob_wo     = "c"
  certificate_password_wo = "d"
  certificate_wo_version  = 0`,
			error: regexp.MustCompile(`Invalid Attribute Value`),
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			api := newIOSSigningFakeAPI(t)

			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   writeOnlySupported(),
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      iosSigningProviderConfig(api.URL()) + body(testCase.credentials),
					PlanOnly:    true,
					ExpectError: testCase.error,
				}},
			})

			// Validation happens before anything is uploaded.
			if stored := api.storedCertificates(); len(stored) != 0 {
				t.Errorf("the provider uploaded %d certificate(s) for a configuration that fails validation, want 0: %+v",
					len(stored), stored)
			}
		})
	}
}

// --- the guard ----------------------------------------------------------------

// TestIOSSigningWriteOnly_GuardsAgainstMissingCredentials drives Create directly,
// because a plan cannot reach these states: the validators reject them. The guard
// exists for the gap after validation — a write-only value that resolved to
// nothing by apply — where carrying on would POST an empty cert_blob or an empty
// cert_password. CircleCI accepts either, so the failure would surface much later,
// as a build that cannot sign, and there is nothing to fall back on: no route
// returns a certificate's content or password.
//
// The resource is given a client pointed at the fake rather than a nil one, so
// "an error was reported" and "the fake stored nothing" together prove no request
// was issued.
func TestIOSSigningWriteOnly_GuardsAgainstMissingCredentials(t *testing.T) {
	base := iosSigningCertificateResourceModel{
		OrganizationId: types.StringValue(iosSigningTestOrgID),
		FileName:       types.StringValue(iosSigningWOFileName),
	}

	// The summary is compared exactly, against Diagnostic.Summary() rather than
	// against the stringified Diagnostics: "Missing iOS signing certificate" is a
	// prefix of "Missing iOS signing certificate password", so a substring match on
	// one blob of text could not tell the two guards apart.
	tests := map[string]struct {
		model    iosSigningCertificateResourceModel
		summary  string
		explains *regexp.Regexp
	}{
		// The shape of a certificate already managed through the write-only pair whose
		// content has gone missing. The error has to name certificate_wo_version, since
		// that is the only clue the practitioner has that this is the write-only path.
		"write-only blob missing": {
			model: func() iosSigningCertificateResourceModel {
				model := base
				model.CertificateWOVersion = types.Int64Value(1)

				return model
			}(),
			summary:  "Missing iOS signing certificate",
			explains: regexp.MustCompile(`certificate_wo_version`),
		},
		"write-only password missing": {
			model: func() iosSigningCertificateResourceModel {
				model := base
				model.CertificateBlobWO = types.StringValue(iosSigningWOBlob)
				model.CertificateWOVersion = types.Int64Value(1)

				return model
			}(),
			summary:  "Missing iOS signing certificate password",
			explains: regexp.MustCompile(`certificate_password_wo`),
		},
		"state-backed password missing": {
			model: func() iosSigningCertificateResourceModel {
				model := base
				model.CertificateBlob = types.StringValue(iosSigningWOBlob)

				return model
			}(),
			summary:  "Missing iOS signing certificate password",
			explains: regexp.MustCompile(`certificate_password`),
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			api := newIOSSigningFakeAPI(t)
			schema := iosSigningCertificateResourceSchemaForTest(t)
			config := configForTest(t, schema, testCase.model)

			// The plan shares the config's raw value, which is harmless: nothing reads
			// the `_wo` attributes off the plan, because a real plan has them nulled.
			// What matters is that certificate_blob and certificate_password are null in
			// it, which is the state a write-only-managed resource is always in.
			plan := tfsdk.Plan{Schema: schema, Raw: config.Raw}

			resp := fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
			(&iosSigningCertificateResource{
				client: circleci.New(circleci.Config{Host: api.URL(), Token: "fake"}),
			}).Create(t.Context(), fwresource.CreateRequest{Config: config, Plan: plan}, &resp)

			var detail string
			for _, diagnostic := range resp.Diagnostics {
				if diagnostic.Summary() == testCase.summary {
					detail = diagnostic.Detail()
				}
			}

			if detail == "" {
				t.Fatalf("Create reported %v, want a %q error — without the guard an empty "+
					"certificate or password would have been uploaded", resp.Diagnostics, testCase.summary)
			}
			if !testCase.explains.MatchString(detail) {
				t.Errorf("Create's error does not explain which attribute is missing: %s", detail)
			}
			if stored := api.storedCertificates(); len(stored) != 0 {
				t.Errorf("Create uploaded %d certificate(s) despite the guard, want 0: %+v", len(stored), stored)
			}
		})
	}
}

// --- helpers ------------------------------------------------------------------

func iosSigningCertificateResourceSchemaForTest(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	(&iosSigningCertificateResource{}).Schema(t.Context(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema diagnostics: %+v", resp.Diagnostics)
	}

	return resp.Schema
}
