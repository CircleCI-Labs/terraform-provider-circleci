// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const orbTestYAML = "version: 2.1\ndescription: an orb\n"

// TestAccOrbVersionResource publishes a version and then destroys the resource.
//
// The destroy is the interesting half: a published orb version cannot be deleted,
// so the provider must remove it from state, warn, and make no API call at all.
func TestAccOrbVersionResource(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_version" "test" {
  orb_id  = %q
  version = "1.0.0"
  yaml    = %q
}
`, orb.ID, orbTestYAML)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("circleci_orb_version.test", "id"),
					resource.TestCheckResourceAttr("circleci_orb_version.test", "orb_id", orb.ID),
					resource.TestCheckResourceAttr("circleci_orb_version.test", "orb_name", "acme/node"),
					resource.TestCheckResourceAttr("circleci_orb_version.test", "version", "1.0.0"),
					resource.TestCheckResourceAttr("circleci_orb_version.test", "yaml", orbTestYAML),
					resource.TestCheckResourceAttr("circleci_orb_version.test", "source", orbTestYAML),
					resource.TestCheckResourceAttr("circleci_orb_version.test", "created_at", orbFakeCreatedAt),
				),
			},
			{
				Config:            config,
				ResourceName:      "circleci_orb_version.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					res, ok := state.RootModule().Resources["circleci_orb_version.test"]
					if !ok {
						return "", fmt.Errorf("resource not found in state")
					}

					return res.Primary.Attributes["id"], nil
				},
			},
		},
	})

	if got := api.requestsFor("POST", "/api/v3/orb/versions"); len(got) != 1 {
		t.Errorf("publish requests = %d, want exactly 1", len(got))
	}
	// Nothing may be deleted: the version stays published forever.
	for _, request := range api.allRequests() {
		if request.Method == "DELETE" {
			t.Errorf("the provider sent %s %s, want no delete for a published orb version",
				request.Method, request.Path)
		}
	}
}

// TestAccOrbVersionResource_ImportIDMustBeUUID rejects a "ns/orb@version"
// reference, which is not something the API can look a version up by directly.
func TestAccOrbVersionResource_ImportIDMustBeUUID(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID)
	api.seedVersion(orb.ID, "1.0.0", orbTestYAML)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_version" "test" {
  orb_id  = %q
  version = "1.0.0"
  yaml    = %q
}
`, orb.ID, orbTestYAML)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:        config,
			ResourceName:  "circleci_orb_version.test",
			ImportState:   true,
			ImportStateId: "acme/node@1.0.0",
			ExpectError:   regexp.MustCompile(`(?s)Invalid import ID.*UUID`),
		}},
	})
}

// TestAccOrbVersionResource_RepublishingStableVersionFails is the failure mode the
// documentation warns about: changing yaml plans a replacement, and republishing
// an existing stable version is rejected. The error has to say why.
func TestAccOrbVersionResource_RepublishingStableVersionFails(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID)

	config := func(yaml string) string {
		return orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_version" "test" {
  orb_id  = %q
  version = "1.0.0"
  yaml    = %q
}
`, orb.ID, yaml)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config(orbTestYAML)},
			{
				Config:      config(orbTestYAML + "# changed\n"),
				ExpectError: regexp.MustCompile(`(?s)already exists.*immutable`),
			},
		},
	})
}

// TestAccOrbVersionResource_DevVersionCanBeRepublished is the documented
// exception: a dev version is mutable, so replacing the resource works.
func TestAccOrbVersionResource_DevVersionCanBeRepublished(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID)

	config := func(yaml string) string {
		return orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_version" "test" {
  orb_id  = %q
  version = "dev:alpha"
  yaml    = %q
}
`, orb.ID, yaml)
	}

	updated := orbTestYAML + "# changed\n"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config(orbTestYAML)},
			{
				Config: config(updated),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_orb_version.test", "version", "dev:alpha"),
					resource.TestCheckResourceAttr("circleci_orb_version.test", "source", updated),
				),
			},
		},
	})
}

// TestAccOrbVersionResource_RejectsBadVersion guards the validator, so that a
// typo is caught at plan time rather than by publishing something permanent.
func TestAccOrbVersionResource_RejectsBadVersion(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_version" "test" {
  orb_id  = %q
  version = "v1.0"
  yaml    = %q
}
`, orb.ID, orbTestYAML)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`semantic version`),
		}},
	})
}
