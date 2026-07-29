// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// This file is the data source half of org_id_deprecation.go: every data source
// that reads an organization accepts it as either `organization_id` (deprecated)
// or `org_id`, and this proves it for each one.
//
// A data source has none of the replacement hazard that shapes the resource
// attributes — it is read fresh on every plan and persists nothing — so what has
// to be proven here is narrower, but it has to be proven per data source rather
// than once: the failure mode of this migration is a Read that still reads only
// one of the two model fields, which no amount of shared-code testing would
// catch. Hence the table, and hence one subtest per data source, named after the
// Terraform type so a regression names something a practitioner would recognise.
//
// Each case asserts four things:
//
//   - a read with `organization_id` works,
//   - a read with `org_id` produces the same result — the two data sources are
//     declared in one configuration and compared, so "the same" is asserted
//     rather than eyeballed across two runs,
//   - both names at once is a configuration error,
//   - neither name is a configuration error.
//
// The organization really reaching the API is not asserted case by case, because
// most of these fakes already enforce it: they carry the organization in the
// request path (audit logs, contexts, groups, group membership, project groups,
// GitHub App, organization settings) or fail the test outright when the query
// parameter is missing (OTLP exporters). Where a fake ignores the organization,
// the case says how it makes the routing observable instead — a recorded query
// parameter for the deploy and runner fakes, and an entity belonging to another
// organization for the iOS signing ones.

// orgIDDataSourceFixture is one data source's fake and the configuration that
// reads through it.
type orgIDDataSourceFixture struct {
	// providerConfig is the provider block pointed at the fake. It is separate
	// from the data source block because a configuration may only declare the
	// provider once, and every case declares its data source twice.
	providerConfig string
	// preamble is HCL that must exist before the read, for the fakes that can
	// only be seeded through the API. Usually empty.
	preamble string
	// orgID is the organization the fake serves.
	orgID string
	// block renders the data source under the given label, with orgAttributes as
	// its organization attribute lines — which may be one of the pair, both, or
	// neither.
	block func(label, orgAttributes string) string
	// routed asserts the organization reached the API under both names, for the
	// fakes that record their requests but do not require the organization. Nil
	// where the fake enforces it itself.
	routed func(t *testing.T)
}

// orgIDDataSourceCase describes one data source's migration to the attribute
// pair.
type orgIDDataSourceCase struct {
	// payload is the attribute whose value must not depend on which of the two
	// organization attribute names the configuration used.
	payload string
	// payloadCheck is what that attribute must hold. Defaults to
	// knownvalue.NotNull(), which is all most cases need: the interesting
	// assertion is that both names produce the same value.
	payloadCheck knownvalue.Check
	// setup starts the fake and returns the configuration to read through it.
	// The fakes are per-subtest, so this is a function of *testing.T.
	setup func(t *testing.T) orgIDDataSourceFixture
}

// orgIDDataSourceDecoyOrgID is a second organization, for the fakes that answer
// an unfiltered listing with everything they hold: an entity parked here must
// not show up in a listing scoped to the organization under test.
const orgIDDataSourceDecoyOrgID = "33333333-3333-3333-3333-333333333333"

// orgIDDataSourceRunnerOrgID is the organization the runner cases name. The
// runner fake ignores it, but the runner attributes validate it as a UUID.
const orgIDDataSourceRunnerOrgID = "00000000-1111-2222-3333-444444444444"

