// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// This file is the test half of org_id_deprecation.go, for every resource and
// ephemeral resource that carries the `organization_id` / `org_id` pair.
// TestProjectResourceUnit_SwitchingOrgAttributeDoesNotReplace covers
// circleci_project, where the pattern was worked out; these cover the rest.
//
// The assertion that matters is a no-op plan across the switch, and it is
// table-driven on purpose. The naive implementation of this deprecation is
// destructive — remove `organization_id` from a configuration, Terraform plans it
// as null, and the resource is destroyed and recreated — so what has to be proven
// is not "the pattern works" in the abstract but "the pattern works on this
// resource". A table makes it obvious that all of them get exactly the same
// treatment, and that adding another resource means adding a case rather than
// copying seventy lines. Each case runs as its own subtest, so a regression names
// the resource that broke.
//
// Negative cases follow, covering the things the no-op assertion cannot:
//
//   - That a genuine organization change still replaces, on circleci_group, which
//     stands in for the resources built with replaces: true.
//   - That a genuine organization change is instead an in-place update on
//     circleci_runner_resource_class, the one resource built with replaces: false.
//   - That both names or neither name is a configuration error, on circleci_group
//     and on both ephemeral resources (which use a different validator).
//
// Except where noted, these behaviours come from shared code, so testing them once
// is testing them everywhere.

// orgAttributeCase describes one resource's migration from `organization_id` to
// `org_id`.
type orgAttributeCase struct {
	// address is the resource address in the rendered configuration.
	address string
	// orgID is the organization the configuration names, and the value both
	// attributes must hold in state.
	orgID string
	// factories is the provider factory map the case needs. Several of these
	// resources are only registered on the governance test provider.
	factories map[string]func() (tfprotov6.ProviderServer, error)
	// setup starts the resource's own fake and returns a renderer that puts the
	// organization under the named attribute. The fakes are per-subtest, so this
	// is a function of *testing.T rather than a prebuilt string.
	setup func(t *testing.T) func(attr string) string
}

