// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// The iOS signing resources reject CircleCI Server during ModifyPlan as well
// as during CRUD, following the pattern in cloud_only.go: without this the
// failure would only surface at apply, after `terraform plan` had already
// reported the resource would be created.
var (
	_ resource.ResourceWithModifyPlan = &iosSigningCertificateResource{}
	_ resource.ResourceWithModifyPlan = &iosSigningConfigResource{}
)

// Each ModifyPlan below is a no-op when the client is unset (Configure has not
// run yet) or when the plan is a destroy (req.Plan.Raw.IsNull()); a resource
// stranded in state after a deployment change must stay removable.

func (r *iosSigningCertificateResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, iosSigningCertificateTypeName, &resp.Diagnostics)
}

func (r *iosSigningConfigResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, iosSigningConfigTypeName, &resp.Diagnostics)
}
