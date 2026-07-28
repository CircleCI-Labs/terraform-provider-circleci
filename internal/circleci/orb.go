// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"fmt"
	"strconv"

	"terraform-provider-circleci/internal/httpcl"
)

// Orb visibility filter values for ListOrbPackagesOptions.Visibility.
const (
	// OrbVisibilityPublic restricts a listing to public orbs.
	OrbVisibilityPublic = "public"
	// OrbVisibilityPrivate restricts a listing to orbs private to an
	// organization. Private orbs are not returned by an unfiltered listing, so a
	// lookup that must find one has to ask for them explicitly.
	OrbVisibilityPrivate = "private"
)

// Orb release channel filter values for ListOrbVersionsOptions.Channel.
const (
	// OrbChannelStable selects published semantic versions.
	OrbChannelStable = "stable"
	// OrbChannelDev selects mutable dev:<label> versions.
	OrbChannelDev = "dev"
)

// Promotion segments for PromoteOrbVersionRequest.Segment.
const (
	OrbSegmentMajor = "major"
	OrbSegmentMinor = "minor"
	OrbSegmentPatch = "patch"
)

// --- domain types ---

// OrbPackage is an orb: the named container that owns a series of orb versions.
// Its Name is fully qualified as "<namespace>/<orb>", which is how the API
// stores and filters it.
//
// Not every field is populated by every route. The /orb/packages collection
// returns a thinner record than the by-id route does — see orbPackageListWire —
// so Namespace, CreatedAt, HomeURL and the usage counts are empty on packages
// that came from a listing.
type OrbPackage struct {
	ID          string
	Name        string
	Namespace   string
	NamespaceID string
	IsPrivate   bool
	IsListed    bool
	CreatedAt   string
	HomeURL     string
	// LatestVersion is the first entry of the version references, which the API
	// returns most-recent first. It is empty for an orb with no versions yet.
	LatestVersion          string
	LatestVersionCreatedAt string
	Last30DaysBuildCount   int64
	Last30DaysProjectCount int64
	Last30DaysOrgCount     int64
	Categories             []OrbCategory
}

// OrbVersion is a single published version of an orb. Once published a version
// is immutable and cannot be deleted or overwritten.
type OrbVersion struct {
	ID      string
	OrbID   string
	OrbName string
	// Version is a semantic version such as "1.2.3" for a stable release, or a
	// "dev:<label>" string for a dev release.
	Version   string
	CreatedAt string
	// Source is the orb YAML. It is only populated on routes that include it;
	// use GetOrbSource to fetch it explicitly.
	Source string
}

// OrbCategory is one of the fixed registry categories an orb can be listed
// under, such as "Build" or "Notifications".
type OrbCategory struct {
	ID   string
	Name string
}

// OrbValidation is the result of checking orb YAML without publishing it.
type OrbValidation struct {
	Valid      bool
	OutputYAML string
	Errors     []string
}

// --- wire types ---

// namespaceRef is the namespace reference carried by an orb package detail
// response, which includes the namespace name.
type namespaceRef struct {
	ID         string `json:"id"`
	Attributes struct {
		Name string `json:"name"`
	} `json:"attributes"`
}

// thinNamespaceRef is the namespace reference carried by the orb package
// collection, which supplies only an id.
type thinNamespaceRef struct {
	ID string `json:"id"`
}

type orbVersionRef struct {
	ID         string `json:"id"`
	Attributes struct {
		Version   string `json:"version"`
		CreatedAt string `json:"created_at"`
	} `json:"attributes"`
}

type orbCategoryRef struct {
	ID         string `json:"id"`
	Attributes struct {
		Name string `json:"name"`
	} `json:"attributes"`
}

