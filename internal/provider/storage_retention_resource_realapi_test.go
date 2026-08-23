// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"terraform-provider-circleci/internal/circleci"
)

// This file is the [NET] counterpart to storage_retention_resource_test.go,
// which -- as that file's own top comment explains -- cannot reach the real
// API at all through the ordinary Terraform harness: circleci_storage_retention
// calls GetPrivate/PutPrivate, which always dial Client.PrivateHost, and that
// host has deliberately no provider attribute (see internal/circleci/private.go),
// so a fake cannot be substituted for it through a `provider "circleci" {
// host = ... }` block. What follows drives the resource's own Create/Read/
// Update/Delete methods directly, exactly the workaround that file's comment
// already documents for its own Cloud-gating tests, but pointed at a real
// circleci.Client with no PrivateHost override -- which is what makes it
// [NET] rather than [FAKE].
//
// The route's own bounds and its atomicity under a partially-invalid write --
// "an out-of-range field alongside two valid ones rejects the whole write and
// applies none of the three fields" -- had been checked exactly once by hand,
// against gitlab-test, with nothing in CI to notice a regression. This file
// turns that into live coverage across every one of this wave's four Cloud
// organizations (testOrgID resolves to whichever one CIRCLECI_TEST_VCS_TYPE
// names as active).
//
// An organization's storage-retention record is a singleton exactly like a
// spend-budget scope (circleci_budget), always present, never created or
// destroyed -- so "safe to run concurrently with itself against the same
// organization" is not a property this resource's own API offers: two
// writers would just race for the one record. What this test *is* safe
// against is a crashed prior run, because it reads the record's
// values before touching anything and restores them in t.Cleanup, which runs
// even when the test fails partway through -- so a crash during the run
// leaves the record wherever it last was written, exactly as an interrupted
// `terraform apply` against this same record would, and the next run's own
// read-then-restore is unaffected by what values happen to be sitting there
// when it starts.
//
// No test here calls t.Parallel(), for the same reason as the other two
// files in this wave: nothing needs it, since a `go test` invocation runs
// them sequentially by construction.

// storageRetentionRealAPIClient builds a client that talks to the real API.
func storageRetentionRealAPIClient() *circleci.Client {
	return circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})
}

