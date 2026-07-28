// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"time"
)

// Deploy component routes.
//
// Like deploy environments (see deploy_environment.go), these are served by
// the API behind the API, which proxies
// /api/v2/deploy/components to the API's
// /private/the API/v1/component BFF routes. Components are declared in
// a project's .circleci/config.yml (the `component` key on a deploy job), so
// there is no create, update or delete here.
//
// CircleCI Cloud only; see the comment on deployEnvironmentsRoute.
const (
	deployComponentsRoute        = "/deploy/components"
	deployComponentRoute         = "/deploy/components/%s"
	deployComponentVersionsRoute = "/deploy/components/%s/versions"
)

// DeployComponent is a CircleCI deploy/release component: a named deployable
// unit tracked across its versions and environments.
//
// ProjectID is a pointer because the API encodes it as a nullable
// UUID (Go's uuid.NullUUID), sent as either a quoted string or JSON null —
// never omitted — so a component with no associated CircleCI project decodes
// to a nil pointer here rather than a zero UUID string.
type DeployComponent struct {
	ID           string        `json:"id"`
	ProjectID    *string       `json:"project_id"`
	Name         string        `json:"name"`
	ReleaseCount int64         `json:"release_count"`
	Labels       []EntityLabel `json:"labels"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	ArchivedAt   *time.Time    `json:"archived_at,omitempty"`
}

// DeployComponentVersion is one published version of a deploy component, as
// last observed in a given environment.
//
// PipelineID, WorkflowID and JobID are plain (non-nullable) UUID fields on the
// wire: the API's Go type uses uuid.UUID rather than uuid.NullUUID for
// these three, so encoding/json's omitempty never applies (a fixed-size array
// type is never "empty") and an unset association serializes as the all-zero
// UUID string rather than being omitted or null. ZeroUUID reports that
// sentinel so callers do not mistake it for a real identifier.
type DeployComponentVersion struct {
	Name           string    `json:"name"`
	Namespace      string    `json:"namespace"`
	EnvironmentID  string    `json:"environment_id"`
	IsLive         bool      `json:"is_live"`
	PipelineID     string    `json:"pipeline_id"`
	WorkflowID     string    `json:"workflow_id"`
	JobID          string    `json:"job_id"`
	JobNumber      int64     `json:"job_number,omitempty"`
	LastDeployedAt time.Time `json:"last_deployed_at"`
}

// ZeroUUID is the all-zero UUID string the API sends for a
// DeployComponentVersion association (pipeline, workflow or job) that was
// never recorded. See the comment on DeployComponentVersion.
const ZeroUUID = "00000000-0000-0000-0000-000000000000"

// DeployComponentService reads CircleCI deploy components.
type DeployComponentService struct {
	client *Client
}

// DeployComponents returns the deploy component service for this client.
func (c *Client) DeployComponents() *DeployComponentService {
	return &DeployComponentService{client: c}
}

// Get fetches a single deploy component by id. A missing component is
// reported as an error satisfying IsNotFound.
func (s *DeployComponentService) Get(ctx context.Context, id string) (*DeployComponent, error) {
	var component DeployComponent
	if err := s.client.GetV2(ctx, deployComponentRoute, &component, RouteParams(id)); err != nil {
		return nil, err
	}

	return &component, nil
}

// List fetches every deploy component in an organization, following
// pagination to the last page. projectID and name filter the result when
// non-empty; either may be left empty to fetch every component.
func (s *DeployComponentService) List(ctx context.Context, orgID, projectID, name string) ([]DeployComponent, error) {
	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[DeployComponent], error) {
		var page PaginatedResponse[DeployComponent]
		err := s.client.GetV2(ctx, deployComponentsRoute, &page,
			Query("org-id", orgID),
			OptionalQuery("project-id", projectID),
			OptionalQuery("component-name", name),
			PageToken(pageToken),
		)

		return page, err
	})
}

// ListVersions fetches every published version of a component, following
// pagination to the last page. The result is nil when the component has no
// recorded versions.
func (s *DeployComponentService) ListVersions(ctx context.Context, componentID string) ([]DeployComponentVersion, error) {
	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[DeployComponentVersion], error) {
		var page PaginatedResponse[DeployComponentVersion]
		err := s.client.GetV2(ctx, deployComponentVersionsRoute, &page,
			RouteParams(componentID),
			PageToken(pageToken),
		)

		return page, err
	})
}
