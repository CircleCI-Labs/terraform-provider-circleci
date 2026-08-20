// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// These tests cover `signing_secret_wo` + `signing_secret_wo_version` on
// circleci_webhook (webhook_write_only.go).
//
// Every case that puts `signing_secret_wo` in a configuration declares a minimum
// Terraform version through writeOnlySupported() — shared with the environment
// variable write-only tests — because write-only attributes are a 1.11 feature
// and without the check the failure on an older CLI is an opaque "Unsupported
// argument" rather than a skip.

const webhookWriteOnlyURL = "https://example.com/hook"

// The first webhook the fake creates: ids are assigned in order from a counter
// (see newFakeWebhookAPI).
const (
	firstFakeWebhookID   = "33333333-4444-5555-6666-000000000001"
	firstFakeWebhookPath = "/api/v2/webhook/" + firstFakeWebhookID
)

// webhookWriteOnlyConfig is webhookFakeResourceConfig with the secret supplied
// as `signing_secret_wo` plus a version. Everything else is identical, which is
// what makes the two configurations comparable.
//
// The URL is fixed rather than a parameter: nothing here varies it, and
// webhook_resource_fake_test.go already covers URL validation.
func webhookWriteOnlyConfig(host, name, secret string, version int, events []string) string {
	eventsList := ""
	for i, e := range events {
		if i > 0 {
			eventsList += ", "
		}
		eventsList += fmt.Sprintf("%q", e)
	}

	return webhookFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_webhook" "test" {
  name                      = %[1]q
  url                       = %[2]q
  signing_secret_wo         = %[3]q
  signing_secret_wo_version = %[4]d
  scope_id                  = %[5]q
  scope_type                = "project"
  events                    = [%[6]s]
}
`, name, webhookWriteOnlyURL, secret, version, fakeWebhookScopeID, eventsList)
}

// storedSigningSecret returns what the fake currently holds for a webhook's
// signing secret: the "****" mask when the last write carried one, "" when it
// did not. It is how these tests observe a secret having been cleared
// server-side, which is the failure mode the whole design guards against.
func (a *fakeWebhookAPI) storedSigningSecret(id string) string {
	a.mu.Lock()
	defer a.mu.Unlock()

	record, ok := a.webhooks[id]
	if !ok {
		return ""
	}

	secret, _ := record["signing_secret"].(string)

	return secret
}

// snapshotStoredSigningSecret reads the stored secret as a step check, which runs
// while the webhook still exists. Reading it after resource.UnitTest returns
// would always find "", because the harness destroys everything on the way out —
// indistinguishable from the secret having been cleared, which is precisely what
// these tests are looking for.
func snapshotStoredSigningSecret(api *fakeWebhookAPI, id string, into *string) func(*terraform.State) error {
	return func(*terraform.State) error {
		*into = api.storedSigningSecret(id)

		return nil
	}
}

// --- both names reach the same request ----------------------------------------

// TestWebhookWriteOnly_SameRequestAsSigningSecret proves `signing_secret` and
// `signing_secret_wo` are two spellings of one argument: the same secret, in the
// same request body, to the same route. The two paths run against separate fakes
// and the recorded bodies are compared, so a difference in any key fails.
func TestWebhookWriteOnly_SameRequestAsSigningSecret(t *testing.T) {
	const secret = "s3cr3t"

	observed := map[string]map[string]any{}

	for name, config := range map[string]func(host string) string{
		"signing_secret": func(host string) string {
			return webhookFakeResourceConfig(host, "hook-1", webhookWriteOnlyURL, secret, []string{"workflow-completed"})
		},
		"signing_secret_wo": func(host string) string {
			return webhookWriteOnlyConfig(host, "hook-1", secret, 1, []string{"workflow-completed"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			api, host := newFakeWebhookAPI(t)

			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   writeOnlySupported(),
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: config(host),
					ConfigStateChecks: []statecheck.StateCheck{
						// The write-only attribute is never persisted, whichever name was
						// used: the framework nulls it in plan and state.
						statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("signing_secret_wo"), knownvalue.Null()),
					},
				}},
			})

			observed[name] = api.lastRequest(t, "POST", "/api/v2/webhook").Body
		})
	}

	// The wire key is "signing-secret", hyphenated: the request side of the webhook
	// routes reads that spelling and ignores the snake_case one it answers with.
	// See circleci.WebhookInput.
	if observed["signing_secret"]["signing-secret"] != secret {
		t.Fatalf(`signing_secret path sent signing-secret = %v, want %q`,
			observed["signing_secret"]["signing-secret"], secret)
	}
	if fmt.Sprint(observed["signing_secret_wo"]) != fmt.Sprint(observed["signing_secret"]) {
		t.Errorf("signing_secret_wo reached the API differently from signing_secret:\n  signing_secret_wo: %v\n  signing_secret:    %v",
			observed["signing_secret_wo"], observed["signing_secret"])
	}
}

// TestWebhookWriteOnly_SigningSecretIsNullInState covers the other half of the
// trade the documentation describes: on the write-only path neither the secret
// nor anything derived from it is in state, while the version is.
func TestWebhookWriteOnly_SigningSecretIsNullInState(t *testing.T) {
	_, host := newFakeWebhookAPI(t)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: webhookWriteOnlyConfig(host, "hook-1", "s3cr3t", 4, []string{"workflow-completed"}),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("signing_secret_wo"), knownvalue.Null()),
				statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("signing_secret"), knownvalue.Null()),
				statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("signing_secret_wo_version"), knownvalue.Int64Exact(4)),
			},
		}},
	})
}

// --- the vault#2900 regression test -------------------------------------------

// TestWebhookWriteOnly_UnrelatedUpdateStillSendsTheSecret is the most important
// test in this file.
//
// An update triggered by an unrelated field — here a rename, with
// `signing_secret_wo` and its version untouched — must still carry the secret.
//
// CircleCI's update route happens to select-keys the body rather than
// full-replace it, so omitting the secret leaves the stored one in place today
// (verified against production: a PUT carrying only `verify-tls` answers with
// signing_secret still "****"). That is a property of the server, not of this
// provider, it is nowhere documented as a guarantee, and the failure mode if it
// ever changes is a silently deleted secret with the apply reporting success. So
// the provider sends the secret on every update and this test holds it to that.
//
// This is a shipped bug in another provider, not a hypothetical.
// hashicorp/terraform-provider-vault#2900: the write-only value was sent only
// when its version had changed, an unrelated field triggered an update, the
// value was omitted, and the full-replace endpoint wiped `token_reviewer_jwt`,
// breaking Kubernetes auth logins. The AWS provider gates on the same condition
// and survives only because ModifyDBInstance is a partial update.
//
// Confirmed to bite: gating the send in webhookResource.Update on
// `!plan.SigningSecretWOVersion.Equal(state.SigningSecretWOVersion)` makes this
// fail with
//
//	update body["signing_secret"] = <nil>, want "s3cr3t"
//	the webhook at the API has signing_secret = "", want the "****" mask
//
// i.e. the secret silently deleted, with the apply reporting success.
func TestWebhookWriteOnly_UnrelatedUpdateStillSendsTheSecret(t *testing.T) {
	const secret = "s3cr3t"

	api, host := newFakeWebhookAPI(t)

	var stored string

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: webhookWriteOnlyConfig(host, "hook-1", secret, 1, []string{"workflow-completed"}),
			},
			{
				// Only `name` changes. The secret and its version are byte-identical to
				// the previous step, so a version-gated implementation sends nothing for
				// signing_secret here.
				Config: webhookWriteOnlyConfig(host, "hook-renamed", secret, 1, []string{"workflow-completed"}),
				Check:  snapshotStoredSigningSecret(api, firstFakeWebhookID, &stored),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_webhook.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("name"), knownvalue.StringExact("hook-renamed")),
				},
			},
		},
	})

	update := api.lastRequest(t, "PUT", firstFakeWebhookPath)

	if update.Body["name"] != "hook-renamed" {
		t.Fatalf(`update body["name"] = %v, want "hook-renamed" — the rename is what was supposed to trigger this update`,
			update.Body["name"])
	}
	// "signing-secret", hyphenated: the request spelling. Sending the snake_case
	// one is the same as sending nothing — see circleci.WebhookInput.
	if update.Body["signing-secret"] != secret {
		t.Errorf(`update body["signing-secret"] = %v, want %q — an update triggered by an unrelated `+
			`change must still send the write-only secret; a version-gated implementation is what `+
			`deleted a live credential in hashicorp/terraform-provider-vault#2900.`,
			update.Body["signing-secret"], secret)
	}

	// And the state at the API. The fake, like production, keeps the stored secret
	// when a PUT omits it, so this cannot fail on its own any more — it is here to
	// catch a body that carried an EMPTY secret, which is what a resolved-to-nothing
	// write-only value would send.
	if stored != "****" {
		t.Errorf("the webhook at the API has signing_secret = %q, want the %q mask — the rename cleared the secret",
			stored, "****")
	}
}

