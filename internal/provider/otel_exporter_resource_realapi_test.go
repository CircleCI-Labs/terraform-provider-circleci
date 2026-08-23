// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// This file is the [NET] counterpart to otel_exporter_resource_test.go and
// otel_exporter_write_only_test.go, both of which are entirely [FAKE]. Before
// this file, every fact cited in otel.go's doc comments ("[NET] Confirmed
// against a real Cloud organization: ...") had been checked exactly once, by
// hand, and nothing in CI would notice if the API's behaviour ever drifted
// back. It runs against whichever organization CIRCLECI_TEST_VCS_TYPE's
// active integration names (testOrgID), so it exercises every one of this
// wave's four Cloud organizations across the acceptance-* CI jobs, not just
// the one the original measurement happened against.
//
// Every endpoint below uses "www.example.com" rather than the fake tests'
// "otel.example.com": the real service's endpoint validator requires the
// hostname to actually resolve publicly (see CreateOTelExporterRequest's doc
// comment), and "otel.example.com" does not resolve at all -- only
// "www.example.com" and the bare apex do, to Cloudflare-fronted, unambiguously
// public addresses. Each test picks its own port so that exporters created by
// different tests never collide on identical endpoint+protocol pairs.
//
// None of these tests call t.Parallel(). Every one of them shares one
// account-wide resource with a hard cap (OTelExporterLimit, 5 per
// organization), so two of them running at once against the same organization
// would race for that budget. Go's testing package runs every test in this
// file sequentially by construction (t.Parallel() is what opts a test out of
// that, and nothing here does), which is what keeps them from racing each
// other within one `go test` invocation; running the exact same test twice
// concurrently by hand against the same organization is not something this
// file tries to make safe, because the API itself has no way to make it safe
// -- there is nothing analogous to a unique name to key a second, independent
// exporter on.

// otelAssertDataSourceListsExporter checks, straight from the flattened
// state attributes of a circleci_otel_exporters data source instance, that
// one of its "exporters" entries has the given id and carries headerName
// redacted to circleci.OTelRedactedHeaderValue -- the plural data source's
// own [NET] coverage, folded into the resource lifecycle test above rather
// than duplicated into a separate file, since it needs the exact same
// created exporter to look for.
func otelAssertDataSourceListsExporter(s *terraform.State, dataSourceAddress, wantID, headerName string) error {
	rs, ok := s.RootModule().Resources[dataSourceAddress]
	if !ok {
		return fmt.Errorf("%s not found in state", dataSourceAddress)
	}

	for i := 0; ; i++ {
		idKey := fmt.Sprintf("exporters.%d.id", i)
		id, ok := rs.Primary.Attributes[idKey]
		if !ok {
			break
		}

		if id != wantID {
			continue
		}

		headerKey := fmt.Sprintf("exporters.%d.headers.%s", i, headerName)
		value, ok := rs.Primary.Attributes[headerKey]
		if !ok {
			return fmt.Errorf("%s lists exporter %s but its headers carry no %q entry", dataSourceAddress, wantID, headerName)
		}
		if value != circleci.OTelRedactedHeaderValue {
			return fmt.Errorf("%s lists exporter %s with header %q = %q, want %q",
				dataSourceAddress, wantID, headerName, value, circleci.OTelRedactedHeaderValue)
		}

		return nil
	}

	return fmt.Errorf("%s does not list exporter %s among its %q entries", dataSourceAddress, wantID, "exporters")
}

// testRequireTFACC mirrors, for a test that talks to the live API directly
// rather than through resource.Test, the gate resource.Test already enforces
// on every one of this wave's other tests: resource.Test itself skips unless
// TF_ACC is set (see EnvTfAcc in terraform-plugin-testing), which is the
// thing that keeps a bare `go test ./...` from a developer who happens to
// have CIRCLE_TOKEN set locally from silently making a real, and here
// sometimes mutating, API call. A test built around a direct
// *circleci.Client instead of resource.Test gets none of that for free, so
// every one of this wave's tests that is not itself wrapped in resource.Test
// calls this first. Message text matches resource.Test's own, so a
// developer seeing either skip recognises it as the same thing.
func testRequireTFACC(t *testing.T) {
	t.Helper()

	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' set")
	}
}

// otelRealAPIClient builds a client that talks to the real API, for the
// corroborating checks below that inspect what the server actually stored
// rather than trusting Terraform's own state. Built from CIRCLE_TOKEN
// directly rather than through the provider's Configure, matching the
// existing convention in url_orb_allow_list_entry_net_test.go.
func otelRealAPIClient() *circleci.Client {
	return circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})
}

