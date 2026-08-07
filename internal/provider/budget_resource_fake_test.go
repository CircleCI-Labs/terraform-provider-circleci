// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"strings"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"terraform-provider-circleci/internal/circleci"
)

// See budget_fake_test.go for why these drive Create/Read/Update/Delete/
// ImportState directly rather than through resource.UnitTest and an HCL
// "provider" block.

const (
	testBudgetOrgID     = "00000000-1111-2222-3333-444444444444"
	testBudgetOrgID2    = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testBudgetProjectID = "55555555-6666-7777-8888-999999999999"
)

// budgetResourceSchemaForTest returns the resource's schema, the same way
// groupMembershipResourceSchemaForTest does.
func budgetResourceSchemaForTest(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	(&budgetResource{}).Schema(t.Context(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}

	return resp.Schema
}

// budgetStateForTest builds a tfsdk.State (or, read as a Plan/Config, the
// identical raw value) from a fully-populated model.
func budgetStateForTest(t *testing.T, schema rschema.Schema, model budgetResourceModel) tfsdk.State {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a state value: %+v", diags)
	}

	return state
}

// budgetPlanModel builds a model the shape a practitioner's configuration
// takes: identifying attributes and credits set, everything computed left
// unknown (as a real plan has them on create).
func budgetPlanModel(orgID, projectID string, credits int64) budgetResourceModel {
	model := budgetResourceModel{
		OrganizationID:    types.StringNull(),
		OrgID:             types.StringValue(orgID),
		ProjectID:         types.StringNull(),
		Credits:           types.Int64Value(credits),
		EnforcementType:   types.StringUnknown(),
		Consumption:       types.Int64Unknown(),
		Percentage:        types.Float64Unknown(),
		ThresholdExceeded: types.BoolUnknown(),
		ID:                types.StringUnknown(),
	}
	if projectID != "" {
		model.ProjectID = types.StringValue(projectID)
	}

	return model
}

func TestBudgetResourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwresource.SchemaResponse{}
	NewBudgetResource().Schema(ctx, fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// organization_id, org_id and project_id all address a distinct API scope;
	// changing any of them must replace rather than silently re-point.
	for _, name := range []string{"organization_id", "org_id", "project_id"} {
		attribute, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("schema is missing the %q attribute", name)
		}

		stringAttribute, ok := attribute.(rschema.StringAttribute)
		if !ok {
			t.Fatalf("attribute %q is %T, want rschema.StringAttribute", name, attribute)
		}

		var forcesReplacement bool
		for _, modifier := range stringAttribute.PlanModifiers {
			if strings.Contains(strings.ToLower(modifier.Description(ctx)), "destroy and recreate") {
				forcesReplacement = true
			}
		}
		if !forcesReplacement {
			t.Errorf("attribute %q has no plan modifier forcing replacement", name)
		}
	}

	// credits must be settable: it is the one attribute an update can actually
	// change.
	credits, ok := resp.Schema.Attributes["credits"]
	if !ok {
		t.Fatal(`schema is missing the "credits" attribute`)
	}
	if !credits.IsRequired() {
		t.Error(`attribute "credits" is not required`)
	}

	// enforcement_type must be Computed-only: the write route has no field for
	// it (see circleci.budgetSetRequest), so accepting it as Optional would let
	// a configuration silently no-op on every apply.
	for _, name := range []string{"enforcement_type", "consumption", "percentage", "threshold_exceeded"} {
		attribute, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("schema is missing the %q attribute", name)
		}
		if !attribute.IsComputed() {
			t.Errorf("attribute %q is not computed", name)
		}
		if attribute.IsOptional() {
			t.Errorf("attribute %q is optional; it must be Computed-only, since the API has no route to set it", name)
		}
	}
}

