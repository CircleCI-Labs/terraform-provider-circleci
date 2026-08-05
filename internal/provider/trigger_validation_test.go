// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// Tests for trigger_validation.go: the per-provider rules for `circleci_trigger`,
// which used to live in Create and now run at plan time.
//
// Every case here asserts two things, and the second is the point:
//
//   - the step is PlanOnly, so the diagnostic has to come out of `terraform plan`.
//     If a rule were still enforced in Create, the plan would succeed and the
//     framework would fail the step with "the plan was not empty" instead — which
//     does not match the expected message, so the test fails.
//   - no request reached the API. Validation happens before the provider is asked
//     to do anything, so an invalid configuration must cost no round-trip at all.
//
// This is the pattern TestWebhookResourceUnit_RejectsUnknownEventName established
// for the `events` validator on webhooks.

// triggerConfig renders a circleci_trigger with the given attribute lines.
func triggerConfig(host, attributes string) string {
	return triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id             = %[1]q
  pipeline_definition_id = %[2]q
%[3]s}
`, fakeTriggerProjectID, fakeTriggerPipelineID, attributes)
}

// TestTriggerResourceUnit_RejectsInvalidCombinationsAtPlanTime is the table
// covering these rules: one case per rule, each proving the rule is a plan-time rule.
func TestTriggerResourceUnit_RejectsInvalidCombinationsAtPlanTime(t *testing.T) {
	cases := map[string]struct {
		attributes string
		wantError  *regexp.Regexp
	}{
		// The provider itself, and the preset, are single-attribute rules and so
		// are schema validators — which is why these two report the framework's own
		// summary and enumerate the accepted values.
		"unrecognized provider": {
			attributes: `
  event_source_provider = "bitbucket_cloud"
`,
			wantError: regexp.MustCompile(
				`(?s)Invalid Attribute Value Match.*event_source_provider.*bitbucket_cloud`,
			),
		},
		"unrecognized event preset": {
			attributes: `
  event_source_provider         = "github_app"
  event_source_repo_external_id = "1234"
  event_preset                  = "not-a-real-preset"
`,
			wantError: regexp.MustCompile(
				`(?s)Invalid Attribute Value Match.*event_preset.*not-a-real-preset`,
			),
		},

		"parameters on a github_app trigger": {
			attributes: `
  event_source_provider         = "github_app"
  event_source_repo_external_id = "1234"
  event_preset                  = "all-pushes"
  parameters                    = { foo = "bar" }
`,
			wantError: regexp.MustCompile(`(?s)does not support parameters`),
		},
		"parameters on a webhook trigger": {
			attributes: `
  event_source_provider        = "webhook"
  event_name                   = "deploy-hook"
  event_source_web_hook_sender = "datadog"
  parameters                   = { foo = "bar" }
`,
			wantError: regexp.MustCompile(`(?s)does not support parameters`),
		},

		"github_app without a repository id": {
			attributes: `
  event_source_provider = "github_app"
  event_preset          = "all-pushes"
`,
			// \s+ rather than a literal space: the diagnostic renderer word-wraps,
			// and this message happens to wrap between the two words.
			wantError: regexp.MustCompile(`(?s)requires\s+event_source_repo_external_id`),
		},
		"github_server with an empty repository id": {
			attributes: `
  event_source_provider         = "github_server"
  event_source_repo_external_id = ""
  event_preset                  = "all-pushes"
`,
			wantError: regexp.MustCompile(`(?s)requires\s+event_source_repo_external_id`),
		},
		// The API parses the external id with strconv.ParseInt and answers a bare
		// "bad request" when that fails — no attribute named, nothing to act on. The
		// attribute has always been documented as the repository's numeric ID.
		"github_app with a repository name instead of its numeric id": {
			attributes: `
  event_source_provider         = "github_app"
  event_source_repo_external_id = "acme-org/some-repo"
  event_preset                  = "all-pushes"
`,
			wantError: regexp.MustCompile(`(?s)numeric ID, not its\s+name`),
		},
		"github_oauth without a repository id": {
			// github_oauth is a repository-backed event source like github_app: the
			// API resolves all three in the same arm of the same switch and rejects a
			// create with no event_source.repo. The provider used to require the id
			// for the other two only, and never sent one for github_oauth at all, so
			// every github_oauth trigger failed with an opaque HTTP 400.
			attributes: `
  event_source_provider = "github_oauth"
  event_preset          = "all-pushes"
