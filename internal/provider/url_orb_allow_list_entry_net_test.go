// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// TestAccURLOrbAllowListEntryNet_Lifecycle is the real-API counterpart to
// url_orb_allow_list_entry_resource_test.go, which is entirely [FAKE]
// (newFakeOrbAllowListAPI). Before this test, circleci_url_orb_allow_list_entry
// had never been run against a real CircleCI installation: this confirms
// create, the empty-plan-after-apply that resource.Test checks automatically,
// import, and — the specific thing this family was asked to confirm — that
// destroy actually removes the entry rather than merely dropping it from
// state, per DeleteURLOrbAllowListEntry's documented semantics.
func TestAccURLOrbAllowListEntryNet_Lifecycle(t *testing.T) {
	orgID := testOrgID(t)

	client := circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})

	const (
		name   = "t2-breadth-probe"
		prefix = "https://raw.githubusercontent.com/CircleCI-Public/t2-breadth-probe/main/"
		auth   = "none"
	)

	var createdID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "circleci_url_orb_allow_list_entry" "net_test" {
  organization = %[1]q
  name         = %[2]q
  prefix       = %[3]q
  auth         = %[4]q
}
`, orgID, name, prefix, auth),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_url_orb_allow_list_entry.net_test", tfjsonpath.New("name"),
						knownvalue.StringExact(name),
					),
					statecheck.ExpectKnownValue(
						"circleci_url_orb_allow_list_entry.net_test", tfjsonpath.New("prefix"),
						knownvalue.StringExact(prefix),
					),
				},
				Check: func(s *terraform.State) error {
					rs, ok := s.RootModule().Resources["circleci_url_orb_allow_list_entry.net_test"]
					if !ok {
						return fmt.Errorf("resource not found in state")
					}

					createdID = rs.Primary.Attributes["id"]
					if createdID == "" {
						return fmt.Errorf("created entry has no id recorded in state")
					}

					// Confirm the entry is actually visible through the client's own Get,
					// which lists the organization's allow list rather than trusting the
					// resource's own cached state.
					entry, err := client.GetURLOrbAllowListEntry(context.Background(), orgID, createdID)
					if err != nil {
						return fmt.Errorf("entry %s was not readable from the live API right after create: %w", createdID, err)
					}
					if entry.Prefix != prefix {
						return fmt.Errorf("live entry prefix = %q, want %q", entry.Prefix, prefix)
					}

					return nil
				},
			},
			// Import round-trip against the real GET route.
			{
				ResourceName:      "circleci_url_orb_allow_list_entry.net_test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) {
					return orgID + "/" + createdID, nil
				},
			},
		},
		// Runs after resource.Test's own destroy step. The specific claim this
		// family was asked to confirm: destroy must actually remove the entry
		// upstream, not merely stop tracking it (unlike circleci_organization_contacts,
		// which deliberately leaves its remote state alone — see that resource's
		// own Delete).
		CheckDestroy: func(*terraform.State) error {
			if createdID == "" {
				return fmt.Errorf("no entry id was captured during apply; the destroy check cannot verify anything")
			}

			_, err := client.GetURLOrbAllowListEntry(context.Background(), orgID, createdID)
			if err == nil {
				return fmt.Errorf("entry %s in organization %s is still present after destroy; "+
					"destroy must remove it, not merely stop tracking it", createdID, orgID)
			}
			if !circleci.IsNotFound(err) {
				return fmt.Errorf("checking whether entry %s was removed returned a non-not-found "+
					"error: %w", createdID, err)
			}

			return nil
		},
	})
}
