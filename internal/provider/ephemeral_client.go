// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"

	"github.com/CircleCI-Public/circleci-sdk-go/runner"
	"github.com/hashicorp/terraform-plugin-framework/diag"

	"terraform-provider-circleci/internal/circleci"
)

// This file exists because ephemeral.ConfigureRequest is a distinct type from
// resource.ConfigureRequest and datasource.ConfigureRequest (configure.go's
// providerData and apiClient take the latter two), even though all three carry
// the same *CircleCiClientWrapper in ProviderData. Rather than change
// configure.go's signature to something like `any`-typed request shims, this
// file has its own small copies for the ephemeral resources in this package.

// ephemeralProviderData extracts the shared client wrapper from an ephemeral
// resource Configure request's ProviderData. ProviderData is nil on the first
// of Terraform's two configure passes, which is not an error: it reports false
// and the caller returns early.
func ephemeralProviderData(data any, diags *diag.Diagnostics) (*CircleCiClientWrapper, bool) {
	if data == nil {
		return nil, false
	}

	wrapper, ok := data.(*CircleCiClientWrapper)
	if !ok {
		diags.AddError(
			"Unexpected Provider Data",
			fmt.Sprintf("Expected *CircleCiClientWrapper, got %T. Please report this issue to the provider developers.", data),
		)

		return nil, false
	}

	return wrapper, true
}

// ephemeralAPIClient extracts the provider's own API client from an ephemeral
// resource Configure request's ProviderData. circleci_usage_export uses this.
func ephemeralAPIClient(data any, diags *diag.Diagnostics) (*circleci.Client, bool) {
	wrapper, ok := ephemeralProviderData(data, diags)
	if !ok {
		return nil, false
	}

	if wrapper.Client == nil {
		diags.AddError(
			"Provider Not Configured",
			"The CircleCI API client is unset. Please report this issue to the provider developers.",
		)

		return nil, false
	}

	return wrapper.Client, true
}

// ephemeralRunnerService extracts the legacy circleci-sdk-go runner service
// from an ephemeral resource Configure request's ProviderData.
// circleci_ephemeral_runner_token uses this: it talks to the same runner admin
// API as the circleci_runner_token resource (see runner_token_resource.go), not
// the provider's own client.
func ephemeralRunnerService(data any, diags *diag.Diagnostics) (*runner.Service, bool) {
	wrapper, ok := ephemeralProviderData(data, diags)
	if !ok {
		return nil, false
	}

	if wrapper.RunnerService == nil {
		diags.AddError(
			"Provider Not Configured",
			"The CircleCI runner service is unset. Please report this issue to the provider developers.",
		)

		return nil, false
	}

	return wrapper.RunnerService, true
}