// TestBudgetResourceCreate_OrgLevel covers the organization-level scope: no
// project_id in the plan, and the PUT body must carry an explicit null rather
// than omitting the key.
func TestBudgetResourceCreate_OrgLevel(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	schema := budgetResourceSchemaForTest(t)
	r := &budgetResource{client: api.client(host)}

	plan := budgetStateForTest(t, schema, budgetPlanModel(testBudgetOrgID, "", 2000000))
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: plan.Raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create returned diagnostics: %v", resp.Diagnostics)
	}

	entries := api.entries(testBudgetOrgID)
	if len(entries) != 1 {
		t.Fatalf("fake has %d budget(s) for the org, want 1: %+v", len(entries), entries)
	}
	if entries[0].projectID != nil {
		t.Errorf("stored budget has project_id = %v, want nil", *entries[0].projectID)
	}
	if entries[0].credits != 2000000 {
		t.Errorf("stored budget credits = %d, want 2000000", entries[0].credits)
	}

	var out budgetResourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if out.ID.ValueString() != entries[0].budgetID {
		t.Errorf("state id = %q, want %q", out.ID.ValueString(), entries[0].budgetID)
	}
	if !out.ProjectID.IsNull() {
		t.Errorf("state project_id = %q, want null", out.ProjectID.ValueString())
	}
	if out.EnforcementType.ValueString() != circleci.BudgetEnforcementWarn {
		t.Errorf("state enforcement_type = %q, want %q", out.EnforcementType.ValueString(), circleci.BudgetEnforcementWarn)
	}
}

// TestBudgetResourceCreate_ProjectLevel covers a per-project scope, and that
// the org-level scope is untouched by it.
func TestBudgetResourceCreate_ProjectLevel(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	schema := budgetResourceSchemaForTest(t)
	r := &budgetResource{client: api.client(host)}

	plan := budgetStateForTest(t, schema, budgetPlanModel(testBudgetOrgID, testBudgetProjectID, 50000))
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: plan.Raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create returned diagnostics: %v", resp.Diagnostics)
	}

	var out budgetResourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if out.ProjectID.ValueString() != testBudgetProjectID {
		t.Errorf("state project_id = %q, want %q", out.ProjectID.ValueString(), testBudgetProjectID)
	}
	if out.Credits.ValueInt64() != 50000 {
		t.Errorf("state credits = %d, want 50000", out.Credits.ValueInt64())
	}
}

// TestBudgetResourceCreate_DifferentOrganizationsAreIndependent covers that a
// budget is scoped to the organization it names: creating one for
// testBudgetOrgID2 must not touch, or be confused with, one already present
// for testBudgetOrgID.
func TestBudgetResourceCreate_DifferentOrganizationsAreIndependent(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	api.seed(testBudgetOrgID, fakeBudgetEntry{budgetID: "budget-org-1", credits: 1000})
	// A pre-existing org-level budget in the second organization too, so the
	// project-level create below has something it must not disturb even
	// within the same organization it targets.
	api.seed(testBudgetOrgID2, fakeBudgetEntry{budgetID: "budget-org-2", credits: 500})
	schema := budgetResourceSchemaForTest(t)
	r := &budgetResource{client: api.client(host)}

	plan := budgetStateForTest(t, schema, budgetPlanModel(testBudgetOrgID2, testBudgetProjectID, 999))
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: plan.Raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create returned diagnostics: %v", resp.Diagnostics)
	}

	if entries := api.entries(testBudgetOrgID); len(entries) != 1 || entries[0].credits != 1000 {
		t.Errorf("testBudgetOrgID's budgets changed: %+v, want the original untouched", entries)
	}

	entries := api.entries(testBudgetOrgID2)
	if len(entries) != 2 {
		t.Fatalf("testBudgetOrgID2 has %d budget(s), want 2 (the pre-existing org-level one plus the new project-level one): %+v",
			len(entries), entries)
	}
	if entries[0].budgetID != "budget-org-2" || entries[0].credits != 500 {
		t.Errorf("testBudgetOrgID2's pre-existing org-level budget changed: %+v, want it untouched", entries[0])
	}
	if entries[1].credits != 999 || entries[1].projectID == nil || *entries[1].projectID != testBudgetProjectID {
		t.Errorf("testBudgetOrgID2's new project budget = %+v, want credits=999 project_id=%q", entries[1], testBudgetProjectID)
	}
}