// orgIDDataSourceCases returns one case per data source that reads an
// organization. Each reuses the fake the data source's own tests already drive,
// so these need no credentials.
func orgIDDataSourceCases() map[string]orgIDDataSourceCase {
	return map[string]orgIDDataSourceCase{
		"circleci_audit_log_access": {
			payload: "has_access",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				srv := newAuditLogConfigServer(t, &auditLogConfigAPI{hasAccess: true})

				return orgIDDataSourceFixture{
					providerConfig: auditLogConfigProviderConfig(srv.URL),
					orgID:          testAuditLogConfigOrg,
					block:          orgIDDataSourceBlock("circleci_audit_log_access", ""),
				}
			},
		},

		"circleci_audit_log_configs": {
			payload: "audit_log_configs",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				srv := newAuditLogConfigServer(t, &auditLogConfigAPI{configs: []map[string]any{{
					"id":          "00000000-0000-0000-0000-000000000001",
					"org_id":      testAuditLogConfigOrg,
					"target_type": circleci.AuditLogTargetTypeS3,
					"is_disabled": false,
					"config": map[string]any{
						"arn":         "arn:aws:iam::123456789012:role/circleci-audit-logs",
						"region":      "us-east-1",
						"bucket_name": "acme-audit-logs",
					},
					"created_by":        "11111111-1111-1111-1111-111111111111",
					"created_at":        "2024-01-02T03:04:05Z",
					"updated_at":        "2024-01-02T03:04:05Z",
					"connection_status": "CONNECTED",
				}}})

				return orgIDDataSourceFixture{
					providerConfig: auditLogConfigProviderConfig(srv.URL),
					orgID:          testAuditLogConfigOrg,
					block:          orgIDDataSourceBlock("circleci_audit_log_configs", ""),
				}
			},
		},

		// The organization is only needed for the lookup by name, so the case
		// names the context by name. A lookup by id names no organization at all,
		// which is why this data source cannot use the plain exactly-one-of
		// validator; see its ConfigValidators.
		"circleci_context": {
			payload: "id",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				api, host := newContextFakeAPI(t)
				api.seedContext("ctx-org-attribute", contextUnitOrgID, "2026-01-02T03:04:05Z")

				return orgIDDataSourceFixture{
					providerConfig: contextFakeProviderConfig(host),
					orgID:          contextUnitOrgID,
					block:          orgIDDataSourceBlock("circleci_context", `  name = "build"`),
				}
			},
		},

		"circleci_contexts": {
			payload: "contexts",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				api, host := newPluralAPI(t)
				api.seedContext(testPluralOrgID, "c1", "build", "2024-01-18T02:16:55Z")

				return orgIDDataSourceFixture{
					providerConfig: pluralProviderConfig(host, "cloud"),
					orgID:          testPluralOrgID,
					block:          orgIDDataSourceBlock("circleci_contexts", ""),
				}
			},
		},

		"circleci_deploy_components": {
			payload: "components",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				api, host := newMockDeployAPI(t)

				return orgIDDataSourceFixture{
					providerConfig: deployProviderConfig(host, "cloud"),
					orgID:          testDeployOrganizationID,
					block:          orgIDDataSourceBlock("circleci_deploy_components", ""),
					// The deploy fake serves its fixtures regardless of the filter,
					// so the recorded request lines are what show the organization
					// was sent under both names.
					routed: func(t *testing.T) {
						orgIDDataSourceRoutedTo(t, api.seenRequests(), "/api/v2/deploy/components", testDeployOrganizationID)
					},
				}
			},
		},

		"circleci_deploy_environments": {
			payload: "environments",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				api, host := newMockDeployAPI(t)

				return orgIDDataSourceFixture{
					providerConfig: deployProviderConfig(host, "cloud"),
					orgID:          testDeployOrganizationID,
					block:          orgIDDataSourceBlock("circleci_deploy_environments", ""),
					routed: func(t *testing.T) {
						orgIDDataSourceRoutedTo(t, api.seenRequests(), "/api/v2/deploy/environments", testDeployOrganizationID)
					},
				}
			},
		},

		"circleci_github_app_installation": {
			payload: "login",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				host := newMockGitHubAppInstallationAPI(t, true)

				return orgIDDataSourceFixture{
					providerConfig: discoveryProviderConfig(host, "cloud"),
					orgID:          testDiscoveryOrgID,
					block:          orgIDDataSourceBlock("circleci_github_app_installation", ""),
				}
			},
		},

		"circleci_github_app_repositories": {
			payload: "repositories",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				_, host := newMockDiscoveryAPI(t)

				return orgIDDataSourceFixture{
					providerConfig: discoveryProviderConfig(host, "cloud"),
					orgID:          testDiscoveryOrgID,
					block:          orgIDDataSourceBlock("circleci_github_app_repositories", ""),
				}
			},
		},

		"circleci_github_app_repository": {
			payload: "external_id",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				_, host := newMockDiscoveryAPI(t)

				return orgIDDataSourceFixture{
					providerConfig: discoveryProviderConfig(host, "cloud"),
					orgID:          testDiscoveryOrgID,
					block:          orgIDDataSourceBlock("circleci_github_app_repository", `  full_name = "acme/api"`),
				}
			},
		},

		"circleci_group": {
			payload: "name",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				api, host := newMockGroupAPI(t)
				groupID := api.seed(testGroupOrgID, "platform", "Platform team")

				return orgIDDataSourceFixture{
					providerConfig: testAccGroupProviderConfig(host, "cloud"),
					orgID:          testGroupOrgID,
					block:          orgIDDataSourceBlock("circleci_group", fmt.Sprintf("  id = %q", groupID)),
				}
			},
		},

		"circleci_groups": {
			payload: "groups",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				api, host := newMockGroupAPI(t)
				api.seed(testGroupOrgID, "platform", "Platform team")

				return orgIDDataSourceFixture{
					providerConfig: testAccGroupProviderConfig(host, "cloud"),
					orgID:          testGroupOrgID,
					block:          orgIDDataSourceBlock("circleci_groups", ""),
				}
			},
		},

		"circleci_group_membership": {
			payload: "user_ids",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				api, host := newMockMembershipAPI(t)
				api.seedMembers(testUserA, testUserB)

				return orgIDDataSourceFixture{
					providerConfig: testAccMembershipProviderConfig(host, "cloud"),
					orgID:          testMembershipOrgID,
					block: orgIDDataSourceBlock(
						"circleci_group_membership",
						fmt.Sprintf("  group_id = %q", testMembershipGroupID),
					),
				}
			},
		},

		// The iOS signing fake answers an unfiltered listing with everything it
		// holds, so the only certificate it holds belongs to another
		// organization: a listing that lost the organization reports it, and the
		// empty-list check below fails.
		"circleci_ios_signing_certificates": {
			payload:      "certificates",
			payloadCheck: knownvalue.ListSizeExact(0),
			setup: func(t *testing.T) orgIDDataSourceFixture {
				api := newIOSSigningFakeAPI(t)

				return orgIDDataSourceFixture{
					providerConfig: iosSigningProviderConfig(api.URL()),
					preamble: fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "elsewhere" {
  organization_id      = %q
  file_name            = "elsewhere.p12"
  certificate_blob     = "cDEyLWJhc2U2NC1jb250ZW50LWVsc2V3aGVyZQ=="
  certificate_password = "s3cret-password"
}
`, orgIDDataSourceDecoyOrgID),
					orgID: iosSigningTestOrgID,
					block: orgIDDataSourceBlock(
						"circleci_ios_signing_certificates",
						"  depends_on = [circleci_ios_signing_certificate.elsewhere]",
					),
				}
			},
		},

		"circleci_ios_signing_configs": {
			payload:      "configs",
			payloadCheck: knownvalue.ListSizeExact(0),
			setup: func(t *testing.T) orgIDDataSourceFixture {
				api := newIOSSigningFakeAPI(t)

				return orgIDDataSourceFixture{
					providerConfig: iosSigningProviderConfig(api.URL()),
					preamble: fmt.Sprintf(`
resource "circleci_ios_signing_certificate" "elsewhere" {
  organization_id      = %[1]q
  file_name            = "elsewhere.p12"
  certificate_blob     = "cDEyLWJhc2U2NC1jb250ZW50LWVsc2V3aGVyZQ=="
  certificate_password = "s3cret-password"
}

resource "circleci_ios_signing_config" "elsewhere" {
  organization_id = %[1]q
  name            = "elsewhere-config"
  certificate_id  = circleci_ios_signing_certificate.elsewhere.id

  provisioning_profiles = [
    {
      file_name = "elsewhere.mobileprovision"
      blob      = "bW9iaWxlcHJvdmlzaW9uLWNvbnRlbnQ="
    },
  ]
}
`, orgIDDataSourceDecoyOrgID),
					orgID: iosSigningTestOrgID,
					block: orgIDDataSourceBlock(
						"circleci_ios_signing_configs",
						"  depends_on = [circleci_ios_signing_config.elsewhere]",
					),
				}
			},
		},

		"circleci_organization_settings": {
			payload: "enable_private_orbs",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				srv, _ := newFakeOrgSettingsAPI(t)

				return orgIDDataSourceFixture{
					providerConfig: fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
`, srv.URL),
					orgID: testOrgSettingsOrgID,
					block: orgIDDataSourceBlock("circleci_organization_settings", ""),
				}
			},
		},

		"circleci_otel_exporters": {
			payload: "exporters",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				srv := newOTelServer(t, newOTelAPI())

				return orgIDDataSourceFixture{
					providerConfig: governanceProviderConfig(srv.URL),
					orgID:          testOTelOrg,
					block:          orgIDDataSourceBlock("circleci_otel_exporters", ""),
				}
			},
		},

		// The grant is created through the resource rather than seeded into the
		// fake, so the pair is exercised the way a practitioner writes it.
		"circleci_project_groups": {
			payload: "groups",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				_, host := newMockProjectGroupAPI(t)

				return orgIDDataSourceFixture{
					providerConfig: testAccMembershipProviderConfig(host, "cloud"),
					preamble: fmt.Sprintf(`
resource "circleci_project_group" "grant" {
  organization_id = %[1]q
  project_id      = %[2]q
  group_id        = %[3]q
  role            = %[4]q
}
`, testPGOrgID, testPGProjectID, testPGGroupA, circleci.ProjectRoleViewer),
					orgID: testPGOrgID,
					block: orgIDDataSourceBlock(
						"circleci_project_groups",
						fmt.Sprintf("  project_id = %q\n  depends_on = [circleci_project_group.grant]", testPGProjectID),
					),
				}
			},
		},

		"circleci_runner_resource_class": {
			payload: "id",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				api := newRunnerFakeAPI(t)
				api.respond("GET", "/api/v3/runner/resource", `{"items":[{
				  "id": "11111111-2222-3333-4444-555555555555",
				  "resource_class": "acc-ns/linux",
				  "description": "linux runners"
				}]}`)

				return orgIDDataSourceFixture{
					providerConfig: runnerProviderConfig(api.URL()),
					orgID:          orgIDDataSourceRunnerOrgID,
					block: orgIDDataSourceBlock(
						"circleci_runner_resource_class",
						`  resource_class = "acc-ns/linux"`,
					),
					// The runner fake serves its registered response whatever the
					// filters are, so the recorded query is what shows the
					// organization was sent under both names.
					routed: func(t *testing.T) {
						orgIDDataSourceRunnerRoutedTo(t, api, "/api/v3/runner/resource")
					},
				}
			},
		},

		"circleci_runner_resource_classes": {
			payload: "resource_classes",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				api := newRunnerFakeAPI(t)
				api.respond("GET", "/api/v3/runner/resource", `{"items":[{
				  "id": "11111111-2222-3333-4444-555555555555",
				  "resource_class": "acc-ns/linux",
				  "description": "linux runners"
				}]}`)

				return orgIDDataSourceFixture{
					providerConfig: runnerProviderConfig(api.URL()),
					orgID:          orgIDDataSourceRunnerOrgID,
					block:          orgIDDataSourceBlock("circleci_runner_resource_classes", ""),
					routed: func(t *testing.T) {
						orgIDDataSourceRunnerRoutedTo(t, api, "/api/v3/runner/resource")
					},
				}
			},
		},

		"circleci_runners": {
			payload: "runners",
			setup: func(t *testing.T) orgIDDataSourceFixture {
				api := newRunnerFakeAPI(t)
				api.respond("GET", "/api/v3/runner", `{"items":[{
				  "name": "acc-ns/acc-rc/agent-one",
				  "hostname": "runner-one",
				  "ip": "10.0.0.1",
				  "version": "3.1.2",
				  "status": "idle",
				  "resource_class": "acc-ns/acc-rc",
				  "first_connected": "2026-01-01T00:00:00Z",
				  "last_connected": "2026-01-02T00:00:00Z",
				  "last_used": null
				}]}`)

				return orgIDDataSourceFixture{
					providerConfig: runnerProviderConfig(api.URL()),
					orgID:          orgIDDataSourceRunnerOrgID,
					block:          orgIDDataSourceBlock("circleci_runners", ""),
					routed: func(t *testing.T) {
						orgIDDataSourceRunnerRoutedTo(t, api, "/api/v3/runner")
					},
				}
			},
		},
	}
}

