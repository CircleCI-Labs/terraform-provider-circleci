// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Pipeline definition routes.
//
// These live under /api/v2 but are served by the public API service rather than
// the core v2 API backend, and CircleCI Server does not route pipeline-definitions
// there. Callers must gate on Client.IsCloud before using them; a Server
// installation answers HTTP 404, which is indistinguishable from a project that
// does not exist.
const pipelineDefinitionsRoute = "/projects/%s/pipeline-definitions"

// pipelineDefinitionRoute addresses a single pipeline definition by id.
const pipelineDefinitionRoute = "/projects/%s/pipeline-definitions/%s"

// RepoInput is the create/update body for a repository reference: only the
// caller-supplied external id. FullName is never accepted on write — it is
// resolved by the server from ExternalID; both the pipeline-definition and
// trigger create bodies carry only external_id.
type RepoInput struct {
	ExternalID string `json:"external_id"`
}

// Repo identifies a source repository within a pipeline definition or trigger.
// Both fields are omitted by the API when empty, and the whole object is omitted
// when neither is known, so a definition with no repository decodes to the zero
// value rather than an error.
type Repo struct {
	FullName   string `json:"full_name"`
	ExternalID string `json:"external_id"`
}

// PipelineConfigSource says where a pipeline's configuration is read from.
type PipelineConfigSource struct {
	Provider string `json:"provider"`
	Repo     Repo   `json:"repo"`
	FilePath string `json:"file_path"`
}

// The config_source providers the pipeline-definition endpoints accept.
//
// Confirmed against the API's own OpenAPI schema, whose config_source is a
// two-branch oneOf.
const (
	// PipelineConfigSourceProviderGitHubApp is CircleCI's GitHub App integration.
	PipelineConfigSourceProviderGitHubApp = "github_app"
	// PipelineConfigSourceProviderGitHubServer is a GitHub Enterprise Server
	// installation.
	PipelineConfigSourceProviderGitHubServer = "github_server"
	// PipelineConfigSourceProviderCircleCI is a CircleCI-hosted configuration: the
	// pipeline's configuration is not read from a VCS repository at all, so this is
	// the one config_source provider with no repo.
	//
	// The schema's "circleci" branch is `additionalProperties: false` and lists only
	// `provider` and `file_path` (both `required`) — no `repo` property at all — so a
	// repo on this branch fails the oneOf outright; it is not merely ignored.
	PipelineConfigSourceProviderCircleCI = "circleci"
)

// PipelineConfigSourceProviders returns every provider config_source_provider
// accepts.
//
// It exists so that the resource's schema validator, its attribute description and
// this client cannot drift from one another — the same reason
// TriggerEventSourceProviders exists.
func PipelineConfigSourceProviders() []string {
	return []string{
		PipelineConfigSourceProviderGitHubApp,
		PipelineConfigSourceProviderGitHubServer,
		PipelineConfigSourceProviderCircleCI,
	}
}

// PipelineConfigSourceProviderNeedsRepo reports whether a config_source provider
// takes a repository. Every provider does except circleci, whose configuration is
// hosted by CircleCI itself rather than checked out from anywhere.
func PipelineConfigSourceProviderNeedsRepo(provider string) bool {
	return provider != PipelineConfigSourceProviderCircleCI
}

// PipelineCheckoutSourceProviders returns every provider checkout_source_provider
// accepts.
//
// Unlike config_source, checkout_source has no CircleCI-hosted branch: the schema
// declares it as a single object (not a oneOf), with `provider` restricted to
// `[github_app, github_server]` and `repo` unconditionally `required`. A pipeline
// definition therefore always needs a real repository to check code out from, even
// when its configuration is hosted by CircleCI itself — confirmed by a test case
// that pairs config_source.provider "circleci" with checkout_source.provider
// "circleci" and gets back a 400 naming "/checkout_source/provider" as not one
// of the allowed values.
func PipelineCheckoutSourceProviders() []string {
	return []string{
		PipelineConfigSourceProviderGitHubApp,
		PipelineConfigSourceProviderGitHubServer,
	}
}

// PipelineCheckoutSource says which repository a pipeline checks out. It is
// distinct from the config source, because a pipeline may take its
// configuration from one repository and its code from another.
type PipelineCheckoutSource struct {
	Provider string `json:"provider"`
	Repo     Repo   `json:"repo"`
}

// PipelineDefinition is a CircleCI pipeline definition: the pairing of a
// configuration source with a checkout source that triggers run against.
//
// CreatedAt is kept as the string the API sent (an RFC 3339 timestamp) so that
// it round-trips into Terraform state exactly as received. It is omitted by the
// API for definitions created before the timestamp was recorded.
type PipelineDefinition struct {
	ID             string                 `json:"id"`
	Name           string                 `json:"name"`
	Description    string                 `json:"description"`
	CreatedAt      string                 `json:"created_at"`
	ConfigSource   PipelineConfigSource   `json:"config_source"`
	CheckoutSource PipelineCheckoutSource `json:"checkout_source"`
}

