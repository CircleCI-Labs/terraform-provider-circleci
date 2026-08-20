// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// testAccProtoV6ProviderFactories is used to instantiate a provider during acceptance testing.
// The factory function is called for each Terraform CLI command to create a provider
// server that the CLI can connect to and interact with.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"circleci": providerserver.NewProtocol6WithError(New("test")()),
}

// testAccProtoV6ProviderFactoriesWithEcho includes the echo provider alongside the circleci provider.
// It allows for testing assertions on data returned by an ephemeral resource during Open.
// The echoprovider is used to arrange tests by echoing ephemeral data into the Terraform state.
// This lets the data be referenced in test assertions with state checks.
/*
var testAccProtoV6ProviderFactoriesWithEcho = map[string]func() (tfprotov6.ProviderServer, error){
	"circleci": providerserver.NewProtocol6WithError(New("test")()),
	"echo":     echoprovider.NewProviderServer(),
}
*/

func testAccPreCheck(t *testing.T) {
	// You can add code here to run prior to any test case execution, for example assertions
	// about the appropriate environment variables being set are common to see in a pre-check
	// function.
	//
	// Missing credentials skip rather than fail so that the suite stays usable
	// for developers without a provisioned CircleCI test account. The
	// fixture identifiers themselves are resolved by the helpers in
	// acctest_test.go, which skip the same way.
	// The token is resolved per integration, so a run against one organization
	// can authenticate as a different account from a run against another:
	// CIRCLECI_TEST_<integration>_TOKEN wins when set, and CIRCLE_TOKEN is the
	// fallback for the common case of one token that reaches every test
	// organization. Without this the per-integration TOKEN variables would be
	// documented and inert, which is worse than not having them — a maintainer
	// would set one and watch it be ignored.
	if token := activeIntegrationToken(t); token != "" {
		t.Setenv("CIRCLE_TOKEN", token)
	}

	if os.Getenv("CIRCLE_TOKEN") == "" {
		t.Skip("no API token for acceptance tests: set CIRCLECI_TEST_<integration>_TOKEN for the " +
			"active integration, or CIRCLE_TOKEN; see the Development section of README.md.")
	}
}

// activeIntegrationToken returns the API token for the integration under test,
// or "" when none is configured. It deliberately does not skip: the caller
// decides, because CIRCLE_TOKEN alone is a legitimate configuration.
func activeIntegrationToken(t *testing.T) string {
	t.Helper()

	key, ok := activeIntegrationKey(os.Getenv("CIRCLECI_TEST_VCS_TYPE"))
	if !ok {
		return ""
	}

	value := os.Getenv("CIRCLECI_TEST_" + key + "_TOKEN")
	if isPlaceholder(value) {
		return ""
	}

	return value
}

// TestActiveIntegrationTokenResolution pins the token resolution, because the
// failure it prevents is silent: a per-integration token that is set and then
// ignored looks identical to one that was never read, and the run simply
// authenticates as whatever CIRCLE_TOKEN happened to hold.
func TestActiveIntegrationTokenResolution(t *testing.T) {
	t.Run("the active integration's token wins", func(t *testing.T) {
		t.Setenv("CIRCLECI_TEST_VCS_TYPE", "github_oauth")
		t.Setenv("CIRCLECI_TEST_GH_OAUTH_TOKEN", "oauth-token")
		t.Setenv("CIRCLECI_TEST_GH_APP_TOKEN", "app-token")

		if got := activeIntegrationToken(t); got != "oauth-token" {
			t.Errorf("activeIntegrationToken() = %q, want the GH_OAUTH token", got)
		}
	})

	t.Run("a placeholder counts as unset", func(t *testing.T) {
		t.Setenv("CIRCLECI_TEST_VCS_TYPE", "github_app")
		t.Setenv("CIRCLECI_TEST_GH_APP_TOKEN", "REPLACE_ME")

		if got := activeIntegrationToken(t); got != "" {
			t.Errorf("activeIntegrationToken() = %q, want empty so CIRCLE_TOKEN can still apply", got)
		}
	})

	t.Run("an unrecognised VCS type resolves to no token", func(t *testing.T) {
		t.Setenv("CIRCLECI_TEST_VCS_TYPE", "not-a-real-vcs")

		if got := activeIntegrationToken(t); got != "" {
			t.Errorf("activeIntegrationToken() = %q, want empty", got)
		}
	})
}
