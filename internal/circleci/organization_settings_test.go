// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// orgSettingsResponse is the v3 envelope the API returns for both the read and
// the update: the entity carries attributes only, with no id and no references.
const orgSettingsResponse = `{
  "data": {
    "attributes": {
      "enable_ai_agents": true,
      "enable_ai_error_summarization": false,
      "enable_certified_public_orbs": true,
      "enable_chunk_ip_ranges": false,
      "enable_image_brownouts": true,
      "enable_minor_ai_features": false,
      "enable_private_orbs": true,
      "enable_resource_class_brownouts": false,
      "enable_uncertified_public_orbs": true,
      "enable_unversioned_config": false,
      "is_bitbucket_workspace_member_org_member": true,
      "is_context_group_restriction_required": false,
      "is_runner_terms_of_service_accepted": true,
      "is_running_disabled": false,
      "is_user_checkout_keys_disabled": true
    }
  }
}`

// recordedCall captures what the client actually sent.
type recordedCall struct {
	method string
	path   string
	body   []byte
}

// newSettingsServer returns a server that records every request and answers each
// with body.
func newSettingsServer(t *testing.T, status int, body string) (*httptest.Server, *[]recordedCall) {
	t.Helper()

	calls := new([]recordedCall)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}

		*calls = append(*calls, recordedCall{method: r.Method, path: r.URL.Path, body: raw})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv, calls
}