// otelRealAPIHeaderName is a header name distinctive enough that a sweep can
// recognise (and clean up after) an exporter this file created, even one left
// behind by a crashed prior run: CircleCI discloses header *names* in full on
// every read, redacting only the value, so the name survives to be swept on
// even though the secret it once carried never will.
const otelRealAPIHeaderName = "x-tf-provider-circleci-acctest"

// otelRealAPISweep deletes every exporter in orgID carrying
// otelRealAPIHeaderName, reporting (via t.Errorf, not silently) any delete
// that fails. Called before the limit test claims the account's exporter
// budget, so a crash in an earlier run of that same test does not permanently
// wedge every later run against a "limit already reached" false positive.
func otelRealAPISweep(t *testing.T, client *circleci.Client, orgID string) {
	t.Helper()

	exporters, err := client.ListOTelExporters(context.Background(), orgID)
	if err != nil {
		t.Fatalf("sweeping org %s for leftover acctest exporters: listing failed: %v", orgID, err)
	}

	for _, exporter := range exporters {
		if _, ok := exporter.Headers[otelRealAPIHeaderName]; !ok {
			continue
		}

		if err := client.DeleteOTelExporter(context.Background(), exporter.ID); err != nil {
			t.Errorf("sweeping leftover acctest exporter %s in org %s: delete failed: %v", exporter.ID, orgID, err)
		}
	}
}

// TestAccOTelExporterResource_RealAPI_HeadersReachTheWireAndSurviveRefresh is
// the live pin for the header-redaction path documented on OTelExporter and
// otelRefreshHeaders: a header value sent on create must actually reach
// CircleCI under the name the API reads it back under (proven by listing the
// exporter with a raw client and finding that name present, redacted to
// circleci.OTelRedactedHeaderValue -- CircleCI does not expose any way to
// confirm the *value* landed, by design, so the name surviving a real round
// trip is the strongest evidence available), and a plan taken right after
// must stay empty (proven by the PlanOnly step) -- which is only true if Read
// kept the real, configured value in state instead of adopting the "xxxx"
// placeholder the live API actually answers with.
func TestAccOTelExporterResource_RealAPI_HeadersReachTheWireAndSurviveRefresh(t *testing.T) {
	orgID := testOrgID(t)
	client := otelRealAPIClient()

	secret := "acctest-" + rand.Text()

	config := fmt.Sprintf(`
resource "circleci_otel_exporter" "test" {
  org_id   = %q
  endpoint = "www.example.com:24317"
  protocol = "grpc"
  headers = {
    %q = %q
  }
}

# depends_on forces this to read after the resource above exists: the two
# have no reference between them, so without it Terraform is free to read
# the data source first, before there is anything for it to see.
data "circleci_otel_exporters" "all" {
  org_id     = %[1]q
  depends_on = [circleci_otel_exporter.test]
}
`, orgID, otelRealAPIHeaderName, secret)

	var createdID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("headers").AtMapKey(otelRealAPIHeaderName),
						knownvalue.StringExact(secret),
					),
				},
				Check: func(s *terraform.State) error {
					rs, ok := s.RootModule().Resources["circleci_otel_exporter.test"]
					if !ok {
						return fmt.Errorf("resource not found in state")
					}

					createdID = rs.Primary.Attributes["id"]
					if createdID == "" {
						return fmt.Errorf("created exporter has no id recorded in state")
					}

					// The corroborating read: what the live API reports for this
					// exporter's headers, independent of anything Terraform cached.
					exporter, err := client.GetOTelExporter(context.Background(), orgID, createdID)
					if err != nil {
						return fmt.Errorf("exporter %s was not readable from the live API right after create: %w", createdID, err)
					}

					value, ok := exporter.Headers[otelRealAPIHeaderName]
					if !ok {
						return fmt.Errorf("live exporter %s has no %q header at all; the header never reached "+
							"the wire under the name the API reads", createdID, otelRealAPIHeaderName)
					}
					if value != circleci.OTelRedactedHeaderValue {
						return fmt.Errorf("live exporter %s header %q = %q, want the redaction placeholder %q "+
							"(a real value coming back would itself be a break in the API's documented "+
							"redaction contract)", createdID, otelRealAPIHeaderName, value, circleci.OTelRedactedHeaderValue)
					}

					// circleci_otel_exporters (the plural data source) must list the
					// same exporter, redacted the same way.
					return otelAssertDataSourceListsExporter(s, "data.circleci_otel_exporters.all", createdID, otelRealAPIHeaderName)
				},
			},
			// The load-bearing step: a refresh against the live API answers every
			// header value with "xxxx", and this plan must still be empty. It would
			// not be if Read ever adopted that placeholder into state instead of
			// preserving the configured secret -- see otelRefreshHeaders.
			{
				Config:   config,
				PlanOnly: true,
			},
			// Import starts with no prior header-name set to compare against, so
			// otelRefreshHeaders cannot tell "nothing changed" from "this is the
			// first read" and adopts the API's redacted map. "headers" therefore
			// comes back as {"...": "xxxx"} on import, not the real secret --
			// exactly the limitation the resource's own schema documents under
			// "Header values cannot be read back."
			{
				ResourceName:            "circleci_otel_exporter.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"headers"},
				ImportStateIdFunc: func(*terraform.State) (string, error) {
					return orgID + "/" + createdID, nil
				},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			if createdID == "" {
				return fmt.Errorf("no exporter id was captured during apply; the destroy check cannot verify anything")
			}

			_, err := client.GetOTelExporter(context.Background(), orgID, createdID)
			if err == nil {
				return fmt.Errorf("exporter %s in organization %s is still present after destroy", createdID, orgID)
			}
			if !errors.Is(err, circleci.ErrNotFound) {
				return fmt.Errorf("checking whether exporter %s was removed returned an unexpected error: %w", createdID, err)
			}

			return nil
		},
	})
}