// orbPackageWire is the detail shape, returned by GET /orb/packages/{id} and by
// every POST that answers with a package.
type orbPackageWire struct {
	ID         string `json:"id"`
	Attributes struct {
		Name                   string `json:"name"`
		IsPrivate              bool   `json:"is_private"`
		IsListed               bool   `json:"is_listed"`
		CreatedAt              string `json:"created_at"`
		HomeURL                string `json:"home_url"`
		Last30DaysBuildCount   int64  `json:"last_30_days_build_count"`
		Last30DaysProjectCount int64  `json:"last_30_days_project_count"`
		Last30DaysOrgCount     int64  `json:"last_30_days_org_count"`
	} `json:"attributes"`
	References struct {
		Namespace  namespaceRef     `json:"namespace"`
		Versions   []orbVersionRef  `json:"orb_versions"`
		Categories []orbCategoryRef `json:"orb_categories"`
	} `json:"references"`
}

// orbPackageListWire is the shape returned by GET /orb/packages.
//
// It is deliberately a second type rather than a reuse of orbPackageWire,
// because the collection is genuinely thinner: references.namespace carries only
// an id with no attributes.name, and the created_at, home_url and usage counts
// are absent. Decoding the collection into the detail type would silently yield
// a package whose Namespace is "" and whose counts are zero, which reads as real
// data rather than as missing data.
type orbPackageListWire struct {
	ID         string `json:"id"`
	Attributes struct {
		Name      string `json:"name"`
		IsPrivate bool   `json:"is_private"`
		IsListed  bool   `json:"is_listed"`
	} `json:"attributes"`
	References struct {
		Namespace  thinNamespaceRef `json:"namespace"`
		Versions   []orbVersionRef  `json:"orb_versions"`
		Categories []orbCategoryRef `json:"orb_categories"`
	} `json:"references"`
}

type orbVersionWire struct {
	ID         string `json:"id"`
	Attributes struct {
		Version   string `json:"version"`
		CreatedAt string `json:"created_at"`
		Source    string `json:"source"`
	} `json:"attributes"`
	References struct {
		Package struct {
			ID         string `json:"id"`
			Attributes struct {
				Name string `json:"name"`
			} `json:"attributes"`
		} `json:"orb_package"`
	} `json:"references"`
}

type orbCategoryWire struct {
	ID         string `json:"id"`
	Attributes struct {
		Name string `json:"name"`
	} `json:"attributes"`
}

type orbValidationWire struct {
	Attributes struct {
		Valid      bool     `json:"is_valid"`
		OutputYAML string   `json:"output_yaml"`
		Errors     []string `json:"errors"`
	} `json:"attributes"`
}

// createOrbPackageWire is the data payload of POST /orb/packages. Unlike the
// namespace routes, the orb routes take the full v3 data/attributes/references
// envelope on the way in as well as out.
type createOrbPackageWire struct {
	Attributes struct {
		Name      string `json:"name"`
		IsPrivate bool   `json:"is_private"`
	} `json:"attributes"`
	References struct {
		Namespace thinNamespaceRef `json:"namespace"`
	} `json:"references"`
}

// publishOrbVersionWire is the data payload of POST /orb/versions.
type publishOrbVersionWire struct {
	Attributes PublishOrbVersionRequest `json:"attributes"`
}

type orbValidateRequest struct {
	YAML           string `json:"yaml"`
	OrganizationID string `json:"org_id,omitempty"`
}

type orbSetListedRequest struct {
	IsListed bool `json:"is_listed"`
}

type orbCategoryRequest struct {
	CategoryID string `json:"category_id"`
}

// --- converters ---

func (w orbPackageWire) toOrbPackage() *OrbPackage {
	pkg := &OrbPackage{
		ID:                     w.ID,
		Name:                   w.Attributes.Name,
		Namespace:              w.References.Namespace.Attributes.Name,
		NamespaceID:            w.References.Namespace.ID,
		IsPrivate:              w.Attributes.IsPrivate,
		IsListed:               w.Attributes.IsListed,
		CreatedAt:              w.Attributes.CreatedAt,
		HomeURL:                w.Attributes.HomeURL,
		Last30DaysBuildCount:   w.Attributes.Last30DaysBuildCount,
		Last30DaysProjectCount: w.Attributes.Last30DaysProjectCount,
		Last30DaysOrgCount:     w.Attributes.Last30DaysOrgCount,
	}

	if len(w.References.Versions) > 0 {
		pkg.LatestVersion = w.References.Versions[0].Attributes.Version
		pkg.LatestVersionCreatedAt = w.References.Versions[0].Attributes.CreatedAt
	}
	pkg.Categories = toOrbCategories(w.References.Categories)

	return pkg
}

