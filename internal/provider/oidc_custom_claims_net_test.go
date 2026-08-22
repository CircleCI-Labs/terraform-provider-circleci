// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"crypto/rand"
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

// This file is the real-API counterpart to oidc_custom_claims_resource_test.go,
// which is entirely [FAKE]. circleci_oidc_custom_claims is one of the two
// resources TESTING.md names as an actual security control (the other is the
// config policy bundle): destroying it resets the org's or project's OIDC
// claims, which changes what a cloud provider's trust policy will accept from
// jobs in that scope. Every test here therefore captures the pre-existing
// claims and restores them in t.Cleanup, and reports (t.Errorf) rather than
// swallows a restore failure.
//
// The task brief specifically asks for project-scope coverage beyond the one
// standalone organization it was previously confirmed on by hand; testProjectID
// resolves to a real, writable project for whichever integration is active
// (see .circleci/config.yml, which sets a *_PROJECT_ID for every acceptance
// job), so TestAccOIDCCustomClaimsResourceNet_ProjectScope below runs against
// all four organizations this suite has, both standalone and classic.

// oidcClaimsTestClient builds a client using CIRCLE_TOKEN, for the direct API
// calls (capture/restore/verify) these tests make outside of Terraform.
func oidcClaimsTestClient() *circleci.Client {
	return circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})
}

// restoreOIDCClaims puts a scope's claims back to what was captured in before,
// reporting (never swallowing) a failure to do so. Called from t.Cleanup, so it
// always runs, even when the test itself failed partway.
func restoreOIDCClaims(t *testing.T, client *circleci.Client, orgID, projectID string, before *circleci.OIDCCustomClaims) {
	t.Helper()

	if before.IsZero() {
		// Nothing to restore: this scope had no customization, and the
		// resource's own Delete (which TestCase's automatic destroy already
		// ran) resets exactly to that state.
		return
	}

	update := circleci.OIDCCustomClaimsUpdate{}
	if len(before.Audience) > 0 {
		audience := append([]string(nil), before.Audience...)
		update.Audience = &audience
	}
	if before.TTL != "" {
		ttl := before.TTL
		update.TTL = &ttl
	}

	if _, err := client.UpdateOIDCCustomClaims(context.Background(), orgID, projectID, update); err != nil {
		t.Errorf(
			"restoring pre-existing OIDC custom claims for organization %s (project %q) after the test: %v",
			orgID, projectID, err,
		)
	}
}

// TestAccOIDCCustomClaimsResourceNet_OrgScope exercises create, update,
// import, and destroy against a real organization's OIDC claims, and pins two
// [NET, measured 2026-08-21] facts recorded in oidc.go: resetting a claim
// leaves its *_updated_at tombstone populated forever, and the API reformats a
// submitted ttl into its own canonical spelling (which is why ttl is excluded
// from ImportStateVerify below, exactly as in the fake-backed equivalent).
func TestAccOIDCCustomClaimsResourceNet_OrgScope(t *testing.T) {
	testAccPreCheck(t)
	orgID := testOrgID(t)
	// OIDC custom claims behave identically regardless of VCS integration or
	// organization class.
	testRequireVCSType(t, acceptanceVCSTypes...)

	client := oidcClaimsTestClient()
	ctx := context.Background()

	before, err := client.GetOIDCCustomClaims(ctx, orgID, "")
	if err != nil {
		t.Fatalf("reading the pre-existing OIDC custom claims for organization %s: %v", orgID, err)
	}
	t.Cleanup(func() { restoreOIDCClaims(t, client, orgID, "", before) })

	audience := fmt.Sprintf("https://acc-test-%s.example.com", rand.Text())

	config := func(ttl string) string {
		return fmt.Sprintf(`
resource "circleci_oidc_custom_claims" "net_test" {
  organization_id = %q
  audience        = [%q]
  ttl             = %q
}
`, orgID, audience, ttl)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("45m"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.net_test", tfjsonpath.New("ttl"), knownvalue.StringExact("45m"),
					),
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.net_test", tfjsonpath.New("audience_updated_at"), knownvalue.NotNull(),
					),
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.net_test", tfjsonpath.New("ttl_updated_at"), knownvalue.NotNull(),
					),
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.net_test", tfjsonpath.New("project_id"), knownvalue.Null(),
					),
				},
			},
			// Update in place: the claims change but the scope does not.
			{
				Config: config("30m"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.net_test", tfjsonpath.New("ttl"), knownvalue.StringExact("30m"),
					),
				},
			},
			{
				ResourceName:                         "circleci_oidc_custom_claims.net_test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "organization_id",
				// The API reformats the stored duration (e.g. "30m" comes back as
				// "30m0s"); durationValue treats those as equal but
				// ImportStateVerify compares raw strings and cannot see that, so
				// this alone would report a false mismatch. See
				// TestAccOIDCCustomClaimsTTLSpellingDoesNotDiff (fake-backed) for
				// the equal-duration behavior this can't directly assert.
				ImportStateVerifyIgnore: []string{"ttl"},
				ImportStateId:           orgID,
			},
			// Delete testing automatically occurs at the end of TestCase.
		},
		CheckDestroy: func(*terraform.State) error {
			claims, err := client.GetOIDCCustomClaims(ctx, orgID, "")
			if err != nil {
				return fmt.Errorf("reading OIDC claims after destroy: %w", err)
			}
			if !claims.IsZero() {
				return fmt.Errorf("claims are not reset to defaults after destroy: %+v", claims)
			}
			// [NET, measured 2026-08-21] the *_updated_at tombstones must survive
			// the reset — see OIDCCustomClaims.AudienceUpdatedAt's doc comment.
			if claims.AudienceUpdatedAt == "" {
				return fmt.Errorf(
					"audience_updated_at was cleared by the destroy-time reset; production never clears " +
						"it once a scope has history",
				)
			}
			if claims.TTLUpdatedAt == "" {
				return fmt.Errorf(
					"ttl_updated_at was cleared by the destroy-time reset; production never clears it " +
						"once a scope has history",
				)
			}

			return nil
		},
	})
}

