// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"sort"
)

// catalogOfferingsRoute is the execution catalog: the resource classes CircleCI
// will run jobs on, and the machine images available for each.
//
// v3 only. CircleCI Server does not route /api/v3 to the public API service, so
// callers must gate this on Client.IsCloud rather than let it fail as a 404.
const catalogOfferingsRoute = "/catalog/offerings"

// CatalogOfferings is the execution catalog, keyed by platform.
//
// Each map goes from resource class name (as written in .circleci/config.yml,
// for example "medium" or "gpu-nvidia-small") to the machine images available on
// it. The response comes from the API, which serializes the
// machine-provisioner Offerings struct straight into a v3 attributes envelope:
// four platform keys, each a map of resource class to image list.
//
// Deprecated holds resource classes still accepted but scheduled for removal.
// Note that a class appearing there is not necessarily absent from its platform
// map, so a validity check should consider both.
type CatalogOfferings struct {
	// Linux is the Linux (machine and Docker) resource classes.
	Linux map[string][]string `json:"linux"`
	// Windows is the Windows resource classes.
	Windows map[string][]string `json:"windows"`
	// MacOS is the macOS resource classes.
	MacOS map[string][]string `json:"macos"`
	// Deprecated is the resource classes that still run but are scheduled for
	// removal, and which organizations opted into resource class brownouts will
	// see fail during a brownout window.
	Deprecated map[string][]string `json:"deprecated"`
}

// ResourceClasses returns the names of every non-deprecated resource class across
// all three platforms, sorted and deduplicated.
//
// It exists so a configuration can validate a resource_class string against one
// flat list. Deprecated classes are excluded deliberately: a class that is only
// reachable through the deprecated map is not something a new configuration
// should be steered towards.
func (o CatalogOfferings) ResourceClasses() []string {
	seen := make(map[string]struct{})
	for _, platform := range []map[string][]string{o.Linux, o.Windows, o.MacOS} {
		for class := range platform {
			seen[class] = struct{}{}
		}
	}

	classes := make([]string, 0, len(seen))
	for class := range seen {
		classes = append(classes, class)
	}
	sort.Strings(classes)

	return classes
}

// catalogOfferingsBody is the entity inside the v3 envelope. The catalog is a
// singleton rather than an addressable entity, so the envelope carries
// attributes but no id and no references: the response is
// {"data": {"attributes": {...}}}.
type catalogOfferingsBody struct {
	Attributes CatalogOfferings `json:"attributes"`
}

// GetCatalogOfferings reads the execution catalog.
//
// v3 only: callers must gate this on Client.IsCloud.
func (c *Client) GetCatalogOfferings(ctx context.Context) (*CatalogOfferings, error) {
	var envelope Entity[catalogOfferingsBody]

	if err := c.GetV3(ctx, catalogOfferingsRoute, &envelope); err != nil {
		return nil, err
	}

	offerings := envelope.Data.Attributes

	return &offerings, nil
}