// TestAccStorageRetentionResource_RealAPI_LifecycleAndAtomicity is the [NET]
// pin for storage_retention_resource.go and SetStorageRetention's doc
// comments: a value at the reported bounds is accepted and read back
// unchanged (Create, Update, the empty-plan-after-apply steps, and Import);
// and a write mixing two in-bound fields with one out-of-bounds field is
// rejected outright and applies *none* of the three -- not the two good ones
// -- which this test proves by reading the record back after the rejected
// write and comparing it to what was there immediately before.
func TestAccStorageRetentionResource_RealAPI_LifecycleAndAtomicity(t *testing.T) {
	testAccPreCheck(t)
	// The baseline GetStorageRetention call below happens before resource.Test
	// is ever reached, so resource.Test's own TF_ACC gate would not stop it on
	// its own -- see testRequireTFACC's doc comment.
	testRequireTFACC(t)
	orgID := testOrgID(t)
	client := storageRetentionRealAPIClient()
	ctx := context.Background()

	// The bounds are per-organization and reported only by this same route
	// (see storageRetentionResource's package comment on "why there is no
	// plan-time bound validation"), so they are read live rather than assumed
	// -- this suite must not assert a shape it did not measure on the
	// organization it is actually running against.
	baseline, err := client.GetStorageRetention(ctx, orgID)
	if err != nil {
		t.Fatalf("reading the organization's current storage-retention record before starting: %v", err)
	}

	// Restore whatever was there before this test touched anything, and
	// report -- rather than swallow -- a failure to do so: this record has no
	// delete, so leaving it wrong is the one way this test could outlive
	// itself.
	t.Cleanup(func() {
		if _, err := client.SetStorageRetention(context.Background(), orgID, baseline.Controls); err != nil {
			t.Errorf("restoring the organization's storage-retention record to its pre-test values %+v: %v",
				baseline.Controls, err)
		}
	})

	low := circleci.StorageRetentionControls{
		CacheDays:     baseline.Limits.Cache.Min,
		WorkspaceDays: baseline.Limits.Workspace.Min,
		ArtifactDays:  baseline.Limits.Artifact.Min,
	}
	high := circleci.StorageRetentionControls{
		CacheDays:     baseline.Limits.Cache.Max,
		WorkspaceDays: baseline.Limits.Workspace.Max,
		ArtifactDays:  baseline.Limits.Artifact.Max,
	}

	configFor := func(c circleci.StorageRetentionControls) string {
		return fmt.Sprintf(`
resource "circleci_storage_retention" "test" {
  org_id                   = %q
  cache_retention_days     = %d
  workspace_retention_days = %d
  artifact_retention_days  = %d
}
`, orgID, c.CacheDays, c.WorkspaceDays, c.ArtifactDays)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configFor(low),
				Check: func(s *terraform.State) error {
					rs, ok := s.RootModule().Resources["circleci_storage_retention.test"]
					if !ok {
						return fmt.Errorf("resource not found in state")
					}
					for attr, want := range map[string]int64{
						"cache_retention_days":     low.CacheDays,
						"workspace_retention_days": low.WorkspaceDays,
						"artifact_retention_days":  low.ArtifactDays,
						"cache_retention_days_min": baseline.Limits.Cache.Min,
						"cache_retention_days_max": baseline.Limits.Cache.Max,
					} {
						got := rs.Primary.Attributes[attr]
						if got != fmt.Sprintf("%d", want) {
							return fmt.Errorf("%s = %s, want %d", attr, got, want)
						}
					}

					return nil
				},
			},
			{
				Config:   configFor(low),
				PlanOnly: true,
			},
			// A genuine change to different, still in-bound values: proves Update
			// really rewrites the live record (SetStorageRetention's PUT), not just
			// that Create did.
			{
				Config: configFor(high),
				Check: func(s *terraform.State) error {
					rs, ok := s.RootModule().Resources["circleci_storage_retention.test"]
					if !ok {
						return fmt.Errorf("resource not found in state")
					}
					for attr, want := range map[string]int64{
						"cache_retention_days":     high.CacheDays,
						"workspace_retention_days": high.WorkspaceDays,
						"artifact_retention_days":  high.ArtifactDays,
					} {
						got := rs.Primary.Attributes[attr]
						if got != fmt.Sprintf("%d", want) {
							return fmt.Errorf("%s = %s, want %d", attr, got, want)
						}
					}

					// Corroborate directly against the live API, independent of
					// anything Terraform cached.
					live, err := client.GetStorageRetention(context.Background(), orgID)
					if err != nil {
						return fmt.Errorf("reading the record back from the live API: %w", err)
					}
					if live.Controls != high {
						return fmt.Errorf("live record = %+v after Update, want %+v", live.Controls, high)
					}

					return nil
				},
			},
			{
				Config:   configFor(high),
				PlanOnly: true,
			},
			// The atomicity pin. This bypasses Terraform entirely -- an apply-time
			// error here would need ExpectError, and this needs to inspect the
			// record immediately before and after the rejected write, which a plan/
			// apply cycle cannot do mid-step.
			{
				Config: configFor(high),
				Check: func(*terraform.State) error {
					before, err := client.GetStorageRetention(context.Background(), orgID)
					if err != nil {
						return fmt.Errorf("reading the record before the atomicity probe: %w", err)
					}

					mixed := circleci.StorageRetentionControls{
						CacheDays:     baseline.Limits.Cache.Min,        // in bound
						WorkspaceDays: baseline.Limits.Workspace.Min,    // in bound
						ArtifactDays:  baseline.Limits.Artifact.Max + 1, // out of bound
					}

					_, err = client.SetStorageRetention(context.Background(), orgID, mixed)
					if err == nil {
						return fmt.Errorf("a write with artifact_retention_days one above its max succeeded, want HTTP 400")
					}
					if !circleci.HasStatus(err, http.StatusBadRequest) {
						return fmt.Errorf("the out-of-bounds write returned %v, want HTTP %d", err, http.StatusBadRequest)
					}

					after, err := client.GetStorageRetention(context.Background(), orgID)
					if err != nil {
						return fmt.Errorf("reading the record after the atomicity probe: %w", err)
					}
					if after.Controls != before.Controls {
						return fmt.Errorf("the rejected write changed the record from %+v to %+v; want it "+
							"left exactly as it was -- specifically, the two in-bound fields "+
							"(cache_retention_days, workspace_retention_days) must NOT have landed "+
							"just because they were valid on their own", before.Controls, after.Controls)
					}

					return nil
				},
			},
			{
				ResourceName:  "circleci_storage_retention.test",
				ImportState:   true,
				ImportStateId: orgID,
				// This resource has no "id" attribute at all -- it is a
				// singleton settings record identified by org_id alone (see
				// storageRetentionResourceModel) -- so the default identifier
				// ImportStateVerify looks for does not exist here.
				ImportStateVerifyIdentifierAttribute: "org_id",
				ImportStateVerify:                    true,
			},
		},
	})
}

// TestStorageRetentionRealAPI_UnknownFieldAnswers500 is the live pin for
// SetStorageRetention's documented exception to this codebase's usual
// "unknown request keys are silently dropped" assumption: this specific
// route answers 500 for one, and applies nothing.
//
// Calls PutPrivate directly (bypassing the typed SetStorageRetention, which
// has no way to send a field it does not know about) to put an actual unknown
// key on the wire.
func TestStorageRetentionRealAPI_UnknownFieldAnswers500(t *testing.T) {
	testAccPreCheck(t)
	testRequireTFACC(t)
	orgID := testOrgID(t)
	client := storageRetentionRealAPIClient()
	ctx := context.Background()

	before, err := client.GetStorageRetention(ctx, orgID)
	if err != nil {
		t.Fatalf("reading the record before the unknown-field probe: %v", err)
	}

	t.Cleanup(func() {
		if _, err := client.SetStorageRetention(context.Background(), orgID, before.Controls); err != nil {
			t.Errorf("restoring the organization's storage-retention record to its pre-test values %+v: %v",
				before.Controls, err)
		}
	})

	body := map[string]any{
		"retention_days_cache":                  before.Controls.CacheDays,
		"retention_days_workspace":              before.Controls.WorkspaceDays,
		"retention_days_artifact":               before.Controls.ArtifactDays,
		"a_field_this_route_does_not_recognise": true,
	}

	err = client.PutPrivate(ctx, "/private/orgs/%s/storage-retention-controls", body, nil, circleci.RouteParams(orgID))
	if err == nil {
		t.Fatal("a write with an unrecognised field succeeded, want HTTP 500")
	}
	if !circleci.HasStatus(err, http.StatusInternalServerError) {
		t.Errorf("the unrecognised-field write returned %v, want HTTP %d", err, http.StatusInternalServerError)
	}

	after, err := client.GetStorageRetention(ctx, orgID)
	if err != nil {
		t.Fatalf("reading the record after the unknown-field probe: %v", err)
	}
	if after.Controls != before.Controls {
		t.Errorf("the rejected write changed the record from %+v to %+v, want it left exactly as it was",
			before.Controls, after.Controls)
	}
}