`,
			wantError: regexp.MustCompile(`(?s)requires\s+event_source_repo_external_id`),
		},
		"github_app with an event name": {
			attributes: `
  event_source_provider         = "github_app"
  event_source_repo_external_id = "1234"
  event_name                    = "push"
`,
			wantError: regexp.MustCompile(`(?s)does not support event_name`),
		},

		"github_oauth without an event preset": {
			attributes: `
  event_source_provider = "github_oauth"
`,
			wantError: regexp.MustCompile(`(?s)github_oauth provider requires event_preset`),
		},
		"github_oauth with a preset it does not accept": {
			// only-tags is a perfectly good preset for a GitHub App event source, so
			// the schema validator passes it: the narrowing is per provider.
			attributes: `
  event_source_provider = "github_oauth"
  event_preset          = "only-tags"
`,
			wantError: regexp.MustCompile(`(?s)github_oauth provider has an unexpected event_preset`),
		},
		"github_oauth with disabled": {
			attributes: `
  event_source_provider = "github_oauth"
  event_preset          = "all-pushes"
  disabled              = true
`,
			wantError: regexp.MustCompile(`(?s)does not support disabled`),
		},

		"webhook without an event name": {
			attributes: `
  event_source_provider        = "webhook"
  event_source_web_hook_sender = "datadog"
`,
			wantError: regexp.MustCompile(`(?s)webhook provider requires an event_name`),
		},
		"webhook without a sender": {
			attributes: `
  event_source_provider = "webhook"
  event_name            = "deploy-hook"
`,
			wantError: regexp.MustCompile(`(?s)webhook provider requires a Webhook Sender`),
		},
		"webhook with an event preset": {
			attributes: `
  event_source_provider        = "webhook"
  event_name                   = "deploy-hook"
  event_source_web_hook_sender = "datadog"
  checkout_ref                 = "main"
  config_ref                   = "main"
  event_preset                 = "all-pushes"
`,
			wantError: regexp.MustCompile(`(?s)webhook provider does not support event_preset`),
		},
		// An inbound POST carries no ref, so there is nothing for either ref to fall
		// back to and the API rejects a create that omits one — the same
		// rule it applies to schedule triggers, enforced in the same place. The
		// documentation said "required" for both providers; only the check was
		// missing, so this planned cleanly and failed on apply.
		"webhook without a checkout ref": {
			attributes: `
  event_source_provider        = "webhook"
  event_name                   = "deploy-hook"
  event_source_web_hook_sender = "datadog"
  config_ref                   = "main"
`,
			wantError: regexp.MustCompile(`(?s)webhook provider requires checkout_ref`),
		},
		"webhook without a config ref": {
			attributes: `
  event_source_provider        = "webhook"
  event_name                   = "deploy-hook"
  event_source_web_hook_sender = "datadog"
  checkout_ref                 = "main"
`,
			wantError: regexp.MustCompile(`(?s)webhook provider requires config_ref`),
		},

		"schedule without an event name": {
			attributes: `
  event_source_provider                   = "schedule"
  checkout_ref                            = "main"
  config_ref                              = "main"
  event_source_schedule_cron_expression   = "0 0 * * *"
  event_source_schedule_attribution_actor = "system"
`,
			wantError: regexp.MustCompile(`(?s)schedule provider requires\s+event_name`),
		},
		"schedule without a checkout ref": {
			attributes: `
  event_source_provider                   = "schedule"
  event_name                              = "nightly"
  config_ref                              = "main"
  event_source_schedule_cron_expression   = "0 0 * * *"
  event_source_schedule_attribution_actor = "system"
`,
			wantError: regexp.MustCompile(`(?s)schedule provider requires\s+checkout_ref`),
		},
		"schedule without a config ref": {
			attributes: `
  event_source_provider                   = "schedule"
  event_name                              = "nightly"
  checkout_ref                            = "main"
  event_source_schedule_cron_expression   = "0 0 * * *"
  event_source_schedule_attribution_actor = "system"
