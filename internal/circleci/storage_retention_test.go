// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const testStorageRetentionOrgID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

// fakeStorageRetentionAPI is a stateful stand-in for the private
// storage-retention-controls route.
//
// It clamps a PUT to the configured bounds instead of rejecting it, and answers
// with 204 No Content and an empty body, exactly as the real endpoint does per
// the org-migration CLI this route was reverse-engineered from
// (github.com/AwesomeCICD/circleci-org-migration-cli).
// A fake that rejected an out-of-bounds PUT with a 4xx, or that echoed the
// request back as the response body, would exercise client and provider code
// paths that can never run against the real API.
//
// It also does not reject unrecognised request fields. There is no evidence
// either way for this specific BFF route, so — following the reasoning in
// runner_fake_test.go for the same situation — this fake defaults to the
// lenient, not-yet-proven-strict behaviour rather than assuming the stricter
// one and risking a fake that is stricter than production.
type fakeStorageRetentionAPI struct {
	server *httptest.Server

	mu      sync.Mutex
	limits  circleci.StorageRetentionLimits
	current circleci.StorageRetentionControls
	gets    int
	puts    []map[string]any
}

func newFakeStorageRetentionAPI(t *testing.T, limits circleci.StorageRetentionLimits, initial circleci.StorageRetentionControls) *fakeStorageRetentionAPI {
	t.Helper()

	api := &fakeStorageRetentionAPI{limits: limits, current: initial}
	api.server = httptest.NewServer(http.HandlerFunc(api.handle))
	t.Cleanup(api.server.Close)

	return api
}

func (a *fakeStorageRetentionAPI) handle(w http.ResponseWriter, r *http.Request) {
	wantPath := "/private/orgs/" + testStorageRetentionOrgID + "/storage-retention-controls"
	if r.URL.Path != wantPath {
		http.NotFound(w, r)

		return
	}

	switch r.Method {
	case http.MethodGet:
		a.mu.Lock()
		defer a.mu.Unlock()

		a.gets++

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(circleci.StorageRetention{
			Controls: a.current,
			Limits:   a.limits,
		})

	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)

			return
		}

		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		var requested circleci.StorageRetentionControls
		if err := json.Unmarshal(body, &requested); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		a.mu.Lock()
		a.puts = append(a.puts, raw)
		a.current = circleci.StorageRetentionControls{
			CacheDays:     clampToBound(requested.CacheDays, a.limits.Cache),
			WorkspaceDays: clampToBound(requested.WorkspaceDays, a.limits.Workspace),
			ArtifactDays:  clampToBound(requested.ArtifactDays, a.limits.Artifact),
		}
		a.mu.Unlock()

		// 204 No Content, no body — the real route answers this way. A fake that
		// echoed the stored (possibly clamped) record back here would let
		// SetStorageRetention appear to work without ever issuing the read-back GET
		// it depends on to report the truth.
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func clampToBound(v int64, bound circleci.StorageRetentionBound) int64 {
	if v < bound.Min {
		return bound.Min
	}

	if v > bound.Max {
		return bound.Max
	}

	return v
}

func (a *fakeStorageRetentionAPI) getCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.gets
}

func (a *fakeStorageRetentionAPI) lastPut() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.puts) == 0 {
		return nil
	}

	return a.puts[len(a.puts)-1]
}

func testStorageRetentionClient(url string) *circleci.Client {
	return circleci.New(circleci.Config{PrivateHost: url, Token: "fake"})
}

func defaultStorageRetentionLimits() circleci.StorageRetentionLimits {
	return circleci.StorageRetentionLimits{
		Cache:     circleci.StorageRetentionBound{Min: 1, Max: 15},
		Workspace: circleci.StorageRetentionBound{Min: 1, Max: 15},
		Artifact:  circleci.StorageRetentionBound{Min: 1, Max: 30},
	}
}

