// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccOrbNamespaceDataSource_ByName(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")

	config := orbProviderConfig(api.URL()) + `
data "circleci_orb_namespace" "test" {
  name = "acme"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.circleci_orb_namespace.test", "id", ns.ID),
				resource.TestCheckResourceAttr("data.circleci_orb_namespace.test", "name", "acme"),
			),
		}},
	})

	// The by-name form is the collection scoped by filter[name].
	request := api.requestsFor("GET", "/api/v3/namespaces")
	if len(request) == 0 {
		t.Fatal("the provider never listed namespaces")
	}
	if got := request[0].Query.Get("filter[name]"); got != "acme" {
		t.Errorf("filter[name] = %q, want %q", got, "acme")
	}
}

func TestAccOrbNamespaceDataSource_ByID(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
data "circleci_orb_namespace" "test" {
  id = %q
}
`, ns.ID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check:  resource.TestCheckResourceAttr("data.circleci_orb_namespace.test", "name", "acme"),
		}},
	})
}

func TestAccOrbNamespaceDataSource_RequiresExactlyOneKey(t *testing.T) {
	api := newOrbFakeAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      orbProviderConfig(api.URL()) + `data "circleci_orb_namespace" "test" {}`,
				ExpectError: regexp.MustCompile(`Exactly one of these attributes must be configured`),
			},
			{
				Config: orbProviderConfig(api.URL()) + `
data "circleci_orb_namespace" "test" {
  id   = "00000001-1111-2222-3333-444444444444"
  name = "acme"
}
`,
				ExpectError: regexp.MustCompile(`Exactly one of these attributes must be configured`),
			},
		},
	})
}

func TestAccOrbNamespaceDataSource_NotFound(t *testing.T) {
	api := newOrbFakeAPI(t)

	config := orbProviderConfig(api.URL()) + `
data "circleci_orb_namespace" "test" {
  name = "missing"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`(?s)Unable to read CircleCI orb namespace.*not found`),
		}},
	})
}

func TestAccOrbDataSource_ByFullName(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID, orbBuildCategoryID)
	api.seedVersion(orb.ID, "1.2.3", orbTestYAML)

	config := orbProviderConfig(api.URL()) + `
data "circleci_orb" "test" {
  full_name = "acme/node"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.circleci_orb.test", "id", orb.ID),
				resource.TestCheckResourceAttr("data.circleci_orb.test", "name", "node"),
				// The namespace name is only in the detail response, so a by-name
				// lookup has to refetch by id to fill it in.
				resource.TestCheckResourceAttr("data.circleci_orb.test", "namespace", "acme"),
				resource.TestCheckResourceAttr("data.circleci_orb.test", "namespace_id", ns.ID),
				resource.TestCheckResourceAttr("data.circleci_orb.test", "latest_version", "1.2.3"),
				resource.TestCheckResourceAttr("data.circleci_orb.test", "categories.0.name", "Build"),
			),
		}},
	})
}

func TestAccOrbsDataSource(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	api.seedOrb("acme/node", ns.ID)
	api.seedOrb("acme/python", ns.ID)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
data "circleci_orbs" "test" {
  namespace_id = %q
}
`, ns.ID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_orbs.test",
					tfjsonpath.New("orbs"),
					knownvalue.ListSizeExact(2),
				),
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				// The collection returns only a namespace id, so the namespace name
				// is taken from the qualified name rather than invented.
				resource.TestCheckResourceAttr("data.circleci_orbs.test", "orbs.0.namespace", "acme"),
				resource.TestCheckResourceAttr("data.circleci_orbs.test", "orbs.0.namespace_id", ns.ID),
			),
		}},
	})

	request := api.requestsFor("GET", "/api/v3/orb/packages")
	if len(request) == 0 {
		t.Fatal("the provider never listed orbs")
	}
	if got := request[0].Query.Get("filter[namespace_id]"); got != ns.ID {
		t.Errorf("filter[namespace_id] = %q, want %q", got, ns.ID)
	}
	if _, set := request[0].Query["filter[certified]"]; set {
		t.Error("filter[certified] was sent although certified was unset in the configuration")
	}
}

func TestAccOrbVersionDataSource(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID)
	version := api.seedVersion(orb.ID, "1.2.3", orbTestYAML)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
data "circleci_orb_version" "by_ref" {
  orb_id  = %q
  version = "1.2.3"
}

data "circleci_orb_version" "by_id" {
  id = %q
}
`, orb.ID, version.ID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.circleci_orb_version.by_ref", "id", version.ID),
				resource.TestCheckResourceAttr("data.circleci_orb_version.by_ref", "orb_name", "acme/node"),
				resource.TestCheckResourceAttr("data.circleci_orb_version.by_ref", "source", orbTestYAML),
				resource.TestCheckResourceAttr("data.circleci_orb_version.by_id", "version", "1.2.3"),
				resource.TestCheckResourceAttr("data.circleci_orb_version.by_id", "orb_id", orb.ID),
			),
		}},
	})

	// The version collection is always scoped to one orb, so filter[orb_id] is
	// required by the API.
	// Match the collection route exactly: the by-id route shares its prefix, and
	// Terraform reads the two data sources in an unspecified order.
	var listed []orbFakeRequest
	for _, request := range api.allRequests() {
		if request.Method == "GET" && request.Path == "/api/v3/orb/versions" {
			listed = append(listed, request)
		}
	}

	if len(listed) == 0 {
		t.Fatal("the provider never listed orb versions")
	}
	// Lookup by version goes through filter[ref], which takes a fully-qualified
	// "namespace/orb@version" reference. The server ignores filter[orb_id]
	// whenever filter[ref] is present, so sending a bare version resolved nothing
	// against production.
	if got, want := listed[0].Query.Get("filter[ref]"), "acme/node@1.2.3"; got != want {
		t.Errorf("filter[ref] = %q, want %q", got, want)
	}
}

func TestAccOrbVersionDataSource_RequiresOrbIDWithVersion(t *testing.T) {
	api := newOrbFakeAPI(t)

	config := orbProviderConfig(api.URL()) + `
data "circleci_orb_version" "test" {
  version = "1.2.3"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`Exactly one of these attributes must be configured`),
		}},
	})
}

func TestAccOrbCategoriesDataSource(t *testing.T) {
	api := newOrbFakeAPI(t)

	config := orbProviderConfig(api.URL()) + `
data "circleci_orb_categories" "test" {}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.circleci_orb_categories.test", "categories.#", "2"),
				resource.TestCheckResourceAttr("data.circleci_orb_categories.test", "categories.0.name", "Build"),
				// ids_by_name is what makes circleci_orb.category_ids usable from a
				// human-readable category name.
				resource.TestCheckResourceAttr("data.circleci_orb_categories.test",
					"ids_by_name.Build", orbBuildCategoryID),
				resource.TestCheckResourceAttr("data.circleci_orb_categories.test",
					"ids_by_name.Notifications", orbNotifyCategoryID),
			),
		}},
	})
}
