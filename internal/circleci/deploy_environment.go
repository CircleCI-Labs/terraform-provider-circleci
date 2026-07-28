// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"time"
)

// Deploy environment routes.
//
// These are served by the API behind the the API facade,
// which proxies /api/v2/deploy/environments to the API's
// /private/the API/v1/environment BFF route. Environments are declared
// in a project's .circleci/config.yml (the `environment` key on a deploy job),
// not created through this API, so there is deliberately no create, update or
// delete here.
//
// the API is not deployed on CircleCI Server: the gateway routes served
// (the CircleCI Server deployment configuration) routes
// the API for project settings, pipeline/run, groups, usage export
// jobs and orbs/namespaces only, never for /api/v2/deploy/*. Callers must gate
// on Client.IsCloud (see requireCloud in internal/provider).
const (
	deployEnvironmentsRoute = "/deploy/environments"
	deployEnvironmentRoute  = "/deploy/environments/%s"
)

// EntityLabel is a key/value label attached to a deploy environment or
// component.
type EntityLabel struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// DeployEnvironment is a CircleCI deploy/release environment: a named
// deployment target (e.g. "staging", "prod") that components deploy into.
//
// Environments are declared in configuration and appear here once CircleCI has
// observed a deploy job reference them, so this type is read-only.
type DeployEnvironment struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	Labels      []EntityLabel `json:"labels"`
	Description string        `json:"description,omitempty"`
}

// DeployEnvironmentService reads CircleCI deploy environments.
type DeployEnvironmentService struct {
	client *Client
}

// DeployEnvironments returns the deploy environment service for this client.
func (c *Client) DeployEnvironments() *DeployEnvironmentService {
	return &DeployEnvironmentService{client: c}
}

// Get fetches a single deploy environment by id. A missing environment is
// reported as an error satisfying IsNotFound.
func (s *DeployEnvironmentService) Get(ctx context.Context, id string) (*DeployEnvironment, error) {
	var env DeployEnvironment
	if err := s.client.GetV2(ctx, deployEnvironmentRoute, &env, RouteParams(id)); err != nil {
		return nil, err
	}

	return &env, nil
}

// List fetches every deploy environment in an organization, following
// pagination to the last page. The result is nil when the organization has no
// deploy environments.
func (s *DeployEnvironmentService) List(ctx context.Context, orgID string) ([]DeployEnvironment, error) {
	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[DeployEnvironment], error) {
		var page PaginatedResponse[DeployEnvironment]
		err := s.client.GetV2(ctx, deployEnvironmentsRoute, &page,
			Query("org-id", orgID),
			PageToken(pageToken),
		)

		return page, err
	})
}