// TestAccOIDCCustomClaimsResourceNet_ProjectScope covers the project-scoped
// variant on every organization class this suite has, not only the single
// standalone organization the earlier one-time pass confirmed it on.
func TestAccOIDCCustomClaimsResourceNet_ProjectScope(t *testing.T) {
	testAccPreCheck(t)
	orgID := testOrgID(t)
	projectID := testProjectID(t)
	testRequireVCSType(t, acceptanceVCSTypes...)

	client := oidcClaimsTestClient()
	ctx := context.Background()

	before, err := client.GetOIDCCustomClaims(ctx, orgID, projectID)
	if err != nil {
		t.Fatalf("reading the pre-existing OIDC custom claims for project %s: %v", projectID, err)
	}
	t.Cleanup(func() { restoreOIDCClaims(t, client, orgID, projectID, before) })

	config := fmt.Sprintf(`
resource "circleci_oidc_custom_claims" "net_test" {
  organization_id = %q
  project_id       = %q
  ttl              = "20m"
}
`, orgID, projectID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.net_test", tfjsonpath.New("project_id"), knownvalue.StringExact(projectID),
					),
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.net_test", tfjsonpath.New("ttl"), knownvalue.StringExact("20m"),
					),
					// audience was never configured for this scope, so it stays
					// null, exactly as the fake-backed equivalent asserts.
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.net_test", tfjsonpath.New("audience"), knownvalue.Null(),
					),
				},
				Check: func(*terraform.State) error {
					claims, err := client.GetOIDCCustomClaims(ctx, orgID, projectID)
					if err != nil {
						return fmt.Errorf("reading live project-scope claims: %w", err)
					}
					if claims.ProjectID != projectID {
						return fmt.Errorf("live claims project_id = %q, want %q", claims.ProjectID, projectID)
					}

					return nil
				},
			},
			{
				ResourceName:                         "circleci_oidc_custom_claims.net_test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "organization_id",
				ImportStateVerifyIgnore:              []string{"ttl"}, // see the org-scope test above
				ImportStateId:                        orgID + "/" + projectID,
			},
			// Delete testing automatically occurs at the end of TestCase.
		},
		CheckDestroy: func(*terraform.State) error {
			claims, err := client.GetOIDCCustomClaims(ctx, orgID, projectID)
			if err != nil {
				return fmt.Errorf("reading OIDC claims after destroy: %w", err)
			}
			if !claims.IsZero() {
				return fmt.Errorf("project-scope claims are not reset to defaults after destroy: %+v", claims)
			}

			return nil
		},
	})
}
