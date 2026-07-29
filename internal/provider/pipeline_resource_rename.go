// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// This file holds everything to do with serving the pipeline definition resource
// under two type names, `circleci_pipeline` (deprecated) and
// `circleci_pipeline_definition`, while the old name is retired. It is one file on
// purpose: ending the deprecation should be deleting it and following the compiler.
//
// The five renamed *data sources* are the same rename one door along, and share the
// helpers in pipeline_rename.go.
//
// WHY THIS RENAME IS WORTH MAKING
//
// CircleCI has two things behind the word "pipeline", and colloquially the word means
// the second one:
//
//   - a pipeline *definition* — checkout source, config source, config file path;
//     `/projects/{project_id}/pipeline-definitions`. This resource.
//   - a pipeline *run* — one execution of a definition, which spawns workflows and
//     jobs; `/pipeline/{id}`.
//
// So `circleci_pipeline` promised the run and delivered the definition, and that is
// the root of a family of user errors where a definition id is passed where a run id
// is wanted — the run-scoped data sources name their identifier `run_id` for the same
// reason. Both are UUIDs, so nothing rejects the mistake until it 404s at apply.
//
// The rename is non-breaking: both names are served by one implementation, differing
// only in pipelineResource.deprecated, and existing state moves across with a `moved`
// block, which MoveState below serves. Both are meant to be registered; until the
// registration lists name the two constructors below, NewPipelineResource keeps
// provider.go serving the old name exactly as before.

// pipelineRenameDeprecationMessage is the warning shown on every plan that still uses
// the old name. It says migration destroys nothing, because the honest worry when told
// to rename something load-bearing is whether it will.
const pipelineRenameDeprecationMessage = "Use circleci_pipeline_definition instead. " +
	"This resource manages a pipeline definition — where to find configuration and " +
	"where to check out code — not a pipeline run, and the old name invited passing a " +
	"definition id where a run id was wanted. The old name keeps working until the next " +
	"major release; migrating is a moved block from circleci_pipeline.<name> to " +
	"circleci_pipeline_definition.<name>, which does not destroy the definition."

// typeName is the type name this instance serves, for diagnostics. A Cloud-only error
// naming a resource type the practitioner did not write is worse than no name at all.
func (r *pipelineResource) typeName() string {
	return renamedTypeName(r.deprecated, pipelineTypeName, pipelineDefinitionTypeName)
}

// NewDeprecatedPipelineResource registers the same implementation under the old type
// name, circleci_pipeline.
func NewDeprecatedPipelineResource() resource.Resource {
	return &pipelineResource{deprecated: true}
}

// MoveState migrates state from `circleci_pipeline` to
// `circleci_pipeline_definition`, so that practitioners write a moved block rather
// than removing and re-importing every pipeline definition they have.
//
// The two names share one Schema, so the source schema is this schema and the move is
// a straight copy: there is nothing to transform, and declaring SourceSchema is what
// makes that a Get/Set rather than raw JSON surgery.
func (r *pipelineResource) MoveState(ctx context.Context) []resource.StateMover {
	var schemaResponse resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)

	return []resource.StateMover{{
		SourceSchema: &schemaResponse.Schema,
		StateMover: func(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
			// SourceTypeName is the whole match; req.SourceProviderAddress is
			// deliberately ignored.
			//
			// The same provider serves both names, so Terraform can only route this
			// request to us — but the address it reports is however the *source*
			// configuration got the provider: registry.terraform.io/circleci/circleci
			// from the registry, a network mirror's own hostname, or a dev_overrides
			// address for a locally built binary. Our own tests are the proof that
			// this is not hypothetical: the plugin-testing harness reports
			// registry.terraform.io/hashicorp/circleci. Pinning a spelling would
			// refuse the move in exactly the environments where the failure is
			// hardest to diagnose, and would buy nothing: no other provider defines
			// circleci_pipeline, and one that did would still have to produce state
			// matching this schema for the copy below to mean anything.
			if req.SourceTypeName != pipelineTypeName {
				return
			}

			// Nil means SourceRawState did not fit SourceSchema, which the framework
			// only logs at DEBUG. Returning quietly would surface as the framework's
			// "no implementation found" error, which points the practitioner at a
			// missing feature rather than at their state.
			if req.SourceState == nil {
				resp.Diagnostics.AddError(
					"Unable to Move circleci_pipeline State",
					"The prior state of the circleci_pipeline resource could not be read "+
						"with the circleci_pipeline_definition schema, even though the two "+
						"are identical. Enable TF_LOG=DEBUG for the underlying error, and "+
						"report this to the provider developers.",
				)

				return
			}

			var state pipelineResourceModel

			resp.Diagnostics.Append(req.SourceState.Get(ctx, &state)...)
			if resp.Diagnostics.HasError() {
				return
			}

			resp.Diagnostics.Append(resp.TargetState.Set(ctx, state)...)
		},
	}}
}
