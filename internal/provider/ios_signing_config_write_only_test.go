// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
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

// These tests cover `provisioning_profiles_wo` +
// `provisioning_profiles_wo_version` on circleci_ios_signing_config
// (ios_signing_config_write_only.go).
//
// Every case that puts `provisioning_profiles_wo` in a configuration declares a
// minimum Terraform version through writeOnlySupported() — shared with the
// environment variable and webhook write-only tests — because write-only
// attributes are a 1.11 feature and without the check the failure on an older CLI
// is an opaque "Unsupported argument" rather than a skip.

const iosSigningConfigWriteOnlyBlob = "bW9iaWxlcHJvdmlzaW9uLWNvbnRlbnQ="

// --- configurations -----------------------------------------------------------

// iosSigningConfigWriteOnlyCert is the certificate every configuration below
// pairs with. It is on the state-backed path deliberately: the point of these
// tests is the profile list, and holding the certificate constant is what makes
// the two profile spellings comparable.
func iosSigningConfigWriteOnlyCert() string {
	return fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "cert" {
  organization_id      = %[1]q
  file_name            = "distribution.p12"
  certificate_blob     = "cDEy...cert..."
  certificate_password = "secret"
}
`, iosSigningTestOrgID)
}

// iosSigningConfigStatefulProfilesConfig supplies the profile through
// `provisioning_profiles`.
func iosSigningConfigStatefulProfilesConfig(host, fileName, blob string) string {
	return iosSigningProviderConfig(host) + iosSigningConfigWriteOnlyCert() + fmt.Sprintf(`
resource "circleci_ios_signing_config" "test" {
  organization_id = %[1]q
  name            = "release-config"
  certificate_id  = circleci_ios_signing_certificate.cert.id

  provisioning_profiles = [
    {
      file_name = %[2]q
      blob      = %[3]q
    },
  ]
}
`, iosSigningTestOrgID, fileName, blob)
}

// iosSigningConfigWriteOnlyProfilesConfig is the same configuration with the
// profile supplied through `provisioning_profiles_wo` plus a version. Everything
// else is identical, which is what makes the two comparable.
//
// The file name is fixed rather than a parameter: nothing here varies it, and the
// interesting rotation is of the blob.
func iosSigningConfigWriteOnlyProfilesConfig(host, blob string, version int) string {
	const fileName = "release.mobileprovision"

	return iosSigningProviderConfig(host) + iosSigningConfigWriteOnlyCert() + fmt.Sprintf(`
resource "circleci_ios_signing_config" "test" {
  organization_id = %[1]q
  name            = "release-config"
  certificate_id  = circleci_ios_signing_certificate.cert.id

  provisioning_profiles_wo = [
    {
      file_name = %[2]q
      blob      = %[3]q
    },
  ]
  provisioning_profiles_wo_version = %[4]d
}
`, iosSigningTestOrgID, fileName, blob, version)
}

// --- fake accessors -----------------------------------------------------------

// storedSigningConfigProfiles returns the profiles the fake holds for the one
// signing configuration it has, straight out of the create body it received.
// Asserting on these is asserting on the request, which is the point: the two
// spellings must reach the API identically.
//
// It reports every configuration's profiles keyed by name so a test does not have
// to know the minted id, which depends on how many certificates were created
// first.
func (a *iosSigningFakeAPI) storedSigningConfigProfiles(name string) []iosSigningFakeProfile {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, cfg := range a.configs {
		if cfg.Name == name {
			return append([]iosSigningFakeProfile(nil), cfg.Profiles...)
		}
	}

	return nil
}

// storedSigningConfigCount reports how many signing configurations the fake holds.
func (a *iosSigningFakeAPI) storedSigningConfigCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return len(a.configs)
}

// snapshotSigningConfigProfiles reads what the fake received as a step check,
// which runs while the configuration still exists. Reading it after
// resource.UnitTest returns would always find nothing, because the harness
// destroys everything on the way out — indistinguishable from the profiles never
// having been sent, which is precisely what these tests are looking for.
func snapshotSigningConfigProfiles(api *iosSigningFakeAPI, name string, into *[]iosSigningFakeProfile) func(*terraform.State) error {
	return func(*terraform.State) error {
		*into = api.storedSigningConfigProfiles(name)

		return nil
	}
}

// --- both names reach the same request ----------------------------------------

// TestIOSSigningConfigWriteOnly_SameRequestAsProvisioningProfiles proves
// `provisioning_profiles` and `provisioning_profiles_wo` are two spellings of one
// argument: the same file name and the same blob, in the same create body. The two
// paths run against separate fakes and the received profiles are compared, so a
// difference in either field fails.
func TestIOSSigningConfigWriteOnly_SameRequestAsProvisioningProfiles(t *testing.T) {
	observed := map[string][]iosSigningFakeProfile{}

	for name, config := range map[string]func(host string) string{
		"provisioning_profiles": func(host string) string {
			return iosSigningConfigStatefulProfilesConfig(host, "release.mobileprovision", iosSigningConfigWriteOnlyBlob)
		},
		"provisioning_profiles_wo": func(host string) string {
			return iosSigningConfigWriteOnlyProfilesConfig(host, iosSigningConfigWriteOnlyBlob, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			api := newIOSSigningFakeAPI(t)

			var received []iosSigningFakeProfile

			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   writeOnlySupported(),
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: config(api.URL()),
					Check:  snapshotSigningConfigProfiles(api, "release-config", &received),
					ConfigStateChecks: []statecheck.StateCheck{
						// The write-only attribute is never persisted, whichever name was
						// used: the framework nulls it in plan and state.
						statecheck.ExpectKnownValue(
							"circleci_ios_signing_config.test",
							tfjsonpath.New("provisioning_profiles_wo"),
							knownvalue.Null(),
						),
					},
				}},
			})

			observed[name] = received
		})
	}

	want := []iosSigningFakeProfile{{FileName: "release.mobileprovision", Blob: iosSigningConfigWriteOnlyBlob}}

	if fmt.Sprint(observed["provisioning_profiles"]) != fmt.Sprint(want) {
		t.Fatalf("the provisioning_profiles path sent %v, want %v", observed["provisioning_profiles"], want)
	}
	if fmt.Sprint(observed["provisioning_profiles_wo"]) != fmt.Sprint(observed["provisioning_profiles"]) {
		t.Errorf("provisioning_profiles_wo reached the API differently from provisioning_profiles:\n"+
			"  provisioning_profiles_wo: %v\n  provisioning_profiles:    %v",
			observed["provisioning_profiles_wo"], observed["provisioning_profiles"])
	}
}

// TestIOSSigningConfigWriteOnly_ProfilesAreNullInState covers the other half of
// the trade the documentation describes: on the write-only path neither the blob
// nor the file name nor anything derived from either is in state, while the
// version is.
//
// `file_name` being null is the part worth pinning. It is not a design choice:
// every child of a write-only nested attribute must itself be write-only (see
// ios_signing_config_write_only.go), so the file name goes with the blob. Nothing
// depends on it being there, and the empty plan in the second step is what proves
// it — Read has no route that reports a profile's content, and the list response
// reports only names, which the resource deliberately does not fold back in.
func TestIOSSigningConfigWriteOnly_ProfilesAreNullInState(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	config := iosSigningConfigWriteOnlyProfilesConfig(api.URL(), iosSigningConfigWriteOnlyBlob, 3)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_ios_signing_config.test",
						tfjsonpath.New("provisioning_profiles_wo"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"circleci_ios_signing_config.test",
						tfjsonpath.New("provisioning_profiles"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"circleci_ios_signing_config.test",
						tfjsonpath.New("provisioning_profiles_wo_version"),
						knownvalue.Int64Exact(3),
					),
					// The computed attributes still come back, so losing `file_name` from
					// state costs nothing that was readable: these are derived from the
					// referenced certificate, not from a profile.
					statecheck.ExpectKnownValue(
						"circleci_ios_signing_config.test",
						tfjsonpath.New("certificate_file_name"),
						knownvalue.StringExact("distribution.p12"),
					),
					statecheck.ExpectKnownValue(
						"circleci_ios_signing_config.test",
						tfjsonpath.New("certificate_type"),
						knownvalue.StringExact("distribution"),
					),
				},
			},
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

					return iosSigningTestOrgID + "/" + res.Primary.Attributes["id"], nil
				},
				ImportStateVerify: true,
				// Both lists are excluded, but for different reasons: neither can be
				// recovered on import, and on this path the prior state has nothing in
				// either of them to compare against anyway.
				ImportStateVerifyIgnore: []string{
					"provisioning_profiles",
					"provisioning_profiles_wo",
					"provisioning_profiles_wo_version",
				},
			},
		},
	})
}

// --- rotation -----------------------------------------------------------------

// TestIOSSigningConfigWriteOnly_RotationNeedsAVersionBump covers both halves of
// the version contract: a changed `provisioning_profiles_wo` alone is invisible to
// Terraform and is therefore not sent, and bumping
// `provisioning_profiles_wo_version` is what sends it.
//
// The middle step is the one worth having. It fails if the resource ever grows
// something that leaks the profiles into state, because then the change alone
// would produce a diff and the version would be pointless.
//
// The bump replaces rather than updates, because there is no update route for a
// signing configuration — the same behaviour `provisioning_profiles` already had.
// One version covers the whole list for the same reason: the list is one unit.
func TestIOSSigningConfigWriteOnly_RotationNeedsAVersionBump(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	const renewed = "cmVuZXdlZC1wcm9maWxl"

	var rotated []iosSigningFakeProfile
	var firstID string

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: iosSigningConfigWriteOnlyProfilesConfig(api.URL(), iosSigningConfigWriteOnlyBlob, 1),
				Check: func(state *terraform.State) error {
					firstID = state.RootModule().Resources["circleci_ios_signing_config.test"].Primary.Attributes["id"]

					return nil
				},
			},
			{
				// A renewed profile, same version: no diff, so no request.
				Config:   iosSigningConfigWriteOnlyProfilesConfig(api.URL(), renewed, 1),
				PlanOnly: true,
			},
			{
				Config: iosSigningConfigWriteOnlyProfilesConfig(api.URL(), renewed, 2),
				Check:  snapshotSigningConfigProfiles(api, "release-config", &rotated),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_ios_signing_config.test",
							plancheck.ResourceActionDestroyBeforeCreate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_ios_signing_config.test",
						tfjsonpath.New("provisioning_profiles_wo_version"),
						knownvalue.Int64Exact(2),
					),
				},
			},
		},
	})

	if len(rotated) != 1 || rotated[0].Blob != renewed {
		t.Errorf("the rotation sent %v, want a single profile with blob %q — a rotation that does not "+
			"reach the wire leaves the old profile live while state claims otherwise", rotated, renewed)
	}
	if firstID == "" {
		t.Error("the first step recorded no id, so the replacement below proves nothing")
	}
}

// --- attribute combinations ---------------------------------------------------

// TestIOSSigningConfigWriteOnly_ProfileAttributeCombinations covers
// iosSigningProfilesConfigValidator and the AlsoRequires/AtLeast/SizeAtLeast
// validators.
//
// ExactlyOneOf rather than Conflicting, unlike circleci_otel_exporter's headers: a
// signing configuration with no provisioning profiles cannot sign anything, which
// is why `provisioning_profiles` was Required to begin with. Making it Optional
// only moved where the refusal comes from, and the "neither set" case below is
// what asserts that.
func TestIOSSigningConfigWriteOnly_ProfileAttributeCombinations(t *testing.T) {
	body := func(profiles string) string {
		return fmt.Sprintf(`
resource "circleci_ios_signing_config" "test" {
  organization_id = %[1]q
  name            = "release-config"
  certificate_id  = "11111111-1111-1111-1111-111111111111"
%[2]s
}
`, iosSigningTestOrgID, profiles)
	}

	profileList := func(attribute string) string {
		return "  " + attribute + " = [{ file_name = \"release.mobileprovision\", blob = \"" +
			iosSigningConfigWriteOnlyBlob + "\" }]"
	}

	// The two halves of ExactlyOneOf report the same detail under different titles
	// — "Missing Attribute Configuration" when neither is set, "Invalid Attribute
	// Combination" when both are — so the detail is what these match on. The
	// whitespace is a pattern rather than a literal because these attribute names
	// are long enough that Terraform wraps the detail onto a second line.
	exactlyOne := regexp.MustCompile(
		`(?s)Exactly one of these attributes must be configured:\s+\[provisioning_profiles,provisioning_profiles_wo\]`,
	)

	tests := map[string]struct {
		profiles string
		error    *regexp.Regexp
	}{
		"neither set": {
			profiles: ``,
			error:    exactlyOne,
		},
		"both set": {
			profiles: profileList("provisioning_profiles") + "\n" +
				profileList("provisioning_profiles_wo") + "\n  provisioning_profiles_wo_version = 1",
			error: exactlyOne,
		},
		"version without provisioning_profiles_wo": {
			profiles: profileList("provisioning_profiles") + "\n  provisioning_profiles_wo_version = 1",
			error:    regexp.MustCompile(`Invalid Attribute Combination`),
		},
		"provisioning_profiles_wo without a version": {
			// The silent-no-op case: without the reverse AlsoRequires this validates
			// fine, is written once, and can then never be rotated, with Terraform
			// reporting "no changes" on every later edit.
			profiles: profileList("provisioning_profiles_wo"),
			error:    regexp.MustCompile(`Invalid Attribute Combination`),
		},
		"version below one": {
			profiles: profileList("provisioning_profiles_wo") + "\n  provisioning_profiles_wo_version = 0",
			error:    regexp.MustCompile(`Invalid Attribute Value`),
		},
		"empty write-only list": {
			profiles: "  provisioning_profiles_wo = []\n  provisioning_profiles_wo_version = 1",
			error:    regexp.MustCompile(`(?s)list must contain at least 1 element`),
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			api := newIOSSigningFakeAPI(t)

			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   writeOnlySupported(),
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      iosSigningProviderConfig(api.URL()) + body(testCase.profiles),
					PlanOnly:    true,
					ExpectError: testCase.error,
				}},
			})

			// Validation happens before anything is written.
			if got := api.storedSigningConfigCount(); got != 0 {
				t.Errorf("the provider created %d signing configuration(s) for a configuration that "+
					"fails validation, want 0", got)
			}
		})
	}
}

// --- the guard --------------------------------------------------------------

// TestIOSSigningConfigWriteOnly_GuardsAgainstMissingProfiles drives Create
// directly, because a plan cannot reach this state: iosSigningProfilesConfigValidator
// rejects a configuration with neither list at validation time. The guard exists
// for the gap after that — a write-only value that resolved to nothing by apply —
// where the alternative is a signing configuration created with no profiles, which
// CircleCI accepts and which can then sign nothing.
//
// Update is not exercised: the resource has none. Every attribute carries
// RequiresReplace because there is no update route for a signing configuration, so
// iosSigningConfigResource.Update is an empty method and the vault#2900 hazard has
// nowhere to occur here.
//
// The client points at a live fake rather than being nil, because
// requireCloud dereferences it. That the fake holds nothing afterwards is what
// proves no request was issued.
func TestIOSSigningConfigWriteOnly_GuardsAgainstMissingProfiles(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	schema := iosSigningConfigResourceSchemaForTest(t)

	// Both lists null, the version set: the shape of a signing configuration
	// already managed through the write-only path whose profiles have gone missing.
	model := iosSigningConfigResourceModel{
		Id:                            types.StringValue("00000001-1111-2222-3333-444444444444"),
		OrganizationId:                types.StringValue(iosSigningTestOrgID),
		OrgId:                         types.StringValue(iosSigningTestOrgID),
		Name:                          types.StringValue("release-config"),
		CertificateId:                 types.StringValue("11111111-1111-1111-1111-111111111111"),
		CertificateFileName:           types.StringValue("distribution.p12"),
		CertificateType:               types.StringValue("distribution"),
		ProvisioningProfilesWOVersion: types.Int64Value(1),
	}

	config := configForTest(t, schema, model)

	createResp := fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	(&iosSigningConfigResource{
		client: circleci.New(circleci.Config{Host: api.URL(), Token: "fake"}),
	}).Create(t.Context(), fwresource.CreateRequest{
		Config: config,
		Plan:   tfsdk.Plan{Schema: schema, Raw: config.Raw},
	}, &createResp)

	reported := fmt.Sprint(createResp.Diagnostics)

	if !regexp.MustCompile(`Missing iOS provisioning profiles`).MatchString(reported) {
		t.Fatalf("Create reported no missing-profiles error; a signing configuration that can sign "+
			"nothing would have been created: %s", reported)
	}
	if !regexp.MustCompile(`provisioning_profiles_wo_version`).MatchString(reported) {
		t.Errorf("Create's error does not explain the write-only path: %s", reported)
	}
	if got := api.storedSigningConfigCount(); got != 0 {
		t.Errorf("the fake holds %d signing configuration(s); the guard must return before any "+
			"request is issued", got)
	}
}

// --- helpers ----------------------------------------------------------------

func iosSigningConfigResourceSchemaForTest(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	(&iosSigningConfigResource{}).Schema(t.Context(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema diagnostics: %+v", resp.Diagnostics)
	}

	// The load-bearing assertion for this whole file, and the reason the write-only
	// list is a parallel attribute rather than a write-only `blob` inside the
	// existing one. ListNestedAttribute.ValidateImplementation rejects a write-only
	// nested attribute whose children are not all write-only
	// (fwschema.InvalidWriteOnlyNestedAttributeDiag), and Terraform runs this during
	// GetProviderSchema, so getting it wrong breaks every operation rather than one
	// path. Asserting it here names the failure instead of leaving it to show up as
	// an unrelated-looking error in an acceptance test.
	if diags := resp.Schema.ValidateImplementation(t.Context()); diags.HasError() {
		t.Fatalf("the schema does not validate: %+v", diags)
	}

	return resp.Schema
}