// orgIDDataSourceBlock builds a block renderer for a data source type, with any
// attributes the read needs besides the organization.
func orgIDDataSourceBlock(typeName, otherAttributes string) func(label, orgAttributes string) string {
	return func(label, orgAttributes string) string {
		body := orgAttributes
		if otherAttributes != "" {
			body += strings.TrimSuffix(otherAttributes, "\n") + "\n"
		}

		return fmt.Sprintf("\ndata %q %q {\n%s}\n", typeName, label, body)
	}
}

// orgIDDataSourceAttributeLine renders one organization attribute assignment.
func orgIDDataSourceAttributeLine(name, orgID string) string {
	return fmt.Sprintf("  %s = %q\n", name, orgID)
}

// orgIDDataSourceRoutedTo asserts that both reads of a path carried the
// organization, for a fake that records raw request lines.
func orgIDDataSourceRoutedTo(t *testing.T, requests []string, path, orgID string) {
	t.Helper()

	var seen int
	for _, request := range requests {
		if !strings.Contains(request, path) {
			continue
		}

		seen++
		if !strings.Contains(request, "org-id="+orgID) {
			t.Errorf("request %q did not carry org-id=%s", request, orgID)
		}
	}

	// Two reads, one per attribute name. Fewer means one of them never reached
	// the API at all.
	if seen < 2 {
		t.Errorf("expected two reads of %s, saw %d (requests: %v)", path, seen, requests)
	}
}

