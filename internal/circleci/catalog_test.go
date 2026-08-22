// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// testCatalogOfferingsBody is the shape production sends: four platform keys, each a
// map of resource class name to image list, serialized into a v3 attributes envelope
// with no id and no references.
const testCatalogOfferingsBody = `{
  "data": {
    "attributes": {
      "linux": {
        "medium": ["ubuntu-2404:current", "ubuntu-2204:current"],
        "large": ["ubuntu-2404:current"]
      },
      "windows": {
        "windows.medium": ["windows-server-2022-gui:current"]
      },
      "macos": {
        "macos.m1.medium.gen1": ["macos-sequoia:current"]
      },
      "deprecated": {
        "ubuntu-2004": ["2024.10.1"]
      }
    }
  }
}`

// newCatalogServer serves the catalog route from handler and records the paths it
// was called with.
func newCatalogServer(t *testing.T, handler http.HandlerFunc) (*circleci.Client, *[]string) {
	t.Helper()

	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.RequestURI)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return circleci.New(circleci.Config{Host: srv.URL, Token: "tok"}), &seen
}

func TestGetCatalogOfferings(t *testing.T) {
	t.Parallel()

	client, seen := newCatalogServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testCatalogOfferingsBody))
	})

	offerings, err := client.GetCatalogOfferings(context.Background())
	if err != nil {
		t.Fatalf("GetCatalogOfferings returned error: %v", err)
	}

	// The route is v3, and it takes no parameters at all.
	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	if got := (*seen)[0]; got != "GET /api/v3/catalog/offerings" {
		t.Errorf("request = %q, want %q", got, "GET /api/v3/catalog/offerings")
	}

	if images := offerings.Linux["medium"]; len(images) != 2 || images[0] != "ubuntu-2404:current" {
		t.Errorf("linux.medium images = %v, want two entries starting with ubuntu-2404:current", images)
	}
	if _, ok := offerings.Windows["windows.medium"]; !ok {
		t.Errorf("windows classes = %v, want a windows.medium entry", offerings.Windows)
	}
	if _, ok := offerings.MacOS["macos.m1.medium.gen1"]; !ok {
		t.Errorf("macos classes = %v, want a macos.m1.medium.gen1 entry", offerings.MacOS)
	}
	if _, ok := offerings.Deprecated["ubuntu-2004"]; !ok {
		t.Errorf("deprecated classes = %v, want an ubuntu-2004 entry", offerings.Deprecated)
	}
}

func TestCatalogOfferingsResourceClasses(t *testing.T) {
	t.Parallel()

	client, _ := newCatalogServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testCatalogOfferingsBody))
	})

	offerings, err := client.GetCatalogOfferings(context.Background())
	if err != nil {
		t.Fatalf("GetCatalogOfferings returned error: %v", err)
	}

	// Sorted, deduplicated, and across all three platforms — the flat list a
	// configuration validates a resource_class string against.
	want := []string{"large", "macos.m1.medium.gen1", "medium", "windows.medium"}
	if got := offerings.ResourceClasses(); !slices.Equal(got, want) {
		t.Errorf("ResourceClasses() = %v, want %v", got, want)
	}

	// A deprecated class that is not offered on any platform must not appear, so
	// that a new configuration is not steered towards it.
	if slices.Contains(offerings.ResourceClasses(), "ubuntu-2004") {
		t.Error("ResourceClasses() includes the deprecated-only class ubuntu-2004, want it excluded")
	}
}

