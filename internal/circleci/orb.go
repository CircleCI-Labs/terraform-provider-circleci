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
	// OrbVisibilityPrivate restricts a namespace listing to orbs private to the
	// organization. It replaces the default public-only view rather than adding to
	// it, and it is read only when a namespace is also given — see
	// ListOrbPackagesOptions. A lookup by name needs neither: it finds a private
	// orb without being asked to.
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
// so Namespace, CreatedAt and HomeURL are empty on packages that came from a
// listing. The usage counts are the exception: [NET] confirms the collection
// reports them too, so a package from a listing has real Last30Days* values,
// not zeroes.
type OrbPackage struct {
	ID          string
	Name        string
	Namespace   string
	NamespaceID string
	IsPrivate   bool
	IsListed    bool
	CreatedAt   string
	HomeURL     string
	// LatestVersion is the first entry of the version references. The registry
	// orders them most-recent first, but that order is not part of the route's
	// contract, so this is a convention rather than a guarantee. It is empty for
	// an orb with no versions yet.
	//
	// How many version references come embedded depends on which route
	// produced this package, and [NET, this investigation, confirmed against a
	// certified public orb with 63 published versions] three different routes
	// give three different counts for the exact same package:
	//   - an ordinary /orb/packages listing (filter[namespace_id], filter
	//     [certified], or no filter) embeds exactly one — the latest — which is
	//     why LatestVersion is all such a listing can offer;
	//   - the /orb/packages/{id} detail route embeds up to 50, and silently
	//     truncates a package with more (measured 50 of 63);
	//   - filter[name] on /orb/packages — the by-name lookup — embeds ALL of
	//     them, uncapped (measured 63 of 63), because it is not a paginated
	//     listing at all; see ListOrbPackagesOptions.Name.
	// A caller that needs the true, complete version history regardless of
	// count should use ListOrbVersions(OrbID: ...), which pages until
	// exhausted, rather than reading references.orb_versions off of any
	// OrbPackage.
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
	ID    string
	OrbID string
	// OrbName is the qualified "<namespace>/<orb>" name of the owning orb.
	//
	// No orb version route reports it. Every one of them carries the owning orb
	// as references.orb_package, but that reference's attributes object is
	// omitted whenever the name is absent, and upstream the name is never
	// present: the version record simply has no orb name on it. So the reference
	// is an id and nothing more, and the name has to be resolved from the orb
	// package by a second request — which is what resolveOrbName does.
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
// because the collection is genuinely thinner in some respects:
// references.namespace carries only an id with no attributes.name, and
// created_at and home_url are absent. Decoding the collection into the detail
// type would silently yield a package whose Namespace is "" and whose
// CreatedAt/HomeURL are empty, which reads as real data rather than as
// missing data.
//
// The usage counts are NOT one of the thinner fields — this is the one place
// this comment used to be wrong. [NET], confirmed against a live account on
// both filter[namespace_id] and filter[name]: every /orb/packages collection
// response carries last_30_days_build_count/project_count/org_count in
// attributes, at the same value the detail route reports. A type that omits
// them (as this one used to) silently reports real traffic as zero on every
// read that goes through a listing — circleci_orbs and circleci_orb_categories
// in particular. See TestListOrbPackagesUsesTheThinnerCollectionShape.
type orbPackageListWire struct {
	ID         string `json:"id"`
	Attributes struct {
		Name                   string `json:"name"`
		IsPrivate              bool   `json:"is_private"`
		IsListed               bool   `json:"is_listed"`
		Last30DaysBuildCount   int64  `json:"last_30_days_build_count"`
		Last30DaysProjectCount int64  `json:"last_30_days_project_count"`
		Last30DaysOrgCount     int64  `json:"last_30_days_org_count"`
	} `json:"attributes"`
	References struct {
		Namespace  thinNamespaceRef `json:"namespace"`
		Versions   []orbVersionRef  `json:"orb_versions"`
		Categories []orbCategoryRef `json:"orb_categories"`
	} `json:"references"`
}

// orbVersionWire is the shape of every orb version response.
//
// attributes.source is present only on the by-id route asked for it explicitly,
// and references.orb_package.attributes is never present at all — see
// OrbVersion.OrbName. Both are decoded anyway so that the client keeps working
// unchanged if the API starts sending them.
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
//
// Attributes.Name is the one field on this route that is NOT symmetric with
// what every response reports: it must be sent bare, with no namespace
// prefix, even though every response — including this route's own — reports
// it namespace-qualified. See CreateOrbPackage's doc comment.
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
		ID:                     w.ID,
		Name:                   w.Attributes.Name,
		NamespaceID:            w.References.Namespace.ID,
		IsPrivate:              w.Attributes.IsPrivate,
		IsListed:               w.Attributes.IsListed,
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
	// Name is the BARE orb name, with no namespace prefix — "node", not
	// "acme/node". See CreateOrbPackage's doc comment: sending the qualified
	// form here, which is what every other orb route accepts and reports, is
	// rejected.
	Name        string
	NamespaceID string
	IsPrivate   bool
}