func (w orbPackageListWire) toOrbPackage() *OrbPackage {
	pkg := &OrbPackage{
		ID:          w.ID,
		Name:        w.Attributes.Name,
		NamespaceID: w.References.Namespace.ID,
		IsPrivate:   w.Attributes.IsPrivate,
		IsListed:    w.Attributes.IsListed,
	}

	if len(w.References.Versions) > 0 {
		pkg.LatestVersion = w.References.Versions[0].Attributes.Version
		pkg.LatestVersionCreatedAt = w.References.Versions[0].Attributes.CreatedAt
	}
	pkg.Categories = toOrbCategories(w.References.Categories)

	return pkg
}

func toOrbCategories(refs []orbCategoryRef) []OrbCategory {
	if len(refs) == 0 {
		return nil
	}

	out := make([]OrbCategory, 0, len(refs))
	for _, ref := range refs {
		out = append(out, OrbCategory{ID: ref.ID, Name: ref.Attributes.Name})
	}

	return out
}

func (w orbVersionWire) toOrbVersion() *OrbVersion {
	return &OrbVersion{
		ID:        w.ID,
		OrbID:     w.References.Package.ID,
		OrbName:   w.References.Package.Attributes.Name,
		Version:   w.Attributes.Version,
		CreatedAt: w.Attributes.CreatedAt,
		Source:    w.Attributes.Source,
	}
}

// --- orb packages ---

// CreateOrbPackageRequest is the input to CreateOrbPackage.
type CreateOrbPackageRequest struct {
	// Name is the fully qualified orb name, "<namespace>/<orb>". The API stores
	// and filters orbs by that qualified form even though the namespace is also
	// given by reference.
	Name        string
	NamespaceID string
	IsPrivate   bool
}

// CreateOrbPackage creates an empty orb in a namespace. An orb has no source
// until a version is published against it with PublishOrbVersion.
func (c *Client) CreateOrbPackage(ctx context.Context, req CreateOrbPackageRequest) (*OrbPackage, error) {
	var body Entity[createOrbPackageWire]
	body.Data.Attributes.Name = req.Name
	body.Data.Attributes.IsPrivate = req.IsPrivate
	body.Data.References.Namespace.ID = req.NamespaceID

	var env Entity[orbPackageWire]
	if err := c.PostV3(ctx, "/orb/packages", body, &env); err != nil {
		return nil, err
	}

	return env.Data.toOrbPackage(), nil
}

// GetOrbPackage retrieves a single orb by its UUID, with the full detail
// response: namespace name, timestamps and usage counts included.
func (c *Client) GetOrbPackage(ctx context.Context, id string) (*OrbPackage, error) {
	var env Entity[orbPackageWire]
	if err := c.GetV3(ctx, "/orb/packages/%s", &env, RouteParams(id)); err != nil {
		return nil, err
	}

	if env.Data.ID == "" {
		return nil, fmt.Errorf("orb %q: %w", id, ErrNotFound)
	}

	return env.Data.toOrbPackage(), nil
}

// ListOrbPackagesOptions scopes a ListOrbPackages call. Every field is optional;
// the zero value lists what the server considers the default set.
type ListOrbPackagesOptions struct {
	NamespaceID string
	// Name filters on the fully qualified "<namespace>/<orb>" name.
	Name string
	// Certified, when non-nil, restricts the listing to CircleCI-certified orbs
	// or excludes them. It is a pointer because leaving the filter off entirely
	// is different from asking for filter[certified]=false.
	Certified *bool
	// Visibility is OrbVisibilityPublic or OrbVisibilityPrivate. Leave it empty
	// for the server default.
	Visibility string
	// PageLimit sets page[limit]; every page is fetched regardless.
	PageLimit int
}

