// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"slices"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestUserDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewUserDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// id is both an input and a result: supplying it looks that user up, omitting it
	// resolves the token's own user and then reports their id.
	id, ok := resp.Schema.Attributes["id"]
	if !ok {
		t.Fatal("schema is missing the id attribute")
	}
	if !id.IsOptional() {
		t.Error("id is not optional, but omitting it must fall back to the current user")
	}
	if !id.IsComputed() {
		t.Error("id is not computed, but it must report the current user's id when omitted")
	}
	if id.IsRequired() {
		t.Error("id is required, but the current user must be readable without one")
	}
}

func TestAccUserDataSource_currentUser(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	config := discoveryProviderConfig(host, "cloud") + `
data "circleci_user" "me" {}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_user.me",
						tfjsonpath.New("id"),
						knownvalue.StringExact(testDiscoveryUserID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_user.me",
						tfjsonpath.New("login"),
						knownvalue.StringExact("octocat"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_user.me",
						tfjsonpath.New("name"),
						knownvalue.StringExact("Mona Lisa Octocat"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_user.me",
						tfjsonpath.New("avatar_url"),
						knownvalue.StringExact("https://avatars.example.com/u/1"),
					),
				},
			},
		},
	})

	// With no id, /me must be the route taken — never /user/ with an empty segment.
	if !slices.Contains(api.seenRequests(), "GET /api/v2/me") {
		t.Errorf("requests = %v, want a GET of /api/v2/me", api.seenRequests())
	}
}

func TestAccUserDataSource_byID(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	config := discoveryProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_user" "other" {
  id = %[1]q
}
`, testDiscoveryUserID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					// Served by /user/{id}, which the mock answers with a different login
					// than /me, so a data source that quietly fell back to /me would fail.
					statecheck.ExpectKnownValue(
						"data.circleci_user.other",
						tfjsonpath.New("login"),
						knownvalue.StringExact("other-user"),
					),
				},
			},
		},
	})

	want := "GET /api/v2/user/" + testDiscoveryUserID
	if !slices.Contains(api.seenRequests(), want) {
		t.Errorf("requests = %v, want a GET of %s", api.seenRequests(), want)
	}
}

func TestAccUserDataSource_notFound(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	config := discoveryProviderConfig(host, "cloud") + `
data "circleci_user" "missing" {
  id = "99999999-9999-9999-9999-999999999999"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`Unable to read CircleCI user 99999999-9999-9999-9999-999999999999`),
		}},
	})
}

func TestAccUserDataSource_forbidden(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	// /me answers 403, not 401, for a token that is not a user token. The error must
	// explain that rather than reporting a bare permissions failure, because the
	// route authorizes the caller and nothing about the request looks wrong.
	api.userForbidden = true

	config := discoveryProviderConfig(host, "cloud") + `
data "circleci_user" "me" {}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`(?s)Unable to read the current CircleCI user.*personal API token`),
		}},
	})
}

func TestUserCollaborationsDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewUserCollaborationsDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// The collection is scoped entirely by the configured token, so there is nothing
	// to narrow it by and the schema must take no arguments at all.
	if len(resp.Schema.Attributes) != 1 {
		t.Errorf("schema has %d attributes, want only collaborations", len(resp.Schema.Attributes))
	}
	collaborations, ok := resp.Schema.Attributes["collaborations"]
	if !ok {
		t.Fatal("schema is missing the collaborations attribute")
	}
	if !collaborations.IsComputed() {
		t.Error("collaborations is not computed, but it is entirely API-derived")
	}
}

func TestAccUserCollaborationsDataSource(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	config := discoveryProviderConfig(host, "cloud") + `
data "circleci_user_collaborations" "test" {}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_user_collaborations.test",
						tfjsonpath.New("collaborations"),
						knownvalue.ListSizeExact(2),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_user_collaborations.test",
						tfjsonpath.New("collaborations").AtSliceIndex(0).AtMapKey("id"),
						knownvalue.StringExact("11111111-1111-1111-1111-111111111111"),
					),
					// Decoded from vcs_type, which is how the v2 API spells it on the
					// wire even though it declares the key in kebab-case internally.
					statecheck.ExpectKnownValue(
						"data.circleci_user_collaborations.test",
						tfjsonpath.New("collaborations").AtSliceIndex(0).AtMapKey("vcs_type"),
						knownvalue.StringExact("circleci"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_user_collaborations.test",
						tfjsonpath.New("collaborations").AtSliceIndex(0).AtMapKey("slug"),
						knownvalue.StringExact("circleci/11111111-1111-1111-1111-111111111111"),
					),
					// A null id must reach Terraform as null, not as "", so that
					// `id != null` is a usable test for "onboarded onto CircleCI".
					statecheck.ExpectKnownValue(
						"data.circleci_user_collaborations.test",
						tfjsonpath.New("collaborations").AtSliceIndex(1).AtMapKey("id"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_user_collaborations.test",
						tfjsonpath.New("collaborations").AtSliceIndex(1).AtMapKey("name"),
						knownvalue.StringExact("not-onboarded"),
					),
				},
			},
		},
	})
}

func TestAccUserCollaborationsDataSource_filtersOnVCSType(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	// The documented way to narrow the set: standalone organizations only, and only
	// the ones CircleCI actually knows about.
	config := discoveryProviderConfig(host, "cloud") + `
data "circleci_user_collaborations" "test" {}

output "standalone_org_ids" {
  value = join(",", [
    for c in data.circleci_user_collaborations.test.collaborations :
    c.id if c.vcs_type == "circleci" && c.id != null
  ])
}
`

	// The GitHub entry is excluded by vcs_type and would also be excluded by the null
	// id, so a single result proves both halves of the filter work.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.TestCheckOutput(
					"standalone_org_ids", "11111111-1111-1111-1111-111111111111",
				),
			},
		},
	})
}