func TestGetOrganizationSettingsRouteAndEnvelope(t *testing.T) {
	t.Parallel()

	srv, calls := newSettingsServer(t, http.StatusOK, orgSettingsResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	settings, err := c.GetOrganizationSettings(context.Background(), "00000000-1111-2222-3333-444444444444")
	if err != nil {
		t.Fatalf("GetOrganizationSettings returned error: %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(*calls))
	}

	got := (*calls)[0]
	if want := http.MethodGet; got.method != want {
		t.Errorf("method = %q, want %q", got.method, want)
	}
	if want := "/api/v3/orgs/00000000-1111-2222-3333-444444444444/settings"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	// Every toggle must be decoded out of data.attributes, not the top level.
	for name, pair := range map[string]struct {
		got  *bool
		want bool
	}{
		"enable_ai_agents":                         {settings.EnableAIAgents, true},
		"enable_ai_error_summarization":            {settings.EnableAIErrorSummarization, false},
		"enable_certified_public_orbs":             {settings.EnableCertifiedPublicOrbs, true},
		"enable_chunk_ip_ranges":                   {settings.EnableChunkIPRanges, false},
		"enable_image_brownouts":                   {settings.EnableImageBrownouts, true},
		"enable_minor_ai_features":                 {settings.EnableMinorAIFeatures, false},
		"enable_private_orbs":                      {settings.EnablePrivateOrbs, true},
		"enable_resource_class_brownouts":          {settings.EnableResourceClassBrownouts, false},
		"enable_uncertified_public_orbs":           {settings.EnableUncertifiedPublicOrbs, true},
		"enable_unversioned_config":                {settings.EnableUnversionedConfig, false},
		"is_bitbucket_workspace_member_org_member": {settings.IsBitbucketWorkspaceMemberOrgMember, true},
		"is_context_group_restriction_required":    {settings.IsContextGroupRestrictionRequired, false},
		"is_runner_terms_of_service_accepted":      {settings.IsRunnerTermsOfServiceAccepted, true},
		"is_running_disabled":                      {settings.IsRunningDisabled, false},
		"is_user_checkout_keys_disabled":           {settings.IsUserCheckoutKeysDisabled, true},
	} {
		if pair.got == nil {
			t.Errorf("%s = nil, want %v: the data/attributes envelope was not unwrapped", name, pair.want)

			continue
		}
		if *pair.got != pair.want {
			t.Errorf("%s = %v, want %v", name, *pair.got, pair.want)
		}
	}
}

func TestGetOrganizationSettingsIgnoresBareBody(t *testing.T) {
	t.Parallel()

	// A body without the envelope must not silently populate the toggles: that
	// would mean the client had guessed the wrong shape and still looked fine.
	bare := `{"enable_private_orbs": true, "is_running_disabled": true}`

	srv, _ := newSettingsServer(t, http.StatusOK, bare)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	settings, err := c.GetOrganizationSettings(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("GetOrganizationSettings returned error: %v", err)
	}

	if settings.EnablePrivateOrbs != nil {
		t.Errorf("EnablePrivateOrbs = %v, want nil: toggles live under data.attributes", *settings.EnablePrivateOrbs)
	}
}

func TestUpdateOrganizationSettingsOmitsUnsetToggles(t *testing.T) {
	t.Parallel()

	srv, calls := newSettingsServer(t, http.StatusOK, orgSettingsResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	enabled, disabled := true, false

	_, err := c.UpdateOrganizationSettings(context.Background(), "org-1", circleci.OrganizationSettings{
		EnablePrivateOrbs: &enabled,
		IsRunningDisabled: &disabled,
	})
	if err != nil {
		t.Fatalf("UpdateOrganizationSettings returned error: %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(*calls))
	}

	got := (*calls)[0]
	if want := http.MethodPost; got.method != want {
		t.Errorf("method = %q, want %q (v3 spells partial updates as POST to /update-settings)", got.method, want)
	}
	if want := "/api/v3/orgs/org-1/update-settings"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	// Assert on the JSON actually sent. This is the whole point of the *bool
	// fields: sending false for a toggle nobody configured would switch off a
	// setting the practitioner never mentioned.
	var sent map[string]any
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatalf("request body %q is not JSON: %v", got.body, err)
	}

	want := map[string]any{"enable_private_orbs": true, "is_running_disabled": false}
	if len(sent) != len(want) {
		t.Errorf("request body = %s, want exactly the two configured toggles %v", got.body, want)
	}
	for key, value := range want {
		if sent[key] != value {
			t.Errorf("request body %s = %v, want %v", key, sent[key], value)
		}
	}

	// And spell out the omissions, since that is the property that matters.
	for _, absent := range []string{
		"enable_ai_agents",
		"enable_ai_error_summarization",
		"enable_certified_public_orbs",
		"enable_chunk_ip_ranges",
		"enable_image_brownouts",
		"enable_minor_ai_features",
		"enable_resource_class_brownouts",
		"enable_uncertified_public_orbs",
		"enable_unversioned_config",
		"is_bitbucket_workspace_member_org_member",
		"is_context_group_restriction_required",
		"is_runner_terms_of_service_accepted",
		"is_user_checkout_keys_disabled",
	} {
		if _, present := sent[absent]; present {
			t.Errorf("request body includes %q, want it omitted so the setting is left alone", absent)
		}
	}
}

func TestUpdateOrganizationSettingsSendsBareBody(t *testing.T) {
	t.Parallel()

	srv, calls := newSettingsServer(t, http.StatusOK, orgSettingsResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	enabled := true
	if _, err := c.UpdateOrganizationSettings(context.Background(), "org-1", circleci.OrganizationSettings{
		EnableAIAgents: &enabled,
	}); err != nil {
		t.Fatalf("UpdateOrganizationSettings returned error: %v", err)
	}

	// The request is the bare settings object; only the response is enveloped.
	var sent map[string]any
	if err := json.Unmarshal((*calls)[0].body, &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if _, wrapped := sent["data"]; wrapped {
		t.Errorf("request body = %s, want the bare settings object with no data envelope", (*calls)[0].body)
	}
	if got, ok := sent["enable_ai_agents"]; !ok || got != true {
		t.Errorf("request body enable_ai_agents = %v, want true at the top level", got)
	}
}

func TestUpdateOrganizationSettingsDecodesResponseEnvelope(t *testing.T) {
	t.Parallel()

	srv, _ := newSettingsServer(t, http.StatusOK, orgSettingsResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	enabled := true

	updated, err := c.UpdateOrganizationSettings(context.Background(), "org-1", circleci.OrganizationSettings{
		EnableAIAgents: &enabled,
	})
	if err != nil {
		t.Fatalf("UpdateOrganizationSettings returned error: %v", err)
	}

	// The update answers with the full settings record, which is what lets the
	// resource save post-apply state without a second read.
	if updated.IsUserCheckoutKeysDisabled == nil || !*updated.IsUserCheckoutKeysDisabled {
		t.Error("IsUserCheckoutKeysDisabled was not populated from the update response")
	}
}

func TestOrganizationSettingsIsEmpty(t *testing.T) {
	t.Parallel()

	if !(circleci.OrganizationSettings{}).IsEmpty() {
		t.Error("IsEmpty() = false for a zero value, want true")
	}

	set := false
	if (circleci.OrganizationSettings{IsRunningDisabled: &set}).IsEmpty() {
		t.Error("IsEmpty() = true for a settings value holding false, want false: false is a real value")
	}
}

func TestGetOrganizationSettingsNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newSettingsServer(t, http.StatusNotFound, `{"error":{"title":"Not Found","detail":"org not found"}}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetOrganizationSettings(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetOrganizationSettings returned no error for a 404")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}