// TestWebhookWriteOnly_UnrelatedUpdateOnTheStatefulPathToo is the same proof for
// `signing_secret`, which reaches Update through the plan rather than through
// configuration. It is cheap and it pins the symmetry: both spellings survive an
// update they did not trigger.
func TestWebhookWriteOnly_UnrelatedUpdateOnTheStatefulPathToo(t *testing.T) {
	api, host := newFakeWebhookAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: webhookFakeResourceConfig(host, "hook-1", webhookWriteOnlyURL, "s3cr3t", []string{"workflow-completed"})},
			{Config: webhookFakeResourceConfig(host, "hook-renamed", webhookWriteOnlyURL, "s3cr3t", []string{"workflow-completed"})},
		},
	})

	if update := api.lastRequest(t, "PUT", firstFakeWebhookPath); update.Body["signing-secret"] != "s3cr3t" {
		t.Errorf(`update body["signing-secret"] = %v, want "s3cr3t"`, update.Body["signing-secret"])
	}
}

// --- rotation -----------------------------------------------------------------

// TestWebhookWriteOnly_RotationNeedsAVersionBump covers both halves of the
// version contract: a changed `signing_secret_wo` alone is invisible to Terraform
// and is therefore not sent, and bumping `signing_secret_wo_version` is what
// sends it.
//
// The middle step is the one worth having. It fails if the resource ever grows
// something that leaks the secret into state, because then the change alone would
// produce a diff and the version would be pointless.
func TestWebhookWriteOnly_RotationNeedsAVersionBump(t *testing.T) {
	api, host := newFakeWebhookAPI(t)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: webhookWriteOnlyConfig(host, "hook-1", "s3cr3t", 1, []string{"workflow-completed"}),
			},
			{
				// New secret, same version: no diff, so no request.
				Config:   webhookWriteOnlyConfig(host, "hook-1", "rotated", 1, []string{"workflow-completed"}),
				PlanOnly: true,
			},
			{
				Config: webhookWriteOnlyConfig(host, "hook-1", "rotated", 2, []string{"workflow-completed"}),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						// In place, not replaced: the webhook keeps its id and its receiver
						// keeps its URL. The version carries no RequiresReplace.
						plancheck.ExpectResourceAction("circleci_webhook.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_webhook.test", tfjsonpath.New("signing_secret_wo_version"), knownvalue.Int64Exact(2)),
				},
			},
		},
	})

	var puts []fakeRecordedRequest
	for _, req := range api.recorded() {
		if req.Method == "PUT" {
			puts = append(puts, req)
		}
	}

	if len(puts) != 1 {
		t.Fatalf("the provider sent %d PUT(s), want 1 (only the version bump; the unbumped change must not be sent): %+v",
			len(puts), puts)
	}
	if puts[0].Body["signing-secret"] != "rotated" {
		t.Errorf(`the rotation sent signing-secret = %v, want "rotated" — a rotation that does not reach `+
			`the wire leaves the old secret live while state claims otherwise`, puts[0].Body["signing-secret"])
	}
}

