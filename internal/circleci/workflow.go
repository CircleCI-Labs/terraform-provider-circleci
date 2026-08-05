// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Workflow routes.
//
// Implemented directly in the core CircleCI v2 API backend and served on both
// CircleCI Cloud and CircleCI Server; see the comment on pipelineRunRoute in
// pipeline_run.go for why no requireCloud gating is needed.
//
// This is a read of mutable runtime state: Status changes as the workflow
// runs. Do not use this data source to derive a resource attribute, only for
// inspection and `check` blocks.
const (
	workflowRoute     = "/workflow/%s"
	workflowJobsRoute = "/workflow/%s/job"
)

// Workflow is one execution of a job graph within a pipeline run.
type Workflow struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
	StoppedAt       string `json:"stopped_at,omitempty"`
	PipelineID      string `json:"pipeline_id"`
	PipelineNumber  int    `json:"pipeline_number"`
	ProjectSlug     string `json:"project_slug"`
	StartedBy       string `json:"started_by"`
	CanceledBy      string `json:"canceled_by,omitempty"`
	ErroredBy       string `json:"errored_by,omitempty"`
	Tag             string `json:"tag,omitempty"`
	MaxAutoReruns   int    `json:"max_auto_reruns,omitempty"`
	AutoRerunNumber int    `json:"auto_rerun_number,omitempty"`
}

// WorkflowJob is one job within a workflow's job graph, as reported by the
// workflow's job listing. It is distinct from Job (job.go), which is a job
// looked up directly by project and job number and carries execution detail
// this listing does not (executor, timing, messages).
type WorkflowJob struct {
	ID                string              `json:"id"`
	Name              string              `json:"name"`
	Type              string              `json:"type"`
	Status            string              `json:"status"`
	StartedAt         string              `json:"started_at"`
	StoppedAt         string              `json:"stopped_at,omitempty"`
	Dependencies      []string            `json:"dependencies"`
	Requires          map[string][]string `json:"requires,omitempty"`
	ProjectSlug       string              `json:"project_slug"`
	JobNumber         int64               `json:"job_number,omitempty"`
	ApprovalRequestID string              `json:"approval_request_id,omitempty"`
	ApprovedBy        string              `json:"approved_by,omitempty"`
	CanceledBy        string              `json:"canceled_by,omitempty"`
}

// WorkflowService reads CircleCI workflows.
type WorkflowService struct {
	client *Client
}

// Workflows returns the workflow service for this client.
func (c *Client) Workflows() *WorkflowService {
	return &WorkflowService{client: c}
}

// Get fetches a single workflow by id. A missing workflow is reported as an
// error satisfying IsNotFound.
func (s *WorkflowService) Get(ctx context.Context, id string) (*Workflow, error) {
	var workflow Workflow
	if err := s.client.GetV2(ctx, workflowRoute, &workflow, RouteParams(id)); err != nil {
		return nil, err
	}

	return &workflow, nil
}

// ListJobs fetches every job in a workflow's job graph, following pagination
// to the last page. A missing workflow is reported as an error satisfying
// IsNotFound.
func (s *WorkflowService) ListJobs(ctx context.Context, workflowID string) ([]WorkflowJob, error) {
	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[WorkflowJob], error) {
		var page PaginatedResponse[WorkflowJob]
		err := s.client.GetV2(ctx, workflowJobsRoute, &page,
			RouteParams(workflowID),
			PageToken(pageToken),
		)

		return page, err
	})
}
