// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Usage export routes.
//
// This is a v2 surface (the CircleCI API in the API, which
// proxies to the API) and is served on both CircleCI Cloud and
// CircleCI Server, unlike the v3-only entities elsewhere in this package.
const (
	usageExportJobsRoute = "/organizations/%s/usage_export_job"
	usageExportJobRoute  = "/organizations/%s/usage_export_job/%s"
)

// Usage export job states, exactly as enumerated by the API's
// openapi_definitions/v2_endpoints/usage_export/schemas.yaml. A job starts
// "created", moves to "processing", and ends at either "completed" or
// "failed" — there is no other terminal state.
const (
	UsageExportJobStateCreated    = "created"
	UsageExportJobStateProcessing = "processing"
	UsageExportJobStateCompleted  = "completed"
	UsageExportJobStateFailed     = "failed"
)

// CreateUsageExportJobRequest is the body of a usage export job create.
//
// SharedOrgIDs is omitted when empty: the handler's Validate only requires
// Start and End, so a plain export must not send a field the caller never set.
type CreateUsageExportJobRequest struct {
	// Start and End bound the export, as RFC 3339 timestamps.
	Start string `json:"start"`
	End   string `json:"end"`
	// SharedOrgIDs additionally reports usage for organizations that share
	// billing with the requesting organization.
	SharedOrgIDs []string `json:"shared_org_ids,omitempty"`
}

// UsageExportJob is the response to creating a usage export job.
//
// This is deliberately a different type from UsageExportJobStatus: the create
// handler's response (the CircleCI API's responseItems, and the
// "usage_export_job" schema) echoes Start and End, but the get handler's
// response (getReport, and the "get_usage_export_job_status" schema) does not
// carry either field and instead reports ErrorReason once a job has failed.
// Collapsing both into one struct would silently zero-value fields the real
// API never sends, which is exactly the kind of client/mock agreement bug this
// package's tests are written to catch.
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
	UsageExportJobID string   `json:"usage_export_job_id"`
	State            string   `json:"state"`
	DownloadURLs     []string `json:"download_urls"`
	// ErrorReason explains a "failed" state. It is empty for every other state.
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
