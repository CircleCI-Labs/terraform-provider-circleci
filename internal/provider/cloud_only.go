// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// The v3-only resources reject CircleCI Server during ModifyPlan as well as
// during CRUD.
//
// Without this the failure only surfaces at apply: `terraform plan` succeeds and
// reports the resource will be created, then the create call fails. For
// infrastructure as code that is the wrong end of the pipeline — a plan check in
// CI would pass and the error would land mid-apply, potentially after other
// resources have already been changed.
//
// ModifyPlan is the earliest hook with access to the configured client.
// ValidateConfig runs before Configure, so the deployment is not known there.
var (
	_ resource.ResourceWithModifyPlan = &orbNamespaceResource{}
	_ resource.ResourceWithModifyPlan = &orbResource{}
	_ resource.ResourceWithModifyPlan = &orbVersionResource{}
	_ resource.ResourceWithModifyPlan = &organizationSettingsResource{}
	_ resource.ResourceWithModifyPlan = &pipelineResource{}
	_ resource.ResourceWithModifyPlan = &triggerResource{}
)

// Each ModifyPlan below is a no-op in two cases: when the client is unset
// (Configure has not run, as happens during some validation passes), and when
// the plan is a destroy (req.Plan.Raw.IsNull()). Allowing a destroy through
// matters — a resource left in state after a deployment change must remain
// removable rather than becoming permanently unmanageable.

func (r *orbNamespaceResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, orbNamespaceTypeName, &resp.Diagnostics)
}

func (r *orbResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, orbTypeName, &resp.Diagnostics)
}

func (r *orbVersionResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, orbVersionTypeName, &resp.Diagnostics)
}

func (r *organizationSettingsResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, organizationSettingsTypeName, &resp.Diagnostics)
}

// circleci_pipeline and circleci_trigger are not v3, but pipeline definitions
// and triggers are served by the public API service under /api/v2 rather than
// the v2 API, and CircleCI Server does not route them there either — see
// pipelineDefinitionsRoute and triggersRoute in internal/circleci. Before this
// migration Configure only received *pipeline.PipelineService /
// *trigger.TriggerService, neither of which carries any deployment
// information, so no gate was possible at all; see DESIGN.md's
// characterization test notes for issue #26.

func (r *pipelineResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, pipelineTypeName, &resp.Diagnostics)
}

func (r *triggerResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, triggerTypeName, &resp.Diagnostics)
}
