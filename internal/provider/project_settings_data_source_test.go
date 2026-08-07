// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccProjectSettingsDataSource(t *testing.T) {
	projectSlug := testStaticProjectSlug(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Read testing
			{
				Config: testProjectSettingsDataSourceConfig(projectSlug),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_project_settings.test_project",
						tfjsonpath.New("slug"),
						knownvalue.StringExact(projectSlug),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_settings.test_project",
						tfjsonpath.New("auto_cancel_builds"),
						knownvalue.Bool(false),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_settings.test_project",
						tfjsonpath.New("build_fork_prs"),
						knownvalue.Bool(false),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_settings.test_project",
						tfjsonpath.New("disable_ssh"),
						knownvalue.Bool(false),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_settings.test_project",
						tfjsonpath.New("forks_receive_secret_env_vars"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_settings.test_project",
						tfjsonpath.New("oss"),
						knownvalue.Bool(false),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_settings.test_project",
						tfjsonpath.New("set_github_status"),
						knownvalue.Bool(false),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_settings.test_project",
						tfjsonpath.New("setup_workflows"),
						knownvalue.Bool(false),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_settings.test_project",
						tfjsonpath.New("write_settings_requires_admin"),
						knownvalue.Bool(false),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_settings.test_project",
						tfjsonpath.New("pr_only_branch_overrides"),
						knownvalue.SetExact(
							[]knownvalue.Check{
								0: knownvalue.StringExact("main"),
							},
						),
					),
				},
			},
		},
	})
}

func testProjectSettingsDataSourceConfig(projectSlug string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = "https://circleci.com/api/v2"
}

data "circleci_project_settings" "test_project" {
  slug = %[1]q
}
`, projectSlug)
}

// projectSettingsDataSourceSchemaForTest returns the data source's schema, so
// that tests can build config values for it.
func projectSettingsDataSourceSchemaForTest(t *testing.T) dschema.Schema {
	t.Helper()

	resp := &datasource.SchemaResponse{}
	NewProjectSettingsDataSource().Schema(context.Background(), datasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema method diagnostics: %+v", resp.Diagnostics)
	}

	return resp.Schema
}

// projectSettingsDataSourceConfigForTest builds a tfsdk.Config from a
// slug-only query, the shape a practitioner's data block takes: every
// Computed attribute is unknown/null in a config, never a value the
// practitioner supplied.
func projectSettingsDataSourceConfigForTest(t *testing.T, schema dschema.Schema, slug string) tfsdk.Config {
	t.Helper()

	model := projectSettingsDataSourceModel{
		AutoCancelBuilds:           types.BoolNull(),
		BuildForkPrs:               types.BoolNull(),
		BuildPrsOnly:               types.BoolNull(),
		DisableSSH:                 types.BoolNull(),
		ForksReceiveSecretEnvVars:  types.BoolNull(),
		OSS:                        types.BoolNull(),
		PROnlyBranchOverrides:      types.SetNull(types.StringType),
		Slug:                       types.StringValue(slug),
		SetGithubStatus:            types.BoolNull(),
		SetupWorkflows:             types.BoolNull(),
		WriteSettingsRequiresAdmin: types.BoolNull(),
	}

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(context.Background(), model); diags.HasError() {
		t.Fatalf("could not build a config value: %+v", diags)
	}

	return tfsdk.Config{Schema: schema, Raw: state.Raw}
}

// TestProjectSettingsDataSourceRejectsDotSegmentInSlug is the provider-layer
// regression test for the vulnerability class internal/circleci/project_settings.go
// defends against: this data source's Read splits a configuration-supplied
// "slug" on "/" into the three arguments GetProjectSettings takes, and the
// schema validates nothing about the resulting segments beyond the
// `^.+/.+/.+$` regex — which a segment of exactly "." or ".." satisfies just
// as well as an ordinary name. Without circleci.GetProjectSettings's own
// check, a slug like "github/./repo" would reach the wire unrejected: the
// fake API below validates the request's path *shape* (five slug-and-fixed
// segments plus "settings"), not the content of each segment, exactly like
// the routes this provider actually talks to, so a request that should never
// have been sent would succeed.
//
// This is defence in depth, not a fix for a reachable bug: slug is Terraform
// configuration here, never third-party input.
//
// It also checks the legitimate cases still parse, including a segment that
// merely contains a dot (a repository named "my.repo"), which must remain
// valid — rejecting that would break real configurations.
func TestProjectSettingsDataSourceRejectsDotSegmentInSlug(t *testing.T) {
	t.Parallel()

	schema := projectSettingsDataSourceSchemaForTest(t)

	tests := []struct {
		name    string
		slug    string
		wantErr bool
	}{
		{name: "dot org segment", slug: "github/./repo", wantErr: true},
		{name: "dot-dot org segment", slug: "github/../repo", wantErr: true},
		{name: "dot project segment", slug: "github/acme/.", wantErr: true},
		{name: "dot-dot project segment", slug: "github/acme/..", wantErr: true},
		{name: "ordinary slug", slug: testProjectSettingsSlug, wantErr: false},
		{name: "segment merely containing a dot", slug: "github/acme/my.repo", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, client := newFakeProjectSettingsAPI(t)
			d := &ProjectSettingsDataSource{client: client}

			resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schema}}
			d.Read(context.Background(), datasource.ReadRequest{
				Config: projectSettingsDataSourceConfigForTest(t, schema, tt.slug),
			}, resp)

			if tt.wantErr {
				if !resp.Diagnostics.HasError() {
					t.Errorf("Read with slug %q produced no diagnostics, want an error", tt.slug)
				}
				if requests := api.recordedRequests(); len(requests) != 0 {
					t.Errorf("requests = %v, want none: a %q slug must be rejected before a request is made",
						requests, tt.slug)
				}

				return
			}

			if resp.Diagnostics.HasError() {
				t.Errorf("Read with slug %q produced diagnostics: %+v, want none", tt.slug, resp.Diagnostics)
			}
			if requests := api.recordedRequests(); len(requests) != 1 {
				t.Errorf("requests = %v, want exactly one GET for a legitimate slug", requests)
			}
		})
	}
}

func TestProjectSettingsDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	schemaRequest := datasource.SchemaRequest{}
	schemaResponse := &datasource.SchemaResponse{}

	NewProjectSettingsDataSource().Schema(ctx, schemaRequest, schemaResponse)

	if schemaResponse.Diagnostics.HasError() {
		t.Fatalf("Schema method diagnostics: %+v", schemaResponse.Diagnostics)
	}

	diagnostics := schemaResponse.Schema.ValidateImplementation(ctx)

	if diagnostics.HasError() {
		t.Fatalf("Schema validation diagnostics: %+v", diagnostics)
	}
}
