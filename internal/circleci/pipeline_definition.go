// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Pipeline definition routes.
//
// These live under /api/v2 but are served by the public API service rather than
// the v2 API, and CircleCI Server does not route pipeline-definitions
// there. Callers must gate on Client.IsCloud before using them; a Server
// installation answers HTTP 404, which is indistinguishable from a project that
// does not exist.
const pipelineDefinitionsRoute = "/projects/%s/pipeline-definitions"

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
