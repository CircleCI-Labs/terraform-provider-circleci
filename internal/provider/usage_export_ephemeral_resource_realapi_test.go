// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"terraform-provider-circleci/internal/circleci"
)

// This file is the [NET] counterpart to usage_export_ephemeral_resource_test.go,
// which is entirely [FAKE]. Before this file, circleci_usage_export had never
// been run against the real usage-export backend at all: not the create
// route, not the poll route, and not the "a completed job can have zero
// download_urls" shape UsageExportJobStatus's doc comment already claims from
// one hand-run measurement against gh-app-cci-1.
//
// What real coverage can and cannot say about an ephemeral resource, stated
// plainly rather than left implicit:
//
//   - It has no persisted refresh to test at all: there is no state file
//     entry to run `terraform plan` against a second time, no Update, no
//     Delete (Open's own doc comment explains why there is deliberately no
//     Close). "Empty plan after apply," this wave's usual proof that Read
//     preserved something, does not exist as a concept here -- every apply
//     is a fresh Open.
//   - usage_export_ephemeral_resource_test.go's own top comment already
//     records why the echo provider -- the framework's documented way to
//     surface an ephemeral value into a checkable state -- cannot be
//     registered in this module today (a terraform-plugin-go/terraform-
//     plugin-testing version mismatch). That limitation is not specific to
//     the fake: it applies exactly as much here. So
//     TestAccUsageExportEphemeralResource_RealAPI_CompletesThroughTerraform
//     below can only prove that a real plan/apply exercises Configure, the
//     real schema, and a real Open against the real backend without
//     erroring -- not what value Open actually produced.
//   - TestUsageExportEphemeralResource_RealAPI_Open, calling Open directly
//     the same way the fake-backed TestUsageExportEphemeralResource_Open
//     does, is the only place in this file that can inspect the real
//     job id, state and download_urls Open actually returned.
//
// No test here calls t.Parallel(): each creates a real, billable-looking (if
// harmless) usage-export job against a shared organization, and there is no
// reason to race them against whatever else that organization's CI job is
// doing at the time.

// usageExportRealAPIWindow returns a one-day export window ending yesterday
// (in UTC), computed at run time rather than hardcoded: the API rejects any
// bound in the future, and a fixed date would eventually become one.
func usageExportRealAPIWindow() (start, end string) {
	now := time.Now().UTC()
	end = now.Add(-24 * time.Hour).Format(time.RFC3339)
	start = now.Add(-48 * time.Hour).Format(time.RFC3339)

	return start, end
}

// TestAccUsageExportEphemeralResource_RealAPI_CompletesThroughTerraform
// proves the wiring this family has never had live coverage for: a real
// `terraform apply` of a bare `ephemeral "circleci_usage_export"` block, with
// no consumer, reaches Configure, builds a real request, creates a real job
// against the real backend and polls it to a terminal state, all without a
// diagnostic. See this file's top comment for what this specific step
// cannot additionally prove.
func TestAccUsageExportEphemeralResource_RealAPI_CompletesThroughTerraform(t *testing.T) {
	orgID := testOrgID(t)
	start, end := usageExportRealAPIWindow()

	config := fmt.Sprintf(`
ephemeral "circleci_usage_export" "test" {
  org_id = %q
  start  = %q
  end    = %q
}
`, orgID, start, end)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
		},
	})
}

// TestUsageExportEphemeralResource_RealAPI_Open is the direct call that can
// actually see what Open produced against the real backend -- mirroring
// TestUsageExportEphemeralResource_Open, but with a real circleci.Client
// (built from CIRCLE_TOKEN, no fake host) in place of the httptest server.
//
// It asserts "completed" specifically, not "completed or failed": a
// well-formed request against a real organization should never fail, and a
// silent tolerance of "failed" here would hide a real regression behind a
// passing test. It does NOT assert download_urls is non-empty --
// UsageExportJobStatus's own doc comment already records, from a real
// measurement, that a window with no matching usage data completes with
// none -- so this only checks that the field decodes as a list (possibly
// empty) rather than erroring.
func TestUsageExportEphemeralResource_RealAPI_Open(t *testing.T) {
	testAccPreCheck(t)
	testRequireTFACC(t)
	orgID := testOrgID(t)
	start, end := usageExportRealAPIWindow()

	e := &usageExportEphemeralResource{client: circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})}

	var schemaResp ephemeral.SchemaResponse
	e.Schema(context.Background(), ephemeral.SchemaRequest{}, &schemaResp)

	cfg := ephemeralConfigFromOverrides(t, schemaResp.Schema, map[string]tftypes.Value{
		"org_id": tftypes.NewValue(tftypes.String, orgID),
		"start":  tftypes.NewValue(tftypes.String, start),
		"end":    tftypes.NewValue(tftypes.String, end),
	})

	openResp := &ephemeral.OpenResponse{
		Result: tfsdk.EphemeralResultData{Schema: schemaResp.Schema, Raw: cfg.Raw},
	}

	e.Open(context.Background(), ephemeral.OpenRequest{Config: cfg}, openResp)

	if openResp.Diagnostics.HasError() {
		t.Fatalf("Open reported an error against the real API: %s", openResp.Diagnostics)
	}

	var got usageExportEphemeralModel
	if diags := openResp.Result.Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("reading Open's result: %s", diags)
	}

	if got.ID.ValueString() == "" {
		t.Error("id is empty, want the real usage_export_job_id")
	}
	if got.State.ValueString() != circleci.UsageExportJobStateCompleted {
		t.Errorf("state = %q, want %q (error_reason: %q)",
			got.State.ValueString(), circleci.UsageExportJobStateCompleted, got.ErrorReason.ValueString())
	}

	var urls []string
	if diags := got.DownloadURLs.ElementsAs(context.Background(), &urls, false); diags.HasError() {
		t.Fatalf("reading download_urls: %s", diags)
	}
	t.Logf("real usage export job %s completed with %d download URL(s)", got.ID.ValueString(), len(urls))
}
