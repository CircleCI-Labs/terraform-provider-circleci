// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	ephschema "github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"terraform-provider-circleci/internal/circleci"
)

// Testing notes: this provider's go.mod pins terraform-plugin-go v0.31.0
// (newer than the terraform-plugin-testing v1.12.0 release this repo also
// pins was built against). That version added GenerateResourceConfig to
// tfprotov6.ProviderServer, which terraform-plugin-testing's vendored
// echoprovider — the framework's documented way to surface an ephemeral
// resource's result into a checkable state, via `provider "echo" { data = ...
// }` — does not implement, so registering it here fails to compile
// (confirmed: `*echoProviderServer does not implement tfprotov6.ProviderServer
// (missing method GenerateResourceConfig)`). Bumping terraform-plugin-testing
// is out of scope for this change (it is a shared dependency, and other
// concurrent work in this repo should not have its build quietly changed from
// under it), so these tests take the documented fallback instead:
//
//   - Acceptance-style tests below drive Open through a real Terraform
//     plan/apply and a fake HTTP backend, asserting on the *requests the fake
//     received* (that a job was created, that status was polled more than
//     once, that a failure/validation error surfaces as a plan-time
//     diagnostic). This still exercises the real schema/Configure/protocol
//     wiring. Confirmed separately that Terraform does call Open for an
//     ephemeral resource block even when nothing else references it — it
//     does not need a consumer such as echo to be evaluated at all — so no
//     workaround for that part was needed.
//   - Because none of the above can inspect the *value* Open actually
//     produces (it never reaches state), TestUsageExportEphemeralResource_Open
//     below calls Open directly, matching the task's documented fallback.
//   - TestUsageExportAwaitTerminalTimesOut and
//     TestUsageExportAwaitTerminalRespectsContextCancellation are also direct
//     unit tests, since they assert on wall-clock behavior that would be
//     awkward to observe through a plan/apply.

// newUsageExportFakeAPI starts a fake of the usage export v2 routes. Get
// answers "processing" for processingCalls requests and then settles on
// terminalState with downloadURLs/errorReason, so tests can exercise the poll
// loop actually looping before it observes a terminal state.
func newUsageExportFakeAPI(t *testing.T, terminalState string, downloadURLs []string, errorReason string, processingCalls int) (*httptest.Server, *int32, *int32) {
	t.Helper()

	var postCalls, getCalls int32

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/organizations/{org_id}/usage_export_job", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&postCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{
			"usage_export_job_id": "11111111-1111-1111-1111-111111111111",
			"state": "created",
			"start": "2024-01-01T00:00:00Z",
			"end": "2024-01-02T00:00:00Z",
			"download_urls": []
		}`)
	})
	mux.HandleFunc("GET /api/v2/organizations/{org_id}/usage_export_job/{id}", func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&getCalls, 1)
		w.Header().Set("Content-Type", "application/json")

		if int(n) <= processingCalls {
			_, _ = fmt.Fprint(w, `{
				"usage_export_job_id": "11111111-1111-1111-1111-111111111111",
				"state": "processing",
				"download_urls": []
			}`)

			return
		}

		urlsJSON, _ := json.Marshal(downloadURLs)
		_, _ = fmt.Fprintf(w, `{
			"usage_export_job_id": "11111111-1111-1111-1111-111111111111",
			"state": %q,
			"download_urls": %s,
			"error_reason": %q
		}`, terminalState, urlsJSON, errorReason)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv, &postCalls, &getCalls
}

// usageExportProviderConfig renders a provider block pointing at the fake, à la
// orbProviderConfig.
func usageExportProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
`, host)
}

// withShortUsageExportPollInterval speeds up the poll loop for the duration of
// a test, restoring the production interval afterward.
func withShortUsageExportPollInterval(t *testing.T, interval time.Duration) {
	t.Helper()

	original := usageExportPollIntervalVar
	usageExportPollIntervalVar = interval
	t.Cleanup(func() { usageExportPollIntervalVar = original })
}