// TestBudgetResourceRead_ListErrorIsNotSilentlyDropped covers that a genuine
// error listing an organization's budgets — modeled here as the fake's stand-
// in for "this token cannot see this organization" — is surfaced as a
// diagnostic, not treated as "no budget for this scope" and silently dropped
// from state the way a real absence is (see
// TestBudgetResourceRead_RemovedOutsideTerraform_DropsFromState). Read must
// tell the two apart, because dropping a live budget from state on a
// transient or permissions error would make Terraform "forget" it and offer
// to recreate it.
func TestBudgetResourceRead_ListErrorIsNotSilentlyDropped(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	api.removeOrg(testBudgetOrgID)
	schema := budgetResourceSchemaForTest(t)
	r := &budgetResource{client: api.client(host)}

	prior := budgetStateForTest(t, schema, budgetResourceModel{
		ID:                types.StringValue("budget-1"),
		OrganizationID:    types.StringValue(testBudgetOrgID),
		OrgID:             types.StringValue(testBudgetOrgID),
		ProjectID:         types.StringNull(),
		Credits:           types.Int64Value(1000),
		EnforcementType:   types.StringValue(circleci.BudgetEnforcementWarn),
		Consumption:       types.Int64Value(0),
		Percentage:        types.Float64Value(0),
		ThresholdExceeded: types.BoolValue(false),
	})

	resp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema}}
	r.Read(t.Context(), fwresource.ReadRequest{State: prior}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("Read returned no diagnostics for a list error, want one")
	}
	if !resp.State.Raw.IsNull() {
		t.Error("Read left state populated after erroring, want a clean empty response rather than a half-applied one")
	}
}

// TestBudgetResourceUpdate_ReusesSameBudgetIDAndSendsNoEnforcementType covers
// two design decisions at once: a second write for the same scope is an
// upsert (same budget_id, no duplicate entry) rather than a second budget, and
// the write body never carries enforcement_type — there is nowhere for it to
// go, and sending it would be evidence this resource pretends to manage a
// field it cannot.
func TestBudgetResourceUpdate_ReusesSameBudgetIDAndSendsNoEnforcementType(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	schema := budgetResourceSchemaForTest(t)
	r := &budgetResource{client: api.client(host)}

	createPlan := budgetStateForTest(t, schema, budgetPlanModel(testBudgetOrgID, "", 1000))
	createResp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: createPlan.Raw}}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create returned diagnostics: %v", createResp.Diagnostics)
	}

	firstID := api.entries(testBudgetOrgID)[0].budgetID

	updatePlan := budgetStateForTest(t, schema, budgetPlanModel(testBudgetOrgID, "", 2000))
	updateResp := &fwresource.UpdateResponse{State: tfsdk.State{Schema: schema}}
	r.Update(t.Context(), fwresource.UpdateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: updatePlan.Raw}}, updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update returned diagnostics: %v", updateResp.Diagnostics)
	}

	entries := api.entries(testBudgetOrgID)
	if len(entries) != 1 {
		t.Fatalf("fake has %d budget(s) for the org after update, want exactly 1 (upsert, not duplicate): %+v",
			len(entries), entries)
	}
	if entries[0].budgetID != firstID {
		t.Errorf("budget_id changed across update: got %q, want %q (same entry)", entries[0].budgetID, firstID)
	}
	if entries[0].credits != 2000 {
		t.Errorf("stored credits = %d, want 2000", entries[0].credits)
	}

	var out budgetResourceModel
	if diags := updateResp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if out.ID.ValueString() != firstID {
		t.Errorf("state id = %q, want %q", out.ID.ValueString(), firstID)
	}
}

