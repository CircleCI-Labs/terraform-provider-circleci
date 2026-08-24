// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// TestRunnerTokenResourceLimitLive proves, against the real runner admin
// API, the limit this provider's documentation and error handling assume but
// had never been checked against a live installation: a resource class holds
// at most 10 tokens, and the 11th create is refused with HTTP 403 rather than
// silently succeeding or answering some other status the provider would
// surface as an opaque error.
//
// This talks to the runner API directly through a raw circleci.Client rather
// than through Terraform. Ten-plus create/destroy cycles through a full
// plan/apply per step would multiply this test's cost for no extra coverage:
// the fact under test is a property of CreateToken's response, not of how the
// provider maps that response into state, and TestAccRunnerTokenResource
// already covers the latter with a single token.
//
// [NET]: exercised on whichever integration CIRCLECI_TEST_VCS_TYPE selects.
// The runner admin API is not gated to a VCS integration — it authorizes
// purely off the resource class's namespace prefix — so this test carries no
// VCS branching of its own.
//
// Not named TestAcc*: it never calls resource.Test, resource.UnitTest or
// resource.ParallelTest (see TestEveryTestAccFunctionUsesAnAcceptanceRunner
// in vcs_gating_test.go), because there is no Terraform config that could
// express "create 11 tokens and check the 11th is refused" without paying
// for ten discardable resources it does not otherwise need.
func TestRunnerTokenResourceLimitLive(t *testing.T) {
	testAccPreCheck(t)

	client := testAccClient(t)
	ctx := context.Background()
	orgID := testOrgID(t)
	resourceClass := testUniqueRunnerResourceClass(t, "acc-test-token-limit")

	rc, err := client.CreateResourceClass(ctx, circleci.ResourceClassInput{
		OrganizationID: orgID,
		ResourceClass:  resourceClass,
		Description:    "tf-acc token-limit test resource class",
	})
	if err != nil {
		t.Fatalf("could not create resource class %s: %v", resourceClass, err)
	}

	// force: true, because the very point of this test is to leave the class
	// holding as many as 10 tokens when it is torn down.
	t.Cleanup(func() {
		if err := client.DeleteResourceClass(ctx, rc.ID, true); err != nil {
			t.Errorf("could not force-delete resource class %s (%s) created by this test: %v",
				resourceClass, rc.ID, err)
		}
	})

	const maxTokensPerClass = 10

	for i := 1; i <= maxTokensPerClass; i++ {
		if _, err := client.CreateToken(ctx, circleci.TokenInput{
			OrganizationID: orgID,
			ResourceClass:  resourceClass,
			Nickname:       fmt.Sprintf("tf-acc-limit-%d-%s", i, rand.Text()),
		}); err != nil {
			t.Fatalf("could not create token %d/%d on %s, so the 10-token limit this test exists to "+
				"check was never reached: %v", i, maxTokensPerClass, resourceClass, err)
		}
	}

	_, err = client.CreateToken(ctx, circleci.TokenInput{
		OrganizationID: orgID,
		ResourceClass:  resourceClass,
		Nickname:       "tf-acc-limit-eleventh-" + rand.Text(),
	})
	if err == nil {
		t.Fatalf("the 11th token on resource class %s was created successfully; CircleCI used to cap "+
			"this at %d with an HTTP 403 naming the limit. If that has changed, update this test and "+
			"every doc comment that repeats the limit (see internal/circleci/runner.go's CreateToken).",
			resourceClass, maxTokensPerClass)
	}
	if !circleci.HasStatus(err, http.StatusForbidden) {
		t.Errorf("the 11th token create failed as expected, but with %v, want an HTTP %d", err, http.StatusForbidden)
	}
}
