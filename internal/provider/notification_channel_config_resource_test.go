// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"terraform-provider-circleci/internal/circleci"
)

const notificationTestOrgID = "44444444-4444-4444-4444-444444444444"

// TestAccNotificationChannelConfigResource_User covers the create/update/
// destroy cycle of a user-scoped email channel config, and asserts that
// changing target and is_enabled is a genuine in-place update.
func TestAccNotificationChannelConfigResource_User(t *testing.T) {
	api := newNotificationFakeAPI(t)

	config := func(target string, enabled bool) string {
		return orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "user"
  channel_type = "email"
  target       = %q
  is_enabled   = %t
  org_id       = %q
}
`, target, enabled, notificationTestOrgID)
	}

	var id string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("me@example.com", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "scope", "user"),
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "target", "me@example.com"),
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "is_enabled", "true"),
					resource.TestCheckResourceAttrSet("circleci_notification_channel_config.test", "user_id"),
					orbCaptureAttr("circleci_notification_channel_config.test", "id", &id),
				),
			},
			{
				Config: config("someone-else@example.com", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "target", "someone-else@example.com"),
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "is_enabled", "false"),
					orbExpectAttr("circleci_notification_channel_config.test", "id", &id),
				),
			},
		},
	})

	if got := len(api.requestsFor("POST", "/update")); got != 1 {
		t.Errorf("update requests = %d, want 1: changing target/is_enabled must be a real update, not a replace", got)
	}
	if got := len(api.requestsEndingIn("POST", "/channel-configs")); got != 1 {
		t.Errorf("create requests = %d, want 1 (only the initial create)", got)
	}
}

// TestAccNotificationChannelConfigResource_ProjectEmailTargetNeverEchoed
// covers the one combination the real API [NET] was confirmed to handle
// differently from every other: a project-scoped, channel_type = "email"
// config accepts a target on create but never returns one afterwards -- not
// on the create response, not on a subsequent get, not in a list. Without
// applyNotificationChannelConfig's "leave target alone when the API sends
// none back" behaviour, this shows up as a plan that is never empty: every
// refresh nulls state's target to "", and the next plan wants to set it back
// to what the configuration says, forever. resource.Test's built-in
// post-apply plan check is what actually catches that, with no assertion of
// its own needed here.
func TestAccNotificationChannelConfigResource_ProjectEmailTargetNeverEchoed(t *testing.T) {
	api := newNotificationFakeAPI(t)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "project"
  channel_type = "email"
  target       = "team@example.com"
  is_enabled   = true
  project_id   = "55555555-5555-5555-5555-555555555555"
  org_id       = %q
}
`, notificationTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "target", "team@example.com"),
				),
				// The bug this guards manifests as a non-empty plan on the
				// implicit post-apply refresh below, not as an error from this
				// step -- resource.Test performs that check on every step
				// unless ExpectNonEmptyPlan is set, which it deliberately is
				// not here.
			},
		},
	})
}

// TestAccNotificationChannelConfigResource_ProjectSlack covers a
// project-scoped Slack config and its resolved channel_name. It seeds an
// active Slack integration first: [NET] measured, a project-scoped Slack
// config with none installed is rejected with 404 (see
// TestAccNotificationChannelConfigResource_ProjectSlackNoIntegration404),
// which an earlier version of this test and the fake it drives against did
// not know or model.
func TestAccNotificationChannelConfigResource_ProjectSlack(t *testing.T) {
	api := newNotificationFakeAPI(t)
	api.seedIntegration("acctest-workspace", "T0123456789", notificationTestOrgID, "acctest-org")

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "project"
  channel_type = "slack"
  target       = "C0123456789"
  is_enabled   = true
  project_id   = "55555555-5555-5555-5555-555555555555"
  org_id       = %q
}
`, notificationTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "channel_name", "#C0123456789"),
					resource.TestCheckResourceAttr("circleci_notification_channel_config.test", "project_id", "55555555-5555-5555-5555-555555555555"),
				),
			},
		},
	})
}

// TestAccNotificationChannelConfigResource_ProjectSlackNoIntegration404
// covers the enforced half of the asymmetry: a project-scoped Slack channel
// config is rejected outright, [NET] measured as a 404 ("Resource does not
// exist or unauthorized"), when the organization has no active Slack
// integration installed. Contrast
// TestAccNotificationChannelConfigResource_UserSlackNoIntegrationWarns, the
// other half, which CircleCI does not enforce at all.
func TestAccNotificationChannelConfigResource_ProjectSlackNoIntegration404(t *testing.T) {
	api := newNotificationFakeAPI(t)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "project"
  channel_type = "slack"
  target       = "C0123456789"
  is_enabled   = true
  project_id   = "55555555-5555-5555-5555-555555555555"
  org_id       = %q
}
`, notificationTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`(?s)Unable to create CircleCI notification channel config.*Resource does not exist or unauthorized`),
		}},
	})
}