// ListPipelineDefinitions returns every pipeline definition on a project.
//
// The response carries no next_page_token: this endpoint returns the whole set
// in one body, so there is nothing to drain. The result is nil when the project
// has no pipeline definitions.
//
// CircleCI Cloud only. See pipelineDefinitionsRoute.
func (c *Client) ListPipelineDefinitions(ctx context.Context, projectID string) ([]PipelineDefinition, error) {
	var response itemsResponse[PipelineDefinition]
	if err := c.GetV2(ctx, pipelineDefinitionsRoute, &response, RouteParams(projectID)); err != nil {
		return nil, err
	}

	return response.Items, nil
}

// PipelineConfigSourceInput is the create body's config_source.
//
// Repo is a pointer so it can be omitted entirely. The API's config_source oneOf
// has two branches: one for github_app/github_server, which requires
// provider+repo+file_path, and one for the "circleci" provider (a CircleCI-hosted
// configuration with no repository at all), whose schema is
// `additionalProperties: false` over only provider and file_path. Sending a repo
// object on the "circleci" branch therefore fails the oneOf outright — this is not
// a case of an extra field being harmlessly ignored — so PipelineConfigSourceProviderNeedsRepo
// must gate whether Repo is populated. Provider and FilePath are sent
// unconditionally on both branches.
type PipelineConfigSourceInput struct {
	Provider string     `json:"provider"`
	Repo     *RepoInput `json:"repo,omitempty"`
	FilePath string     `json:"file_path"`
}

// PipelineCheckoutSourceInput is the create body's checkout_source, and —
// unlike PipelineConfigSourceInput on update — also the update body's
// checkout_source: the update route reuses the same checkout_source shape as
// create, so provider and repo remain updatable for the checkout source even
// though they are not for the config source.
type PipelineCheckoutSourceInput struct {
	Provider string    `json:"provider"`
	Repo     RepoInput `json:"repo"`
}

// CreatePipelineDefinitionInput is the create body for a pipeline definition.
type CreatePipelineDefinitionInput struct {
	Name           string                      `json:"name"`
	Description    string                      `json:"description"`
	ConfigSource   PipelineConfigSourceInput   `json:"config_source"`
	CheckoutSource PipelineCheckoutSourceInput `json:"checkout_source"`
}

// CreatePipelineDefinition creates a pipeline definition on a project and
// returns it as stored.
//
// This replaces circleci-sdk-go's pipeline.PipelineService.Create. The wire
// shape itself was not the bug (see DESIGN.md's "Characterization tests state
// the bug in the assertion"); the SDK migration is what makes it possible to
// fix project_id's missing RequiresReplace, treat a 404 as drift rather than a
// hard error, and gate the resource off CircleCI Server, none of which the SDK
// client exposed enough information to do.
//
// CircleCI Cloud only. See pipelineDefinitionsRoute.
func (c *Client) CreatePipelineDefinition(ctx context.Context, projectID string, input CreatePipelineDefinitionInput) (*PipelineDefinition, error) {
	var created PipelineDefinition
	if err := c.PostV2(ctx, pipelineDefinitionsRoute, input, &created, RouteParams(projectID)); err != nil {
		return nil, err
	}

	return &created, nil
}

// GetPipelineDefinition returns one pipeline definition by id. A missing
// definition is reported as an error satisfying IsNotFound.
//
// CircleCI Cloud only. See pipelineDefinitionsRoute.
func (c *Client) GetPipelineDefinition(ctx context.Context, projectID, id string) (*PipelineDefinition, error) {
	var found PipelineDefinition
	if err := c.GetV2(ctx, pipelineDefinitionRoute, &found, RouteParams(projectID, id)); err != nil {
		return nil, err
	}

	return &found, nil
}

// PipelineConfigSourceUpdateInput is the update body's config_source: file_path
// ONLY. Provider and repo are immutable after creation — the update route's
// config_source has no provider or repo field at all, unlike the create shape.
type PipelineConfigSourceUpdateInput struct {
	FilePath string `json:"file_path"`
}

// UpdatePipelineDefinitionInput is the update body for a pipeline definition.
// CheckoutSource reuses the create shape (see PipelineCheckoutSourceInput);
// ConfigSource does not.
type UpdatePipelineDefinitionInput struct {
	Name           string                          `json:"name"`
	Description    string                          `json:"description"`
	ConfigSource   PipelineConfigSourceUpdateInput `json:"config_source"`
	CheckoutSource PipelineCheckoutSourceInput     `json:"checkout_source"`
}

// UpdatePipelineDefinition updates a pipeline definition's mutable fields and
// returns it as stored.
//
// CircleCI Cloud only. See pipelineDefinitionsRoute.
func (c *Client) UpdatePipelineDefinition(ctx context.Context, projectID, id string, input UpdatePipelineDefinitionInput) (*PipelineDefinition, error) {
	var updated PipelineDefinition
	if err := c.PatchV2(ctx, pipelineDefinitionRoute, input, &updated, RouteParams(projectID, id)); err != nil {
		return nil, err
	}

	return &updated, nil
}

// DeletePipelineDefinition deletes a pipeline definition by id.
//
// CircleCI Cloud only. See pipelineDefinitionsRoute.
func (c *Client) DeletePipelineDefinition(ctx context.Context, projectID, id string) error {
	return c.DeleteV2(ctx, pipelineDefinitionRoute, RouteParams(projectID, id))
}