func TestGetCatalogOfferingsError(t *testing.T) {
	t.Parallel()

	// v3 errors carry the {"error": {...}} envelope, with only id and title on a
	// 403. The detail must surface the title rather than just the status.
	client, _ := newCatalogServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"id":"01JQ8W0K0000000000000000","title":"Forbidden"}}`))
	})

	_, err := client.GetCatalogOfferings(context.Background())
	if err == nil {
		t.Fatal("GetCatalogOfferings returned no error for a 403, want one")
	}
	if !circleci.IsUnauthorized(err) {
		t.Errorf("IsUnauthorized(%v) = false, want true", err)
	}
	if detail := circleci.Detail(err); !strings.Contains(detail, "Forbidden") {
		t.Errorf("Detail() = %q, want it to carry the envelope title", detail)
	}
}

// TestGetCatalogOfferingsNetShape is the real-API counterpart to every other
// test in this file, which all serve testCatalogOfferingsBody from a fake.
// The specific thing it exists to check: circleci_catalog_offerings was
// suspected of carrying resource-class specs (cpu, ram, size, ordering) that
// GetCatalogOfferings' typed struct would silently drop if present, since
// encoding/json ignores fields a destination struct does not declare. Decoding
// into a raw map instead, rather than circleci.CatalogOfferings, is what
// makes this test capable of noticing a field the typed path could not.
//
// [NET, reproduced against the live API on 2026-08-21, using CIRCLE_TOKEN's
// own organization]: every resource-class value under every one of the four
// platform keys is a bare JSON array of image-tag strings — never an object —
// and the attributes object carries no key besides linux/windows/macos/deprecated.
// circleci_catalog_offerings' resource_classes output (name only, no cpu, ram,
// size or ordering) is the whole of what this route offers; a sizing tool needs
// a different data source.
func TestGetCatalogOfferingsNetShape(t *testing.T) {
	token := os.Getenv("CIRCLE_TOKEN")
	if token == "" {
		t.Skip("CIRCLE_TOKEN must be set for this network test")
	}

	client := circleci.New(circleci.Config{Token: token})

	var envelope circleci.Entity[struct {
		Attributes map[string]json.RawMessage `json:"attributes"`
	}]

	if err := client.GetV3(context.Background(), "/catalog/offerings", &envelope); err != nil {
		t.Fatalf("GET /catalog/offerings against the live API returned error: %v", err)
	}

	attrs := envelope.Data.Attributes

	for _, key := range []string{"linux", "windows", "macos", "deprecated"} {
		if _, ok := attrs[key]; !ok {
			t.Errorf("live catalog attributes are missing expected key %q (got keys %v)", key, mapKeys(attrs))
		}
	}
	for key := range attrs {
		if !slices.Contains([]string{"linux", "windows", "macos", "deprecated"}, key) {
			t.Errorf("live catalog attributes carry an unexpected top-level key %q — "+
				"circleci_catalog_offerings' schema has no attribute for it", key)
		}
	}

	checkedAtLeastOneClass := false

	for platform, classesRaw := range attrs {
		var classes map[string]json.RawMessage
		if err := json.Unmarshal(classesRaw, &classes); err != nil {
			t.Errorf("platform %q is not an object of resource-class name to value: %v", platform, err)

			continue
		}

		for class, value := range classes {
			checkedAtLeastOneClass = true

			// The whole point of this test: if the API ever attached a spec object
			// (cpu, ram, size, ordering, ...) to a resource class instead of a bare
			// image list, this decode into []string would fail, where decoding the
			// same response through circleci.CatalogOfferings would not — a struct
			// field decode silently ignores JSON object keys it does not declare.
			var images []string
			if err := json.Unmarshal(value, &images); err != nil {
				t.Errorf(
					"platform %q class %q value is not a bare array of image strings (got %s): %v — "+
						"the API may now carry resource-class spec fields (cpu/ram/size/ordering) that "+
						"circleci_catalog_offerings does not expose",
					platform, class, string(value), err,
				)
			}
		}
	}

	if !checkedAtLeastOneClass {
		t.Fatal("the live catalog reported no resource classes on any platform for this token's " +
			"organization; this test needs at least one to check the per-class value shape")
	}
}

// mapKeys returns the keys of m, for a diagnostic message; order is
// unspecified and irrelevant here.
func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	return keys
}

func TestGetCatalogOfferingsEmptyPlatforms(t *testing.T) {
	t.Parallel()

	// An organization with no macOS or Windows entitlement sees empty objects
	// rather than nulls, and ResourceClasses must cope with both.
	client, _ := newCatalogServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"data":{"attributes":{"linux":{"medium":["ubuntu-2404:current"]},"windows":{},"macos":null,"deprecated":{}}}}`,
		))
	})

	offerings, err := client.GetCatalogOfferings(context.Background())
	if err != nil {
		t.Fatalf("GetCatalogOfferings returned error: %v", err)
	}

	want := []string{"medium"}
	if got := offerings.ResourceClasses(); !slices.Equal(got, want) {
		t.Errorf("ResourceClasses() = %v, want %v", got, want)
	}
}
