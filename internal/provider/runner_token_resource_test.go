// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// The resource class here is unique per run, and no longer the same literal
// name TestAccRunnerResourceClassResource uses. Those two tests were creating
// "<namespace>/acc-test-runner" each: they never overlap in a single sequential
// run, but one of them dying between create and destroy left the other failing
// on a 409 for a resource class it did not create — a leak in one test
// surfacing as a permanent failure in another.
//
// The nickname is randomised too. A duplicate nickname is not a conflict (a
// token is keyed on its own id), so this one is about attribution rather than
// collision: a token left behind by an interrupted run can be told apart from
// one belonging to a live run.
func TestAccRunnerTokenResource(t *testing.T) {
	organizationId := testOrgID(t)
	resourceClass := testUniqueRunnerResourceClass(t, "acc-test-runner-token")
	nickname := fmt.Sprintf("acc-test-token-%s", rand.Text())
	uuidRegex := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccRunnerTokenConfig(organizationId, resourceClass, nickname),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("resource_class"),
						knownvalue.StringExact(resourceClass),
					),
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("nickname"),
						knownvalue.StringExact(nickname),
					),
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("id"),
						knownvalue.StringRegexp(uuidRegex),
					),
					// token is sensitive but should be non-empty after create
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("token"),
						knownvalue.NotNull(),
					),
				},
			},
			// ImportState testing — token value will be empty after import since it's write-once
			{
				ResourceName:      "circleci_runner_token.test",
				ImportState:       true,
				ImportStateVerify: true,
				// Neither organization attribute survives an import: the import ID
				// is "resource_class/token_id" and the token representation carries
				// no organization, so there is nothing for Read to fill them from.
				ImportStateVerifyIgnore: []string{"token", "organization_id", "org_id"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rc, found := s.RootModule().Resources["circleci_runner_token.test"].Primary.Attributes["resource_class"]
					if !found {
						return "", errors.New("attribute resource_class not found")
					}
					id, found := s.RootModule().Resources["circleci_runner_token.test"].Primary.Attributes["id"]
					if !found {
						return "", errors.New("attribute id not found")
					}
					return fmt.Sprintf("%s/%s", rc, id), nil
				},
			},
			// Update after import testing. organization_id/org_id are not
			// RequiresReplace on this resource (see the Schema method's comment
			// on why), so filling one back in from configuration is a genuine
			// in-place update, not a replacement -- unlike before this was
			// fixed, when supplying the organization ConfigValidators requires
			// would destroy and recreate the imported token. See
			// TestRunnerTokenImport for the fake-backed version of this.
			{
				Config: testAccRunnerTokenConfig(organizationId, resourceClass, nickname),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(organizationId),
					),
				},
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

