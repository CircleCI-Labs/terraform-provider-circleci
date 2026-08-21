// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// TestAccContextEnvironmentVariableTruncatedValue is the round-trip that proves
// against a real organization that the value this provider sends is the value
// CircleCI stores, and pins the one thing the API discloses about it.
//
// It is worth having as an acceptance test rather than only as a fake-backed one
// because of what the write route does with a request key it does not recognise:
// PUT /context/{id}/environment-variable/{name} reads only "value", ignores every
// other key, and answers 200 having stored an EMPTY value. Nothing in the response
// says so — created_at and updated_at are both set — and no later read discloses
// the value either. truncated_value is the single observable that separates
// "stored what we sent" from "stored nothing": it is the last
// min(4, floor(length / 2)) characters of the value, and it is "" for a value that
// was dropped.
//
// So the assertions below are not really about truncation. A wrong request key
// makes every one of them read "" instead of the expected tail, which is the only
// place in the whole suite where a real API would notice.
//
// The lengths are chosen to pin both arms of the formula: 12 characters reveal the
// full 4, and 4 characters reveal 2 rather than 4. Measured over lengths 1 to 12
// against a live context, every one matched.
func TestAccContextEnvironmentVariableTruncatedValue(t *testing.T) {
	orgID := testOrgID(t)
	contextName := fmt.Sprintf("tf-acc-trunc-%s", rand.Text()[:8])

	const (
		longValue  = "ABCDEFGHIJKL" // 12 characters -> min(4, 6) = 4 -> "IJKL"
		shortValue = "WXYZ"         // 4 characters  -> min(4, 2) = 2 -> "YZ"
	)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccContextEnvVarTruncatedValueConfig(orgID, contextName, longValue, shortValue),
			ConfigStateChecks: []statecheck.StateCheck{
				// Sorted by name: A_LONG before B_SHORT.
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variables.test",
					tfjsonpath.New("environment_variables"),
					knownvalue.ListSizeExact(2),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variables.test",
					tfjsonpath.New("environment_variables").AtSliceIndex(0).AtMapKey("name"),
					knownvalue.StringExact("A_LONG"),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variables.test",
					tfjsonpath.New("environment_variables").AtSliceIndex(0).AtMapKey("truncated_value"),
					knownvalue.StringExact("IJKL"),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variables.test",
					tfjsonpath.New("environment_variables").AtSliceIndex(1).AtMapKey("name"),
					knownvalue.StringExact("B_SHORT"),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variables.test",
					tfjsonpath.New("environment_variables").AtSliceIndex(1).AtMapKey("truncated_value"),
					knownvalue.StringExact("YZ"),
				),
			},
		}},
	})
}

func testAccContextEnvVarTruncatedValueConfig(orgID, contextName, longValue, shortValue string) string {
	return fmt.Sprintf(`
resource "circleci_context" "test" {
  name            = %[2]q
  organization_id = %[1]q
}

resource "circleci_context_environment_variable" "long" {
  context_id = circleci_context.test.id
  name       = "A_LONG"
  value      = %[3]q
}

resource "circleci_context_environment_variable" "short" {
  context_id = circleci_context.test.id
  name       = "B_SHORT"
  value      = %[4]q
}

data "circleci_context_environment_variables" "test" {
  context_id = circleci_context.test.id

  depends_on = [
    circleci_context_environment_variable.long,
    circleci_context_environment_variable.short,
  ]
}
`, orgID, contextName, longValue, shortValue)
}