// TestBudgetResourceRead_ReflectsLiveStatistics proves Read reports what the
// API has right now — including enforcement_type, which this resource cannot
// set — rather than the prior state, mirroring
// TestGroupMembershipResourceRead's assertion for the same reason.
func TestBudgetResourceRead_ReflectsLiveStatistics(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	api.seed(testBudgetOrgID, fakeBudgetEntry{
		budgetID:          "budget-live",
		credits:           50000,
		enforcementType:   circleci.BudgetEnforcementBlock,
		projectID:         strPtr(testBudgetProjectID),
		consumption:       12345,
		percentage:        24.69,
		thresholdExceeded: true,
	})
	schema := budgetResourceSchemaForTest(t)
	r := &budgetResource{client: api.client(host)}

	prior := budgetStateForTest(t, schema, budgetResourceModel{
		ID:                types.StringValue("budget-live"),
		OrganizationID:    types.StringValue(testBudgetOrgID),
		OrgID:             types.StringValue(testBudgetOrgID),
		ProjectID:         types.StringValue(testBudgetProjectID),
		Credits:           types.Int64Value(50000),
		EnforcementType:   types.StringValue(circleci.BudgetEnforcementWarn), // stale on purpose
		Consumption:       types.Int64Value(0),
		Percentage:        types.Float64Value(0),
		ThresholdExceeded: types.BoolValue(false),
	})

	resp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema}}
	r.Read(t.Context(), fwresource.ReadRequest{State: prior}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned diagnostics: %v", resp.Diagnostics)
	}

	var out budgetResourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}

	if out.EnforcementType.ValueString() != circleci.BudgetEnforcementBlock {
		t.Errorf("enforcement_type = %q, want %q (live value, not stale state)",
			out.EnforcementType.ValueString(), circleci.BudgetEnforcementBlock)
	}
	if out.Consumption.ValueInt64() != 12345 {
		t.Errorf("consumption = %d, want 12345", out.Consumption.ValueInt64())
	}
	if out.Percentage.ValueFloat64() != 24.69 {
		t.Errorf("percentage = %v, want 24.69", out.Percentage.ValueFloat64())
	}
	if !out.ThresholdExceeded.ValueBool() {
		t.Error("threshold_exceeded = false, want true")
	}
}

// TestBudgetResourceRead_RemovedOutsideTerraform_DropsFromState covers a
// budget deleted outside Terraform: since there is no single-item GET, this
// is detected by the entry's absence from the list, and Read must drop the
// resource from state rather than error.
func TestBudgetResourceRead_RemovedOutsideTerraform_DropsFromState(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	// No seed at all: the org has no budgets.
	schema := budgetResourceSchemaForTest(t)
	r := &budgetResource{client: api.client(host)}

	prior := budgetStateForTest(t, schema, budgetResourceModel{
		ID:                types.StringValue("budget-gone"),
		OrganizationID:    types.StringValue(testBudgetOrgID),
		OrgID:             types.StringValue(testBudgetOrgID),
		ProjectID:         types.StringNull(),
		Credits:           types.Int64Value(1000),
		EnforcementType:   types.StringValue(circleci.BudgetEnforcementWarn),
		Consumption:       types.Int64Value(0),
		Percentage:        types.Float64Value(0),
		ThresholdExceeded: types.BoolValue(false),
	})

	resp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema}}
	r.Read(t.Context(), fwresource.ReadRequest{State: prior}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned diagnostics for a missing budget, want a clean drop from state: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("Read left state populated for a missing budget, want it removed")
	}
}

// TestBudgetResourceDelete covers the normal path: Delete sends the stored id
// to the delete route and the entry disappears from the fake.
func TestBudgetResourceDelete(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	api.seed(testBudgetOrgID, fakeBudgetEntry{budgetID: "budget-to-delete", credits: 1000})
	schema := budgetResourceSchemaForTest(t)
	r := &budgetResource{client: api.client(host)}

	state := budgetStateForTest(t, schema, budgetResourceModel{
		ID:                types.StringValue("budget-to-delete"),
		OrganizationID:    types.StringValue(testBudgetOrgID),
		OrgID:             types.StringValue(testBudgetOrgID),
		ProjectID:         types.StringNull(),
		Credits:           types.Int64Value(1000),
		EnforcementType:   types.StringValue(circleci.BudgetEnforcementWarn),
		Consumption:       types.Int64Value(0),
		Percentage:        types.Float64Value(0),
		ThresholdExceeded: types.BoolValue(false),
	})

	resp := &fwresource.DeleteResponse{State: state}
	r.Delete(t.Context(), fwresource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete returned diagnostics: %v", resp.Diagnostics)
	}
	if entries := api.entries(testBudgetOrgID); len(entries) != 0 {
		t.Errorf("fake still has %d budget(s) after delete: %+v", len(entries), entries)
	}
}