// TestAccOTelExporterResource_RealAPI_WriteOnlyHeadersRotateOnVersionBump is
// the live pin for the headers_wo path: bumping headers_wo_version must
// destroy and recreate the exporter (there is no update route -- see
// otelHeadersWriteOnlyVersionAttribute), the new header's name must reach the
// live API the same way the state-backed path's does, and headers_wo_names
// must reflect whichever header name is current after each apply.
//
// Rotation is proven by changing the header's *name*, not only its value: a
// destroy-and-recreate is a genuinely new exporter, with nothing carried over
// from the old one, and asserting on a name change is something a value-only
// rotation could not distinguish from an update that mysteriously preserved
// the old exporter.
func TestAccOTelExporterResource_RealAPI_WriteOnlyHeadersRotateOnVersionBump(t *testing.T) {
	orgID := testOrgID(t)
	client := otelRealAPIClient()

	firstHeaderName := otelRealAPIHeaderName + "-v1"
	secondHeaderName := otelRealAPIHeaderName + "-v2"

	configFor := func(headerName, version string) string {
		return fmt.Sprintf(`
resource "circleci_otel_exporter" "test" {
  org_id   = %q
  endpoint = "www.example.com:24318"
  protocol = "grpc"
  headers_wo = {
    %q = "acctest-%s"
  }
  headers_wo_version = %s
}
`, orgID, headerName, version, version)
	}

	var firstID, secondID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		TerraformVersionChecks:   writeOnlySupported(),
		Steps: []resource.TestStep{
			{
				Config: configFor(firstHeaderName, "1"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("headers_wo_names"),
						knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact(firstHeaderName)}),
					),
				},
				Check: func(s *terraform.State) error {
					rs, ok := s.RootModule().Resources["circleci_otel_exporter.test"]
					if !ok {
						return fmt.Errorf("resource not found in state")
					}
					firstID = rs.Primary.Attributes["id"]
					if firstID == "" {
						return fmt.Errorf("created exporter has no id recorded in state")
					}

					return nil
				},
			},
			{
				Config:   configFor(firstHeaderName, "1"),
				PlanOnly: true,
			},
			{
				Config: configFor(secondHeaderName, "2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_otel_exporter.test", plancheck.ResourceActionDestroyBeforeCreate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("headers_wo_names"),
						knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact(secondHeaderName)}),
					),
				},
				Check: func(s *terraform.State) error {
					rs, ok := s.RootModule().Resources["circleci_otel_exporter.test"]
					if !ok {
						return fmt.Errorf("resource not found in state")
					}
					secondID = rs.Primary.Attributes["id"]
					if secondID == "" {
						return fmt.Errorf("recreated exporter has no id recorded in state")
					}
					if secondID == firstID {
						return fmt.Errorf("id stayed %q across a headers_wo_version bump, want a new id: "+
							"there is no update route, so a rotation must destroy and recreate", firstID)
					}

					// Corroborate against the live API that the *new* exporter carries
					// the *new* header name -- proof the rotated secret's key, not just
					// some header, reached the wire on the replacement.
					exporter, err := client.GetOTelExporter(context.Background(), orgID, secondID)
					if err != nil {
						return fmt.Errorf("exporter %s was not readable from the live API right after rotation: %w", secondID, err)
					}
					if value, ok := exporter.Headers[secondHeaderName]; !ok {
						return fmt.Errorf("live exporter %s has no %q header after rotation", secondID, secondHeaderName)
					} else if value != circleci.OTelRedactedHeaderValue {
						return fmt.Errorf("live exporter %s header %q = %q, want %q", secondID, secondHeaderName, value, circleci.OTelRedactedHeaderValue)
					}
					if _, ok := exporter.Headers[firstHeaderName]; ok {
						return fmt.Errorf("live exporter %s still carries the pre-rotation header %q; "+
							"rotation should have replaced it with an entirely new exporter", secondID, firstHeaderName)
					}

					return nil
				},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			if secondID == "" {
				return fmt.Errorf("no post-rotation exporter id was captured; the destroy check cannot verify anything")
			}

			_, err := client.GetOTelExporter(context.Background(), orgID, secondID)
			if err == nil {
				return fmt.Errorf("exporter %s in organization %s is still present after destroy", secondID, orgID)
			}
			if !errors.Is(err, circleci.ErrNotFound) {
				return fmt.Errorf("checking whether exporter %s was removed returned an unexpected error: %w", secondID, err)
			}

			return nil
		},
	})
}

