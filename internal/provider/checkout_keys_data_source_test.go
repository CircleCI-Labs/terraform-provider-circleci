// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func checkoutKeysDataSourceConfig(host, digest string) string {
	cfg := checkoutKeyProviderConfig(host) + fmt.Sprintf(`
data "circleci_checkout_keys" "test" {
  project_slug = %q
`, checkoutKeyTestSlug)

	if digest != "" {
		cfg += fmt.Sprintf("  digest = %q\n", digest)
	}

	return cfg + "}\n"
}

// TestAccCheckoutKeysDataSource reads a project's keys across two pages, which
// also proves the client follows the v2 page-token.
func TestAccCheckoutKeysDataSource(t *testing.T) {
	api := &checkoutKeyAPI{
		pageSize: 1,
		keys: []map[string]any{
			{
				"public_key":  "ssh-rsa AAAA",
				"type":        "deploy-key",
				"fingerprint": "aa:bb",
				"preferred":   true,
				"created_at":  "2024-01-02T03:04:05.000Z",
			},
			{
				"public_key":  "ssh-rsa BBBB",
				"type":        "github-user-key",
				"fingerprint": "cc:dd",
				"preferred":   false,
				"created_at":  "2024-02-03T04:05:06.000Z",
			},
		},
	}
	srv := newCheckoutKeyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: checkoutKeysDataSourceConfig(srv.URL, "md5"),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_checkout_keys.test",
					tfjsonpath.New("checkout_keys"),
					knownvalue.ListSizeExact(2),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_checkout_keys.test",
					tfjsonpath.New("checkout_keys").AtSliceIndex(0),
					knownvalue.ObjectExact(map[string]knownvalue.Check{
						"fingerprint": knownvalue.StringExact("aa:bb"),
						"type":        knownvalue.StringExact("deploy-key"),
						"public_key":  knownvalue.StringExact("ssh-rsa AAAA"),
						"preferred":   knownvalue.Bool(true),
						"created_at":  knownvalue.StringExact("2024-01-02T03:04:05.000Z"),
					}),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_checkout_keys.test",
					tfjsonpath.New("checkout_keys").AtSliceIndex(1),
					knownvalue.ObjectExact(map[string]knownvalue.Check{
						"fingerprint": knownvalue.StringExact("cc:dd"),
						// The API's own vocabulary is preserved here, unlike the
						// resource's type attribute.
						"type":       knownvalue.StringExact("github-user-key"),
						"public_key": knownvalue.StringExact("ssh-rsa BBBB"),
						"preferred":  knownvalue.Bool(false),
						"created_at": knownvalue.StringExact("2024-02-03T04:05:06.000Z"),
					}),
				),
			},
		}},
	})

	requests := api.recorded()

	var sawFirstPage, sawSecondPage bool
	for _, req := range requests {
		if strings.Contains(req, "%2F") {
			t.Errorf("request %q escapes the project slug separators", req)
		}

		switch req {
		case "GET /api/v2/project/github/my-org/my-repo/checkout-key?digest=md5":
			sawFirstPage = true
		case "GET /api/v2/project/github/my-org/my-repo/checkout-key?digest=md5&page-token=page-1":
			sawSecondPage = true
		}
	}

	if !sawFirstPage {
		t.Errorf("no first-page request with the expected URI, got %q", requests)
	}
	if !sawSecondPage {
		t.Errorf("no second-page request with the expected URI, got %q", requests)
	}
}

// TestAccCheckoutKeysDataSource_NoKeys checks that a project with no keys yields
// an empty list rather than a null one, so configurations can iterate over it.
func TestAccCheckoutKeysDataSource_NoKeys(t *testing.T) {
	api := &checkoutKeyAPI{}
	srv := newCheckoutKeyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: checkoutKeysDataSourceConfig(srv.URL, ""),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_checkout_keys.test",
					tfjsonpath.New("checkout_keys"),
					knownvalue.ListSizeExact(0),
				),
			},
		}},
	})

	// With no digest configured the parameter must be omitted entirely, so the
	// API's own default applies.
	for _, req := range api.recorded() {
		if strings.Contains(req, "digest") {
			t.Errorf("request %q sends a digest parameter, want it omitted", req)
		}
	}
}