// TestAccNotificationChannelConfigResource_RejectsMismatchedProjectID guards
// the scope/project_id validation: project_id must be omitted for user scope.
func TestAccNotificationChannelConfigResource_RejectsMismatchedProjectID(t *testing.T) {
	api := newNotificationFakeAPI(t)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "user"
  channel_type = "email"
  target       = "me@example.com"
  is_enabled   = true
  project_id   = "55555555-5555-5555-5555-555555555555"
  org_id       = %q
}
`, notificationTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`project_id must be omitted`),
		}},
	})
}

// TestAccNotificationChannelConfigResource_ImportRoundTrips proves import
// round-trips cleanly, unlike the ios-signing and otel-exporter resources:
// every attribute here comes back from a read (see applyNotificationChannelConfig),
// there is no unreadable secret and no RequiresReplace attribute whose value
// import cannot recover, so the plan right after import is genuinely empty
// once the configuration matches what CircleCI reports -- not a one-time
// update or replacement.
//
// The channel config is seeded directly into the fake, bypassing Terraform
// Create entirely, to stand in for one that already exists and was never
// created by this Terraform run. ImportStatePersist makes the second step
// plan against the imported state rather than against whatever a previous
// step left behind.
func TestAccNotificationChannelConfigResource_ImportRoundTrips(t *testing.T) {
	api := newNotificationFakeAPI(t)

	const id = "66666666-6666-6666-6666-666666666666"
	api.channelCfgs[id] = &notificationFakeChannelConfig{
		ID:          id,
		Scope:       "user",
		ChannelType: "email",
		Target:      "me@example.com",
		IsEnabled:   true,
		OrgID:       notificationTestOrgID,
		UserID:      "77777777-7777-7777-7777-777777777777",
	}

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "user"
  channel_type = "email"
  target       = "me@example.com"
  is_enabled   = true
  org_id       = %q
}
`, notificationTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "circleci_notification_channel_config.test",
				ImportState:        true,
				ImportStateId:      id,
				ImportStatePersist: true,
				Config:             config,
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_notification_channel_config.test", plancheck.ResourceActionNoop,
						),
					},
				},
			},
		},
	})
}