// TestOTelExporterRealAPI_HeaderAddedOutsideTerraformIsUntestable documents,
// rather than tests, a scenario the fake covers
// (TestAccOTelExporterHeaderAddedOutsideTerraform) that the real API has no
// route to reproduce: there is no PATCH for an existing exporter's headers,
// so "a header changes on an exporter Terraform already created" cannot
// happen at all outside Terraform, real API or fake. The fake can only
// simulate it by mutating its own in-memory map directly, bypassing the API
// contract entirely -- which is legitimate for a unit test of
// otelRefreshHeaders' comparison logic, but is not a "one-time hand
// verification" this wave's mandate asks to be turned into live coverage,
// because there was never a live version of it to begin with.
func TestOTelExporterRealAPI_HeaderAddedOutsideTerraformIsUntestable(t *testing.T) {
	t.Skip("not a gap in coverage: the real API has no route that could produce this scenario " +
		"(no update route exists for an exporter's headers at all), so there is nothing live to " +
		"measure here -- see this test's doc comment")
}

// TestOTelExporterRealAPI_LimitAnswers422 is the live pin for
// circleci.OTelExporterLimit: creating a sixth exporter in an organization
// that already holds five must answer HTTP 422, exactly the shape
// CreateOTelExporter's doc comment already claims from one hand-run
// measurement.
//
// otelRealAPISweep runs first so a crashed earlier run of this same test does
// not leave the account permanently pinned at the limit for every later run.
// If the organization still has exporters this sweep does not recognise as
// its own once that sweep finishes, the test skips rather than deleting
// something it cannot prove it owns -- consistent with never mutating remote
// state this suite did not create in order to make a test pass.
func TestOTelExporterRealAPI_LimitAnswers422(t *testing.T) {
	testAccPreCheck(t)
	testRequireTFACC(t)
	orgID := testOrgID(t)
	client := otelRealAPIClient()
	ctx := context.Background()

	otelRealAPISweep(t, client, orgID)

	existing, err := client.ListOTelExporters(ctx, orgID)
	if err != nil {
		t.Fatalf("listing exporters for org %s: %v", orgID, err)
	}

	headroom := circleci.OTelExporterLimit - len(existing)
	if headroom <= 0 {
		t.Skipf("organization %s already has %d exporter(s) not recognised as this suite's own "+
			"(limit is %d); refusing to touch them to make this test pass", orgID, len(existing), circleci.OTelExporterLimit)
	}

	var created []string
	t.Cleanup(func() {
		for _, id := range created {
			if err := client.DeleteOTelExporter(context.Background(), id); err != nil {
				t.Errorf("cleaning up acctest exporter %s in org %s: %v", id, orgID, err)
			}
		}
	})

	for i := 0; i < headroom; i++ {
		exporter, err := client.CreateOTelExporter(ctx, circleci.CreateOTelExporterRequest{
			OrgID:    orgID,
			Endpoint: fmt.Sprintf("www.example.com:%d", 25000+i),
			Protocol: circleci.OTelProtocolGRPC,
			Headers:  map[string]string{otelRealAPIHeaderName: "acctest-filler"},
		})
		if err != nil {
			t.Fatalf("filling exporter slot %d/%d for org %s: %v", i+1, headroom, orgID, err)
		}
		created = append(created, exporter.ID)
	}

	_, err = client.CreateOTelExporter(ctx, circleci.CreateOTelExporterRequest{
		OrgID:    orgID,
		Endpoint: fmt.Sprintf("www.example.com:%d", 25000+headroom),
		Protocol: circleci.OTelProtocolGRPC,
		Headers:  map[string]string{otelRealAPIHeaderName: "acctest-overflow"},
	})
	if err == nil {
		t.Fatal("creating an exporter beyond the limit succeeded, want HTTP 422")
	}
	if !circleci.HasStatus(err, http.StatusUnprocessableEntity) {
		t.Errorf("creating an exporter beyond the limit returned %v, want an HTTP %d", err, http.StatusUnprocessableEntity)
	}
}