// TestBudgetResourceDelete_ToleratesAlreadyDeleted covers that a 404 from the
// delete route is treated as success, not an error.
func TestBudgetResourceDelete_ToleratesAlreadyDeleted(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	// Nothing seeded: the delete route will 404.
	schema := budgetResourceSchemaForTest(t)
	r := &budgetResource{client: api.client(host)}

	state := budgetStateForTest(t, schema, budgetResourceModel{
		ID:                types.StringValue("already-gone"),
		OrganizationID:    types.StringValue(testBudgetOrgID),
		OrgID:             types.StringValue(testBudgetOrgID),
		ProjectID:         types.StringNull(),
		Credits:           types.Int64Value(1000),
		EnforcementType:   types.StringValue(circleci.BudgetEnforcementWarn),
		Consumption:       types.Int64Value(0),
		Percentage:        types.Float64Value(0),
		ThresholdExceeded: types.BoolValue(false),
	})

	resp := &fwresource.DeleteResponse{State: state}
	r.Delete(t.Context(), fwresource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("Delete of an already-deleted budget returned diagnostics, want none: %v", resp.Diagnostics)
	}
}

// --- ImportState -----------------------------------------------------------

// TestBudgetResourceImportState_RoundTripsWithRead proves the round trip
// `terraform import` actually drives for this resource: ImportState sets only
// the scope (org_id and, for a per-project budget, project_id — see
// budgetResource.ImportState's own doc comment for why `id` and the
// statistics are deliberately left for Read to fill in, there being no
// single-budget GET), and the framework's own post-import refresh then calls
// Read against that state. This asserts the combination reproduces exactly
// the state Create left behind — id, credits, enforcement_type, consumption,
// percentage and threshold_exceeded all match — which is the condition that
// makes the plan following an import empty. There is no secret attribute on
// this resource to complicate that.
func TestBudgetResourceImportState_RoundTripsWithRead(t *testing.T) {
	t.Parallel()

	for _, scope := range []struct {
		name      string
		projectID string
	}{
		{"OrgLevel", ""},
		{"ProjectLevel", testBudgetProjectID},
	} {
		t.Run(scope.name, func(t *testing.T) {
			t.Parallel()

			api, host := newFakeBudgetAPI(t)
			schema := budgetResourceSchemaForTest(t)
			r := &budgetResource{client: api.client(host)}

			createPlan := budgetStateForTest(t, schema, budgetPlanModel(testBudgetOrgID, scope.projectID, 1234))
			createResp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
			r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: createPlan.Raw}}, createResp)
			if createResp.Diagnostics.HasError() {
				t.Fatalf("Create returned diagnostics: %v", createResp.Diagnostics)
			}

			var created budgetResourceModel
			if diags := createResp.State.Get(t.Context(), &created); diags.HasError() {
				t.Fatalf("reading back created state: %v", diags)
			}

			importID := testBudgetOrgID
			if scope.projectID != "" {
				importID += "/" + scope.projectID
			}

			// ImportState only ever receives a bare scope id and an empty
			// (all-null-but-typed) state to write into, the same as a real
			// `terraform import` call.
			importResp := &fwresource.ImportStateResponse{
				State: budgetStateForTest(t, schema, emptyBudgetModelForTest()),
			}
			r.ImportState(t.Context(), fwresource.ImportStateRequest{ID: importID}, importResp)
			if importResp.Diagnostics.HasError() {
				t.Fatalf("ImportState returned diagnostics: %v", importResp.Diagnostics)
			}

			readResp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema}}
			r.Read(t.Context(), fwresource.ReadRequest{State: importResp.State}, readResp)
			if readResp.Diagnostics.HasError() {
				t.Fatalf("Read returned diagnostics: %v", readResp.Diagnostics)
			}

			var imported budgetResourceModel
			if diags := readResp.State.Get(t.Context(), &imported); diags.HasError() {
				t.Fatalf("reading back imported state: %v", diags)
			}

			if imported.ID.ValueString() != created.ID.ValueString() {
				t.Errorf("imported id = %q, want %q (the value Create produced)", imported.ID.ValueString(), created.ID.ValueString())
			}
			if imported.OrgID.ValueString() != created.OrgID.ValueString() {
				t.Errorf("imported org_id = %q, want %q", imported.OrgID.ValueString(), created.OrgID.ValueString())
			}
			if imported.ProjectID.ValueString() != created.ProjectID.ValueString() {
				t.Errorf("imported project_id = %q, want %q", imported.ProjectID.ValueString(), created.ProjectID.ValueString())
			}
			if imported.Credits.ValueInt64() != created.Credits.ValueInt64() {
				t.Errorf("imported credits = %d, want %d", imported.Credits.ValueInt64(), created.Credits.ValueInt64())
			}
			if imported.EnforcementType.ValueString() != created.EnforcementType.ValueString() {
				t.Errorf("imported enforcement_type = %q, want %q",
					imported.EnforcementType.ValueString(), created.EnforcementType.ValueString())
			}
			if imported.Consumption.ValueInt64() != created.Consumption.ValueInt64() {
				t.Errorf("imported consumption = %d, want %d", imported.Consumption.ValueInt64(), created.Consumption.ValueInt64())
			}
			if imported.Percentage.ValueFloat64() != created.Percentage.ValueFloat64() {
				t.Errorf("imported percentage = %v, want %v", imported.Percentage.ValueFloat64(), created.Percentage.ValueFloat64())
			}
			if imported.ThresholdExceeded.ValueBool() != created.ThresholdExceeded.ValueBool() {
				t.Errorf("imported threshold_exceeded = %v, want %v",
					imported.ThresholdExceeded.ValueBool(), created.ThresholdExceeded.ValueBool())
			}
		})
	}
}

