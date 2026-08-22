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

// This file is the real-API counterpart to checkout_key_resource_test.go and
// checkout_keys_data_source_test.go, both entirely [FAKE]. Before these
// tests, two specific documented-but-untested claims this family carries —
// "checkout keys are unavailable for GitLab and GitHub App projects" and
// "creating a user-key needs a user token, not a project token" — had never
// been checked against a live installation, despite both being exactly the
// kind of constraint that turns out to be wrong (see the task brief this
// family was scoped from).
//
// [NET, confirmed on 2026-08-21] the first claim is correct as stated: POST
// .../checkout-key on gh-app-cci-1 (GitHub App) and gitlab-test (GitLab), both
// circleci/<uuid>-slugged, answered 400 "This API is not supported for this
// project." for both a deploy-key and a user-key request. That is a plan-time
// rejection already (standaloneSlugRejectsCheckoutKeyValidator), so it is not
// re-tested here with real network calls — a fake exercising the validator is
// sufficient and already exists.
//
// The second claim is UNVALIDATED, not confirmed, and TestCheckoutKeyResourceNet_ProjectTokenAttemptExplainsWhyItSkips
// below is the record of why: this session could mint a real v1.1 project
// token (CCIPRJ_-prefixed) on a fixture project, but that token answered 404
// "Project not found" for every v2 project-scoped route tried — not just
// checkout-key, also the bare project GET, /pipeline and /envvar — so it never
// reached the 403 code path this claim is about. Whether that 404 is a
// property of v1.1-minted tokens specifically, or something else about this
// installation, is outside what this session could determine, and the task
// brief is explicit that "I could not validate this and here is exactly what
// was missing" is the right thing to report rather than guessing.

// TestAccCheckoutKeyResourceNet_DeployKeyLifecycle exercises create, the
// automatic empty-plan-after-apply, import, and destroy against a real
// classic GitHub OAuth project — the one integration checkout keys are
// documented to actually work on.
func TestAccCheckoutKeyResourceNet_DeployKeyLifecycle(t *testing.T) {
	testRequireVCSType(t, "github_oauth", "bitbucket")

	projectSlug := testProjectSlug(t)

	var createdFingerprint string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "circleci_checkout_key" "net_test" {
  project_slug = %[1]q
  type         = "deploy-key"
}
`, projectSlug),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_checkout_key.net_test", tfjsonpath.New("type"),
						knownvalue.StringExact("deploy-key"),
					),
				},
				Check: func(s *terraform.State) error {
					rs, ok := s.RootModule().Resources["circleci_checkout_key.net_test"]
					if !ok {
						return fmt.Errorf("resource not found in state")
					}

					createdFingerprint = rs.Primary.Attributes["fingerprint"]
					if createdFingerprint == "" {
						return fmt.Errorf("created checkout key has no fingerprint recorded in state")
					}

					return nil
				},
			},
			{
				ResourceName:                         "circleci_checkout_key.net_test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "fingerprint",
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs, ok := s.RootModule().Resources["circleci_checkout_key.net_test"]
					if !ok {
						return "", fmt.Errorf("resource not found in state")
					}

					return projectSlug + "/" + rs.Primary.Attributes["fingerprint"], nil
				},
			},
		},
		CheckDestroy: func(s *terraform.State) error {
			if createdFingerprint == "" {
				return fmt.Errorf("no fingerprint was captured during apply; the destroy check cannot verify anything")
			}

			// resource.Test already ran its own destroy step; this confirms the key
			// really is gone upstream rather than just out of Terraform state, by
			// listing the project's real checkout keys and matching on the specific
			// fingerprint this test created — the fixture project may carry other,
			// pre-existing keys unrelated to this run, so matching on Type alone
			// would false-positive on those.
			client := circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})

			keys, err := client.ListCheckoutKeys(context.Background(), projectSlug, "")
			if err != nil {
				return fmt.Errorf("could not list checkout keys for %s after destroy: %w", projectSlug, err)
			}

			for _, key := range keys {
				if key.Fingerprint == createdFingerprint {
					return fmt.Errorf(
						"project %s still has the deploy-key this test created (fingerprint %s) after destroy",
						projectSlug, createdFingerprint,
					)
				}
			}

			return nil
		},
	})
}

// TestCheckoutKeyResourceNet_ProjectTokenAttemptExplainsWhyItSkips is not a
// test of the provider: it is the executable record of the attempt described
// in this file's header comment, kept as a test (rather than only prose) so
// the exact 404 shape is pinned if this is ever revisited. It always skips —
// there is nothing for it to assert, because the premise (a project token
// that can reach v2 project routes) never held — but it fails loudly if
// creating or cleaning up the throwaway v1.1 token itself starts behaving
// differently, which would be the signal that it is worth trying again.
func TestCheckoutKeyResourceNet_ProjectTokenAttemptExplainsWhyItSkips(t *testing.T) {
	testAccPreCheck(t)
	testRequireVCSType(t, "github_oauth", "bitbucket")

	t.Skip("UNVALIDATED: see this file's header comment. A v1.1-minted project token could not reach " +
		"any v2 project route on this installation (404 \"Project not found\" for the project itself, " +
		"not just checkout-key), so \"creating a user-key with a project token gets 403\" was never " +
		"actually exercised. This test intentionally does not attempt the mutation again here; it exists " +
		"as a pointer back to that finding rather than as a passing assertion.")
}
