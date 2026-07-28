// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"strconv"
)

// Pipeline run routes.
//
// "Pipeline" already names a pipeline *definition* in this provider (see
// pipeline_definition.go and the circleci_pipeline resource/data source,
// which predate this file and wrap the legacy circleci-sdk-go client). The
// v3 API renames the run concept to "run" for exactly this reason; this
// provider follows that naming for anything new rather than overloading
// "pipeline" further; see DESIGN.md.
//
// These routes are implemented directly in the CircleCI v2 API v2 API
// (the CircleCI API, the CircleCI API and
// the CircleCI API) and are served on both CircleCI Cloud and CircleCI Server: the
// API's web process ("frontend", image the API-api) is what CircleCI
// Server's gateway catch-all route for /api routes to, so no requireCloud gating
// is needed here.
const (
	pipelineRunRoute       = "/pipeline/%s"
	pipelineRunConfigRoute = "/pipeline/%s/config"
)

// PipelineRunActor is the person or integration that triggered a pipeline run.
type PipelineRunActor struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

// PipelineRunTrigger summarizes what triggered a pipeline run.
type PipelineRunTrigger struct {
	Type       string           `json:"type"`
	ReceivedAt string           `json:"received_at"`
	Actor      PipelineRunActor `json:"actor"`
}

// PipelineRunCommit is the latest commit associated with a pipeline run's VCS
// trigger.
type PipelineRunCommit struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// PipelineRunVCS is the VCS information for a pipeline run. It is absent for
// pipelines triggered directly through the API (trigger.type == "api") rather
// than by a VCS event.
type PipelineRunVCS struct {
	ProviderName        string             `json:"provider_name"`
	OriginRepositoryURL string             `json:"origin_repository_url"`
	TargetRepositoryURL string             `json:"target_repository_url"`
	Revision            string             `json:"revision"`
	Branch              string             `json:"branch,omitempty"`
	Tag                 string             `json:"tag,omitempty"`
	Commit              *PipelineRunCommit `json:"commit,omitempty"`
	ReviewID            string             `json:"review_id,omitempty"`
	ReviewURL           string             `json:"review_url,omitempty"`
}

// PipelineRunError describes one error encountered while processing a
// pipeline run's configuration.
type PipelineRunError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// PipelineRunWarning describes one warning encountered while processing a
// pipeline run's configuration.
type PipelineRunWarning struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// PipelineRun is one execution ("run") of a CircleCI pipeline: a point-in-time
// record of a triggered build, its VCS context and its processing state.
//
// This is a read of mutable runtime state: State, Errors and Warnings change
// as the run's configuration is fetched and compiled, so every refresh can
// return a different value until the run reaches a terminal state. Do not use
// this data source to derive a resource attribute — only for inspection and
// `check` blocks. See circleci_pipeline_run's documentation.
type PipelineRun struct {
	ID                string               `json:"id"`
	Number            int                  `json:"number"`
	ProjectSlug       string               `json:"project_slug"`
	CreatedAt         string               `json:"created_at"`
	UpdatedAt         string               `json:"updated_at,omitempty"`
	Errors            []PipelineRunError   `json:"errors"`
	Warnings          []PipelineRunWarning `json:"warnings"`
	State             string               `json:"state"`
	Trigger           PipelineRunTrigger   `json:"trigger"`
	VCS               *PipelineRunVCS      `json:"vcs,omitempty"`
	TriggerParameters map[string]any       `json:"trigger_parameters,omitempty"`
}

// PipelineRunConfig is the source and compiled configuration for a pipeline
// run. SetupConfig and CompiledSetupConfig are present only when the pipeline
// used setup workflows to generate follow-on configuration.
type PipelineRunConfig struct {
	Source              string `json:"source"`
	Compiled            string `json:"compiled"`
	SetupConfig         string `json:"setup_config,omitempty"`
	CompiledSetupConfig string `json:"compiled_setup_config,omitempty"`
}

// PipelineRunService reads CircleCI pipeline runs.
type PipelineRunService struct {
	client *Client
}

// PipelineRuns returns the pipeline run service for this client.
func (c *Client) PipelineRuns() *PipelineRunService {
	return &PipelineRunService{client: c}
}

// Get fetches a single pipeline run by id. A missing run is reported as an
// error satisfying IsNotFound.
func (s *PipelineRunService) Get(ctx context.Context, id string) (*PipelineRun, error) {
	var run PipelineRun
	if err := s.client.GetV2(ctx, pipelineRunRoute, &run, RouteParams(id)); err != nil {
		return nil, err
	}

	return &run, nil
}

// GetByNumber fetches a single pipeline run by project slug and pipeline
// number. The slug is escaped per path segment rather than as one opaque
// value, so its "/" separators stay literal instead of becoming "%2F" (which
// intermediate proxies reject and CircleCI Server does not match); see
// checkoutKeyProjectPath in checkout_key.go, reused here.
func (s *PipelineRunService) GetByNumber(ctx context.Context, projectSlug string, number int) (*PipelineRun, error) {
	slug, err := checkoutKeyProjectPath(projectSlug)
	if err != nil {
		return nil, err
	}

	var run PipelineRun
	route := "/project/" + slug + "/pipeline/" + strconv.Itoa(number)
	if err := s.client.GetV2(ctx, route, &run); err != nil {
		return nil, err
	}

	return &run, nil
}

// GetConfig fetches the source and compiled configuration for a pipeline run
// by id. A missing run is reported as an error satisfying IsNotFound.
func (s *PipelineRunService) GetConfig(ctx context.Context, id string) (*PipelineRunConfig, error) {
	var config PipelineRunConfig
	if err := s.client.GetV2(ctx, pipelineRunConfigRoute, &config, RouteParams(id)); err != nil {
		return nil, err
	}

	return &config, nil
}
