// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Deploy project settings route.
//
// Like deploy environments and components (see deploy_environment.go), this is
// served by a backend behind a proxy, which forwards
// /api/v2/deploy/projects/{id}/settings to that backend's own private route.
//
// The backend's own route is genuinely read-write: it also registers
// `PATCH .../settings`. But the public proxy only forwards this GET — PATCH on
// the public path answers 404, while the same PATCH against the private route
// answers 400 for an empty body, which only happens if the route exists and the
// token authenticated. So the write half is real but is not exposed publicly.
// This provider exposes the route as a data
// source only, both for that reason and because the two settings it carries
// are pipeline-definition ids, and pipeline definitions are already out of
// scope for this workstream (see pipeline_definition.go) — modelling the write
// side would require either duplicating that scope or taking a dependency on
// it. A future resource can add the write side without affecting this data
// source's shape, once the public route forwards PATCH.
//
// CircleCI Cloud only; see the comment on deployEnvironmentsRoute.
const deployProjectSettingsRoute = "/deploy/projects/%s/settings"

// DeploySettings is a project's deploy settings: the pipeline definitions
// used for automatic deploys and rollbacks.
//
// Both fields are pointers because the backend sends them as a pointer to
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

// Get fetches the deploy settings for a project by its backend
// project id (the CircleCI project UUID, not a VCS slug). A missing project is
// reported as an error satisfying IsNotFound.
func (s *DeploySettingsService) Get(ctx context.Context, projectID string) (*DeploySettings, error) {
	var settings DeploySettings
	if err := s.client.GetV2(ctx, deployProjectSettingsRoute, &settings, RouteParams(projectID)); err != nil {
		return nil, err
	}

	return &settings, nil
}
