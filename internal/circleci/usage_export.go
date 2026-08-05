// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"time"
)

// Usage export routes.
//
// This is a v2 surface: a backend service owns the two public routes itself
// and proxies each onward to another, downstream backend service, whose own
// paths are the plural /organizations/{org}/usage_export_jobs[/{id}]. That
// downstream service is where every rule below is actually enforced.
//
// That last detail is what decides availability. The route is not v3, so
// nothing about the API version rules CircleCI Server out -- and Server's own
// gateway does route both paths through. But Server does not deploy the
// downstream service at all: the routes are wired to a placeholder upstream
// for that one backend. So these two routes exist on Server and cannot
// succeed there, which is a worse failure than a 404 and the reason callers
// should gate on Client.IsCloud rather than trust the version.
const (
	usageExportJobsRoute = "/organizations/%s/usage_export_job"
	usageExportJobRoute  = "/organizations/%s/usage_export_job/%s"
)

// Usage export job states. There are exactly four, and both the public schema
// and the backend's own enum agree on them: a job starts "created", moves to
// "processing", and ends at either "completed" or "failed".
//
// The set is closed, and narrower than the states the backing store distinguishes
// internally: the store separates a query failure from a client error and from an
// unknown error, and the API collapses all three onto "failed". A store state the
// API does not recognize is reported as "created". So a caller polling for a
// terminal state never has to handle a fifth value, and "failed" is the only
// terminal failure however the export actually broke.
const (
	UsageExportJobStateCreated    = "created"
	UsageExportJobStateProcessing = "processing"
	UsageExportJobStateCompleted  = "completed"
	UsageExportJobStateFailed     = "failed"
)

// Usage export window limits, enforced by the create route's own handler.
// Each is a separate 400 with its own message, all four checked before the export
// is queued, so a caller can reject the same configurations up front and give a
// better diagnostic than the API's.
const (
	// UsageExportMaxWindow is the widest End-minus-Start the create handler
	// accepts. Strictly greater than 31 days is refused.
	UsageExportMaxWindow = 31 * 24 * time.Hour
	// UsageExportMaxAge is how far back Start may reach. The handler compares
	// against now minus 366 days, so this is a wall-clock limit a caller cannot
	// check without agreeing on "now".
	UsageExportMaxAge = 366 * 24 * time.Hour
	// UsageExportURLValidity is how long the signed download URLs a completed job
	// hands back remain usable. They are minted per request, at the moment a job
	// is observed to be complete, rather than when the job finished -- so the
	// clock starts at the poll that returned them.
	UsageExportURLValidity = 36 * time.Hour
)

// CreateUsageExportJobRequest is the body of a usage export job create.
//
// Start and End are strings here, but not at the far end: the backend binds
// both into time.Time, so anything Go's RFC 3339 decoder refuses is a 400
// "malformed request body" that names no field. They are checked in order against
// End-before-Start, Start older than UsageExportMaxAge, either one in the future,
// and a window wider than UsageExportMaxWindow.
//
// SharedOrgIDs is omitted when empty: the route rejects a body key it does not
// recognize outright (400 "Unexpected field"), which makes sending only what
// the caller set the safe default rather than a nicety. It is honoured rather
// than ignored -- it reaches the export query as an additional set of
// organizations to report usage for -- but the backend binds it to a slice of
// UUIDs, so a single non-UUID entry fails the whole body with the same
// unhelpful "malformed request body" a bad timestamp gives.
type CreateUsageExportJobRequest struct {
	// Start and End bound the export, as RFC 3339 timestamps.
	Start string `json:"start"`
	End   string `json:"end"`
	// SharedOrgIDs additionally reports usage for organizations that share
	// billing with the requesting organization. Every entry must be a UUID.
	SharedOrgIDs []string `json:"shared_org_ids,omitempty"`
}

// UsageExportJob is the response to creating a usage export job.
//
// This is deliberately a different type from UsageExportJobStatus: the create
// route's response (the "usage_export_job" schema) echoes Start and End, but
// the get route's response (the "get_usage_export_job_status" schema) does not
// carry either field and instead reports ErrorReason once a job has failed.
// Collapsing both into one struct would silently zero-value fields the real
// API never sends, which is exactly the kind of client/mock agreement bug this
// package's tests are written to catch.
// State on a create response is always "created": the route hard-codes it
// rather than reporting whatever the queued query has already become. Start and
// End are echoes of the request re-serialized from time.Time, so they come back
// normalized (a UTC offset written as +00:00 returns as Z, trailing zeros in
// fractional seconds are dropped) and must not be compared byte-for-byte against
// what was sent. DownloadURLs is always null here -- URLs are only minted for a
// job already observed complete.
type UsageExportJob struct {
	UsageExportJobID string   `json:"usage_export_job_id"`
	State            string   `json:"state"`
	Start            string   `json:"start"`
	End              string   `json:"end"`
	DownloadURLs     []string `json:"download_urls"`
}

// UsageExportJobStatus is the response to polling a usage export job. See
// UsageExportJob for why this is not the same type.
type UsageExportJobStatus struct {
	UsageExportJobID string `json:"usage_export_job_id"`
	State            string `json:"state"`
	// DownloadURLs is null for every state but "completed", and for "completed"
	// it is minted fresh on each request and valid for UsageExportURLValidity
	// from then. Each URL is itself the credential: it grants the download with
	// no further authentication.
	DownloadURLs []string `json:"download_urls"`
	// ErrorReason explains a "failed" state. It is empty for every other state.
	//
	// The omitempty is a faithful copy of the public route's own tag rather than a
	// convenience: the downstream backend always emits the key, but the public
	// route drops it when empty, so absent and empty have to mean the same thing
	// here.
	ErrorReason string `json:"error_reason,omitempty"`
}

// UsageExportService creates and polls usage export jobs.
type UsageExportService struct {
	client *Client
}

// UsageExports returns the usage export service for this client.
func (c *Client) UsageExports() *UsageExportService {
	return &UsageExportService{client: c}
}

// Create starts a usage export job. Its download URLs are never ready
// immediately — the returned job's State is "created" or "processing" — so
// callers must poll Get until State is "completed" or "failed".
func (s *UsageExportService) Create(ctx context.Context, orgID string, req CreateUsageExportJobRequest) (*UsageExportJob, error) {
	var job UsageExportJob
	if err := s.client.PostV2(ctx, usageExportJobsRoute, req, &job, RouteParams(orgID)); err != nil {
		return nil, err
	}

	return &job, nil
}

// Get fetches the current status of a usage export job. A missing job is
// reported as an error satisfying IsNotFound.
func (s *UsageExportService) Get(ctx context.Context, orgID, jobID string) (*UsageExportJobStatus, error) {
	var status UsageExportJobStatus
	if err := s.client.GetV2(ctx, usageExportJobRoute, &status, RouteParams(orgID, jobID)); err != nil {
		return nil, err
	}

	return &status, nil
}