// ListOrbPackages lists orbs, following the v3 cursor to the last page.
func (c *Client) ListOrbPackages(ctx context.Context, opts ListOrbPackagesOptions) ([]OrbPackage, error) {
	certified := ""
	if opts.Certified != nil {
		certified = strconv.FormatBool(*opts.Certified)
	}

	return DrainV3(ctx, func(ctx context.Context, cursor string) (List[OrbPackage], error) {
		var page List[orbPackageListWire]
		err := c.GetV3(ctx, "/orb/packages", &page,
			Filter("namespace_id", opts.NamespaceID),
			Filter("name", opts.Name),
			Filter("certified", certified),
			Filter("visibility", opts.Visibility),
			PageLimit(opts.PageLimit),
			PageCursor(cursor),
		)
		if err != nil {
			return List[OrbPackage]{}, err
		}

		out := List[OrbPackage]{Meta: page.Meta, Page: page.Page}
		for _, wire := range page.Data {
			out.Data = append(out.Data, *wire.toOrbPackage())
		}

		return out, nil
	})
}

// GetOrbPackageByName resolves an orb by its fully qualified "<namespace>/<orb>"
// name.
//
// Two details of the API shape are handled here. The collection returns HTTP 200
// with an empty data array rather than a 404 when nothing matches, so an empty
// result becomes ErrNotFound. And the collection's records carry only a
// namespace id, so the match is refetched by id to return a fully populated
// package.
//
// A private orb is not returned by an unfiltered listing, so a miss is retried
// with filter[visibility]=private before giving up.
func (c *Client) GetOrbPackageByName(ctx context.Context, fullName string) (*OrbPackage, error) {
	for _, visibility := range []string{"", OrbVisibilityPrivate} {
		pkgs, err := c.ListOrbPackages(ctx, ListOrbPackagesOptions{
			Name:       fullName,
			Visibility: visibility,
		})
		if err != nil {
			return nil, err
		}

		for i := range pkgs {
			// filter[name] is not documented as an exact match, so confirm it
			// rather than trusting the first record.
			if pkgs[i].Name == fullName {
				return c.GetOrbPackage(ctx, pkgs[i].ID)
			}
		}
	}

	return nil, fmt.Errorf("orb %q: %w", fullName, ErrNotFound)
}

// SetOrbListed shows or hides an orb in the public registry listing. It is an
// in-place change and returns the updated orb.
func (c *Client) SetOrbListed(ctx context.Context, id string, listed bool) (*OrbPackage, error) {
	var env Entity[orbPackageWire]
	err := c.PostV3(ctx, "/orb/packages/%s/set-listed", orbSetListedRequest{IsListed: listed}, &env,
		RouteParams(id),
	)
	if err != nil {
		return nil, err
	}

	return env.Data.toOrbPackage(), nil
}

// AddOrbCategory adds an orb to a registry category and returns the updated orb.
func (c *Client) AddOrbCategory(ctx context.Context, id, categoryID string) (*OrbPackage, error) {
	return c.changeOrbCategory(ctx, "/orb/packages/%s/add-category", id, categoryID)
}

// RemoveOrbCategory removes an orb from a registry category and returns the
// updated orb.
func (c *Client) RemoveOrbCategory(ctx context.Context, id, categoryID string) (*OrbPackage, error) {
	return c.changeOrbCategory(ctx, "/orb/packages/%s/remove-category", id, categoryID)
}

func (c *Client) changeOrbCategory(ctx context.Context, route, id, categoryID string) (*OrbPackage, error) {
	var env Entity[orbPackageWire]
	err := c.PostV3(ctx, route, orbCategoryRequest{CategoryID: categoryID}, &env,
		RouteParams(id),
	)
	if err != nil {
		return nil, err
	}

	return env.Data.toOrbPackage(), nil
}