`,
			wantError: regexp.MustCompile(`(?s)schedule provider requires\s+config_ref`),
		},
		"schedule without a cron expression": {
			attributes: `
  event_source_provider                   = "schedule"
  event_name                              = "nightly"
  checkout_ref                            = "main"
  config_ref                              = "main"
  event_source_schedule_attribution_actor = "system"
`,
			wantError: regexp.MustCompile(
				`(?s)schedule provider requires\s+event_source_schedule_cron_expression`,
			),
		},
		"schedule without an attribution actor": {
			// The attribute is Optional+Computed, so it is *null* in configuration
			// when omitted where the plan would show it as unknown. Create had to
			// test both, and shipped once testing only IsNull().
			attributes: `
  event_source_provider                 = "schedule"
  event_name                            = "nightly"
  checkout_ref                          = "main"
  config_ref                            = "main"
  event_source_schedule_cron_expression = "0 0 * * *"
`,
			wantError: regexp.MustCompile(
				`(?s)schedule provider requires\s+event_source_schedule_attribution_actor`,
			),
		},
		"schedule with an event preset": {
			attributes: `
  event_source_provider                   = "schedule"
  event_name                              = "nightly"
  checkout_ref                            = "main"
  config_ref                              = "main"
  event_source_schedule_cron_expression   = "0 0 * * *"
  event_source_schedule_attribution_actor = "system"
  event_preset                            = "all-pushes"
`,
			wantError: regexp.MustCompile(`(?s)schedule provider does not support event_preset`),
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			api, host := newFakeTriggerAPI(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: triggerConfig(host, testCase.attributes),
					// PlanOnly is what makes this a test of *when* the rule fires.
					PlanOnly:    true,
					ExpectError: testCase.wantError,
				}},
			})

			if requests := api.recorded(); len(requests) != 0 {
				t.Errorf("the provider made %d request(s) for a configuration that fails validation, "+
					"want 0: %+v", len(requests), requests)
			}
		})
	}
}

// TestTriggerResourceUnit_ProviderValidatorNamesEveryAcceptedValue keeps the
// diagnostic for a wrong `event_source_provider` useful.
//
// The old apply-time message said "should be either github_app, github_server,
// webhook, or schedule" — which omitted github_oauth, a provider this resource does
// support. Deriving the validator from circleci.TriggerEventSourceProviders means
// the message cannot fall behind the list again.
func TestTriggerResourceUnit_ProviderValidatorNamesEveryAcceptedValue(t *testing.T) {
	_, host := newFakeTriggerAPI(t)

	// The framework renders the accepted set as a space-separated list of quoted
	// values, and the renderer word-wraps it, so the separator has to tolerate a
	// newline.
	quoted := make([]string, 0, len(circleci.TriggerEventSourceProviders()))
	for _, provider := range circleci.TriggerEventSourceProviders() {
		quoted = append(quoted, regexp.QuoteMeta(`"`+provider+`"`))
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: triggerConfig(host, `
  event_source_provider = "gitlab"
`),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`(?s)` + strings.Join(quoted, `\s+`)),
		}},
	})
}

// TestTriggerResourceUnit_ValidCombinationsStillApply is the other half of the
// table above: the rules must reject only what they mean to.
//
// github_oauth is here because it could not be applied at all before this change —
// Create's switch had no case for it, so the default arm rejected the provider its
// own documentation described. And github_app appears without `event_preset`
// because Create tested isValidEventPreset(""), which is false, making the preset
// required for GitHub App event sources in code while every piece of documentation
// in the repository called it optional. The documentation was right: the preset
// selects which events fire the trigger, and CircleCI has a default.
func TestTriggerResourceUnit_ValidCombinationsStillApply(t *testing.T) {
	cases := map[string]struct {
		attributes string
		checks     []statecheck.StateCheck
		// wantAbsentFromCreate names keys the create body must not carry at all.
		wantAbsentFromCreate []string
		// wantRepoExternalIDInCreate, when set, is the value the create body's
		// event_source.repo.external_id must carry. Asserted on the bytes sent
		// rather than on state, because a missing repo is invisible in state: the
		// API echoes back what it resolved, and the practitioner's own value is
		// already in the plan.
		wantRepoExternalIDInCreate string
	}{
		"github_app without an event preset": {
			attributes: `
  event_source_provider         = "github_app"
  event_source_repo_external_id = "1234"
  checkout_ref                  = "main"
  config_ref                    = "main"
