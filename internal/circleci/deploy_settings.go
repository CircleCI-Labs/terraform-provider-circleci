// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Deploy project settings route.
//
// Like deploy environments and components (see deploy_environment.go), this is
// served by the API behind the API, which proxies
// /api/v2/deploy/projects/{id}/settings to the API's
// /private/the API/v1/project/{id}/settings BFF route.
//
// This route is genuinely read-write: the API also registers
// `PATCH .../settings` (bff.the API). This
// provider exposes it as a data source only, because the two settings it
// carries are pipeline-definition ids, and pipeline definitions are already
// out of scope for this workstream (see pipeline_definition.go) — modelling
// the write side would require either duplicating that scope or taking a
// dependency on it. A future resource can add the write side without
// affecting this data source's shape.
//
// CircleCI Cloud only; see the comment on deployEnvironmentsRoute.
const deployProjectSettingsRoute = "/deploy/projects/%s/settings"

// DeploySettings is a project's the API settings: the pipeline
// definitions used for automatic deploys and rollbacks.
//
// Both fields are pointers because the API sends them as a pointer to
// a UUID with `omitempty`, so an unset definition is omitted from the response
// entirely rather than sent as a zero value.
type DeploySettings struct {
	RollbackPipelineDefinitionID *string `json:"rollback_pipeline_definition_id,omitempty"`
	DeployPipelineDefinitionID   *string `json:"deploy_pipeline_definition_id,omitempty"`
}

// DeploySettingsService reads a project's deploy settings.
type DeploySettingsService struct {
	client *Client
}

// DeployProjectSettings returns the deploy settings service for this client.
func (c *Client) DeployProjectSettings() *DeploySettingsService {
	return &DeploySettingsService{client: c}
}

// Get fetches the deploy settings for a project by its the API
// project id (the CircleCI project UUID, not a VCS slug). A missing project is
// reported as an error satisfying IsNotFound.
func (s *DeploySettingsService) Get(ctx context.Context, projectID string) (*DeploySettings, error) {
	var settings DeploySettings
	if err := s.client.GetV2(ctx, deployProjectSettingsRoute, &settings, RouteParams(projectID)); err != nil {
		return nil, err
	}

	return &settings, nil
}