func TestBudgetResourceImportState_OrgLevel(t *testing.T) {
	t.Parallel()

	r, ok := NewBudgetResource().(*budgetResource)
	if !ok {
		t.Fatal("NewBudgetResource did not return a *budgetResource")
	}

	schema := budgetResourceSchemaForTest(t)
	resp := &fwresource.ImportStateResponse{
		State: budgetStateForTest(t, schema, emptyBudgetModelForTest()),
	}
	r.ImportState(t.Context(), fwresource.ImportStateRequest{ID: testBudgetOrgID}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("ImportState returned diagnostics: %v", resp.Diagnostics)
	}

	var out budgetResourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if out.OrgID.ValueString() != testBudgetOrgID {
		t.Errorf("org_id = %q, want %q", out.OrgID.ValueString(), testBudgetOrgID)
	}
	if !out.ProjectID.IsNull() {
		t.Errorf("project_id = %q, want null for an organization-level import", out.ProjectID.ValueString())
	}
}

func TestBudgetResourceImportState_ProjectLevel(t *testing.T) {
	t.Parallel()

	r, ok := NewBudgetResource().(*budgetResource)
	if !ok {
		t.Fatal("NewBudgetResource did not return a *budgetResource")
	}

	schema := budgetResourceSchemaForTest(t)
	resp := &fwresource.ImportStateResponse{
		State: budgetStateForTest(t, schema, emptyBudgetModelForTest()),
	}
	r.ImportState(t.Context(), fwresource.ImportStateRequest{
		ID: testBudgetOrgID + "/" + testBudgetProjectID,
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("ImportState returned diagnostics: %v", resp.Diagnostics)
	}

	var out budgetResourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if out.OrgID.ValueString() != testBudgetOrgID {
		t.Errorf("org_id = %q, want %q", out.OrgID.ValueString(), testBudgetOrgID)
	}
	if out.ProjectID.ValueString() != testBudgetProjectID {
		t.Errorf("project_id = %q, want %q", out.ProjectID.ValueString(), testBudgetProjectID)
	}
}

func TestBudgetResourceImportState_RejectsEmptyID(t *testing.T) {
	t.Parallel()

	r, ok := NewBudgetResource().(*budgetResource)
	if !ok {
		t.Fatal("NewBudgetResource did not return a *budgetResource")
	}

	resp := &fwresource.ImportStateResponse{
		State: tfsdk.State{Schema: budgetResourceSchemaForTest(t)},
	}
	r.ImportState(t.Context(), fwresource.ImportStateRequest{ID: ""}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("ImportState of an empty ID produced no error, want one")
	}
}

// --- the gate ----------------------------------------------------------------

// TestBudgetResourceRequiresCloud_Create is the guard test: deployment =
// "server" must be rejected before any request reaches the API.
func TestBudgetResourceRequiresCloud_Create(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	schema := budgetResourceSchemaForTest(t)
	serverClient := circleci.New(circleci.Config{
		Host:        "http://127.0.0.1:1",
		PrivateHost: host,
		Token:       "fake",
		Deployment:  circleci.DeploymentServer,
	})
	r := &budgetResource{client: serverClient}

	plan := budgetStateForTest(t, schema, budgetPlanModel(testBudgetOrgID, "", 1000))
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: plan.Raw}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("Create on a server deployment produced no diagnostics, want one")
	}

	found := false
	for _, d := range resp.Diagnostics {
		if strings.Contains(d.Summary(), "circleci_budget requires CircleCI Cloud") {
			found = true
		}
	}
	if !found {
		t.Errorf("diagnostics = %v, want a %q summary", resp.Diagnostics, "circleci_budget requires CircleCI Cloud")
	}
	if len(api.recorded()) != 0 {
		t.Errorf("server deployment reached the fake API: %v, want no requests at all", api.recorded())
	}
}

