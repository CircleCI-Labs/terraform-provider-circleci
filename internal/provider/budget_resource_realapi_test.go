// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"terraform-provider-circleci/internal/circleci"
)

// This file is the [NET] counterpart to budget_resource_fake_test.go, which is
// entirely [FAKE]. Everything budget.go's doc comments attribute to a "[NET]
// measurement against gh-app-cci-1 (2026-08-21)" -- the new-id-on-every-write
// behaviour above all -- had been checked exactly once, by hand, with nothing
// in CI to notice if it regressed. It runs against whichever organization
// CIRCLECI_TEST_VCS_TYPE's active integration names (testOrgID), so it
// exercises every one of this wave's four Cloud organizations across the
// acceptance-* CI jobs.
//
// A spend budget has no name to make a fresh instance of it unique: the API
// models at most one budget per (organization, project) scope, so the two
// scopes this file manages -- the organization-level budget, and the one on
// testProjectID's project -- are each a singleton. That makes "safe to run
// concurrently with itself" a property the API itself does not offer for
// this resource, the same as it would not for circleci_storage_retention: two
// processes writing the same scope at once would just race, last write
// standing, and there is no way to give a second one its own object to
// manage. What these tests are safe against is the more useful case named in
// this wave's brief -- a crashed prior run's leftovers -- because SetBudget is
// upsert-shaped: if a run before this one died after creating a budget for
// this scope and never got to delete it, the very next run's PUT still
// converges the scope to what that run wants (minting yet another new id, as
// documented), rather than failing on a conflict.
//
// No test in this file calls t.Parallel(): Go runs everything here
// sequentially unless a test opts out, which is what keeps this file's own
// organization-level and project-level tests from racing each other's writes
// to a *different* scope, but running the exact same test twice by hand
// against the same organization at the same time is not something this file
// tries to make safe.

// budgetAssertDataSourceListsBudget checks, straight from the flattened state
// attributes of a circleci_budgets data source instance, that one of its
// "budgets" entries has the given id and credits -- the plural data source's
// own [NET] coverage, folded into the shared lifecycle helper above rather
// than duplicated into a separate test, since it needs the exact same budget
// this test just created.
func budgetAssertDataSourceListsBudget(s *terraform.State, dataSourceAddress, wantID string, wantCredits int) error {
	rs, ok := s.RootModule().Resources[dataSourceAddress]
	if !ok {
		return fmt.Errorf("%s not found in state", dataSourceAddress)
	}

	for i := 0; ; i++ {
		idKey := fmt.Sprintf("budgets.%d.id", i)
		id, ok := rs.Primary.Attributes[idKey]
		if !ok {
			break
		}

		if id != wantID {
			continue
		}

		creditsKey := fmt.Sprintf("budgets.%d.credits", i)
		if got := rs.Primary.Attributes[creditsKey]; got != fmt.Sprintf("%d", wantCredits) {
			return fmt.Errorf("%s lists budget %s with credits = %s, want %d", dataSourceAddress, wantID, got, wantCredits)
		}

		return nil
	}

	return fmt.Errorf("%s does not list budget %s among its %q entries", dataSourceAddress, wantID, "budgets")
}

// budgetRealAPIClient builds a client that talks to the real API. Built from
// CIRCLE_TOKEN directly, matching otelRealAPIClient and the existing
// url_orb_allow_list_entry_net_test.go convention.
func budgetRealAPIClient() *circleci.Client {
	return circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})
}

