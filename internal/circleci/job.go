// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"strconv"
)

// Job route.
//
// GET /api/v2/jobs/{id} — the route the CircleCI v2 API reference documents
// for looking a job up by its UUID — is a pass-through to a backend that only
// exists on CircleCI Cloud, and CircleCI Server's gateway has no
// /api/v2/jobs/* route at all. That makes it Cloud-only with no fallback.
//
// GET /api/v2/project/{slug}/job/{job-number} is used here instead, because it
// is served on both CircleCI Cloud and CircleCI Server. Addressing a job by
// project slug and number rather than by UUID is a real difference from the
// documented route, and is called out in this data source's documentation.
//
// This is a read of mutable runtime state: Status changes as the job runs. Do
// not use this data source to derive a resource attribute, only for
// inspection and `check` blocks.

// JobExecutor describes the compute the job ran on. Both fields are pointers
// because the API only populates them once the job has been dispatched, so an
// unscheduled job reports a nil executor and a dispatched-but-not-yet-allocated
// job may still have either
// field unset.
type JobExecutor struct {
	ResourceClass *string `json:"resource_class"`
	Type          *string `json:"type"`
}

// JobMessage is one message CircleCI's execution platform attached to a job,
// such as a resource-class fallback notice.
type JobMessage struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Reason  string `json:"reason,omitempty"`
}

// JobParallelRun is the status of one parallel run ("task") of a job with
// parallelism greater than one. Per-run step detail is not exposed here for
// the same reason artifacts and test results are not: it is per-build detail
// that would make this data source read as a partial build log rather than a
// job summary.
type JobParallelRun struct {
	Index  int    `json:"index"`
	Status string `json:"status"`
}

// JobContext is one context a job used, by name only.
type JobContext struct {
	Name string `json:"name"`
}

// JobLatestWorkflow identifies the workflow a job was most recently part of.
type JobLatestWorkflow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// JobPipeline identifies the pipeline run a job belongs to.
type JobPipeline struct {
	ID string `json:"id"`
}

// JobProject identifies the project a job belongs to.
type JobProject struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	ExternalURL string `json:"external_url"`
}

// JobOrganization identifies the organization a job's project belongs to.
type JobOrganization struct {
	Name string `json:"name"`
}

// Job is a single job execution, looked up by project slug and job number.
//
// Number, Name, Contexts, LatestWorkflow, Organization, Parallelism,
// Pipeline, Project and WebURL are computed elsewhere in the v2 API and merged
// into this response verbatim, so their shape is defined by the older v2 job
// representation rather than by this route; the other fields reflect the job's
// current runtime state directly.
type Job struct {
	CreatedAt      string             `json:"created_at,omitempty"`
	Duration       *int64             `json:"duration"`
	Executor       *JobExecutor       `json:"executor,omitempty"`
	Messages       []JobMessage       `json:"messages"`
	QueuedAt       string             `json:"queued_at,omitempty"`
	StartedAt      string             `json:"started_at,omitempty"`
	Status         string             `json:"status"`
	StoppedAt      string             `json:"stopped_at,omitempty"`
	ParallelRuns   []JobParallelRun   `json:"parallel_runs,omitempty"`
	Contexts       []JobContext       `json:"contexts,omitempty"`
	LatestWorkflow *JobLatestWorkflow `json:"latest_workflow,omitempty"`
	Name           string             `json:"name,omitempty"`
	Number         int64              `json:"number,omitempty"`
	Organization   *JobOrganization   `json:"organization,omitempty"`
	Parallelism    int                `json:"parallelism,omitempty"`
	Pipeline       *JobPipeline       `json:"pipeline,omitempty"`
	Project        *JobProject        `json:"project,omitempty"`
	WebURL         string             `json:"web_url,omitempty"`
}

// JobService reads CircleCI jobs.
type JobService struct {
	client *Client
}

// Jobs returns the job service for this client.
func (c *Client) Jobs() *JobService {
	return &JobService{client: c}
}

// Get fetches a single job by project slug and job number. The slug is
// escaped per path segment; see the comment on PipelineRunService.GetByNumber
// in pipeline_run.go.
func (s *JobService) Get(ctx context.Context, projectSlug string, jobNumber int64) (*Job, error) {
	slug, err := checkoutKeyProjectPath(projectSlug)
	if err != nil {
		return nil, err
	}

	var job Job
	route := "/project/" + slug + "/job/" + strconv.FormatInt(jobNumber, 10)
	if err := s.client.GetV2(ctx, route, &job); err != nil {
		return nil, err
	}

	return &job, nil
}
