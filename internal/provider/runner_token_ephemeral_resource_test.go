// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"terraform-provider-circleci/internal/circleci"
)

// See the top of usage_export_ephemeral_resource_test.go for why these tests
// do not use the echo provider.

const (
	ephemeralRunnerTokenTestOrgID = "33333333-3333-3333-3333-333333333333"
	ephemeralRunnerTokenTestID    = "11111111-2222-3333-4444-555555555555"
)

// TestAccEphemeralRunnerTokenResource_openAndClose drives
// circleci_ephemeral_runner_token through a real Terraform apply against the
// fake runner API from runner_fake_test.go, checking the fake's recorded
// requests: that Open created a token, and that Close deleted it again — all
// within the single apply that opened it, since an ephemeral resource's Close
// runs at the end of the operation that opened it.
func TestAccEphemeralRunnerTokenResource_openAndClose(t *testing.T) {
	api := newRunnerFakeAPI(t)
	api.respond("POST", "/api/v3/runner/token", `{
		"id": "`+ephemeralRunnerTokenTestID+`",
		"nickname": "acc-test-token",
		"resource_class": "acc-ns/acc-rc",
		"token": "secret-token-value",
		"created_at": "2026-01-01T00:00:00Z"
	}`)
	// DELETE has no registered response, which the fake answers with an empty
	// 200 — what the real API returns for a successful delete.

	config := runnerProviderConfig(api.URL()) + `
ephemeral "circleci_ephemeral_runner_token" "this" {
  organization_id = "` + ephemeralRunnerTokenTestOrgID + `"
  resource_class   = "acc-ns/acc-rc"
  nickname         = "acc-test-token"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
		},
	})

	postRequests := api.requestsFor("POST", "/api/v3/runner/token")
	if len(postRequests) == 0 {
		t.Fatal("no POST /api/v3/runner/token request recorded; Open never created a token")
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(postRequests[0].Body), &body); err != nil {
		t.Fatalf("create request body is not JSON: %v", err)
	}
	if body["org_id"] != ephemeralRunnerTokenTestOrgID {
		t.Errorf("create request org_id = %v, want %q", body["org_id"], ephemeralRunnerTokenTestOrgID)
	}
	if body["resource_class"] != "acc-ns/acc-rc" {
		t.Errorf("create request resource_class = %v, want %q", body["resource_class"], "acc-ns/acc-rc")
	}

	deleteRequests := api.requestsFor("DELETE", "/api/v3/runner/token/"+ephemeralRunnerTokenTestID)
	if len(deleteRequests) == 0 {
		t.Error("no DELETE request recorded for the created token; Close did not clean it up")
	}
}

// TestAccEphemeralRunnerTokenResource_invalidOrganizationID asserts the
// organization_id validator runs before any request is made: the runner admin
// API takes a UUID only, never an organization slug, and the API's own error
// for the wrong shape is a plain 400 that is far less clear than catching it
// in the config.
func TestAccEphemeralRunnerTokenResource_invalidOrganizationID(t *testing.T) {
	api := newRunnerFakeAPI(t)

	config := runnerProviderConfig(api.URL()) + `
ephemeral "circleci_ephemeral_runner_token" "this" {
  organization_id = "not-a-uuid"
  resource_class   = "acc-ns/acc-rc"
  nickname         = "acc-test-token"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`organization UUID`),
			},
		},
	})

	if len(api.allRequests()) != 0 {
		t.Error("a request reached the fake API despite the invalid organization_id, want none")
	}
}

// TestAccEphemeralRunnerTokenResource_closeDeleteFailureDoesNotFailApply
// asserts the design choice documented on Close: a delete failure that is not
// "already gone" (circleci.IsNotFound) warns rather than errors, so it must
// not fail the apply that opened the ephemeral resource.
func TestAccEphemeralRunnerTokenResource_closeDeleteFailureDoesNotFailApply(t *testing.T) {
	var deleteCalls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCalls++
			// 400, not 5xx: the provider's client retries 5xx responses up to
			// 3 times with exponential backoff, which would slow this test
			// down. A 4xx is not retried and still exercises the same
			// "delete failed" path in Close.
			http.Error(w, "boom", http.StatusBadRequest)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "` + ephemeralRunnerTokenTestID + `",
			"nickname": "acc-test-token",
			"resource_class": "acc-ns/acc-rc",
			"token": "secret-token-value",
			"created_at": "2026-01-01T00:00:00Z"
		}`))
	}))
	t.Cleanup(srv.Close)

	config := `