func TestGetStorageRetention_HappyPath(t *testing.T) {
	t.Parallel()

	api := newFakeStorageRetentionAPI(t, defaultStorageRetentionLimits(), circleci.StorageRetentionControls{
		CacheDays: 15, WorkspaceDays: 15, ArtifactDays: 30,
	})
	client := testStorageRetentionClient(api.server.URL)

	got, err := client.GetStorageRetention(context.Background(), testStorageRetentionOrgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Controls.CacheDays != 15 || got.Controls.WorkspaceDays != 15 || got.Controls.ArtifactDays != 30 {
		t.Errorf("Controls: got %+v", got.Controls)
	}

	if got.Limits.Artifact.Min != 1 || got.Limits.Artifact.Max != 30 {
		t.Errorf("Limits.Artifact: got %+v, want min=1 max=30", got.Limits.Artifact)
	}
}

func TestGetStorageRetention_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	client := testStorageRetentionClient(srv.URL)

	_, err := client.GetStorageRetention(context.Background(), testStorageRetentionOrgID)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// TestSetStorageRetention_SendsExactFieldNames guards the wire format: the
// service's JSON keys are retention_days_cache/workspace/artifact, not the Go
// field names, and a mismatch here means every write silently no-ops against
// the real API while apply reports success.
func TestSetStorageRetention_SendsExactFieldNames(t *testing.T) {
	t.Parallel()

	api := newFakeStorageRetentionAPI(t, defaultStorageRetentionLimits(), circleci.StorageRetentionControls{})
	client := testStorageRetentionClient(api.server.URL)

	_, err := client.SetStorageRetention(context.Background(), testStorageRetentionOrgID, circleci.StorageRetentionControls{
		CacheDays: 10, WorkspaceDays: 7, ArtifactDays: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sent := api.lastPut()
	if sent == nil {
		t.Fatal("PUT was never received")
	}

	for key, want := range map[string]float64{
		"retention_days_cache":     10,
		"retention_days_workspace": 7,
		"retention_days_artifact":  1,
	} {
		got, ok := sent[key]
		if !ok {
			t.Errorf("request body missing %q; got keys %v", key, sent)

			continue
		}

		if got != want {
			t.Errorf("%s: got %v, want %v", key, got, want)
		}
	}
}

// TestSetStorageRetention_ReadsBackAfterWriting asserts the Get-after-Put
// contract that makes clamping visible: the PUT itself answers 204 with no
// body, so the only way to learn what CircleCI actually stored is a follow-up
// GET, and SetStorageRetention must perform it rather than trust the request it
// sent.
func TestSetStorageRetention_ReadsBackAfterWriting(t *testing.T) {
	t.Parallel()

	api := newFakeStorageRetentionAPI(t, defaultStorageRetentionLimits(), circleci.StorageRetentionControls{})
	client := testStorageRetentionClient(api.server.URL)

	got, err := client.SetStorageRetention(context.Background(), testStorageRetentionOrgID, circleci.StorageRetentionControls{
		CacheDays: 10, WorkspaceDays: 7, ArtifactDays: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if api.getCount() != 1 {
		t.Errorf("expected exactly 1 GET after the PUT, got %d", api.getCount())
	}

	if got.Controls.CacheDays != 10 || got.Controls.WorkspaceDays != 7 || got.Controls.ArtifactDays != 1 {
		t.Errorf("Controls: got %+v, want cache=10 workspace=7 artifact=1", got.Controls)
	}
}

// TestSetStorageRetention_ReportsClampedValues is the scenario the whole
// Get-after-Put design exists for: a value outside the plan's bounds is not
// rejected, it is silently clamped, and the only way a caller finds out is by
// reading the record back.
func TestSetStorageRetention_ReportsClampedValues(t *testing.T) {
	t.Parallel()

	api := newFakeStorageRetentionAPI(t, defaultStorageRetentionLimits(), circleci.StorageRetentionControls{})
	client := testStorageRetentionClient(api.server.URL)

	got, err := client.SetStorageRetention(context.Background(), testStorageRetentionOrgID, circleci.StorageRetentionControls{
		CacheDays:     1000, // plan max is 15
		WorkspaceDays: 0,    // plan min is 1
		ArtifactDays:  30,   // within bounds
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Controls.CacheDays != 15 {
		t.Errorf("CacheDays: got %d, want clamped to plan max 15", got.Controls.CacheDays)
	}

	if got.Controls.WorkspaceDays != 1 {
		t.Errorf("WorkspaceDays: got %d, want clamped to plan min 1", got.Controls.WorkspaceDays)
	}

	if got.Controls.ArtifactDays != 30 {
		t.Errorf("ArtifactDays: got %d, want unchanged at 30", got.Controls.ArtifactDays)
	}
}

// TestSetStorageRetention_PutErrorSkipsReadBack asserts SetStorageRetention
// does not paper over a failed write with an unrelated read: if the PUT fails,
// callers must see that error, not a GET result that may predate the write
// entirely.
func TestSetStorageRetention_PutErrorSkipsReadBack(t *testing.T) {
	t.Parallel()

	var gets int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets++
		}

		http.Error(w, `{"message":"invalid"}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	client := testStorageRetentionClient(srv.URL)

	_, err := client.SetStorageRetention(context.Background(), testStorageRetentionOrgID, circleci.StorageRetentionControls{
		CacheDays: 10, WorkspaceDays: 7, ArtifactDays: 1,
	})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	if gets != 0 {
		t.Errorf("expected no GET after a failed PUT, got %d", gets)
	}
}