// TestRunnerTokenImport is the fake-backed counterpart to
// TestAccRunnerTokenResource's import step, and exists for the same reason
// TestRunnerResourceClassImport does: the acceptance test needs TF_ACC and
// real credentials, so it never runs for a developer running `go test ./...`.
//
// It is also a regression test for a real defect this review found: before it
// was fixed, organization_id/org_id were RequiresReplaceIfConfigured on this
// resource. The token representation carries no organization for Read to
// recover, so neither attribute survives an import -- and because
// ConfigValidators requires exactly one of them to be configured, the very
// first plan after import always supplied one, which forced a replacement:
// destroying the imported token (permanently invalidating it) and creating a
// brand new one in its place. Importing a runner token was therefore actively
// destructive, not merely unable to recover the secret value. This asserts
// the fixed behaviour: a genuine, in-place Update, matching
// circleci_runner_resource_class.
func TestRunnerTokenImport(t *testing.T) {
	api := newRunnerFakeAPI(t)
	api.respond("GET", "/api/v3/runner/token", `{"items":[
	  {"id": "11111111-2222-3333-4444-555555555555", "nickname": "ci", "resource_class": "acc-ns/linux", "created_at": "2026-01-01T00:00:00Z"}
	]}`)

	const orgID = "00000000-1111-2222-3333-444444444444"

	config := runnerProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_runner_token" "test" {
  organization_id = %q
  resource_class  = "acc-ns/linux"
  nickname        = "ci"
}
`, orgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "circleci_runner_token.test",
				ImportState:        true,
				ImportStateId:      "acc-ns/linux/11111111-2222-3333-4444-555555555555",
				ImportStatePersist: true,
				Config:             config,
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_runner_token.test", plancheck.ResourceActionUpdate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(orgID),
					),
					// token stays null: the API never discloses it again after create,
					// so nothing ever fills it in after an import.
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("token"),
						knownvalue.Null(),
					),
				},
			},
		},
	})
}

// TestRunnerTokenResource_ReadPreservesTokenAcrossRefresh is a fake-backed
// regression test for the central claim this resource makes about the
// secret: once a token is created, its value survives a later refresh, even
// though [NET] confirms (see TestAccRunnerTokenResource, which passed
// against both a standalone and a classic real organization) that
// GET /api/v3/runner/token never discloses it again.
//
// The fake mirrors that: the create response carries the secret, the list
// response (which Read calls) omits the "token" key entirely, matching the
// real API's json:"token,omitempty" shape. A plain Config-only step is not
// enough to catch a regression here: terraform-plugin-testing's post-apply
// "refresh plan" check only fails on an unexpected DIFF, and a Computed
// attribute silently going from a known value to null during refresh is not
// a diff by that check's definition (nothing in config asked for a specific
// value, so the planned value simply tracks whatever Read produced, the same
// as the null-after-import case in TestRunnerTokenImport). So this uses an
// explicit RefreshState step and inspects the actually-refreshed state on
// disk with a plain TestCheckFunc, the same shape
// TestContextResourceUnit_DestroyAlreadyGoneSucceeds uses to make an
// otherwise-invisible Read outcome assertable.
//
// Confirmed by reverting the fix: adding `state.Token = types.StringNull()`
// right after the "preserve value from state" comment in Read makes this
// test fail with "Attribute 'token' not found" (the refreshed state has no
// value for it at all); removing that line restores a pass. See the report
// for both outputs.
func TestRunnerTokenResource_ReadPreservesTokenAcrossRefresh(t *testing.T) {
	api := newRunnerFakeAPI(t)
	const tokenID = "11111111-2222-3333-4444-555555555555"
	const secretValue = "fake-secret-jwvI2h8fW3q"

	api.respond("POST", "/api/v3/runner/token", `{
		"id": "`+tokenID+`",
		"resource_class": "acc-ns/linux",
		"nickname": "ci",
		"created_at": "2026-01-01T00:00:00Z",
		"token": "`+secretValue+`"
	}`)
	// The list response Read calls omits "token" entirely -- the real shape,
	// per Token's json:"token,omitempty" tag and the [NET] observation above.
	api.respond("GET", "/api/v3/runner/token", `{"items":[
		{"id": "`+tokenID+`", "resource_class": "acc-ns/linux", "nickname": "ci", "created_at": "2026-01-01T00:00:00Z"}
	]}`)

	config := runnerProviderConfig(api.URL()) + `
resource "circleci_runner_token" "test" {
  org_id         = "00000000-1111-2222-3333-444444444444"
  resource_class = "acc-ns/linux"
  nickname       = "ci"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_runner_token.test",
						tfjsonpath.New("token"),
						knownvalue.StringExact(secretValue),
					),
				},
			},
			{
				RefreshState: true,
				Check: resource.TestCheckResourceAttr(
					"circleci_runner_token.test", "token", secretValue,
				),
			},
		},
	})

	if requests := api.requestsFor("GET", "/api/v3/runner/token"); len(requests) == 0 {
		t.Error("expected at least one GET /api/v3/runner/token request (the explicit refresh), got none")
	}
}