// --- exactly one of ----------------------------------------------------------

// TestWebhookWriteOnly_ExactlyOneSecret covers webhookSigningSecretConfigValidator
// and the AlsoRequires/AtLeast validators on the version.
func TestWebhookWriteOnly_ExactlyOneSecret(t *testing.T) {
	body := func(secrets string) string {
		return `
resource "circleci_webhook" "test" {
  name       = "hook-1"
  url        = "` + webhookWriteOnlyURL + `"
  scope_id   = "` + fakeWebhookScopeID + `"
  scope_type = "project"
  events     = ["workflow-completed"]
` + secrets + `
}
`
	}

	// The two halves of ExactlyOneOf report the same detail under different titles
	// — "Missing Attribute Configuration" when neither is set, "Invalid Attribute
	// Combination" when both are — so the detail is what these match on.
	//
	// The whitespace is a pattern rather than a literal because these two attribute
	// names are long enough that Terraform wraps the detail onto a second line,
	// between the colon and the list. `value,value_wo` on the environment variable
	// resources fits on one line and does not.
	exactlyOne := regexp.MustCompile(`(?s)Exactly one of these attributes must be configured:\s+\[signing_secret,signing_secret_wo\]`)

	tests := map[string]struct {
		secrets string
		error   *regexp.Regexp
	}{
		"neither set": {
			secrets: ``,
			error:   exactlyOne,
		},
		"both set": {
			secrets: "  signing_secret = \"a\"\n  signing_secret_wo = \"b\"\n  signing_secret_wo_version = 1",
			error:   exactlyOne,
		},
		"version without signing_secret_wo": {
			secrets: "  signing_secret = \"a\"\n  signing_secret_wo_version = 1",
			error:   regexp.MustCompile(`Invalid Attribute Combination`),
		},
		"signing_secret_wo without a version": {
			secrets: "  signing_secret_wo = \"a\"",
			error:   regexp.MustCompile(`Invalid Attribute Combination`),
		},
		"version below one": {
			secrets: "  signing_secret_wo = \"a\"\n  signing_secret_wo_version = 0",
			error:   regexp.MustCompile(`Invalid Attribute Value`),
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			api, host := newFakeWebhookAPI(t)

			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   writeOnlySupported(),
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      webhookFakeProviderConfig(host) + body(testCase.secrets),
					PlanOnly:    true,
					ExpectError: testCase.error,
				}},
			})

			// Validation happens before anything is written.
			if requests := api.recorded(); len(requests) != 0 {
				t.Errorf("the provider made %d request(s) for a configuration that fails validation, want 0: %+v",
					len(requests), requests)
			}
		})
	}
}

