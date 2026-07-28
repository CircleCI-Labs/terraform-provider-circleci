// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"terraform-provider-circleci/internal/circleci"
)

// requireStandaloneCapable records an error when a resource that needs a
// `circleci` type ("standalone") organization is used against CircleCI Server.
//
// CircleCI groups and project role grants are documented as supported only for
// standalone organizations, and a CircleCI Server installation is *always* a
// `github` type organization. So `deployment = "server"` is definitively
// incompatible, and saying so is much clearer than the API error the request
// would otherwise produce.
//
// Note what this does NOT catch: a CircleCI *Cloud* organization can also be
// `github` or `bitbucket` type, and those cannot use groups either. The provider
// is configured with an organization UUID rather than a slug, so it cannot tell
// the type without an extra lookup, and the requirement is documented on each
// resource instead. This check is necessary but not sufficient — it rules out the
// case that is knowable from configuration alone.
func requireStandaloneCapable(client *circleci.Client, typeName string, diags *diag.Diagnostics) bool {
	if client.IsCloud() {
		return true
	}

	diags.AddError(
		fmt.Sprintf("%s requires a standalone CircleCI organization", typeName),
		fmt.Sprintf(
			"%s needs a `circleci` type (standalone) organization. A CircleCI Server "+
				"installation is always a `github` type organization, so this resource cannot be "+
				"used there. The provider is configured with deployment = %q for host %q.\n\n"+
				"On CircleCI Cloud, note that `github` and `bitbucket` organizations cannot use "+
				"this resource either; only `circleci/<uuid>` organizations can.",
			typeName, string(client.Deployment()), client.Host(),
		),
	)

	return false
}