// TestRunnerTokenResource_DeleteAlreadyGoneSucceeds and
// TestRunnerTokenResource_DeleteFailureSurfacesError together are the
// resource-level counterpart to circleci.TestDeleteTokenNotFound: that test
// only proves the client function returns an error satisfying IsNotFound for
// a 404, not that Delete on the resource actually swallows it (and only it).
// These call Delete directly, the same way
// TestGroupMembershipResourceDelete_toleratesAlreadyDeletedGroup does for
// group_membership, to check both directions the task description asks
// for: absence is success, but a genuine failure still surfaces.
func TestRunnerTokenResource_DeleteAlreadyGoneSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			http.Error(w, `{"message":"not found with provided token: check permissions to view or admin self-hosted runners"}`, http.StatusNotFound)

			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	r := &runnerTokenResource{client: circleci.New(circleci.Config{Host: "http://127.0.0.1:1", RunnerHost: srv.URL, Token: "tok"})}
	schema := runnerTokenResourceSchemaForTest(t)
	state := runnerTokenStateForTest(t, schema, runnerTokenResourceModel{
		Id:            types.StringValue("11111111-2222-3333-4444-555555555555"),
		ResourceClass: types.StringValue("acc-ns/linux"),
		Nickname:      types.StringValue("ci"),
		CreatedAt:     types.StringValue("2026-01-01T00:00:00Z"),
		Token:         types.StringValue("secret"),
	})

	resp := &fwresource.DeleteResponse{State: state}
	r.Delete(t.Context(), fwresource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("Delete of an already-gone token returned diagnostics, want none: %v", resp.Diagnostics)
	}
}

// TestRunnerTokenResource_DeleteFailureSurfacesError is the flip side of
// TestRunnerTokenResource_DeleteAlreadyGoneSucceeds: a delete failure that is
// NOT "already gone" must still fail the destroy, or a genuinely-stuck
// token would be silently dropped from state while remaining live on the
// service.
func TestRunnerTokenResource_DeleteFailureSurfacesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			http.Error(w, `{"message":"internal error"}`, http.StatusInternalServerError)

			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	r := &runnerTokenResource{client: circleci.New(circleci.Config{Host: "http://127.0.0.1:1", RunnerHost: srv.URL, Token: "tok"})}
	schema := runnerTokenResourceSchemaForTest(t)
	state := runnerTokenStateForTest(t, schema, runnerTokenResourceModel{
		Id:            types.StringValue("11111111-2222-3333-4444-555555555555"),
		ResourceClass: types.StringValue("acc-ns/linux"),
		Nickname:      types.StringValue("ci"),
		CreatedAt:     types.StringValue("2026-01-01T00:00:00Z"),
		Token:         types.StringValue("secret"),
	})

	resp := &fwresource.DeleteResponse{State: state}
	r.Delete(t.Context(), fwresource.DeleteRequest{State: state}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("Delete swallowed a non-404 failure, want it to surface as an error")
	}
}

// runnerTokenResourceSchemaForTest returns the resource's schema, the same
// way budgetResourceSchemaForTest does.
func runnerTokenResourceSchemaForTest(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	(&runnerTokenResource{}).Schema(t.Context(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}

	return resp.Schema
}

// runnerTokenStateForTest builds a tfsdk.State from a fully-populated model,
// the same way budgetStateForTest does.
func runnerTokenStateForTest(t *testing.T, schema rschema.Schema, model runnerTokenResourceModel) tfsdk.State {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a state value: %+v", diags)
	}

	return state
}

func testAccRunnerTokenConfig(organizationId, resourceClass, nickname string) string {
	return fmt.Sprintf(`
resource "circleci_runner_resource_class" "test" {
  organization_id = %[1]q
  resource_class  = %[2]q
}

resource "circleci_runner_token" "test" {
  organization_id = %[1]q
  resource_class  = circleci_runner_resource_class.test.resource_class
  nickname        = %[3]q
}
`, organizationId, resourceClass, nickname)
}