`,
			checks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("circleci_trigger.test",
					tfjsonpath.New("event_preset"), knownvalue.Null()),
			},
		},
		"github_oauth": {
			attributes: `
  event_source_provider         = "github_oauth"
  event_source_repo_external_id = "1234"
  event_preset                  = "only-build-prs"
`,
			checks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("circleci_trigger.test",
					tfjsonpath.New("event_source_provider"), knownvalue.StringExact("github_oauth")),
				statecheck.ExpectKnownValue("circleci_trigger.test",
					tfjsonpath.New("event_preset"), knownvalue.StringExact("only-build-prs")),
			},
			// The bug this catches: Create's switch populated event_source.repo for
			// github_app and github_server only, so a github_oauth create went out
			// with no repo and the API answered "bad request". This case used to omit
			// the repository id entirely and still pass, because the fake did not
			// require one either — client and fake wrong in the same way. See
			// rejectInvalidCreate in trigger_resource_fake_test.go.
			wantRepoExternalIDInCreate: "1234",
			// `disabled` is unsupported for github_oauth, and the attribute has a
			// default, so the plan carries `false` whether or not anyone asked for it.
			// Sending it anyway would make the provider fail on the one thing it just
			// started allowing. See triggerDisabledInput.
			wantAbsentFromCreate: []string{"disabled"},
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			api, host := newFakeTriggerAPI(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:            triggerConfig(host, testCase.attributes),
					ConfigStateChecks: testCase.checks,
				}},
			})

			create := api.lastRequest(t, "POST",
				"/api/v2/projects/"+fakeTriggerProjectID+"/pipeline-definitions/"+
					fakeTriggerPipelineID+"/triggers")

			for _, key := range testCase.wantAbsentFromCreate {
				if value, present := create.Body[key]; present {
					t.Errorf("the create body carries %q = %v, which this event source does not support",
						key, value)
				}
			}

			if testCase.wantRepoExternalIDInCreate != "" {
				eventSource, _ := create.Body["event_source"].(map[string]any)
				repo, ok := eventSource["repo"].(map[string]any)
				if !ok {
					t.Fatalf("the create body has no event_source.repo, which this event source "+
						"requires: %+v", create.Body)
				}
				if got := repo["external_id"]; got != testCase.wantRepoExternalIDInCreate {
					t.Errorf("create event_source.repo.external_id = %v, want %q",
						got, testCase.wantRepoExternalIDInCreate)
				}
			}
		})
	}
}

// TestTriggerResourceUnit_UpdateIsValidatedToo covers the second copy of these
// rules, which lived in Update.
//
// An invalid *change* has to be caught at plan time for the same reason an invalid
// create does, and it is the more dangerous of the two: the resource already
// exists, so a diagnostic from Update lands with the trigger half-managed. Removing
// event_source_repo_external_id from an existing github_app trigger is the case an
// acceptance test already covered against the real API.
func TestTriggerResourceUnit_UpdateIsValidatedToo(t *testing.T) {
	api, host := newFakeTriggerAPI(t)

	valid := triggerConfig(host, `
  event_source_provider         = "github_app"
  event_source_repo_external_id = "1234"
  event_preset                  = "all-pushes"
  checkout_ref                  = "main"
  config_ref                    = "main"
`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: valid},
			{
				Config: triggerConfig(host, `
  event_source_provider = "github_app"
  event_preset          = "all-pushes"
  checkout_ref          = "main"
  config_ref            = "main"
`),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)requires\s+event_source_repo_external_id`),
			},
			// Back to a configuration that validates. Not decoration: validation runs
			// on a destroy's configuration too, so a test whose last step is invalid
			// cannot tear itself down — and neither could a practitioner, without
			// fixing the configuration first or reaching for -refresh=false.
			{Config: valid},
		},
	})

	// The failed step must have issued no PATCH: the create, its follow-up read and
	// the refreshes are the only traffic.
	for _, request := range api.recorded() {
		if request.Method == "PATCH" {
			t.Errorf("the provider sent a PATCH for a configuration that fails validation: %+v", request)
		}
	}
}
