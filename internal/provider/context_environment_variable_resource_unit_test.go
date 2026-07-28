// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// These are fake-server-backed unit tests for
// context_environment_variable_resource.go: they need no TF_ACC and no
// CircleCI credentials, unlike TestAccContextEnvironmentVariableResource.

const contextEnvVarUnitContextID = "ctx-fixed-2"

// contextEnvVarResourceUnitConfig builds a
// circleci_context_environment_variable config. Every call site in this
// package points context_id at contextEnvVarUnitContextID and name at
// "API_KEY"; those are fixed here rather than threaded through as parameters
// that would never vary.
func contextEnvVarResourceUnitConfig(host, value string) string {
	return legacyContextProviderConfig(host) + fmt.Sprintf(`
resource "circleci_context_environment_variable" "test" {
  context_id = %[1]q
  name       = "API_KEY"
  value      = %[2]q
}
`, contextEnvVarUnitContextID, value)
}

func TestContextEnvVarResourceUnit_CRUD(t *testing.T) {
	api, host := newContextLegacyAPI(t)
	api.seedContext(contextEnvVarUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextEnvVarResourceUnitConfig(host, "s3cr3t"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("name"), knownvalue.StringExact("API_KEY")),
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("value"), knownvalue.StringExact("s3cr3t")),
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("context_id"), knownvalue.StringExact(contextEnvVarUnitContextID)),
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("created_at"), knownvalue.StringExact("2024-01-02T03:04:05.000Z")),
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("updated_at"), knownvalue.StringExact("2024-06-01T00:00:00.000Z")),
				},
			},
			// Update: the value changes but created_at must stay put, since the PUT
			// route is an atomic upsert rather than a delete-then-recreate.
			{
				Config: contextEnvVarResourceUnitConfig(host, "n3w-s3cr3t"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("value"), knownvalue.StringExact("n3w-s3cr3t")),
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("created_at"), knownvalue.StringExact("2024-01-02T03:04:05.000Z")),
					statecheck.ExpectKnownValue("circleci_context_environment_variable.test", tfjsonpath.New("updated_at"), knownvalue.StringExact("2024-06-02T00:00:00.000Z")),
				},
			},
		},
	})

	var sawCreate, sawUpdate, sawDelete bool
	putCount := 0
	for _, req := range api.recorded() {
		switch req {
		case "PUT /api/v2/context/" + contextEnvVarUnitContextID + "/environment-variable/API_KEY":
			putCount++
		case "DELETE /api/v2/context/" + contextEnvVarUnitContextID + "/environment-variable/API_KEY":
			sawDelete = true
		}
	}
	sawCreate = putCount >= 1
	sawUpdate = putCount >= 2
	if !sawCreate {
		t.Errorf("no PUT request with the expected URI, got %q", api.recorded())
	}
	if !sawUpdate {
		t.Errorf("expected at least 2 PUT requests (create + update), got %d: %q", putCount, api.recorded())
	}
	if !sawDelete {
		t.Errorf("no delete request with the expected URI, got %q", api.recorded())
	}
}

// TestContextEnvVarResourceUnit_Import mirrors
// TestAccContextEnvironmentVariableResource's workaround: value cannot be read
// back from the API (the API never returns it, only a truncated_value used
// elsewhere), so ImportStateVerify must ignore it and the config must be
// re-applied afterwards to reconcile it into state.
func TestContextEnvVarResourceUnit_Import(t *testing.T) {
	api, host := newContextLegacyAPI(t)
	api.seedContext(contextEnvVarUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextEnvVarResourceUnitConfig(host, "s3cr3t"),
			},
			{
				ResourceName:                         "circleci_context_environment_variable.test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
				ImportStateVerifyIgnore:              []string{"value"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources["circleci_context_environment_variable.test"]

					return fmt.Sprintf("%s/%s", rs.Primary.Attributes["context_id"], rs.Primary.Attributes["name"]), nil
				},
			},
			// Re-apply so state has "value" again, allowing Destroy to run cleanly.
			{
				Config: contextEnvVarResourceUnitConfig(host, "s3cr3t"),
			},
		},
	})
}

func TestContextEnvVarResourceUnit_ImportInvalidID(t *testing.T) {
	api, host := newContextLegacyAPI(t)
	api.seedContext(contextEnvVarUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextEnvVarResourceUnitConfig(host, "s3cr3t"),
			},
			{
				ResourceName:  "circleci_context_environment_variable.test",
				ImportState:   true,
				ImportStateId: "no-slash-here",
				ExpectError:   regexp.MustCompile(`Invalid Import ID Format`),
			},
		},
	})
}

// TestContextEnvVarResourceUnit_RemovedOutsideTerraform proves drift detection:
// the env var vanishing from the list drops the resource from state, so the
// next plan recreates it.
func TestContextEnvVarResourceUnit_RemovedOutsideTerraform(t *testing.T) {
	api, host := newContextLegacyAPI(t)
	api.seedContext(contextEnvVarUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextEnvVarResourceUnitConfig(host, "s3cr3t"),
			},
			{
				PreConfig:          func() { api.removeEnvVar(contextEnvVarUnitContextID, "API_KEY") },
				Config:             contextEnvVarResourceUnitConfig(host, "s3cr3t"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestContextEnvVarResourceUnit_ReadAPIError proves a 4xx from the API on
// refresh surfaces as a Terraform diagnostic rather than silently dropping the
// resource from state.
func TestContextEnvVarResourceUnit_ReadAPIError(t *testing.T) {
	api, host := newContextLegacyAPI(t)
	api.seedContext(contextEnvVarUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextEnvVarResourceUnitConfig(host, "s3cr3t"),
			},
			{
				// A 4xx status is used rather than a 5xx so the legacy client's
				// built-in retry policy (RetryMax: 10, exponential backoff) does not
				// retry the request for real seconds before giving up.
				PreConfig:   func() { api.fail(400, "Bad request.") },
				Config:      contextEnvVarResourceUnitConfig(host, "s3cr3t"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Unable to Read CircleCI context environment variable`),
			},
			{
				// Clear the failure so the framework's own destroy step, which runs
				// after the last step regardless of outcome, can actually succeed.
				PreConfig: func() { api.fail(0, "") },
				Config:    contextEnvVarResourceUnitConfig(host, "s3cr3t"),
			},
		},
	})
}
