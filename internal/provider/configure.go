// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"terraform-provider-circleci/internal/circleci"
)

// providerData extracts the shared client wrapper from a Configure request's
// ProviderData. ProviderData is nil on the first of Terraform's two configure
// passes, which is not an error: it reports false and the caller returns early.
func providerData(data any, diags *diag.Diagnostics) (*CircleCiClientWrapper, bool) {
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

// apiClient extracts the provider's own API client from a Configure request's
// ProviderData. New resources and data sources use this rather than one of the
// legacy circleci-sdk-go services.
func apiClient(data any, diags *diag.Diagnostics) (*circleci.Client, bool) {
	wrapper, ok := providerData(data, diags)
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

// requireCloud records an error when a v3-only resource is used against
// CircleCI Server.
//
// CircleCI Server does not route /api/v3 to the public API service, so these
// resources cannot work there. Failing with an explicit message is much clearer
// than the HTTP 404 the request would otherwise produce, which is
// indistinguishable from a missing resource.
func requireCloud(client *circleci.Client, typeName string, diags *diag.Diagnostics) bool {
	if client.IsCloud() {
		return true
	}

	diags.AddError(
		fmt.Sprintf("%s requires CircleCI Cloud", typeName),
		fmt.Sprintf(
			"%s is backed by the CircleCI v3 API, which is not available on CircleCI Server. "+
				"The provider is configured with deployment = %q for host %q.\n\n"+
				"Remove this resource from configurations that target CircleCI Server.",
			typeName, string(client.Deployment()), client.Host(),
		),
	)

	return false
}