// ValidateOrbYAML checks orb YAML without publishing it. organizationID may be
// empty; it is needed only for YAML that references private orbs.
func (c *Client) ValidateOrbYAML(ctx context.Context, yaml, organizationID string) (*OrbValidation, error) {
	body := orbValidateRequest{YAML: yaml, OrganizationID: organizationID}

	var env Entity[orbValidationWire]
	if err := c.PostV3(ctx, "/orb/packages/validate", body, &env); err != nil {
		return nil, err
	}

	return &OrbValidation{
		Valid:      env.Data.Attributes.Valid,
		OutputYAML: env.Data.Attributes.OutputYAML,
		Errors:     env.Data.Attributes.Errors,
	}, nil
}

// --- orb versions ---

// PublishOrbVersionRequest is the input to PublishOrbVersion. It doubles as the
// attributes object of the request envelope, hence the JSON tags.
type PublishOrbVersionRequest struct {
	OrbID string `json:"orb_id"`
	YAML  string `json:"yaml"`
	// Version is a semantic version such as "1.2.3", or a "dev:<label>" string
	// for a mutable dev release.
	Version string `json:"version"`
}

// PublishOrbVersion publishes a version of an orb.
//
// Publishing a stable version is permanent: the version cannot be edited,
// republished with different source, or deleted. Republishing an existing
// version is an error. Dev versions ("dev:<label>") may be overwritten and
// expire on their own.
func (c *Client) PublishOrbVersion(ctx context.Context, req PublishOrbVersionRequest) (*OrbVersion, error) {
	var body Entity[publishOrbVersionWire]
	body.Data.Attributes = req

	var env Entity[orbVersionWire]
	if err := c.PostV3(ctx, "/orb/versions", body, &env); err != nil {
		return nil, err
	}

	return env.Data.toOrbVersion(), nil
}

// GetOrbVersion retrieves an orb version by its UUID.
//
// The response does not carry the YAML source; use GetOrbSource for that.
func (c *Client) GetOrbVersion(ctx context.Context, id string) (*OrbVersion, error) {
	var env Entity[orbVersionWire]
	if err := c.GetV3(ctx, "/orb/versions/%s", &env, RouteParams(id)); err != nil {
		return nil, err
	}

	if env.Data.ID == "" {
		return nil, fmt.Errorf("orb version %q: %w", id, ErrNotFound)
	}

	return env.Data.toOrbVersion(), nil
}

// ListOrbVersionsOptions scopes a ListOrbVersions call. OrbID is required: the
// version collection has no global form.
type ListOrbVersionsOptions struct {
	OrbID string
	// Channel is OrbChannelStable or OrbChannelDev. Leave it empty for both.
	Channel string
	// Ref filters on a version reference, either "<namespace>/<orb>@1.2.3" or a
	// bare version string, depending on what the server accepts.
	Ref string
	// PageLimit sets page[limit]; every page is fetched regardless.
	PageLimit int
}

// ListOrbVersions lists an orb's versions, following the v3 cursor to the last
// page. Versions come back most recent first.
func (c *Client) ListOrbVersions(ctx context.Context, opts ListOrbVersionsOptions) ([]OrbVersion, error) {
	return DrainV3(ctx, func(ctx context.Context, cursor string) (List[OrbVersion], error) {
		var page List[orbVersionWire]
		err := c.GetV3(ctx, "/orb/versions", &page,
			Filter("orb_id", opts.OrbID),
			Filter("channel", opts.Channel),
			Filter("ref", opts.Ref),
			PageLimit(opts.PageLimit),
			PageCursor(cursor),
		)
		if err != nil {
			return List[OrbVersion]{}, err
		}

		out := List[OrbVersion]{Meta: page.Meta, Page: page.Page}
		for _, wire := range page.Data {
			out.Data = append(out.Data, *wire.toOrbVersion())
		}

		return out, nil
	})
}