// budgetRealAPILifecycleCase builds the shared create/update/import/destroy
// resource.TestCase for one scope (org-level when projectID is "",
// per-project otherwise), pinning the one behaviour this family was
// specifically flagged as needing live coverage for: id churns on every
// write to an existing scope, including one that only sends new credits, not
// a resource lifecycle bug the old "reuse the id" plan modifier would have
// papered over silently.
//
// It skips, naming why, rather than disturbing an existing budget at this
// scope it did not create: this is a singleton per scope, so there is no way
// to tell "a leftover from a crashed run of this same test" apart from "a
// budget someone is actually relying on," and only the first is safe to
// destroy.
//
// This builds a resource.TestCase rather than calling resource.Test itself
// so that each TestAcc* entry point below can call resource.Test directly
// with the result: that is what this package's network-coverage
// instrumentation (see TestEveryTestAccFunctionUsesAnAcceptanceRunner in
// vcs_gating_test.go) can actually see, where a call to resource.Test buried
// inside a shared helper could not be.
func budgetRealAPILifecycleCase(t *testing.T, orgID, projectID string) resource.TestCase {
	t.Helper()

	// The pre-flight FindBudget call below happens before resource.Test is
	// ever reached, so resource.Test's own TF_ACC gate would not stop it on
	// its own -- see testRequireTFACC's doc comment.
	testRequireTFACC(t)

	client := budgetRealAPIClient()
	ctx := context.Background()

	var projectIDPtr *string
	if projectID != "" {
		projectIDPtr = &projectID
	}

	if existing, ok, err := client.FindBudget(ctx, orgID, projectIDPtr); err != nil {
		t.Fatalf("checking for an existing budget at this scope before starting: %v", err)
	} else if ok {
		t.Skipf("organization %s already has a budget for this scope (id %s); this is a singleton "+
			"per scope and this test will not disturb a budget it did not create", orgID, existing.BudgetID)
	}

	// depends_on forces the data source to read after the resource exists:
	// the two have no reference between them otherwise, so Terraform is free
	// to read the data source first.
	dataSource := fmt.Sprintf(`
data "circleci_budgets" "all" {
  org_id     = %q
  depends_on = [circleci_budget.test]
}
`, orgID)

	configFor := func(credits int) string {
		if projectID == "" {
			return fmt.Sprintf(`
resource "circleci_budget" "test" {
  org_id  = %q
  credits = %d
}
`, orgID, credits) + dataSource
		}

		return fmt.Sprintf(`
resource "circleci_budget" "test" {
  org_id     = %q
  project_id = %q
  credits    = %d
}
`, orgID, projectID, credits) + dataSource
	}

	var firstID, secondID string

	return resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configFor(1000),
				Check: func(s *terraform.State) error {
					rs, ok := s.RootModule().Resources["circleci_budget.test"]
					if !ok {
						return fmt.Errorf("resource not found in state")
					}
					firstID = rs.Primary.Attributes["id"]
					if firstID == "" {
						return fmt.Errorf("created budget has no id recorded in state")
					}
					if got := rs.Primary.Attributes["credits"]; got != "1000" {
						return fmt.Errorf("credits = %s, want 1000", got)
					}
					if got := rs.Primary.Attributes["enforcement_type"]; got != circleci.BudgetEnforcementWarn {
						return fmt.Errorf("enforcement_type = %q, want the freshly-created default %q", got, circleci.BudgetEnforcementWarn)
					}

					// circleci_budgets (the plural data source) must list this same
					// budget with the same credits.
					return budgetAssertDataSourceListsBudget(s, "data.circleci_budgets.all", firstID, 1000)
				},
			},
			// A refresh with nothing to change must not itself churn the id: only a
			// write does that (SetBudget), and Read never calls it (FindBudget only).
			{
				Config:   configFor(1000),
				PlanOnly: true,
			},
			// The pin: an ordinary credits-only update is planned as an in-place
			// Update (not a replace -- credits carries no RequiresReplace), and yet
			// the live API mints an entirely new budget_id for it. A plan modifier
			// that told Terraform to keep the prior id here (UseStateForUnknown, which
			// budget_resource.go's schema deliberately omits -- see its comment on
			// the id attribute) would make this step fail with "Provider produced
			// inconsistent result after apply" the moment the real write landed a
			// different id than promised.
			{
				Config: configFor(2000),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_budget.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: func(s *terraform.State) error {
					rs, ok := s.RootModule().Resources["circleci_budget.test"]
					if !ok {
						return fmt.Errorf("resource not found in state")
					}
					secondID = rs.Primary.Attributes["id"]
					if secondID == "" {
						return fmt.Errorf("updated budget has no id recorded in state")
					}
					if secondID == firstID {
						return fmt.Errorf("budget id stayed %q across a credits-only update, want a new id: "+
							"CircleCI mints a new budget_id on every write to an existing scope, per measurement", firstID)
					}
					if got := rs.Primary.Attributes["credits"]; got != "2000" {
						return fmt.Errorf("credits = %s, want 2000", got)
					}

					return nil
				},
			},
			{
				ResourceName: "circleci_budget.test",
				ImportState:  true,
				ImportStateIdFunc: func(*terraform.State) (string, error) {
					if projectID == "" {
						return orgID, nil
					}

					return orgID + "/" + projectID, nil
				},
				ImportStateVerify: true,
				// consumption/percentage/threshold_exceeded are live spend statistics
				// CircleCI recomputes independently on every read (see Budget's doc
				// comment) -- not a value this resource writes and reads back
				// unchanged, so they can legitimately differ, by a small amount, from
				// one read to the next even with no configuration change at all.
				ImportStateVerifyIgnore: []string{"consumption", "percentage", "threshold_exceeded"},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			if secondID == "" {
				return fmt.Errorf("no post-update budget id was captured; the destroy check cannot verify anything")
			}

			if _, ok, err := client.FindBudget(ctx, orgID, projectIDPtr); err != nil {
				return fmt.Errorf("listing budgets for org %s after destroy: %w", orgID, err)
			} else if ok {
				return fmt.Errorf("a budget still exists at this scope in organization %s after destroy", orgID)
			}

			return nil
		},
	}
}