// TestBudgetResourceRequiresCloud_Delete proves Delete is gated the same way
// as Create/Read/Update — see budgetResource.Delete's own doc comment for why,
// unlike circleci_storage_retention or circleci_organization_contacts.
func TestBudgetResourceRequiresCloud_Delete(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	api.seed(testBudgetOrgID, fakeBudgetEntry{budgetID: "budget-1", credits: 1000})
	schema := budgetResourceSchemaForTest(t)
	serverClient := circleci.New(circleci.Config{
		Host:        "http://127.0.0.1:1",
		PrivateHost: host,
		Token:       "fake",
		Deployment:  circleci.DeploymentServer,
	})
	r := &budgetResource{client: serverClient}

	state := budgetStateForTest(t, schema, budgetResourceModel{
		ID:                types.StringValue("budget-1"),
		OrganizationID:    types.StringValue(testBudgetOrgID),
		OrgID:             types.StringValue(testBudgetOrgID),
		ProjectID:         types.StringNull(),
		Credits:           types.Int64Value(1000),
		EnforcementType:   types.StringValue(circleci.BudgetEnforcementWarn),
		Consumption:       types.Int64Value(0),
		Percentage:        types.Float64Value(0),
		ThresholdExceeded: types.BoolValue(false),
	})

	resp := &fwresource.DeleteResponse{State: state}
	r.Delete(t.Context(), fwresource.DeleteRequest{State: state}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("Delete on a server deployment produced no diagnostics, want one")
	}
	if entries := api.entries(testBudgetOrgID); len(entries) != 1 {
		t.Errorf("server deployment's Delete reached the fake API: entries = %+v, want the seeded budget untouched", entries)
	}
}