// --- the guard --------------------------------------------------------------

// TestWebhookWriteOnly_GuardsAgainstAMissingSecret drives Create and Update
// directly, because a plan cannot reach this state: the ExactlyOneOf validator
// rejects a configuration with neither secret at validation time. The guard
// exists for the gap after that — a write-only value that resolved to nothing by
// apply — where the alternative is a request carrying an empty signing-secret,
// which stores no secret on a create and is discarded on an update, either way
// leaving an apply that reports a secret nothing set. There is nothing to fall
// back on: the API returns the secret only as a mask, so it cannot be read and
// re-sent.
func TestWebhookWriteOnly_GuardsAgainstAMissingSecret(t *testing.T) {
	t.Parallel()

	schema := webhookResourceSchemaForTest(t)

	// signing_secret and signing_secret_wo both null, the version set: the shape of
	// a webhook already managed through the write-only path whose secret has gone
	// missing.
	model := webhookResourceModel{
		Id:                     types.StringValue(firstFakeWebhookID),
		Name:                   types.StringValue("hook-1"),
		Url:                    types.StringValue(webhookWriteOnlyURL),
		VerifyTls:              types.BoolValue(true),
		SigningSecretWOVersion: types.Int64Value(1),
		ScopeId:                types.StringValue(fakeWebhookScopeID),
		ScopeType:              types.StringValue("project"),
		Events:                 types.SetValueMust(types.StringType, []attr.Value{types.StringValue("workflow-completed")}),
	}

	config := configForTest(t, schema, model)

	createResp := fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	(&webhookResource{}).Create(t.Context(), fwresource.CreateRequest{
		Config: config,
		Plan:   tfsdk.Plan{Schema: schema, Raw: config.Raw},
	}, &createResp)

	updateResp := fwresource.UpdateResponse{State: tfsdk.State{Schema: schema}}
	(&webhookResource{}).Update(t.Context(), fwresource.UpdateRequest{
		Config: config,
		Plan:   tfsdk.Plan{Schema: schema, Raw: config.Raw},
		State:  tfsdk.State{Schema: schema, Raw: config.Raw},
	}, &updateResp)

	// Both methods are called with a nil client: reaching the API at all would
	// panic, so "an error was reported" and "nothing panicked" together prove the
	// request was never issued.
	for name, reported := range map[string]string{
		"Create": fmt.Sprint(createResp.Diagnostics),
		"Update": fmt.Sprint(updateResp.Diagnostics),
	} {
		if !regexp.MustCompile(`Missing webhook signing secret`).MatchString(reported) {
			t.Errorf("%s reported no missing-secret error; a PUT with an empty signing_secret would have been sent: %s",
				name, reported)

			continue
		}
		if !regexp.MustCompile(`signing_secret_wo_version`).MatchString(reported) {
			t.Errorf("%s's error does not explain the write-only path: %s", name, reported)
		}
	}
}

// --- the mask tripwire ------------------------------------------------------

// TestWebhookSecretLooksUnmasked covers webhookSecretLooksUnmasked, whose only
// interesting case is the one the community provider's version gets wrong: an
// empty string is a webhook with no secret, not a disclosure.
func TestWebhookSecretLooksUnmasked(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"":            false, // no secret configured at all — not a disclosure
		"****":        false, // the documented mask
		"********":    false, // a longer mask would be a reasonable change
		"s3cr3t":      true,
		"s3cr3t****":  true, // partially masked still discloses
		"****s3cr3t*": true,
	}

	for secret, want := range tests {
		if got := webhookSecretLooksUnmasked(secret); got != want {
			t.Errorf("webhookSecretLooksUnmasked(%q) = %v, want %v", secret, got, want)
		}
	}
}

// --- helpers ----------------------------------------------------------------

func webhookResourceSchemaForTest(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	(&webhookResource{}).Schema(t.Context(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema diagnostics: %+v", resp.Diagnostics)
	}

	return resp.Schema
}
