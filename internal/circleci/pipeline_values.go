// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// pipelineValuesRoute reports the pipeline.* values available to a pipeline
// run.
//
// Implemented directly in the CircleCI API in
// the API and proxied to the v2 API ("Circle") backend —
// the CircleCI API's fixture uses
// test.Circle, the same backend pipelineRunRoute in pipeline_run.go proxies
// to — so, like that route, this is available on both CircleCI Cloud and
// CircleCI Server and needs no requireCloud gating.
const pipelineValuesRoute = "/pipeline/%s/values"

// GetValues returns the built-in pipeline.* values available to the pipeline
// run identified by id: things like pipeline.id, pipeline.number,
// pipeline.project.git_url and pipeline.git.branch. These are the fixed set
// CircleCI documents at https://circleci.com/docs/reference/pipeline-variables/,
// not user-supplied pipeline parameters (see Trigger.Parameters for those).
//
// The route answers a flat JSON object whose values may be strings or
// numbers — handler_get_values_test.go's fixture includes pipeline.number as
// a JSON number rather than a string — so every value is rendered to a string
// with formatParameterValue, the same helper Trigger.ParameterStrings uses
// for the same reason: it is the only form Terraform's map attributes can
// hold. The result is nil when the route answers an empty object.
//
// A missing pipeline run answers an ordinary HTTP 404 (IsNotFound); an
// invalid (non-UUID) id answers 400, confirmed against
// handler_get_values_test.go's "400 response for invalid pipeline ID" case.
func (s *PipelineRunService) GetValues(ctx context.Context, id string) (map[string]string, error) {
	var raw map[string]any
	if err := s.client.GetV2(ctx, pipelineValuesRoute, &raw, RouteParams(id)); err != nil {
		return nil, err
	}

	if len(raw) == 0 {
		return nil, nil
	}

	rendered := make(map[string]string, len(raw))
	for key, value := range raw {
		rendered[key] = formatParameterValue(value)
	}

	return rendered, nil
}
