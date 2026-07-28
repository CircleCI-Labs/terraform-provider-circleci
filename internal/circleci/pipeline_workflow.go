// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// pipelineWorkflowsRoute lists the workflows belonging to one pipeline run.
//
// This closes the middle of a chain the provider already exposed at both ends:
// circleci_workflow reads a workflow by id and circleci_workflow_jobs reads that
// workflow's jobs, but nothing got from a pipeline to its workflows, so the only
// way in was to already know a workflow id.
//
// Served by the v2 API on both Cloud and Server, like the other workflow
// routes — see the comment on workflowRoute in workflow.go.
//
// The same caveat applies as to the rest of the workflow surface: this is mutable
// runtime state. Status changes as the pipeline runs, so it is for inspection and
// `check` blocks, not for deriving a resource attribute.
const pipelineWorkflowsRoute = "/pipeline/%s/workflow"

// ListByPipeline fetches every workflow in a pipeline run, following pagination to
// the last page. A missing pipeline is reported as an error satisfying IsNotFound.
//
// It lives on WorkflowService rather than a pipeline service because what it
// returns is a Workflow: keeping the decoding next to the type avoids a second
// wire struct drifting from this one.
func (s *WorkflowService) ListByPipeline(ctx context.Context, pipelineID string) ([]Workflow, error) {
	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[Workflow], error) {
		var page PaginatedResponse[Workflow]
		err := s.client.GetV2(ctx, pipelineWorkflowsRoute, &page,
			RouteParams(pipelineID),
			PageToken(pageToken),
		)

		return page, err
	})
}