// orgIDDataSourceRunnerRoutedTo is orgIDDataSourceRoutedTo for the runner fake,
// which records parsed requests rather than raw lines.
func orgIDDataSourceRunnerRoutedTo(t *testing.T, api *runnerFakeAPI, path string) {
	t.Helper()

	requests := api.requestsFor("GET", path)
	for _, request := range requests {
		if got := request.Query.Get("org-id"); got != orgIDDataSourceRunnerOrgID {
			t.Errorf("read of %s sent org-id = %q, want %q", path, got, orgIDDataSourceRunnerOrgID)
		}
	}

	if len(requests) < 2 {
		t.Errorf("expected two reads of %s, saw %d (requests: %v)", path, len(requests), api.allRequests())
	}
}

// TestOrgIDDeprecation_DataSourcesAcceptEitherOrgAttribute reads every migrated
// data source twice in one configuration — once under `organization_id`, once
// under `org_id` — and requires the two to agree.
//
// One configuration rather than two runs, because the assertion that matters is
// that the results are identical: a Read that still looks only at
// `organization_id` leaves the `org_id` copy asking the API for organization "",
// which either fails outright or, on an endpoint that treats an absent filter as
// "everything", quietly answers with the wrong data. Comparing the two side by
// side catches both.
func TestOrgIDDeprecation_DataSourcesAcceptEitherOrgAttribute(t *testing.T) {
	for typeName, testCase := range orgIDDataSourceCases() {
		t.Run(typeName, func(t *testing.T) {
			fixture := testCase.setup(t)

			deprecated := "data." + typeName + ".deprecated"
			current := "data." + typeName + ".current"

			payloadCheck := testCase.payloadCheck
			if payloadCheck == nil {
				payloadCheck = knownvalue.NotNull()
			}

			config := fixture.providerConfig + fixture.preamble +
				fixture.block("deprecated", orgIDDataSourceAttributeLine("organization_id", fixture.orgID)) +
				fixture.block("current", orgIDDataSourceAttributeLine("org_id", fixture.orgID))

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: config,
					ConfigStateChecks: []statecheck.StateCheck{
						statecheck.ExpectKnownValue(deprecated, tfjsonpath.New(testCase.payload), payloadCheck),
						statecheck.ExpectKnownValue(current, tfjsonpath.New(testCase.payload), payloadCheck),
						statecheck.CompareValuePairs(
							deprecated, tfjsonpath.New(testCase.payload),
							current, tfjsonpath.New(testCase.payload),
							compare.ValuesSame(),
						),
					},
				}},
			})

			if fixture.routed != nil {
				fixture.routed(t)
			}
		})
	}
}

