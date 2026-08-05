// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Every runner attribute naming a resource class is checked against
// runnerResourceClassPattern, which mirrors the rules the API enforces.
// These tests exist because the pattern used to be
// `^[^/]+/[^/]+$` — one slash and nothing else — while the service applies three
// further rules that no fake could have surfaced:
//
//   - the namespace must match `^[a-z0-9_-]+$`, so an upper-case namespace is
//     refused;
//   - a "." anywhere in the value is refused outright, because a dot is the
//     executor separator in a fully qualified id ("runner.acme/linux") and the
//     caller must not supply that part;
//   - the class must match `^[a-zA-Z0-9:_+.-]+$`, so a space or a slash is out.
//
// The consequence of the loose pattern was an HTTP 400 during apply for values a
// practitioner would plausibly write — `acme/ubuntu-22.04` most of all — which is
// exactly what a plan-time validator exists to prevent. The assertions below check
// the fake received nothing at all, so they fail if a value starts slipping past
// the plan again rather than merely changing which error is reported.

// runnerResourceClassAttributeConfigs renders a minimal configuration for each
// place a resource class name can be written, with the name substituted in. Every
// one of them must enforce the same rule; before this, the singular data source
// and circleci_runner_token enforced nothing.
func runnerResourceClassAttributeConfigs(resourceClass string) map[string]string {
	const organizationID = "00000000-1111-2222-3333-444444444444"

	return map[string]string{
		"circleci_runner_resource_class": fmt.Sprintf(`
resource "circleci_runner_resource_class" "test" {
  org_id         = %q
  resource_class = %q
}
`, organizationID, resourceClass),

		"circleci_runner_token": fmt.Sprintf(`
resource "circleci_runner_token" "test" {
  org_id         = %q
  resource_class = %q
  nickname       = "acc-token"
}
`, organizationID, resourceClass),

		"circleci_ephemeral_runner_token": fmt.Sprintf(`
ephemeral "circleci_ephemeral_runner_token" "test" {
  org_id         = %q
  resource_class = %q
  nickname       = "acc-token"
}
`, organizationID, resourceClass),

		"data.circleci_runner_resource_class": fmt.Sprintf(`
data "circleci_runner_resource_class" "test" {
  org_id         = %q
  resource_class = %q
}
`, organizationID, resourceClass),

		"data.circleci_runners": fmt.Sprintf(`
data "circleci_runners" "test" {
  resource_class = %q
}
`, resourceClass),

		"data.circleci_runner_tokens": fmt.Sprintf(`
data "circleci_runner_tokens" "test" {
  resource_class = %q
}
`, resourceClass),

		"data.circleci_runner_task_counts": fmt.Sprintf(`
data "circleci_runner_task_counts" "test" {
  resource_class = %q
}
`, resourceClass),
	}
}

// TestRunnerResourceClassFormatIsRejectedEverywhere checks that each shape the
// service refuses is refused at plan time, by every attribute that names a
// resource class.
func TestRunnerResourceClassFormatIsRejectedEverywhere(t *testing.T) {
	invalid := map[string]string{
		// The namespace must match `^[a-z0-9_-]+$`: no upper case.
		"upper case namespace": "Acme/linux",
		// The API rejects the value on sight of a dot, wherever it is.
		"dotted class":     "acme/ubuntu-22.04",
		"dotted namespace": "acme.corp/linux",
		// A missing namespace, and a third segment, both fail the two-part split.
		"no namespace":   "linux",
		"three segments": "acme/linux/extra",
		// The class pattern admits no whitespace.
		"space in class": "acme/my runner",
	}

	for name, resourceClass := range invalid {
		for target, body := range runnerResourceClassAttributeConfigs(resourceClass) {
			t.Run(name+"/"+target, func(t *testing.T) {
				api := newRunnerFakeAPI(t)

				resource.UnitTest(t, resource.TestCase{
					ProtoV6ProviderFactories: runnerProtoV6ProviderFactories,
					Steps: []resource.TestStep{{
						Config:      runnerProviderConfig(api.URL()) + body,
						ExpectError: regexp.MustCompile(`must be in the format 'namespace/name'`),
					}},
				})

				// The point of the validator: the request is never made.
				if requests := api.allRequests(); len(requests) != 0 {
					t.Errorf("expected no requests to reach the runner API, got %v", requests)
				}
			})
		}
	}
}

// TestRunnerResourceClassFormatAcceptsWhatTheServiceAccepts is the other half:
// the tightened pattern must not have started refusing names the service allows.
// A mixed-case *class* is fine (resourceRegexp includes A-Z), as are ':', '+',
// '_' and '-'; only the namespace is lower-case-only.
func TestRunnerResourceClassFormatAcceptsWhatTheServiceAccepts(t *testing.T) {
	valid := []string{
		"acme/linux",
		"acme/Linux-Large",
		"acme-corp/linux",
		"acme_corp/linux_x86",
		"acme/linux:large",
		"acme/linux+gpu",
		"a1/b2",
	}

	for _, resourceClass := range valid {
		t.Run(resourceClass, func(t *testing.T) {
			if !runnerResourceClassPattern.MatchString(resourceClass) {
				t.Errorf("runnerResourceClassPattern rejects %q, which the API accepts", resourceClass)
			}
		})
	}
}