// CreateOrbPackage creates an empty orb in a namespace. An orb has no source
// until a version is published against it with PublishOrbVersion.
//
// req.Name must be the BARE orb name, with no namespace prefix.
//
// [NET, this investigation, against two separate real accounts]: sending the
// fully qualified "<namespace>/<orb>" form here — which is what GetOrbPackage,
// ListOrbPackages and every other orb route both accept and report, and what
// every earlier version of this client sent — is rejected on EVERY name tried:
// a brand-new name, a name that already belongs to an existing orb in the same
// namespace, a single character, all in a namespace created fresh for this
// investigation with every orb-related organization setting explicitly turned
// on. Every one of those answers the same
// `400 {"error":{"title":"Cannot create an Orb named '<name>': this name is
// invalid. See the documentation..."}}`, including the literal duplicate —
// which is the tell: a real duplicate-name check answers "an Orb with that
// name already exists," not "this name is invalid," so this was never about
// quota, entitlement, or the name's content. It is that this one route, alone
// among the orb routes, wants the bare form and treats the "/" in a qualified
// name as what makes the name "invalid." Sending the bare name (verified
// against the same accounts) succeeds with `201`, and the response's own
// attributes.name still comes back namespace-qualified — the server adds the
// prefix that this route uniquely does not want handed to it on the way in.
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
//
// The filters are not independent, and the server's precedence is not obvious
// from the route:
//
//   - Name short-circuits everything. When filter[name] is present the handler
//     resolves that one name and returns immediately, so Certified, Visibility
//     and NamespaceID are all ignored, and the answer is never paginated —
//     [NET, freshly reconfirmed this investigation against a certified public
//     orb with 63 published versions] it also embeds every one of those 63 in
//     references.orb_versions, uncapped, where the by-id detail route for the
//     identical package truncates the same list at 50. See OrbPackage.LatestVersion.
//   - Visibility is only consulted alongside NamespaceID, and the two settings
//     are mutually exclusive rather than additive: a namespace listing is
//     public-only unless Visibility is OrbVisibilityPrivate, which makes it
//     private-only. There is no way to ask for both in one request, and a
//     Visibility with no NamespaceID does nothing at all.
//   - Certified is only consulted when NamespaceID is empty.
//   - [NET] A NamespaceID listing with no Visibility filter also silently
//     excludes any orb whose is_listed is false, even though such an orb is
//     public (not private) and reads back fine by id or by Name. An orb hidden
//     with SetOrbListed(id, false) therefore disappears from ListOrbPackages
//     and from circleci_orbs entirely, not just from CircleCI's own public
//     registry search.
type ListOrbPackagesOptions struct {
	NamespaceID string
	// Name filters on the fully qualified "<namespace>/<orb>" name. It is an
	// exact lookup that overrides every other field here.
	//
	// This is the read side; do not confuse it with CreateOrbPackageRequest.Name,
	// which — despite living in the same file, for the same resource — must be
	// the BARE name with no namespace prefix. Every read route reports and
	// filters on the qualified form; only the create route rejects it.
	Name string
	// Certified, when non-nil, restricts the listing to CircleCI-certified orbs.
	//
	// It is a pointer for symmetry with the schema attribute that feeds it, not
	// because the server distinguishes the two: filter[certified]=false is read
	// as "no certification filter", exactly like omitting it. Only true has an
	// effect, and only on a listing with no NamespaceID.
	Certified *bool
	// Visibility is OrbVisibilityPublic or OrbVisibilityPrivate, and is only
	// honoured together with NamespaceID. See the type comment.
	Visibility string
	// PageLimit sets page[limit]; every page is fetched regardless. The server
	// caps it at 1000 and rejects anything larger with a 400.
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
// Exactly one listing request is made, with no visibility filter. filter[name]
// is served by a dedicated by-name lookup that runs before the handler reads
// filter[visibility] at all, and that lookup is not restricted to public orbs —
// so a private orb is found on the first attempt, and retrying with
// filter[visibility]=private would only repeat the identical request.
func (c *Client) GetOrbPackageByName(ctx context.Context, fullName string) (*OrbPackage, error) {
	pkgs, err := c.ListOrbPackages(ctx, ListOrbPackagesOptions{Name: fullName})
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

	version := env.Data.toOrbVersion()
	c.resolveOrbName(ctx, version)

	return version, nil
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

	version := env.Data.toOrbVersion()
	c.resolveOrbName(ctx, version)

	return version, nil
}

// orbNilUUID is what the orb version routes render for an absent orb reference.
// references.orb_package.id is a UUID field with no omit rule on it, so a version
// record that carries no orb id serializes as the zero UUID rather than as an
// absent key. It is not an id anything can be fetched by.
const orbNilUUID = "00000000-0000-0000-0000-000000000000"

// resolveOrbName fills version.OrbName, which no orb version route reports. See
// OrbVersion.OrbName for why it is always missing.
//
// The lookup is best effort and deliberately swallows its error. It runs after
// the caller's own request has already succeeded — including after a publish,
// which is irreversible — so turning a failure to decorate the result into a
// failure of the whole call would lose a version that was just created. An empty
// OrbName is the lesser harm, and it is what callers already had to tolerate.
func (c *Client) resolveOrbName(ctx context.Context, version *OrbVersion) {
	if version == nil || version.OrbName != "" {
		return
	}
	if version.OrbID == "" || version.OrbID == orbNilUUID {
		return
	}

	if pkg, err := c.GetOrbPackage(ctx, version.OrbID); err == nil {
		version.OrbName = pkg.Name
	}
}

// ListOrbVersionsOptions scopes a ListOrbVersions call.
//
// One of OrbID and Ref is required. Ref takes precedence and is served by a
// separate resolver, so when it is set OrbID and Channel are both ignored; when
// it is not set, OrbID is mandatory and a missing or non-UUID value is a 400.
type ListOrbVersionsOptions struct {
	OrbID string
	// Channel is OrbChannelStable or OrbChannelDev.
	//
	// Leaving it empty is the same as OrbChannelStable, not "both": the handler
	// routes to the dev listing only for the exact value "dev", and to the
	// stable listing for everything else. There is no request that returns both
	// channels.
	Channel string
	// Ref filters on a fully qualified version reference,
	// "<namespace>/<orb>@1.2.3". A bare version string resolves nothing: the
	// server hands the whole value to a by-reference resolver that needs the orb
	// name to find the orb at all. See GetOrbVersionByRef.
	Ref string
	// PageLimit sets page[limit]; every page is fetched regardless.
	PageLimit int
}

// ListOrbVersions lists an orb's versions, following the v3 cursor to the last
// page.
//
// The order is the upstream registry's and is not part of the public contract.
// Callers here treat the first entry as the most recent one, which matches what
// the registry has always returned, but nothing in the route guarantees it.
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

	// The orb was fetched above to build the reference, so the name the version
	// routes never report is already in hand: no second lookup is needed here.
	for i := range versions {
		versions[i].OrbName = orb.Name
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

// PromoteOrbVersionRequest is the input to PromoteOrbVersion.
//
// Supply exactly one of Segment and SemanticVersion. Supplying neither is a 400,
// and because both fields are omitempty the zero value serializes to {}, which
// the server rejects as an empty body rather than with the more helpful message
// about the two fields. Supplying both is not an error: Segment wins and
// SemanticVersion is discarded.
type PromoteOrbVersionRequest struct {
	// Segment is OrbSegmentMajor, OrbSegmentMinor or OrbSegmentPatch, and bumps
	// that part of the orb's current highest version.
	Segment string `json:"segment,omitempty"`
	// SemanticVersion names the stable version to publish as, instead of
	// deriving it from Segment.
	SemanticVersion string `json:"semantic_version,omitempty"`
}

// PromoteOrbVersion publishes a dev version as a stable semantic version.
//
// [NET, this investigation]: the response reuses the SAME version id that was
// passed in — it does not mint a new one — with attributes.version changed to
// the promoted semantic version, and GetOrbVersion(id) afterward agrees with
// that. That much matches "the promoted version is immutable" once it settles.
// What this investigation could not reconcile: for several seconds afterward
// (and possibly longer; this was not chased further), ListOrbVersions with
// Channel: OrbChannelDev on the same orb still listed that same id under its
// original dev:<label> version string, disagreeing with the by-id GET taken at
// the same moment. Treat the dev-channel listing as potentially stale
// immediately after a promotion, and confirm a promotion by reading the
// returned id back with GetOrbVersion rather than by re-listing either
// channel. Separately, and independently of that: the label itself is not
// consumed by promotion — dev:<label> can be published again right after, and
// answers 201 with a brand new id.
func (c *Client) PromoteOrbVersion(ctx context.Context, id string, req PromoteOrbVersionRequest) (*OrbVersion, error) {
	var env Entity[orbVersionWire]
	if err := c.PostV3(ctx, "/orb/versions/%s/promote", req, &env, RouteParams(id)); err != nil {
		return nil, err
	}

	version := env.Data.toOrbVersion()
	c.resolveOrbName(ctx, version)

	return version, nil
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
//
// The collection takes no filters at all — not even filter[name] — which is why
// GetOrbCategoryByName has to list and match locally.
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