// TestOrgIDDeprecation_DataSourcesRequireExactlyOneOrgAttribute covers the two
// ways of getting the pair wrong, for every migrated data source: both names, or
// neither.
//
// Per data source rather than once on a representative, because three of them
// cannot use the shared exactly-one-of validator — circleci_context needs no
// organization at all for a lookup by id, and the two runner listings treat the
// organization as one optional filter among several — so each of those three
// spells the rule out differently and could get it wrong on its own.
func TestOrgIDDeprecation_DataSourcesRequireExactlyOneOrgAttribute(t *testing.T) {
	// Both names is always an Invalid Attribute Combination; neither is a Missing
	// Attribute Configuration from the exactly-one-of and at-least-one-of
	// validators, and an Invalid Attribute Combination from circleci_context's
	// name-needs-an-organization rule.
	wantError := regexp.MustCompile(`(?s)Invalid Attribute Combination|Missing Attribute Configuration`)

	for typeName, testCase := range orgIDDataSourceCases() {
		t.Run(typeName, func(t *testing.T) {
			fixture := testCase.setup(t)

			both := orgIDDataSourceAttributeLine("organization_id", fixture.orgID) +
				orgIDDataSourceAttributeLine("org_id", fixture.orgID)

			for name, orgAttributes := range map[string]string{"both set": both, "neither set": ""} {
				t.Run(name, func(t *testing.T) {
					resource.UnitTest(t, resource.TestCase{
						ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
						Steps: []resource.TestStep{{
							Config:      fixture.providerConfig + fixture.preamble + fixture.block("test", orgAttributes),
							ExpectError: wantError,
						}},
					})
				})
			}
		})
	}
}