// orgAttributeCases returns one case per resource. Each reuses the fake the
// resource's existing tests already drive, so these tests need no credentials.
func orgAttributeCases() map[string]orgAttributeCase {
	return map[string]orgAttributeCase{
		"circleci_group": {
			address:   "circleci_group.test",
			orgID:     testGroupOrgID,
			factories: testAccProtoV6ProviderFactories,
			setup: func(t *testing.T) func(string) string {
				_, host := newMockGroupAPI(t)

				return func(attr string) string {
					return testAccGroupProviderConfig(host, "cloud") + fmt.Sprintf(`
resource "circleci_group" "test" {
  %s   = %q
  name = "platform"
}
`, attr, testGroupOrgID)
				}
			},
		},

		"circleci_context": {
			address:   "circleci_context.test",
			orgID:     contextUnitOrgID,
			factories: testAccProtoV6ProviderFactories,
			setup: func(t *testing.T) func(string) string {
				_, host := newContextFakeAPI(t)

				return func(attr string) string {
					return contextFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_context" "test" {
  %s   = %q
  name = "build"
}
`, attr, contextUnitOrgID)
				}
			},
		},

		"circleci_project_group": {
			address:   "circleci_project_group.test",
			orgID:     testPGOrgID,
			factories: testAccProtoV6ProviderFactories,
			setup: func(t *testing.T) func(string) string {
				_, host := newMockProjectGroupAPI(t)

				return func(attr string) string {
					return testAccMembershipProviderConfig(host, "cloud") + fmt.Sprintf(`
resource "circleci_project_group" "test" {
  %s         = %q
  project_id = %q
  group_id   = %q
  role       = %q
}
`, attr, testPGOrgID, testPGProjectID, testPGGroupA, circleci.ProjectRoleViewer)
				}
			},
		},

		"circleci_organization_settings": {
			address:   "circleci_organization_settings.test",
			orgID:     testOrgSettingsOrgID,
			factories: testAccProtoV6ProviderFactories,
			setup: func(t *testing.T) func(string) string {
				srv, _ := newFakeOrgSettingsAPI(t)

				return func(attr string) string {
					return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}

resource "circleci_organization_settings" "test" {
  %s                  = %q
  enable_private_orbs = true
}
`, srv.URL, attr, testOrgSettingsOrgID)
				}
			},
		},

		"circleci_oidc_custom_claims": {
			address:   "circleci_oidc_custom_claims.test",
			orgID:     testOIDCOrgID,
			factories: governanceProviderFactories,
			setup: func(t *testing.T) func(string) string {
				srv := newOIDCClaimsServer(t, newOIDCClaimsAPI())

				return func(attr string) string {
					return governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_oidc_custom_claims" "test" {
  %s  = %q
  ttl = "1h"
}
`, attr, testOIDCOrgID)
				}
			},
		},

		"circleci_otel_exporter": {
			address:   "circleci_otel_exporter.test",
			orgID:     testOTelOrg,
			factories: governanceProviderFactories,
			setup: func(t *testing.T) func(string) string {
				srv := newOTelServer(t, newOTelAPI())

				return func(attr string) string {
					return governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_otel_exporter" "test" {
  %s       = %q
  endpoint = "otel.example.com:4317"
  protocol = %q
}
`, attr, testOTelOrg, circleci.OTelProtocolGRPC)
				}
			},
		},

		"circleci_orb_namespace": {
			address:   "circleci_orb_namespace.test",
			orgID:     orbTestOrgID,
			factories: testAccProtoV6ProviderFactories,
			setup: func(t *testing.T) func(string) string {
				api := newOrbFakeAPI(t)

				return func(attr string) string {
					return orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_namespace" "test" {
  name = "acme"
  %s   = %q
}
`, attr, orbTestOrgID)
				}
			},
		},

		"circleci_runner_token": {
			address:   "circleci_runner_token.test",
			orgID:     orgAttributeRunnerTokenOrgID,
			factories: testAccProtoV6ProviderFactories,
			setup: func(t *testing.T) func(string) string {
				api := newRunnerFakeAPI(t)

				const token = `{
					"id": "` + orgAttributeRunnerTokenID + `",
					"nickname": "org-attr-token",
					"resource_class": "acme-ns/acme-rc",
					"created_at": "2026-01-01T00:00:00Z"
				}`

				// Create discloses the secret; a list never does. The delete has no
				// registered response, which the fake answers with an empty 200 —
				// what the real API returns.
				api.respond("POST", "/api/v3/runner/token",
					`{"id":"`+orgAttributeRunnerTokenID+`","nickname":"org-attr-token",`+
						`"resource_class":"acme-ns/acme-rc","token":"secret-token-value",`+
						`"created_at":"2026-01-01T00:00:00Z"}`)
				api.respond("GET", "/api/v3/runner/token", `{"items":[`+token+`]}`)

				return func(attr string) string {
					return runnerProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_runner_token" "test" {
  %s             = %q
  resource_class = "acme-ns/acme-rc"
  nickname       = "org-attr-token"
}
`, attr, orgAttributeRunnerTokenOrgID)
				}
			},
		},

		// The only case whose resource passes replaces: false — changing the
		// organization on a runner resource class has never forced replacement. The
		// no-op assertion below is identical regardless, which is the point: the
		// switch between attribute names must be invisible either way. What differs
		// is what a *genuine* organization change does, covered separately by
		// TestRunnerResourceClassResourceUnit_ChangingOrgUpdatesInPlace.
		"circleci_runner_resource_class": {
			address:   "circleci_runner_resource_class.test",
			orgID:     orgAttributeResourceClassOrgID,
			factories: testAccProtoV6ProviderFactories,
			setup: func(t *testing.T) func(string) string {
				api := newRunnerFakeAPI(t)
				newRunnerResourceClassFakeResponses(api)

				return func(attr string) string {
					return runnerResourceClassOrgConfig(api.URL(), attr, orgAttributeResourceClassOrgID)
				}
			},
		},

		"circleci_audit_log_config": {
			address:   "circleci_audit_log_config.test",
			orgID:     testAuditLogConfigOrg,
			factories: testAccProtoV6ProviderFactories,
			setup: func(t *testing.T) func(string) string {
				srv := newAuditLogConfigServer(t, &auditLogConfigAPI{})

				return func(attr string) string {
					return auditLogConfigProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_audit_log_config" "test" {
  %s          = %q
  target_type = %q
  arn         = "arn:aws:iam::123456789012:role/circleci-audit-logs"
  bucket_name = "acme-audit-logs"
  region      = "us-east-1"
}
`, attr, testAuditLogConfigOrg, circleci.AuditLogTargetTypeS3)
				}
			},
		},

		"circleci_ios_signing_certificate": {
			address:   "circleci_ios_signing_certificate.test",
			orgID:     iosSigningTestOrgID,
			factories: testAccProtoV6ProviderFactories,
			setup: func(t *testing.T) func(string) string {
				api := newIOSSigningFakeAPI(t)

				return func(attr string) string {
					return iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "test" {
  %s                   = %q
  file_name            = "distribution.p12"
  certificate_blob     = "cDEyLWJhc2U2NC1jb250ZW50LWRpc3RyaWJ1dGlvbg=="
  certificate_password = "s3cret-password"
}
`, attr, iosSigningTestOrgID)
				}
			},
		},

		// The signing configuration needs a certificate to point at, and the
		// certificate carries the same attribute pair, so both are migrated
		// together. That makes the assertion stronger rather than weaker: if the
		// certificate were replaced, its new id would force the configuration to be
		// replaced too, and the no-op check below would catch it.
		"circleci_ios_signing_config": {
			address:   "circleci_ios_signing_config.test",
			orgID:     iosSigningTestOrgID,
			factories: testAccProtoV6ProviderFactories,
			setup: func(t *testing.T) func(string) string {
				api := newIOSSigningFakeAPI(t)

				return func(attr string) string {
					return iosSigningProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "cert" {
  %[1]s                = %[2]q
  file_name            = "distribution.p12"
  certificate_blob     = "cDEyLWJhc2U2NC1jb250ZW50LWRpc3RyaWJ1dGlvbg=="
  certificate_password = "s3cret-password"
}

resource "circleci_ios_signing_config" "test" {
  %[1]s          = %[2]q
  name           = "release-config"
  certificate_id = circleci_ios_signing_certificate.cert.id

  provisioning_profiles = [
    {
      file_name = "release.mobileprovision"
      blob      = "bW9iaWxlcHJvdmlzaW9uLWNvbnRlbnQ="
    },
  ]
}
`, attr, iosSigningTestOrgID)
				}
			},
		},
	}
}

const (
	// orgAttributeRunnerTokenOrgID is the organization circleci_runner_token's
	// case names. The runner fake ignores it, but the resource must still send it.
	orgAttributeRunnerTokenOrgID = "44444444-4444-4444-4444-444444444444"
	// orgAttributeRunnerTokenID is the token id the runner fake mints.
	orgAttributeRunnerTokenID = "55555555-6666-7777-8888-999999999999"

	// orgAttributeResourceClassOrgID is the organization
	// circleci_runner_resource_class's cases name. Both attribute names carry the
	// runner API's UUID-only shape check, so this has to be a UUID even though the
	// fake never looks at it.
	orgAttributeResourceClassOrgID = "66666666-6666-6666-6666-666666666666"
	// orgAttributeResourceClassOtherOrgID is a second organization, for the
	// genuine-change test.
	orgAttributeResourceClassOtherOrgID = "77777777-7777-7777-7777-777777777777"
	// orgAttributeResourceClassName is the "namespace/name" resource class the
	// fake reports.
	orgAttributeResourceClassName = "acme-ns/acme-rc"
)

// newRunnerResourceClassFakeResponses registers the create and list responses
// circleci_runner_resource_class needs. The runner API reports no organization on
// a resource class — the owning organization is derived from the namespace — which
// is exactly why the provider has to mirror the two attribute names itself rather
// than refreshing either from the API.
func newRunnerResourceClassFakeResponses(api *runnerFakeAPI) {
	const body = `{
		"id": "88888888-8888-8888-8888-888888888888",
		"resource_class": "` + orgAttributeResourceClassName + `",
		"description": "org attribute migration"
	}`

	api.respond("POST", "/api/v3/runner/resource", body)
	api.respond("GET", "/api/v3/runner/resource", `{"items":[`+body+`]}`)
}

// runnerResourceClassOrgConfig renders a circleci_runner_resource_class naming
// its organization under attr.
func runnerResourceClassOrgConfig(runnerHost, attr, orgID string) string {
	return runnerProviderConfig(runnerHost) + fmt.Sprintf(`
resource "circleci_runner_resource_class" "test" {
  %s             = %q
  resource_class = %q
  description    = "org attribute migration"
}
`, attr, orgID, orgAttributeResourceClassName)
}

// TestOrgIDDeprecation_SwitchingOrgAttributeDoesNotReplace is the test the
// organization_id -> org_id deprecation lives or dies by, for every resource
// where changing the organization replaces the resource.
//
// Introducing org_id naively — as a second Optional attribute — means that a
// practitioner following our own deprecation advice removes organization_id from
// their configuration, Terraform plans it as null, sees a change, and destroys and
// recreates the resource. For most of these that means losing something that
// cannot be recreated by Terraform at all: a group's membership, an audit log
// config's delivery history, a signing certificate whose private key the API
// never returns.
//
// Two things prevent it, and this test is what proves they work together:
// Computed retains the prior value instead of planning null, and
// RequiresReplaceIfConfigured skips replacement when the configuration value is
// null while still replacing on a genuine organization change.
//
// The assertion is a no-op plan, not merely "not a replacement". Anything else —
// an in-place update, a drift diff — would mean the two names are not truly
// interchangeable and practitioners would see churn on upgrade.
func TestOrgIDDeprecation_SwitchingOrgAttributeDoesNotReplace(t *testing.T) {
	for name, testCase := range orgAttributeCases() {
		t.Run(name, func(t *testing.T) {
			config := testCase.setup(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testCase.factories,
				Steps: []resource.TestStep{
					{
						// The state a practitioner already has today.
						Config: config("organization_id"),
						ConfigStateChecks: []statecheck.StateCheck{
							statecheck.ExpectKnownValue(
								testCase.address,
								tfjsonpath.New("organization_id"),
								knownvalue.StringExact(testCase.orgID),
							),
							// Both are populated, so the value is available under
							// either name before any migration happens.
							statecheck.ExpectKnownValue(
								testCase.address,
								tfjsonpath.New("org_id"),
								knownvalue.StringExact(testCase.orgID),
							),
						},
					},
					{
						// The migration: same organization, new attribute name.
						Config: config("org_id"),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{
								plancheck.ExpectResourceAction(testCase.address, plancheck.ResourceActionNoop),
							},
						},
						ConfigStateChecks: []statecheck.StateCheck{
							statecheck.ExpectKnownValue(
								testCase.address,
								tfjsonpath.New("org_id"),
								knownvalue.StringExact(testCase.orgID),
							),
							statecheck.ExpectKnownValue(
								testCase.address,
								tfjsonpath.New("organization_id"),
								knownvalue.StringExact(testCase.orgID),
							),
						},
					},
					{
						// And back again, so the deprecation is not a one-way door.
						Config: config("organization_id"),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{
								plancheck.ExpectResourceAction(testCase.address, plancheck.ResourceActionNoop),
							},
						},
					},
				},
			})
		})
	}
}

// TestGroupResourceUnit_ChangingOrgStillReplaces is the other half of the safety
// above: making the switch a no-op must not have disabled replacement for a real
// organization change. CircleCI has no route that moves a group between
// organizations, so this has to be a destroy and create.
//
// circleci_group stands in for the whole set. The behaviour comes from
// RequiresReplaceIfConfigured in org_id_deprecation.go, which every one of these
// resources gets from the same builder.
func TestGroupResourceUnit_ChangingOrgStillReplaces(t *testing.T) {
	_, host := newMockGroupAPI(t)

	config := func(orgID string) string {
		return testAccGroupProviderConfig(host, "cloud") + fmt.Sprintf(`
resource "circleci_group" "test" {
  org_id = %q
  name   = "platform"
}
`, orgID)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config(testGroupOrgID)},
			{
				Config: config("99999999-9999-9999-9999-999999999999"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_group.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			},
		},
	})
}

// TestRunnerResourceClassResourceUnit_ChangingOrgUpdatesInPlace is the mirror
// image of TestGroupResourceUnit_ChangingOrgStillReplaces, and the only test of
// the replaces: false branch of the attribute builders.
//
// circleci_runner_resource_class is the one resource carrying this pair whose
// organization_id has never forced replacement, so it is the one resource where
// the deprecation must NOT introduce one: a practitioner who changes the
// organization gets the in-place update they got before. Getting this wrong is
// not a cosmetic difference — the replacement it would otherwise plan destroys the
// resource class, and with it every runner token issued against it.
//
// Asserting ResourceActionUpdate rather than merely "not a replacement" also pins
// down that both names end up agreeing in state afterwards: an in-place update
// whose result disagreed with the plan would fail the apply outright.
func TestRunnerResourceClassResourceUnit_ChangingOrgUpdatesInPlace(t *testing.T) {
	for _, attr := range []string{"organization_id", "org_id"} {
		t.Run(attr, func(t *testing.T) {
			api := newRunnerFakeAPI(t)
			newRunnerResourceClassFakeResponses(api)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{Config: runnerResourceClassOrgConfig(api.URL(), attr, orgAttributeResourceClassOrgID)},
					{
						Config: runnerResourceClassOrgConfig(api.URL(), attr, orgAttributeResourceClassOtherOrgID),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{
								plancheck.ExpectResourceAction(
									"circleci_runner_resource_class.test",
									plancheck.ResourceActionUpdate,
								),
							},
						},
						ConfigStateChecks: []statecheck.StateCheck{
							statecheck.ExpectKnownValue(
								"circleci_runner_resource_class.test",
								tfjsonpath.New("organization_id"),
								knownvalue.StringExact(orgAttributeResourceClassOtherOrgID),
							),
							statecheck.ExpectKnownValue(
								"circleci_runner_resource_class.test",
								tfjsonpath.New("org_id"),
								knownvalue.StringExact(orgAttributeResourceClassOtherOrgID),
							),
						},
					},
				},
			})
		})
	}
}

// --- ephemeral resources -----------------------------------------------------
//
// The two ephemeral resources cannot be tested the way the managed resources
// above are. An ephemeral resource holds no state: there is no prior value for
// Computed to retain, nothing to replace, and no plan to assert is a no-op — which
// is exactly why org_id_deprecation.go gives them plain Optional attributes. What
// is left to prove is that the new name reaches the API identically to the old
// one, and that the pair is still exactly-one.
//
// "Identically" is asserted on the requests the fake received rather than on the
// ephemeral resource's result, because an ephemeral result is never written to
// state and so cannot be read by a statecheck — see the top of
// usage_export_ephemeral_resource_test.go for why the echo provider, which exists
// to work around that, is unusable in this repo.

// newUsageExportOrgRecordingAPI is a usage export fake that records the
// organization each request was addressed to, and reports a job that is already
// complete so the poll loop settles without waiting.
//
// newUsageExportFakeAPI, which the rest of the usage export tests use, counts
// requests but discards their paths — and the path is where the organization
// appears on these routes, which is the whole assertion here.
func newUsageExportOrgRecordingAPI(t *testing.T) (host string, recorded func() []string) {
	t.Helper()

	var mu sync.Mutex
	var seen []string

	record := func(orgID string) {
		mu.Lock()
		defer mu.Unlock()

		seen = append(seen, orgID)
	}

	const job = `{"usage_export_job_id":"11111111-1111-1111-1111-111111111111",` +
		`"state":"completed","download_urls":["https://example.com/a.csv.gz"],"error_reason":""}`

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/organizations/{org_id}/usage_export_job", func(w http.ResponseWriter, r *http.Request) {
		record("POST " + r.PathValue("org_id"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, job)
	})
	mux.HandleFunc("GET /api/v2/organizations/{org_id}/usage_export_job/{id}", func(w http.ResponseWriter, r *http.Request) {
		record("GET " + r.PathValue("org_id"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, job)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()

		return append([]string(nil), seen...)
	}
}

// TestOrgIDDeprecation_EphemeralRunnerTokenAcceptsEitherName proves
// circleci_ephemeral_runner_token sends the same organization to the API under
// either attribute name.
//
// The create request bodies are compared byte for byte between the two runs, not
// just the organization field: an ephemeral resource's result is unobservable, so
// the request is the only place the two configurations can be shown to be
// equivalent, and any difference at all there would be a difference in behaviour.
func TestOrgIDDeprecation_EphemeralRunnerTokenAcceptsEitherName(t *testing.T) {
	bodies := map[string]string{}

	for _, attr := range []string{"organization_id", "org_id"} {
		t.Run(attr, func(t *testing.T) {
			api := newRunnerFakeAPI(t)
			api.respond("POST", "/api/v3/runner/token",
				`{"id":"`+orgAttributeRunnerTokenID+`","nickname":"org-attr-token",`+
					`"resource_class":"acme-ns/acme-rc","token":"secret-token-value",`+
					`"created_at":"2026-01-01T00:00:00Z"}`)

			config := runnerProviderConfig(api.URL()) + fmt.Sprintf(`
ephemeral "circleci_ephemeral_runner_token" "this" {
  %s             = %q
  resource_class = "acme-ns/acme-rc"
  nickname       = "org-attr-token"
}
`, attr, orgAttributeRunnerTokenOrgID)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps:                    []resource.TestStep{{Config: config}},
			})

			create := api.firstRequest(t, "POST", "/api/v3/runner/token")

			var payload struct {
				OrganizationID string `json:"org_id"`
			}
			if err := json.Unmarshal([]byte(create.Body), &payload); err != nil {
				t.Fatalf("could not decode the create request body %q: %s", create.Body, err)
			}
			if payload.OrganizationID != orgAttributeRunnerTokenOrgID {
				t.Errorf("create request org_id = %q, want %q", payload.OrganizationID, orgAttributeRunnerTokenOrgID)
			}

			bodies[attr] = create.Body
		})
	}

	if bodies["organization_id"] != bodies["org_id"] {
		t.Errorf(
			"the two attribute names produced different create requests:\n  organization_id: %s\n  org_id:          %s",
			bodies["organization_id"], bodies["org_id"],
		)
	}
}

// TestOrgIDDeprecation_EphemeralUsageExportAcceptsEitherName is the same
// assertion for circleci_usage_export, where the organization travels in the URL
// path rather than a request body: both names must address the same organization
// on both the create and the status route.
func TestOrgIDDeprecation_EphemeralUsageExportAcceptsEitherName(t *testing.T) {
	const orgID = "22222222-2222-2222-2222-222222222222"

	requests := map[string][]string{}

	for _, attr := range []string{"organization_id", "org_id"} {
		t.Run(attr, func(t *testing.T) {
			host, recorded := newUsageExportOrgRecordingAPI(t)

			config := usageExportProviderConfig(host) + fmt.Sprintf(`
ephemeral "circleci_usage_export" "this" {
  %s    = %q
  start = "2024-01-01T00:00:00Z"
  end   = "2024-01-02T00:00:00Z"
}
`, attr, orgID)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps:                    []resource.TestStep{{Config: config}},
			})

			seen := recorded()
			if len(seen) == 0 {
				t.Fatal("no request reached the usage export fake; no job was ever created")
			}
			for _, request := range seen {
				if !strings.HasSuffix(request, orgID) {
					t.Errorf("request %q did not address organization %q (all: %v)", request, orgID, seen)
				}
			}

			requests[attr] = seen
		})
	}

	if !slices.Equal(requests["organization_id"], requests["org_id"]) {
		t.Errorf(
			"the two attribute names produced different requests:\n  organization_id: %v\n  org_id:          %v",
			requests["organization_id"], requests["org_id"],
		)
	}
}

// TestOrgIDDeprecation_EphemeralResourcesRequireExactlyOneOrgAttribute covers the
// two ways of getting the pair wrong on an ephemeral resource: both names, or
// neither. This is orgIDEphemeralConfigValidator rather than the resource
// validator the managed resources use, so it needs its own coverage.
func TestOrgIDDeprecation_EphemeralResourcesRequireExactlyOneOrgAttribute(t *testing.T) {
	const orgID = "22222222-2222-2222-2222-222222222222"

	configs := map[string]func(t *testing.T, orgAttributes string) string{
		"circleci_ephemeral_runner_token": func(t *testing.T, orgAttributes string) string {
			t.Helper()

			return runnerProviderConfig(newRunnerFakeAPI(t).URL()) + `
ephemeral "circleci_ephemeral_runner_token" "this" {
  ` + orgAttributes + `
  resource_class = "acme-ns/acme-rc"
  nickname       = "org-attr-token"
}
`
		},

		"circleci_usage_export": func(t *testing.T, orgAttributes string) string {
			t.Helper()

			host, _ := newUsageExportOrgRecordingAPI(t)

			return usageExportProviderConfig(host) + `
ephemeral "circleci_usage_export" "this" {
  ` + orgAttributes + `
  start = "2024-01-01T00:00:00Z"
  end   = "2024-01-02T00:00:00Z"
}
`
		},
	}

	orgAttributes := map[string]string{
		"both set":    fmt.Sprintf("organization_id = %[1]q\n  org_id          = %[1]q", orgID),
		"neither set": "",
	}

	for name, render := range configs {
		for combination, attributes := range orgAttributes {
			t.Run(name+"/"+combination, func(t *testing.T) {
				resource.UnitTest(t, resource.TestCase{
					ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
					Steps: []resource.TestStep{{
						Config:      render(t, attributes),
						ExpectError: regexp.MustCompile(`(?s)Invalid Attribute Combination|Missing Attribute Configuration`),
					}},
				})
			})
		}
	}
}

// TestGroupResourceUnit_RequiresExactlyOneOrgAttribute covers the two ways of
// getting the pair wrong: both names, or neither.
//
// Exactly one rather than at least one: both set would be ambiguous if they
// disagreed, and neither leaves the resource unaddressable. As above,
// circleci_group stands in for the set, because orgIDConfigValidator is shared.
func TestGroupResourceUnit_RequiresExactlyOneOrgAttribute(t *testing.T) {
	_, host := newMockGroupAPI(t)

	both := testAccGroupProviderConfig(host, "cloud") + fmt.Sprintf(`
resource "circleci_group" "test" {
  organization_id = %[1]q
  org_id          = %[1]q
  name            = "platform"
}
`, testGroupOrgID)

	neither := testAccGroupProviderConfig(host, "cloud") + `
resource "circleci_group" "test" {
  name = "platform"
}
`

	for name, config := range map[string]string{"both set": both, "neither set": neither} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      config,
					ExpectError: regexp.MustCompile(`(?s)Invalid Attribute Combination|Missing Attribute Configuration`),
				}},
			})
		})
	}
}