// TestOTelExporterRealAPI_RejectsTooManyHeaders is the live pin for
// OTelExporterHeaderLimit's "six headers" measurement. Rejected at create, so
// nothing is left behind to clean up.
func TestOTelExporterRealAPI_RejectsTooManyHeaders(t *testing.T) {
	testAccPreCheck(t)
	testRequireTFACC(t)
	orgID := testOrgID(t)
	client := otelRealAPIClient()

	headers := make(map[string]string, circleci.OTelExporterHeaderLimit+1)
	for i := 0; i <= circleci.OTelExporterHeaderLimit; i++ {
		headers[fmt.Sprintf("x-acctest-h%d", i)] = "v"
	}

	_, err := client.CreateOTelExporter(context.Background(), circleci.CreateOTelExporterRequest{
		OrgID:    orgID,
		Endpoint: "www.example.com:24319",
		Protocol: circleci.OTelProtocolGRPC,
		Headers:  headers,
	})
	if err == nil {
		t.Fatal("creating an exporter with too many headers succeeded, want HTTP 400")
	}
	if !circleci.HasStatus(err, http.StatusBadRequest) {
		t.Errorf("too many headers returned %v, want HTTP %d", err, http.StatusBadRequest)
	}
}

// TestOTelExporterRealAPI_RejectsReservedHeaderName is the live pin for the
// "grpc-" prefix measurement. Rejected at create, so nothing is left behind.
func TestOTelExporterRealAPI_RejectsReservedHeaderName(t *testing.T) {
	testAccPreCheck(t)
	testRequireTFACC(t)
	orgID := testOrgID(t)
	client := otelRealAPIClient()

	_, err := client.CreateOTelExporter(context.Background(), circleci.CreateOTelExporterRequest{
		OrgID:    orgID,
		Endpoint: "www.example.com:24320",
		Protocol: circleci.OTelProtocolGRPC,
		Headers:  map[string]string{"grpc-acctest": "v"},
	})
	if err == nil {
		t.Fatal("creating an exporter with a grpc-prefixed header name succeeded, want HTTP 400")
	}
	if !circleci.HasStatus(err, http.StatusBadRequest) {
		t.Errorf("reserved header name returned %v, want HTTP %d", err, http.StatusBadRequest)
	}
}

// TestOTelExporterRealAPI_RejectsLoopbackEndpoint is the live pin for the
// loopback-endpoint measurement. Rejected at create, so nothing is left
// behind.
func TestOTelExporterRealAPI_RejectsLoopbackEndpoint(t *testing.T) {
	testAccPreCheck(t)
	testRequireTFACC(t)
	orgID := testOrgID(t)
	client := otelRealAPIClient()

	_, err := client.CreateOTelExporter(context.Background(), circleci.CreateOTelExporterRequest{
		OrgID:    orgID,
		Endpoint: "127.0.0.1:4317",
		Protocol: circleci.OTelProtocolGRPC,
	})
	if err == nil {
		t.Fatal("creating an exporter with a loopback endpoint succeeded, want HTTP 400")
	}
	if !circleci.HasStatus(err, http.StatusBadRequest) {
		t.Errorf("loopback endpoint returned %v, want HTTP %d", err, http.StatusBadRequest)
	}
}
