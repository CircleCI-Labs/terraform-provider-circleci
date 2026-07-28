// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const (
	testUsageExportOrgID = "3ddcf1d1-7f5f-4139-8cef-71ad0921a968"
	testUsageExportJobID = "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"
	// testUsageExportStart and testUsageExportEnd match the literals used in
	// the API's handler_post_test.go, so a shape mismatch there
	// would look the same here.
	testUsageExportStart = "2023-12-10T00:00:00Z"
	testUsageExportEnd   = "2023-12-20T00:00:00Z"
)

// usageExportRequest is one request as the mock server saw it.
type usageExportRequest struct {
	method string
	path   string
	body   string
}

func newUsageExportServer(t *testing.T, handler http.HandlerFunc) (*circleci.Client, *[]usageExportRequest) {
	t.Helper()

	var seen []usageExportRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		seen = append(seen, usageExportRequest{method: r.Method, path: r.URL.Path, body: string(body)})

		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return circleci.New(circleci.Config{Host: srv.URL, Token: "tok"}), &seen
}

// TestUsageExportServiceCreate mirrors the API's
// "201 response when creating a usage export with a 10-day range" fixture
// (the CircleCI API): the same request body shape in,
// the same response shape out.
func TestUsageExportServiceCreate(t *testing.T) {
	t.Parallel()

	client, seen := newUsageExportServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{
			"state": "created",
			"start": "`+testUsageExportStart+`",
			"end": "`+testUsageExportEnd+`",
			"usage_export_job_id": "`+testUsageExportJobID+`",
			"download_urls": []
		}`)
	})

	sharedOrgID1 := "11111111-1111-1111-1111-111111111111"
	sharedOrgID2 := "22222222-2222-2222-2222-222222222222"

	job, err := client.UsageExports().Create(context.Background(), testUsageExportOrgID, circleci.CreateUsageExportJobRequest{
		Start:        testUsageExportStart,
		End:          testUsageExportEnd,
		SharedOrgIDs: []string{sharedOrgID1, sharedOrgID2},
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if job.UsageExportJobID != testUsageExportJobID {
		t.Errorf("job id = %q, want %q", job.UsageExportJobID, testUsageExportJobID)
	}
	if job.State != circleci.UsageExportJobStateCreated {
		t.Errorf("state = %q, want %q", job.State, circleci.UsageExportJobStateCreated)
	}
	if job.Start != testUsageExportStart || job.End != testUsageExportEnd {
		t.Errorf("start/end = %q/%q, want %q/%q", job.Start, job.End, testUsageExportStart, testUsageExportEnd)
	}
	if len(job.DownloadURLs) != 0 {
		t.Errorf("download urls = %v, want none for a freshly created job", job.DownloadURLs)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	got := (*seen)[0]

	wantPath := "/api/v2/organizations/" + testUsageExportOrgID + "/usage_export_job"
	if got.method != http.MethodPost || got.path != wantPath {
		t.Errorf("request = %s %s, want POST %s", got.method, got.path, wantPath)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(got.body), &body); err != nil {
		t.Fatalf("request body %q is not JSON: %v", got.body, err)
	}
	wantBody := map[string]any{
		"start":          testUsageExportStart,
		"end":            testUsageExportEnd,
		"shared_org_ids": []any{sharedOrgID1, sharedOrgID2},
	}
	if len(body) != len(wantBody) {
		t.Errorf("request body = %v, want %v", body, wantBody)
	}
	for key, want := range wantBody {
		gotJSON, _ := json.Marshal(body[key])
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("request body %q = %v, want %v", key, body[key], want)
		}
	}
}

// TestUsageExportServiceCreateOmitsSharedOrgIDsWhenEmpty guards the
// omitempty tag: a create with no shared orgs must not send a field the
// caller never set.
func TestUsageExportServiceCreateOmitsSharedOrgIDsWhenEmpty(t *testing.T) {
	t.Parallel()

	client, seen := newUsageExportServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{
			"state": "created",
			"start": "`+testUsageExportStart+`",
			"end": "`+testUsageExportEnd+`",
			"usage_export_job_id": "`+testUsageExportJobID+`",
			"download_urls": []
		}`)
	})

	_, err := client.UsageExports().Create(context.Background(), testUsageExportOrgID, circleci.CreateUsageExportJobRequest{
		Start: testUsageExportStart,
		End:   testUsageExportEnd,
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte((*seen)[0].body), &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if _, ok := body["shared_org_ids"]; ok {
		t.Errorf("request body = %v, want no shared_org_ids key", body)
	}
}

// TestUsageExportServiceGet mirrors the API's
// "200 response when requesting a report" fixture
// (the CircleCI API). Notably the fixture has no
// start/end fields at all: UsageExportJobStatus must not assume they exist.
func TestUsageExportServiceGet(t *testing.T) {
	t.Parallel()

	client, seen := newUsageExportServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"state": "completed",
			"usage_export_job_id": "`+testUsageExportJobID+`",
			"download_urls": ["url1", "url2"]
		}`)
	})

	status, err := client.UsageExports().Get(context.Background(), testUsageExportOrgID, testUsageExportJobID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	if status.UsageExportJobID != testUsageExportJobID {
		t.Errorf("job id = %q, want %q", status.UsageExportJobID, testUsageExportJobID)
	}
	if status.State != circleci.UsageExportJobStateCompleted {
		t.Errorf("state = %q, want %q", status.State, circleci.UsageExportJobStateCompleted)
	}
	if want := []string{"url1", "url2"}; len(status.DownloadURLs) != len(want) || status.DownloadURLs[0] != want[0] || status.DownloadURLs[1] != want[1] {
		t.Errorf("download urls = %v, want %v", status.DownloadURLs, want)
	}
	if status.ErrorReason != "" {
		t.Errorf("error reason = %q, want empty for a completed job", status.ErrorReason)
	}

	wantPath := "/api/v2/organizations/" + testUsageExportOrgID + "/usage_export_job/" + testUsageExportJobID
	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", got.method, got.path, wantPath)
	}
}

// TestUsageExportServiceGetFailed exercises the other terminal state and its
// accompanying error_reason.
func TestUsageExportServiceGetFailed(t *testing.T) {
	t.Parallel()

	client, _ := newUsageExportServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"state": "failed",
			"usage_export_job_id": "`+testUsageExportJobID+`",
			"download_urls": [],
			"error_reason": "the date range is too large"
		}`)
	})

	status, err := client.UsageExports().Get(context.Background(), testUsageExportOrgID, testUsageExportJobID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	if status.State != circleci.UsageExportJobStateFailed {
		t.Errorf("state = %q, want %q", status.State, circleci.UsageExportJobStateFailed)
	}
	if status.ErrorReason != "the date range is too large" {
		t.Errorf("error reason = %q, want %q", status.ErrorReason, "the date range is too large")
	}
}

// TestUsageExportServiceGetNotFound mirrors the API's
// "400 response when requesting a non existent report" fixture, which the mock
// backend answers with a 404 and a plain {"message": ...} body — the v2 error
// shape circleci.Detail already knows how to render.
func TestUsageExportServiceGetNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newUsageExportServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Usage export not found."}`)
	})

	_, err := client.UsageExports().Get(context.Background(), testUsageExportOrgID, testUsageExportJobID)
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
	if detail := circleci.Detail(err); detail == "" {
		t.Error(`Detail() = "", want the server message`)
	}
}
