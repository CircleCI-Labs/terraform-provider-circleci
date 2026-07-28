// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"terraform-provider-circleci/internal/circleci"
)

func TestProviderDataNilIsNotAnError(t *testing.T) {
	t.Parallel()

	// Terraform calls Configure twice; ProviderData is nil on the first pass, so
	// this must not raise a diagnostic.
	var diags diag.Diagnostics

	if _, ok := providerData(nil, &diags); ok {
		t.Error("providerData(nil) reported ok, want false")
	}
	if diags.HasError() {
		t.Errorf("providerData(nil) added diagnostics: %v", diags)
	}
}

func TestProviderDataWrongType(t *testing.T) {
	t.Parallel()

	var diags diag.Diagnostics

	if _, ok := providerData("not a wrapper", &diags); ok {
		t.Error("providerData(string) reported ok, want false")
	}
	if !diags.HasError() {
		t.Error("providerData(string) added no error diagnostic, want one")
	}
}

func TestProviderDataSuccess(t *testing.T) {
	t.Parallel()

	var diags diag.Diagnostics
	want := &CircleCiClientWrapper{Client: circleci.New(circleci.Config{Token: "tok"})}

	got, ok := providerData(want, &diags)
	if !ok {
		t.Fatalf("providerData reported not ok, diagnostics: %v", diags)
	}
	if got != want {
		t.Error("providerData returned a different wrapper than it was given")
	}
	if diags.HasError() {
		t.Errorf("providerData added diagnostics: %v", diags)
	}
}

func TestAPIClient(t *testing.T) {
	t.Parallel()

	t.Run("returns the client", func(t *testing.T) {
		t.Parallel()

		var diags diag.Diagnostics
		client := circleci.New(circleci.Config{Token: "tok"})

		got, ok := apiClient(&CircleCiClientWrapper{Client: client}, &diags)
		if !ok {
			t.Fatalf("apiClient reported not ok, diagnostics: %v", diags)
		}
		if got != client {
			t.Error("apiClient returned a different client than was configured")
		}
	})

	t.Run("errors when the client is unset", func(t *testing.T) {
		t.Parallel()

		var diags diag.Diagnostics

		if _, ok := apiClient(&CircleCiClientWrapper{}, &diags); ok {
			t.Error("apiClient reported ok for a wrapper with no client, want false")
		}
		if !diags.HasError() {
			t.Error("apiClient added no error diagnostic for a nil client, want one")
		}
	})

	t.Run("silent on nil provider data", func(t *testing.T) {
		t.Parallel()

		var diags diag.Diagnostics

		if _, ok := apiClient(nil, &diags); ok {
			t.Error("apiClient(nil) reported ok, want false")
		}
		if diags.HasError() {
			t.Errorf("apiClient(nil) added diagnostics: %v", diags)
		}
	})
}

func TestRequireCloud(t *testing.T) {
	t.Parallel()

	t.Run("allows cloud", func(t *testing.T) {
		t.Parallel()

		var diags diag.Diagnostics
		client := circleci.New(circleci.Config{Token: "tok", Deployment: circleci.DeploymentCloud})

		if !requireCloud(client, "circleci_orb", &diags) {
			t.Error("requireCloud returned false on cloud, want true")
		}
		if diags.HasError() {
			t.Errorf("requireCloud added diagnostics on cloud: %v", diags)
		}
	})

	t.Run("rejects server with an actionable message", func(t *testing.T) {
		t.Parallel()

		var diags diag.Diagnostics
		client := circleci.New(circleci.Config{
			Host:       "https://circleci.example.com",
			Token:      "tok",
			Deployment: circleci.DeploymentServer,
		})

		if requireCloud(client, "circleci_orb", &diags) {
			t.Error("requireCloud returned true on server, want false")
		}
		if !diags.HasError() {
			t.Fatal("requireCloud added no error diagnostic on server, want one")
		}

		// The message must name the resource, the deployment and the host, so the
		// practitioner can tell why it failed. An opaque 404 is what we are
		// replacing.
		combined := diags.Errors()[0].Summary() + "\n" + diags.Errors()[0].Detail()
		for _, want := range []string{"circleci_orb", "CircleCI Cloud", "server", "circleci.example.com", "v3"} {
			if !strings.Contains(combined, want) {
				t.Errorf("diagnostic = %q, want it to mention %q", combined, want)
			}
		}
	})

	t.Run("defaults to cloud", func(t *testing.T) {
		t.Parallel()

		var diags diag.Diagnostics
		client := circleci.New(circleci.Config{Token: "tok"})

		if !requireCloud(client, "circleci_orb", &diags) {
			t.Error("requireCloud returned false for the default deployment, want true")
		}
	})
}