// TestAccUsageExportEphemeralResource_completed drives circleci_usage_export
// through a real Terraform plan/apply against a fake backend that reports
// "processing" twice before "completed", proving the poll loop actually
// re-checks the job's status through the real protocol rather than only in a
// unit test.
func TestAccUsageExportEphemeralResource_completed(t *testing.T) {
	withShortUsageExportPollInterval(t, 20*time.Millisecond)

	srv, postCalls, getCalls := newUsageExportFakeAPI(t, circleci.UsageExportJobStateCompleted,
		[]string{"https://example.com/a.csv.gz", "https://example.com/b.csv.gz"}, "", 2)

	config := usageExportProviderConfig(srv.URL) + `
ephemeral "circleci_usage_export" "this" {
  organization_id = "22222222-2222-2222-2222-222222222222"
  start            = "2024-01-01T00:00:00Z"
  end              = "2024-01-02T00:00:00Z"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
		},
	})

	if atomic.LoadInt32(postCalls) == 0 {
		t.Error("no usage export job was created")
	}
	// At least 3 GETs per plan/apply cycle (2 "processing" + 1 "completed"):
	// proof the loop polled more than once before stopping.
	if atomic.LoadInt32(getCalls) < 3 {
		t.Errorf("get calls = %d, want at least 3 (the loop should poll past the first \"processing\" response)", atomic.LoadInt32(getCalls))
	}
}

// TestAccUsageExportEphemeralResource_failed asserts that a job which reaches
// the "failed" terminal state surfaces as a plan/apply error naming the
// error_reason, rather than being silently treated as a success with no URLs.
func TestAccUsageExportEphemeralResource_failed(t *testing.T) {
	withShortUsageExportPollInterval(t, 20*time.Millisecond)

	srv, _, _ := newUsageExportFakeAPI(t, circleci.UsageExportJobStateFailed, nil, "the date range is too large", 0)

	config := usageExportProviderConfig(srv.URL) + `
ephemeral "circleci_usage_export" "this" {
  organization_id = "22222222-2222-2222-2222-222222222222"
  start            = "2024-01-01T00:00:00Z"
  end              = "2024-01-02T00:00:00Z"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`usage export job failed`),
			},
		},
	})
}

// TestAccUsageExportEphemeralResource_invalidTimestamp asserts that a
// malformed start/end timestamp is rejected before any request is made,
// rather than surfacing as a confusing API error.
func TestAccUsageExportEphemeralResource_invalidTimestamp(t *testing.T) {
	srv, postCalls, _ := newUsageExportFakeAPI(t, circleci.UsageExportJobStateCompleted, nil, "", 0)

	config := usageExportProviderConfig(srv.URL) + `
ephemeral "circleci_usage_export" "this" {
  organization_id = "22222222-2222-2222-2222-222222222222"
  start            = "not-a-timestamp"
  end              = "2024-01-02T00:00:00Z"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?i)invalid start timestamp`),
			},
		},
	})

	if atomic.LoadInt32(postCalls) != 0 {
		t.Error("a request reached the fake API despite the invalid start timestamp, want none")
	}
}

// ephemeralConfigFromOverrides builds a tfsdk.Config for sch with the given
// top-level attribute values; every attribute not named in overrides is null.
// This lets a test call Open directly without going through a real Terraform
// plan/apply — the documented fallback for asserting on an ephemeral
// resource's actual result, which a plan/apply cannot surface without the
// (here, unusable — see the top of this file) echo provider.
func ephemeralConfigFromOverrides(t *testing.T, sch ephschema.Schema, overrides map[string]tftypes.Value) tfsdk.Config {
	t.Helper()

	ctx := context.Background()

	objType, ok := sch.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatalf("schema type is not an object: %T", sch.Type())
	}

	values := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, attrType := range objType.AttributeTypes {
		if override, ok := overrides[name]; ok {
			values[name] = override

			continue
		}

		values[name] = tftypes.NewValue(attrType, nil)
	}

	return tfsdk.Config{Schema: sch, Raw: tftypes.NewValue(objType, values)}
}