provider "circleci" {
  host        = "http://127.0.0.1:1"
  runner_host = "` + srv.URL + `"
  key         = "fake"
}

ephemeral "circleci_ephemeral_runner_token" "this" {
  organization_id = "` + ephemeralRunnerTokenTestOrgID + `"
  resource_class   = "acc-ns/acc-rc"
  nickname         = "acc-test-token"
}
`

	// No ExpectError: a Close-time delete failure must surface as a warning,
	// not fail the apply that created the token.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
		},
	})

	if deleteCalls == 0 {
		t.Error("Close never attempted to delete the token")
	}
}

// TestEphemeralRunnerTokenResource_Open is the direct unit test for the value
// Open produces — see the top of usage_export_ephemeral_resource_test.go for
// why this is a direct call rather than a state check via echo.
func TestEphemeralRunnerTokenResource_Open(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "` + ephemeralRunnerTokenTestID + `",
			"nickname": "acc-test-token",
			"resource_class": "acc-ns/acc-rc",
			"token": "secret-token-value",
			"created_at": "2026-01-01T00:00:00Z"
		}`))
	}))
	t.Cleanup(srv.Close)

	circleciClient := circleci.New(circleci.Config{Host: "http://127.0.0.1:1", RunnerHost: srv.URL, Token: "tok"})
	e := &ephemeralRunnerTokenResource{client: circleciClient}

	var schemaResp ephemeral.SchemaResponse
	e.Schema(context.Background(), ephemeral.SchemaRequest{}, &schemaResp)

	cfg := ephemeralConfigFromOverrides(t, schemaResp.Schema, map[string]tftypes.Value{
		"organization_id": tftypes.NewValue(tftypes.String, ephemeralRunnerTokenTestOrgID),
		"resource_class":  tftypes.NewValue(tftypes.String, "acc-ns/acc-rc"),
		"nickname":        tftypes.NewValue(tftypes.String, "acc-test-token"),
	})

	openResp := &ephemeral.OpenResponse{
		Result: tfsdk.EphemeralResultData{Schema: schemaResp.Schema, Raw: cfg.Raw},
		// Private is deliberately left nil: privatestate.ProviderData lives in
		// an internal package of terraform-plugin-framework that this module
		// cannot import, so a direct unit test cannot pre-populate it the way
		// the framework's own server does before calling Open. Open's SetKey
		// call below therefore reports an extra diagnostic here that would not
		// occur in production (see the acceptance tests above for coverage of
		// the real private-state round trip through Close).
	}

	e.Open(context.Background(), ephemeral.OpenRequest{Config: cfg}, openResp)

	var got ephemeralRunnerTokenModel
	if diags := openResp.Result.Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("reading Open's result: %s", diags)
	}

	if got.ID.ValueString() != ephemeralRunnerTokenTestID {
		t.Errorf("id = %q, want %q", got.ID.ValueString(), ephemeralRunnerTokenTestID)
	}
	if got.Token.ValueString() != "secret-token-value" {
		t.Errorf("token = %q, want %q", got.Token.ValueString(), "secret-token-value")
	}
	if got.CreatedAt.ValueString() != "2026-01-01T00:00:00Z" {
		t.Errorf("created_at = %q, want %q", got.CreatedAt.ValueString(), "2026-01-01T00:00:00Z")
	}
}

// TestEphemeralRunnerTokenClose_noPrivateStateIsANoOp exercises Close directly
// for the case Open never got far enough to record a token id (for example
// because it failed before creating one): Close must not attempt a delete
// with an empty id.
func TestEphemeralRunnerTokenClose_noPrivateStateIsANoOp(t *testing.T) {
	var deleteCalls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCalls++
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	circleciClient := circleci.New(circleci.Config{Host: "http://127.0.0.1:1", RunnerHost: srv.URL, Token: "tok"})
	e := &ephemeralRunnerTokenResource{client: circleciClient}

	// req.Private is left at its zero value (nil): GetKey on a nil
	// *privatestate.ProviderData is documented to return (nil, nil) rather
	// than erroring, so this also exercises that path.
	var resp ephemeral.CloseResponse
	e.Close(context.Background(), ephemeral.CloseRequest{}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Close reported an error for a no-op case: %s", resp.Diagnostics)
	}
	if deleteCalls != 0 {
		t.Errorf("Close called delete %d times, want 0 when there is no recorded token id", deleteCalls)
	}
}