// GetOrbVersionByRef resolves one version of an orb by its version string, such
// as "1.2.3", "dev:alpha" or the "volatile" alias.
//
// filter[ref] takes a FULLY QUALIFIED reference — "namespace/orb@version" — and
// when it is present the server ignores filter[orb_id] entirely. Passing a bare
// version string resolves nothing, so the orb is looked up first to build the
// qualified ref.
func (c *Client) GetOrbVersionByRef(ctx context.Context, orbID, ref string) (*OrbVersion, error) {
	orb, err := c.GetOrbPackage(ctx, orbID)
	if err != nil {
		return nil, err
	}

	if orb.Name == "" {
		return nil, fmt.Errorf(
			"resolving orb %q: the API did not report its name, so a qualified version "+
				"reference cannot be built", orbID,
		)
	}

	// OrbPackage.Name is the qualified "namespace/orb" form, which is what
	// filter[ref] expects before the "@version" suffix.
	qualified := orb.Name + "@" + ref

	versions, err := c.ListOrbVersions(ctx, ListOrbVersionsOptions{Ref: qualified})
	if err != nil {
		return nil, err
	}

	if len(versions) == 0 {
		return nil, fmt.Errorf("orb version %q: %w", qualified, ErrNotFound)
	}

	for i := range versions {
		if versions[i].Version == ref {
			return &versions[i], nil
		}
	}

	// An alias such as "volatile" does not equal the resolved version string, so
	// fall back to whatever the server selected.
	return &versions[0], nil
}

// PromoteOrbVersionRequest is the input to PromoteOrbVersion. Supply exactly one
// of Segment and SemanticVersion.
type PromoteOrbVersionRequest struct {
	// Segment is OrbSegmentMajor, OrbSegmentMinor or OrbSegmentPatch, and bumps
	// that part of the orb's current highest version.
	Segment string `json:"segment,omitempty"`
	// SemanticVersion names the stable version to publish as, instead of
	// deriving it from Segment.
	SemanticVersion string `json:"semantic_version,omitempty"`
}

// PromoteOrbVersion publishes a dev version as a stable semantic version. The
// promoted version is a new, immutable version; the dev version is untouched.
func (c *Client) PromoteOrbVersion(ctx context.Context, id string, req PromoteOrbVersionRequest) (*OrbVersion, error) {
	var env Entity[orbVersionWire]
	if err := c.PostV3(ctx, "/orb/versions/%s/promote", req, &env, RouteParams(id)); err != nil {
		return nil, err
	}

	return env.Data.toOrbVersion(), nil
}

// GetOrbSource returns the YAML source of an orb version.
//
// The route answers text/plain, so it needs a raw decoder rather than the JSON
// decoder the verb helpers install for a non-nil destination.
func (c *Client) GetOrbSource(ctx context.Context, id string) (string, error) {
	var source string

	err := c.GetV3(ctx, "/orb/versions/%s/source", nil,
		RouteParams(id),
		httpcl.StringDecoder(&source),
	)
	if err != nil {
		return "", err
	}

	return source, nil
}

// --- orb categories ---

// ListOrbCategories lists every registry category, following the v3 cursor to
// the last page. The set is small and fixed by CircleCI.
func (c *Client) ListOrbCategories(ctx context.Context) ([]OrbCategory, error) {
	return DrainV3(ctx, func(ctx context.Context, cursor string) (List[OrbCategory], error) {
		var page List[orbCategoryWire]
		if err := c.GetV3(ctx, "/orb/categories", &page, PageCursor(cursor)); err != nil {
			return List[OrbCategory]{}, err
		}

		out := List[OrbCategory]{Meta: page.Meta, Page: page.Page}
		for _, wire := range page.Data {
			out.Data = append(out.Data, OrbCategory{ID: wire.ID, Name: wire.Attributes.Name})
		}

		return out, nil
	})
}

// GetOrbCategoryByName resolves a category by its exact name.
//
// The categories collection takes no filter, so this lists and matches locally
// rather than asking the server to filter. A name that matches nothing is
// ErrNotFound.
func (c *Client) GetOrbCategoryByName(ctx context.Context, name string) (*OrbCategory, error) {
	categories, err := c.ListOrbCategories(ctx)
	if err != nil {
		return nil, err
	}

	for i := range categories {
		if categories[i].Name == name {
			return &categories[i], nil
		}
	}

	return nil, fmt.Errorf("orb category %q: %w", name, ErrNotFound)
}