// TestAccBudgetResource_RealAPI_OrgLevel is the org-level (project_id null)
// half of the shared lifecycle above. It calls resource.Test itself, rather
// than leaving that call inside budgetRealAPILifecycleCase, so this
// package's network-coverage instrumentation can see that it does.
func TestAccBudgetResource_RealAPI_OrgLevel(t *testing.T) {
	resource.Test(t, budgetRealAPILifecycleCase(t, testOrgID(t), ""))
}

// TestAccBudgetResource_RealAPI_ProjectLevel is the per-project half,
// proving the two scopes are independently addressable against the live API
// (FindBudget's own project_id-based matching), not just in the fake.
func TestAccBudgetResource_RealAPI_ProjectLevel(t *testing.T) {
	orgID := testOrgID(t)
	resource.Test(t, budgetRealAPILifecycleCase(t, orgID, testProjectID(t)))
}

// TestBudgetRealAPI_ZeroCreditsRejectedOnTheWire is the live pin for the
// "credits=0 -> 400" measurement in budget_resource.go's schema comment. It
// calls SetBudget directly rather than through Terraform: the schema's own
// int64validator.AtLeast(1) would refuse a 0 at plan time before any request
// ever reached the API, which would prove the provider's validator agrees
// with itself, not that the live API actually enforces this the way the
// comment claims.
//
// Uses testProjectID's scope rather than the organization-level one: the
// write is rejected before anything is stored (confirmed below by comparing
// FindBudget before and after), so this is safe to run regardless of whether
// budgetRealAPILifecycle's project-level test also touches that scope in the
// same run -- a rejected write changes nothing for either to race over.
func TestBudgetRealAPI_ZeroCreditsRejectedOnTheWire(t *testing.T) {
	testAccPreCheck(t)
	testRequireTFACC(t)
	orgID := testOrgID(t)
	projectID := testProjectID(t)
	client := budgetRealAPIClient()
	ctx := context.Background()

	before, beforeOK, err := client.FindBudget(ctx, orgID, &projectID)
	if err != nil {
		t.Fatalf("reading the scope's budget before the rejected write: %v", err)
	}

	err = client.SetBudget(ctx, orgID, &projectID, 0)
	if err == nil {
		t.Fatal("SetBudget with credits=0 succeeded, want HTTP 400")
	}
	if !circleci.HasStatus(err, http.StatusBadRequest) {
		t.Errorf("SetBudget with credits=0 returned %v, want HTTP %d", err, http.StatusBadRequest)
	}

	after, afterOK, err := client.FindBudget(ctx, orgID, &projectID)
	if err != nil {
		t.Fatalf("reading the scope's budget after the rejected write: %v", err)
	}
	if beforeOK != afterOK || (beforeOK && before.BudgetID != after.BudgetID) {
		t.Errorf("the rejected credits=0 write changed this scope's budget (before: ok=%v %+v, after: ok=%v %+v), "+
			"want it left exactly as it was", beforeOK, before, afterOK, after)
	}
}

// TestBudgetRealAPI_DeleteMissingIsNotTrustworthyAsGone is the live pin for
// budgetResource.Delete's reason it corroborates a delete failure instead of
// trusting the status.
//
// It used to assert HTTP 500 specifically, because that is what this route
// answered when it was measured. On 2026-08-27 the acceptance suite caught it
// answering 404 {"error":"Budget not found"} instead — a cleaner answer, and a
// change we do not control. Confirmed by hand on three organizations before
// touching this test.
//
// So the assertion was pinning an incidental status rather than the property
// Delete actually depends on, and it failed while the provider was behaving
// correctly. It now asserts the property: the route reports a never-assigned id
// as an ERROR of some kind, and Delete must not conclude "already gone" from
// that error alone — whatever status CircleCI chooses next. The status is still
// reported, so a future change is visible in the log without being fatal.
func TestBudgetRealAPI_DeleteMissingIsNotTrustworthyAsGone(t *testing.T) {
	testAccPreCheck(t)
	testRequireTFACC(t)
	orgID := testOrgID(t)
	client := budgetRealAPIClient()

	err := client.DeleteBudget(context.Background(), orgID, "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("deleting a never-assigned budget_id succeeded, want an error")
	}

	// The observation, recorded rather than asserted: this route has already
	// changed its status once under us, and the provider's correctness does not
	// depend on which one it is.
	t.Logf("deleting a never-assigned budget_id answered: %v", err)

	// What the provider depends on is that Delete does not conclude "already
	// gone" from this error alone, and that is asserted where it belongs — at the
	// resource layer, against a double, in the two tests named for it
	// (tolerates-already-deleted and errors-when-the-scope-still-has-a-budget).
	// Re-asserting a specific status here would only re-create the brittleness
	// this rewrite removed.
}