// TestBudgetResourceModifyPlan_AllowsDestroy is the escape hatch, mirroring
// TestCloudOnlyModifyPlanAllowsDestroy: a null plan (a destroy) must pass
// through ModifyPlan even on a server deployment, so the plan itself never
// blocks reaching Delete.
func TestBudgetResourceModifyPlan_AllowsDestroy(t *testing.T) {
	t.Parallel()

	serverClient := circleci.New(circleci.Config{
		Host:       "https://circleci.example.com",
		Token:      "fake",
		Deployment: circleci.DeploymentServer,
	})
	r := &budgetResource{client: serverClient}

	destroyPlan := tfsdk.Plan{
		Raw:    tftypes.NewValue(tftypes.Object{}, nil),
		Schema: rschema.Schema{},
	}

	var resp fwresource.ModifyPlanResponse
	r.ModifyPlan(t.Context(), fwresource.ModifyPlanRequest{Plan: destroyPlan}, &resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("ModifyPlan rejected a destroy on a server deployment: %v", resp.Diagnostics.Errors())
	}
}

// strPtr is a small helper for building a *string literal inline.
func strPtr(s string) *string { return &s }

// emptyBudgetModelForTest is an all-null model, for seeding an
// ImportStateResponse's initial State: ImportState only ever calls
// SetAttribute on individual paths, which needs a typed (if entirely null)
// object to write into — a bare tfsdk.State{Schema: schema} has no such value
// at all.
func emptyBudgetModelForTest() budgetResourceModel {
	return budgetResourceModel{
		ID:                types.StringNull(),
		OrganizationID:    types.StringNull(),
		OrgID:             types.StringNull(),
		ProjectID:         types.StringNull(),
		Credits:           types.Int64Null(),
		EnforcementType:   types.StringNull(),
		Consumption:       types.Int64Null(),
		Percentage:        types.Float64Null(),
		ThresholdExceeded: types.BoolNull(),
	}
}

// TestBudgetResourceCreate_WritesStateWhenFindBudgetFails is a regression test
// for issue #37.
//
// SetBudget is an upsert, so this one is self-healing in a way
// circleci_orb_version and circleci_orb are not: a retry after this point
// simply repeats the same write and converges. Even so, before the fix, a
// failure from FindBudget — the call Create makes right after SetBudget to
// learn the id, enforcement type and statistics none of which SetBudget's own
// response carries — returned before resp.State.Set, so a budget CircleCI had
// already written had no record in Terraform state until the next apply
// happened to succeed.
//
// removeOrg is enough to reach this: unlike the list route, the fake's set
// route (mirroring the real upsert PUT) does not consult missingOrgs, so
// SetBudget still succeeds while the following FindBudget/list fails with
// 403 — exactly "first call succeeds, second fails" with no new fake
// plumbing needed.
func TestBudgetResourceCreate_WritesStateWhenFindBudgetFails(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	api.removeOrg(testBudgetOrgID)
	schema := budgetResourceSchemaForTest(t)
	r := &budgetResource{client: api.client(host)}

	plan := budgetStateForTest(t, schema, budgetPlanModel(testBudgetOrgID, "", 1000))
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: plan.Raw}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("Create returned no diagnostics for a failed FindBudget call, want one")
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("Create left state empty after SetBudget succeeded; the budget now exists at CircleCI " +
			"with nothing in Terraform tracking it")
	}

	var out budgetResourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if out.OrgID.ValueString() != testBudgetOrgID {
		t.Errorf("state org_id = %q, want %q", out.OrgID.ValueString(), testBudgetOrgID)
	}
	if out.Credits.ValueInt64() != 1000 {
		t.Errorf("state credits = %d, want 1000 (the value SetBudget already wrote)", out.Credits.ValueInt64())
	}

	// SetBudget itself succeeded: the fake's set route ignores missingOrgs, so
	// the write really did happen, matching the "write already succeeded"
	// premise of this test.
	if entries := api.entries(testBudgetOrgID); len(entries) != 1 || entries[0].credits != 1000 {
		t.Errorf("fake budgets for the org = %+v, want one entry with credits=1000 (the write from "+
			"SetBudget, which does not consult missingOrgs)", entries)
	}
}
