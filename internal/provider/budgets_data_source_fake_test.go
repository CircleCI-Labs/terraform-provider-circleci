// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// See budget_fake_test.go for why these drive Read directly rather than
// through resource.UnitTest and an HCL "data" block.

// budgetsDataSourceSchemaForTest returns the data source's schema.
func budgetsDataSourceSchemaForTest(t *testing.T) dschema.Schema {
	t.Helper()

	resp := &datasource.SchemaResponse{}
	(&budgetsDataSource{}).Schema(t.Context(), datasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}

	return resp.Schema
}

// budgetsConfigForTest builds a tfsdk.Config from an org_id-only query, the
// shape a practitioner's data block takes.
func budgetsConfigForTest(t *testing.T, schema dschema.Schema, orgID string) tfsdk.Config {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	model := budgetsDataSourceModel{
		OrganizationID: types.StringNull(),
		OrgID:          types.StringValue(orgID),
		Budgets:        nil,
	}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a config value: %+v", diags)
	}

	return tfsdk.Config{Schema: schema, Raw: state.Raw}
}

// TestBudgetsDataSourceRead_ListsAll covers the happy path: both an org-level
// and a per-project budget come back, in the API's own order, with every
// field populated.
func TestBudgetsDataSourceRead_ListsAll(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	api.seed(testBudgetOrgID, fakeBudgetEntry{
		budgetID:        "budget-org",
		credits:         1000000,
		enforcementType: circleci.BudgetEnforcementWarn,
	})
	api.seed(testBudgetOrgID, fakeBudgetEntry{
		budgetID:          "budget-proj",
		credits:           50000,
		enforcementType:   circleci.BudgetEnforcementBlock,
		projectID:         strPtr(testBudgetProjectID),
		consumption:       1000,
		percentage:        2.0,
		thresholdExceeded: false,
	})

	schema := budgetsDataSourceSchemaForTest(t)
	d := &budgetsDataSource{client: api.client(host)}

	config := budgetsConfigForTest(t, schema, testBudgetOrgID)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schema}}
	d.Read(t.Context(), datasource.ReadRequest{Config: config}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned diagnostics: %v", resp.Diagnostics)
	}

	var out budgetsDataSourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}

	if len(out.Budgets) != 2 {
		t.Fatalf("got %d budget(s), want 2: %+v", len(out.Budgets), out.Budgets)
	}

	org := out.Budgets[0]
	if org.ID.ValueString() != "budget-org" || !org.ProjectID.IsNull() {
		t.Errorf("org budget = %+v, want id=budget-org project_id=null", org)
	}
	if org.Credits.ValueInt64() != 1000000 || org.EnforcementType.ValueString() != circleci.BudgetEnforcementWarn {
		t.Errorf("org budget = %+v, want credits=1000000 enforcement_type=warn", org)
	}

	proj := out.Budgets[1]
	if proj.ID.ValueString() != "budget-proj" || proj.ProjectID.ValueString() != testBudgetProjectID {
		t.Errorf("project budget = %+v, want id=budget-proj project_id=%q", proj, testBudgetProjectID)
	}
	if proj.Consumption.ValueInt64() != 1000 || proj.Percentage.ValueFloat64() != 2.0 {
		t.Errorf("project budget stats = %+v, want consumption=1000 percentage=2.0", proj)
	}
}

// TestBudgetsDataSourceRead_EmptyOrgReturnsEmptyList proves an organization
// with no budgets reports an empty list, not null, so configurations can
// iterate over it unconditionally.
func TestBudgetsDataSourceRead_EmptyOrgReturnsEmptyList(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	schema := budgetsDataSourceSchemaForTest(t)
	d := &budgetsDataSource{client: api.client(host)}

	config := budgetsConfigForTest(t, schema, testBudgetOrgID)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schema}}
	d.Read(t.Context(), datasource.ReadRequest{Config: config}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned diagnostics: %v", resp.Diagnostics)
	}

	var out budgetsDataSourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}

	if out.Budgets == nil {
		t.Error("budgets is nil, want a non-nil empty slice")
	}
	if len(out.Budgets) != 0 {
		t.Errorf("budgets = %+v, want none", out.Budgets)
	}
}

// TestBudgetsDataSourceRead_RequiresCloud is the guard test: deployment =
// "server" must be rejected before any request reaches the API.
func TestBudgetsDataSourceRead_RequiresCloud(t *testing.T) {
	t.Parallel()

	api, host := newFakeBudgetAPI(t)
	schema := budgetsDataSourceSchemaForTest(t)
	serverClient := circleci.New(circleci.Config{
		Host:        "http://127.0.0.1:1",
		PrivateHost: host,
		Token:       "fake",
		Deployment:  circleci.DeploymentServer,
	})
	d := &budgetsDataSource{client: serverClient}

	config := budgetsConfigForTest(t, schema, testBudgetOrgID)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schema}}
	d.Read(t.Context(), datasource.ReadRequest{Config: config}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("Read on a server deployment produced no diagnostics, want one")
	}

	found := false
	for _, diag := range resp.Diagnostics {
		if strings.Contains(diag.Summary(), "circleci_budgets requires CircleCI Cloud") {
			found = true
		}
	}
	if !found {
		t.Errorf("diagnostics = %v, want a %q summary", resp.Diagnostics, "circleci_budgets requires CircleCI Cloud")
	}
	if len(api.recorded()) != 0 {
		t.Errorf("server deployment reached the fake API: %v, want no requests at all", api.recorded())
	}
}
