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
// It rejects a PUT carrying any value outside the configured bounds with 400
// and leaves the stored record untouched, and answers an accepted PUT with 204
// No Content and an empty body — both measured [NET] against gitlab-test on
// 2026-08-21 (see storage_retention.go's SetStorageRetention doc comment for
// the detail). An earlier version of this fake clamped instead of rejecting; it
// was wrong, not merely unconfirmed — the real endpoint answers 400 and applies
// nothing, so a fake that clamped could pass a test asserting behaviour the
// real API cannot produce.
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
		if !inBound(requested.CacheDays, a.limits.Cache) ||
			!inBound(requested.WorkspaceDays, a.limits.Workspace) ||
			!inBound(requested.ArtifactDays, a.limits.Artifact) {
			a.mu.Unlock()
			// Matches the measured [NET] shape: reject, apply nothing.
			http.Error(w, `{"error":"Invalid value given"}`, http.StatusBadRequest)

			return
		}

		a.puts = append(a.puts, raw)
		a.current = requested
		a.mu.Unlock()

		// 204 No Content, no body — the real route answers this way. A fake that
		// echoed the stored record back here would let SetStorageRetention appear
		// to work without ever issuing the read-back GET it depends on to report
		// the truth.
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// inBound reports whether v falls within [bound.Min, bound.Max], inclusive —
// the real route's measured acceptance range; anything else is a 400.
func inBound(v int64, bound circleci.StorageRetentionBound) bool {
	return v >= bound.Min && v <= bound.Max
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

// TestSetStorageRetention_RejectsOutOfBoundsValue replaces a test that used to
// assert the opposite of measured behaviour (that an out-of-bounds value gets
// silently clamped to the nearest bound). [NET] measurement against
// gitlab-test (2026-08-21) shows CircleCI rejects the whole write with 400
// instead, and applies none of the three fields — not even the two that were
// within bounds. See SetStorageRetention's doc comment for the full
// measurement.
func TestSetStorageRetention_RejectsOutOfBoundsValue(t *testing.T) {
	t.Parallel()

	initial := circleci.StorageRetentionControls{CacheDays: 5, WorkspaceDays: 5, ArtifactDays: 5}
	api := newFakeStorageRetentionAPI(t, defaultStorageRetentionLimits(), initial)
	client := testStorageRetentionClient(api.server.URL)

	_, err := client.SetStorageRetention(context.Background(), testStorageRetentionOrgID, circleci.StorageRetentionControls{
		CacheDays:     1000, // plan max is 15
		WorkspaceDays: 7,    // within bounds
		ArtifactDays:  20,   // within bounds
	})
	if err == nil {
		t.Fatal("expected an error for a cache value above the plan max, got nil")
	}
	if circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(err) = true, want false: this is a validation rejection, not a missing resource")
	}

	// The whole write must have been rejected, not partially applied: the two
	// in-bounds fields must be exactly what they were before, not the new
	// values that accompanied the rejected one.
	got, getErr := client.GetStorageRetention(context.Background(), testStorageRetentionOrgID)
	if getErr != nil {
		t.Fatalf("GetStorageRetention after the rejected write: %v", getErr)
	}
	if got.Controls != initial {
		t.Errorf("Controls after a rejected write = %+v, want unchanged at %+v", got.Controls, initial)
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