// TestUsageExportEphemeralResource_Open is the direct unit test called for by
// this file's top comment: it asserts on the value Open actually produces,
// which no acceptance test here can observe.
func TestUsageExportEphemeralResource_Open(t *testing.T) {
	withShortUsageExportPollInterval(t, 5*time.Millisecond)

	srv, _, _ := newUsageExportFakeAPI(t, circleci.UsageExportJobStateCompleted,
		[]string{"https://example.com/a.csv.gz"}, "", 1)

	e := &usageExportEphemeralResource{client: circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})}

	var schemaResp ephemeral.SchemaResponse
	e.Schema(context.Background(), ephemeral.SchemaRequest{}, &schemaResp)

	cfg := ephemeralConfigFromOverrides(t, schemaResp.Schema, map[string]tftypes.Value{
		"organization_id": tftypes.NewValue(tftypes.String, "22222222-2222-2222-2222-222222222222"),
		"start":           tftypes.NewValue(tftypes.String, "2024-01-01T00:00:00Z"),
		"end":             tftypes.NewValue(tftypes.String, "2024-01-02T00:00:00Z"),
	})

	openResp := &ephemeral.OpenResponse{
		Result: tfsdk.EphemeralResultData{Schema: schemaResp.Schema, Raw: cfg.Raw},
	}

	e.Open(context.Background(), ephemeral.OpenRequest{Config: cfg}, openResp)

	if openResp.Diagnostics.HasError() {
		t.Fatalf("Open reported an error: %s", openResp.Diagnostics)
	}

	var got usageExportEphemeralModel
	if diags := openResp.Result.Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("reading Open's result: %s", diags)
	}

	if got.ID.ValueString() != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("id = %q, want %q", got.ID.ValueString(), "11111111-1111-1111-1111-111111111111")
	}
	if got.State.ValueString() != circleci.UsageExportJobStateCompleted {
		t.Errorf("state = %q, want %q", got.State.ValueString(), circleci.UsageExportJobStateCompleted)
	}

	var urls []string
	if diags := got.DownloadURLs.ElementsAs(context.Background(), &urls, false); diags.HasError() {
		t.Fatalf("reading download_urls: %s", diags)
	}
	if len(urls) != 1 || urls[0] != "https://example.com/a.csv.gz" {
		t.Errorf("download_urls = %v, want [\"https://example.com/a.csv.gz\"]", urls)
	}
}

// TestUsageExportAwaitTerminalTimesOut proves the poll loop terminates within
// its cap for a job that never reaches "completed" or "failed", rather than
// hanging a plan or apply forever, and that the resulting error is useful: it
// names the job id and the last known state.
func TestUsageExportAwaitTerminalTimesOut(t *testing.T) {
	withShortUsageExportPollInterval(t, 5*time.Millisecond)

	// processingCalls is huge: Get must always answer "processing", so the
	// only way out of the loop is the timeout.
	srv, _, _ := newUsageExportFakeAPI(t, circleci.UsageExportJobStateCompleted, nil, "", 1_000_000)
	e := &usageExportEphemeralResource{client: circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})}

	const timeout = 100 * time.Millisecond
	const jobID = "11111111-1111-1111-1111-111111111111"

	start := time.Now()
	_, err := e.awaitTerminal(context.Background(), "22222222-2222-2222-2222-222222222222", jobID, circleci.UsageExportJobStateProcessing, timeout)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("awaitTerminal returned no error for a job that never completes, want a timeout error")
	}

	// Bounded: allow generous slack for scheduling jitter, but this must not
	// be anywhere close to hanging indefinitely.
	if elapsed > 2*time.Second {
		t.Errorf("awaitTerminal took %s to give up with a %s timeout, want it bounded near the cap", elapsed, timeout)
	}

	msg := err.Error()
	if !strings.Contains(msg, jobID) {
		t.Errorf("error %q does not name the job id %q", msg, jobID)
	}
	if !strings.Contains(msg, circleci.UsageExportJobStateProcessing) {
		t.Errorf("error %q does not name the last known state %q", msg, circleci.UsageExportJobStateProcessing)
	}
}

// TestUsageExportAwaitTerminalRespectsContextCancellation proves that
// canceling the caller's context stops the loop promptly too, independent of
// the poll_timeout cap.
func TestUsageExportAwaitTerminalRespectsContextCancellation(t *testing.T) {
	withShortUsageExportPollInterval(t, 5*time.Millisecond)

	srv, _, _ := newUsageExportFakeAPI(t, circleci.UsageExportJobStateCompleted, nil, "", 1_000_000)
	e := &usageExportEphemeralResource{client: circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := e.awaitTerminal(ctx, "22222222-2222-2222-2222-222222222222", "job-1", circleci.UsageExportJobStateProcessing, time.Minute)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("awaitTerminal returned no error after context cancellation, want one")
	}
	if elapsed > 2*time.Second {
		t.Errorf("awaitTerminal took %s to notice context cancellation, want it to return promptly", elapsed)
	}
}
