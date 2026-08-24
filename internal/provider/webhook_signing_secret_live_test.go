// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"crypto/rand"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// TestWebhookResourceSigningSecretReachesTheWireLive proves, against the
// real webhook service and independently of anything Terraform state ever
// records, that a signing secret configured through this provider is genuinely
// stored rather than accepted with a 2xx and silently dropped — the failure
// mode circleci.WebhookInput's doc comment records as having shipped once
// already, from sending the response's snake_case spelling instead of the
// request's hyphenated one.
//
// Terraform state can never check this itself: GetWebhook's response
// discloses only WebhookSigningSecretMask or "", never the value in force
// (see Webhook.SigningSecret's doc comment), so a Terraform-only test — one
// that only ever inspects what Create or Update wrote into *plan* — cannot
// distinguish "the secret reached the API" from "the API silently kept
// whatever it already had (or had nothing)". This test uses a raw
// circleci.Client instead, so it can call GetWebhook independently after each
// write and observe the one fact the API is willing to disclose: whether a
// secret is present at all.
//
// It does NOT attempt to prove that ROTATING an existing secret to a new,
// different, non-empty value lands. That turned out not to be provable from
// outside the service: an investigation for this test found that clearing an
// existing secret (an update sending signing-secret="") is silently ignored
// rather than honoured — see the "AN EMPTY signing-secret DOES NOT CLEAR ONE
// THAT ALREADY EXISTS" section of WebhookInput's doc comment for the measured
// exchange — and GetWebhook answers with the same mask whether an existing
// secret was actually replaced or left alone, so there is no read on this API
// that can tell those two apart once both values are non-empty. What IS
// provable, and what this test checks, is the presence transition: a webhook
// created with no secret reads back with none, and setting one for the first
// time (whether on create or on a later update) makes the mask appear.
//
// [NET]: exercised on whichever integration CIRCLECI_TEST_VCS_TYPE selects.
// The webhook routes behave identically on every integration this provider
// supports — nothing here is gated to a particular VCS type.
//
// Not named TestAcc*: it never calls resource.Test, resource.UnitTest or
// resource.ParallelTest (see TestEveryTestAccFunctionUsesAnAcceptanceRunner
// in vcs_gating_test.go). That is not an oversight — the paragraph above is
// exactly why no Terraform-only test could ever do this test's job: state
// itself cannot distinguish the two outcomes this test exists to tell apart.
func TestWebhookResourceSigningSecretReachesTheWireLive(t *testing.T) {
	testAccPreCheck(t)

	client := testAccClient(t)
	ctx := context.Background()
	projectID := testProjectID(t)

	// 1. A secret configured on CREATE reaches the wire.
	withSecret, err := client.CreateWebhook(ctx, circleci.WebhookInput{
		Name:          "tf-acc-webhook-secret-" + rand.Text(),
		URL:           "https://example.com/webhook",
		VerifyTLS:     true,
		SigningSecret: "created-with-" + rand.Text(),
		Scope:         circleci.WebhookScope{ID: projectID, Type: circleci.WebhookScopeTypeProject},
		Events:        []string{circleci.WebhookEventWorkflowCompleted},
	})
	if err != nil {
		t.Fatalf("could not create a webhook with a signing secret: %v", err)
	}
	t.Cleanup(func() {
		if err := client.DeleteWebhook(ctx, withSecret.ID); err != nil && !circleci.IsNotFound(err) {
			t.Errorf("could not delete webhook %s created by this test: %v", withSecret.ID, err)
		}
	})

	fetched, err := client.GetWebhook(ctx, withSecret.ID)
	if err != nil {
		t.Fatalf("could not read webhook %s after create: %v", withSecret.ID, err)
	}
	if !fetched.HasSigningSecret() {
		t.Fatalf("webhook %s reports no signing secret right after being created with one; "+
			"SigningSecret=%q, want %q", withSecret.ID, fetched.SigningSecret, circleci.WebhookSigningSecretMask)
	}

	// 2. A webhook created WITHOUT a secret reads back with none — the
	// baseline the next step's transition is measured against.
	withoutSecret, err := client.CreateWebhook(ctx, circleci.WebhookInput{
		Name:      "tf-acc-webhook-nosecret-" + rand.Text(),
		URL:       "https://example.com/webhook",
		VerifyTLS: true,
		Scope:     circleci.WebhookScope{ID: projectID, Type: circleci.WebhookScopeTypeProject},
		Events:    []string{circleci.WebhookEventWorkflowCompleted},
	})
	if err != nil {
		t.Fatalf("could not create a webhook without a signing secret: %v", err)
	}
	t.Cleanup(func() {
		if err := client.DeleteWebhook(ctx, withoutSecret.ID); err != nil && !circleci.IsNotFound(err) {
			t.Errorf("could not delete webhook %s created by this test: %v", withoutSecret.ID, err)
		}
	})

	if withoutSecret.HasSigningSecret() {
		t.Fatalf("webhook %s reports a signing secret though none was configured on create", withoutSecret.ID)
	}

	// 3. Setting a secret for the first time on an UPDATE — the one write this
	// route is provably not a no-op for, unlike clearing one (see the doc
	// comment above) — reaches the wire too.
	if _, err := client.UpdateWebhook(ctx, withoutSecret.ID, circleci.WebhookInput{
		Name:          withoutSecret.Name,
		URL:           withoutSecret.URL,
		VerifyTLS:     withoutSecret.VerifyTLS,
		Events:        withoutSecret.Events,
		SigningSecret: "added-on-update-" + rand.Text(),
	}); err != nil {
		t.Fatalf("could not update webhook %s to add a signing secret: %v", withoutSecret.ID, err)
	}

	afterAdd, err := client.GetWebhook(ctx, withoutSecret.ID)
	if err != nil {
		t.Fatalf("could not read webhook %s after adding a signing secret: %v", withoutSecret.ID, err)
	}
	if !afterAdd.HasSigningSecret() {
		t.Fatalf("webhook %s still reports no signing secret after an update that set one; "+
			"the write did not reach the wire", withoutSecret.ID)
	}

	// 4. The discovered, doc-affecting fact: an update that sends an EMPTY
	// signing-secret to a webhook that already has one does NOT clear it.
	// This is asserted deliberately, not merely tolerated, so that if CircleCI
	// ever starts honouring an empty signing-secret as a clear, this test
	// fails and says so — at which point WebhookInput's doc comment should be
	// updated to match, rather than this assertion being loosened silently.
	if _, err := client.UpdateWebhook(ctx, afterAdd.ID, circleci.WebhookInput{
		Name:      afterAdd.Name,
		URL:       afterAdd.URL,
		VerifyTLS: afterAdd.VerifyTLS,
		Events:    afterAdd.Events,
		// SigningSecret left as "" — this is the write under test.
	}); err != nil {
		t.Fatalf("could not update webhook %s with an empty signing secret: %v", afterAdd.ID, err)
	}

	afterEmptyUpdate, err := client.GetWebhook(ctx, afterAdd.ID)
	if err != nil {
		t.Fatalf("could not read webhook %s after an update with an empty signing secret: %v", afterAdd.ID, err)
	}
	if !afterEmptyUpdate.HasSigningSecret() {
		t.Errorf("webhook %s has no signing secret after an update sending an empty one; "+
			"this contradicts what this test (and WebhookInput's doc comment) currently expect — "+
			"CircleCI may have started honouring an empty signing-secret as a clear. If so, update "+
			"the \"AN EMPTY signing-secret DOES NOT CLEAR ONE THAT ALREADY EXISTS\" section of "+
			"WebhookInput's doc comment in internal/circleci/webhook.go and this test's own comment.",
			afterAdd.ID)
	}
}