// TestAccNotificationChannelConfigResource_DriftRecreates drops the resource
// from state when the config is gone, rather than failing the refresh.
func TestAccNotificationChannelConfigResource_DriftRecreates(t *testing.T) {
	api := newNotificationFakeAPI(t)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_notification_channel_config" "test" {
  scope        = "user"
  channel_type = "email"
  target       = "me@example.com"
  is_enabled   = true
  org_id       = %q
}
`, notificationTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()

					api.channelCfgs = map[string]*notificationFakeChannelConfig{}
				},
				Config:             config,
				ExpectNonEmptyPlan: true,
				PlanOnly:           true,
			},
		},
	})
}

// --- Slack/integration asymmetry: warning on the unenforced half ------------
//
// terraform-plugin-testing's resource.Test/UnitTest harness has no way to
// assert on a Warning diagnostic from an apply -- it surfaces errors (via
// ExpectError) but not warnings, which never fail a run and are not
// otherwise exposed to a Check function. So, like
// storage_retention_resource_test.go and project_settings_resource_test.go
// before it, this drives notificationChannelConfigResource.Create and
// .Update directly, the same way those two files already do for cases the
// standard harness cannot reach.

// notificationChannelConfigSchema returns the resource schema, so a test can
// build plan and state values for it.
func notificationChannelConfigSchema(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	NewNotificationChannelConfigResource().Schema(t.Context(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema method diagnostics: %+v", resp.Diagnostics)
	}

	return resp.Schema
}

// notificationChannelConfigState builds a state/plan value holding model. Plan,
// prior state and the empty state a resource writes into are all the same
// shape, so the same helper serves all three -- the same convention
// storageRetentionState and projectSettingsState use.
func notificationChannelConfigState(
	t *testing.T, schema rschema.Schema, model notificationChannelConfigResourceModel,
) tfsdk.State {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a state value: %+v", diags)
	}

	return state
}

// notificationChannelConfigUserSlackPlan is a user-scoped Slack channel config
// plan: the half of the asymmetry that has no server-side validation to catch
// a bogus channel ID or a missing integration. Every Computed attribute is
// null, matching what an actual plan holds before Create ever runs.
func notificationChannelConfigUserSlackPlan(orgID, target string) notificationChannelConfigResourceModel {
	return notificationChannelConfigResourceModel{
		ID:          types.StringNull(),
		Scope:       types.StringValue(circleci.NotificationScopeUser),
		ChannelType: types.StringValue(circleci.NotificationChannelTypeSlack),
		Target:      types.StringValue(target),
		ChannelName: types.StringNull(),
		IsEnabled:   types.BoolValue(true),
		ProjectID:   types.StringNull(),
		OrgID:       types.StringValue(orgID),
		UserID:      types.StringNull(),
	}
}

// createNotificationChannelConfig drives Create for a plan and returns the
// resulting response, so a test can inspect its diagnostics (including
// warnings, which resource.Test cannot see).
func createNotificationChannelConfig(
	t *testing.T, client *circleci.Client, plan notificationChannelConfigResourceModel,
) *fwresource.CreateResponse {
	t.Helper()

	ctx := t.Context()
	schema := notificationChannelConfigSchema(t)
	r := &notificationChannelConfigResource{client: client}

	empty := notificationChannelConfigResourceModel{
		ID: types.StringUnknown(), Scope: types.StringNull(), ChannelType: types.StringNull(),
		Target: types.StringNull(), ChannelName: types.StringNull(), IsEnabled: types.BoolNull(),
		ProjectID: types.StringNull(), OrgID: types.StringNull(), UserID: types.StringNull(),
	}

	resp := &fwresource.CreateResponse{State: notificationChannelConfigState(t, schema, empty)}
	r.Create(ctx, fwresource.CreateRequest{Plan: tfsdk.Plan{
		Schema: schema,
		Raw:    notificationChannelConfigState(t, schema, plan).Raw,
	}}, resp)

	return resp
}

// TestNotificationChannelConfigResourceUnit_UserSlackNoIntegrationWarns
// covers the unenforced half of the asymmetry documented on the resource
// Schema: a user-scoped Slack channel config succeeds against the real API
// even with no active integration and a syntactically bogus channel ID, so
// this provider adds its own Warning where CircleCI gives none. This is the
// decision recorded there -- surface it, rather than silently matching
// CircleCI's silence.
func TestNotificationChannelConfigResourceUnit_UserSlackNoIntegrationWarns(t *testing.T) {
	api := newNotificationFakeAPI(t)
	// Deliberately not seeded with any integration: this org has none.

	client := circleci.New(circleci.Config{Host: api.URL()})
	plan := notificationChannelConfigUserSlackPlan(notificationTestOrgID, "not-a-real-channel-id")

	resp := createNotificationChannelConfig(t, client, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %+v", resp.Diagnostics)
	}

	if resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("Create warnings = %d, want exactly 1 (no active Slack integration): %+v",
			resp.Diagnostics.WarningsCount(), resp.Diagnostics.Warnings())
	}

	detail := resp.Diagnostics.Warnings()[0].Detail()
	for _, want := range []string{notificationTestOrgID, "not-a-real-channel-id"} {
		if !regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(detail) {
			t.Errorf("warning detail does not mention %q: %s", want, detail)
		}
	}
}

// TestNotificationChannelConfigResourceUnit_UserSlackActiveIntegrationNoWarning
// is the negative case: with an active Slack integration installed for the
// organization, Create must not warn -- there is nothing to be uncertain
// about.
func TestNotificationChannelConfigResourceUnit_UserSlackActiveIntegrationNoWarning(t *testing.T) {
	api := newNotificationFakeAPI(t)
	api.seedIntegration("acctest-workspace", "T0123456789", notificationTestOrgID, "acctest-org")

	client := circleci.New(circleci.Config{Host: api.URL()})
	plan := notificationChannelConfigUserSlackPlan(notificationTestOrgID, "C0123456789")

	resp := createNotificationChannelConfig(t, client, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %+v", resp.Diagnostics)
	}

	if resp.Diagnostics.WarningsCount() != 0 {
		t.Errorf("Create warnings = %+v, want none: an active integration exists", resp.Diagnostics.Warnings())
	}
}

// TestNotificationChannelConfigResourceUnit_ProjectScopeNeverWarns confirms
// the warning is scoped to the asymmetry it exists for: a project-scoped
// Slack config is never the subject of it, because CircleCI already enforces
// that half itself (404 with no integration -- see
// TestAccNotificationChannelConfigResource_ProjectSlackNoIntegration404).
// This test seeds an active integration so Create succeeds at all, and
// confirms success alone carries no warning.
func TestNotificationChannelConfigResourceUnit_ProjectScopeNeverWarns(t *testing.T) {
	api := newNotificationFakeAPI(t)
	api.seedIntegration("acctest-workspace", "T0123456789", notificationTestOrgID, "acctest-org")

	client := circleci.New(circleci.Config{Host: api.URL()})
	plan := notificationChannelConfigResourceModel{
		ID:          types.StringNull(),
		Scope:       types.StringValue(circleci.NotificationScopeProject),
		ChannelType: types.StringValue(circleci.NotificationChannelTypeSlack),
		Target:      types.StringValue("C0123456789"),
		ChannelName: types.StringNull(),
		IsEnabled:   types.BoolValue(true),
		ProjectID:   types.StringValue("55555555-5555-5555-5555-555555555555"),
		OrgID:       types.StringValue(notificationTestOrgID),
		UserID:      types.StringNull(),
	}

	resp := createNotificationChannelConfig(t, client, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %+v", resp.Diagnostics)
	}

	if resp.Diagnostics.WarningsCount() != 0 {
		t.Errorf("Create warnings = %+v, want none for a project-scoped config", resp.Diagnostics.Warnings())
	}
}
