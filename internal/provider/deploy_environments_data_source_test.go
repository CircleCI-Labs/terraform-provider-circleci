// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

func testAccDeployEnvironmentsConfig(host, deployment string) string {
	return deployProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_deploy_environments" "test" {
  organization_id = %[1]q
}
`, testDeployOrganizationID)
}

func TestDeployEnvironmentsDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewDeployEnvironmentsDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// The organization is the scope of the listing, but it is accepted as either
	// `organization_id` or `org_id` while the former is deprecated, so both are
	// Optional and the config validator requires exactly one. See
	// org_id_deprecation.go.
	for _, name := range []string{"organization_id", "org_id"} {
		if !resp.Schema.Attributes[name].IsOptional() {
			t.Errorf("attribute %q is not optional, but one of the pair must be settable", name)
		}
	}
	if !resp.Schema.Attributes["environments"].IsComputed() {
		t.Error("environments is not computed, but it is entirely API-derived")
	}
}

func TestAccDeployEnvironmentsDataSource(t *testing.T) {
	api, host := newMockDeployAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: deployProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDeployEnvironmentsConfig(host, "cloud"),
				Check: func(*terraform.State) error {
					for _, req := range api.seenRequests() {
						if req == "GET /api/v2/deploy/environments?org-id="+testDeployOrganizationID {
							return nil
						}
					}

					return fmt.Errorf("no request scoped the listing by org-id; requests seen: %v", api.seenRequests())
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_environments.test",
						tfjsonpath.New("environments"),
						knownvalue.ListSizeExact(1),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_environments.test",
						tfjsonpath.New("environments").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("prod-app"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_deploy_environments.test",
						tfjsonpath.New("environments").AtSliceIndex(0).AtMapKey("labels"),
						knownvalue.MapExact(map[string]knownvalue.Check{
							"env": knownvalue.StringExact("prod"),
						}),
					),
				},
			},
		},
	})
}

func TestAccDeployEnvironmentsDataSource_serverDeployment(t *testing.T) {
	_, host := newMockDeployAPI(t)

	// The backend behind this route is not deployed on CircleCI Server, so
	// deployment = "server" must be rejected rather than attempting a request
	// the Server installation cannot route.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: deployProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccDeployEnvironmentsConfig(host, "server"),
			ExpectError: regexp.MustCompile(`circleci_deploy_environments requires CircleCI Cloud`),
		}},
	})
}

// TestEntityLabelsToMapDuplicateKeys pins the one place where the deploy
// data sources cannot represent what the API sent.
//
// Labels arrive as a JSON *list* of {key, value} pairs, and the API does not
// require the key to be unique within an entity: two labels can share a key.
// Terraform exposes them as a map keyed by label key, so one of the two
// values cannot be represented. The value that survives
// must not depend on the order the rows came back in, and the one that is
// dropped must be reported — silently keeping whichever pair happened to be
// last is how a label visible in the CircleCI UI goes missing from state with
// no signal at all.
func TestEntityLabelsToMapDuplicateKeys(t *testing.T) {
	t.Parallel()

	labels := []circleci.EntityLabel{
		{Key: "env", Value: "prod"},
		{Key: "team", Value: "deploy"},
		{Key: "env", Value: "staging"},
	}

	value, diags := entityLabelsToMap(context.Background(), labels)
	if diags.HasError() {
		t.Fatalf("entityLabelsToMap returned errors: %v", diags.Errors())
	}

	elements := value.Elements()
	if len(elements) != 2 {
		t.Fatalf("label count = %d, want 2 (the duplicate key collapses)", len(elements))
	}
	env, ok := elements["env"].(types.String)
	if !ok {
		t.Fatalf(`labels["env"] has type %T, want types.String`, elements["env"])
	}
	if got := env.ValueString(); got != "prod" {
		t.Errorf(`labels["env"] = %q, want "prod": the first pair in API order must win, not the last`, got)
	}

	warnings := diags.Warnings()
	if len(warnings) != 1 {
		t.Fatalf("warning count = %d, want 1 naming the dropped label", len(warnings))
	}
	if summary := warnings[0].Summary(); !strings.Contains(summary, "env") {
		t.Errorf("warning summary = %q, want it to name the duplicated key %q", summary, "env")
	}
	if detail := warnings[0].Detail(); !strings.Contains(detail, "staging") {
		t.Errorf("warning detail = %q, want it to name the dropped value %q", detail, "staging")
	}
}

// TestEntityLabelsToMapNoDuplicatesIsQuiet guards against the warning above
// firing on the ordinary case, which would make every deploy read noisy.
func TestEntityLabelsToMapNoDuplicatesIsQuiet(t *testing.T) {
	t.Parallel()

	value, diags := entityLabelsToMap(context.Background(), []circleci.EntityLabel{
		{Key: "env", Value: "prod"},
		{Key: "team", Value: "deploy"},
	})
	if len(diags) != 0 {
		t.Errorf("diagnostics = %v, want none for distinct label keys", diags)
	}
	if got := len(value.Elements()); got != 2 {
		t.Errorf("label count = %d, want 2", got)
	}
}
