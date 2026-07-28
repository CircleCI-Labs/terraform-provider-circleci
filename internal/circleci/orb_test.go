// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const (
	orbID   = "33333333-3333-3333-3333-333333333333"
	orbNsID = "11111111-1111-1111-1111-111111111111"
	orbVerN = "44444444-4444-4444-4444-444444444444"

	// orbDetailEntity is the by-id shape: the namespace reference carries a name,
	// and the usage counts are present.
	orbDetailEntity = `{"data":{
		"id":"33333333-3333-3333-3333-333333333333",
		"attributes":{
			"name":"acme/node","is_private":false,"is_listed":true,
			"created_at":"2026-01-02T03:04:05Z","home_url":"https://example.com",
			"last_30_days_build_count":7,"last_30_days_project_count":3,"last_30_days_org_count":2
		},
		"references":{
			"namespace":{"id":"11111111-1111-1111-1111-111111111111","attributes":{"name":"acme"}},
			"orb_versions":[{"id":"44444444-4444-4444-4444-444444444444","attributes":{"version":"1.2.3","created_at":"2026-02-02T00:00:00Z"}}],
			"orb_categories":[{"id":"55555555-5555-5555-5555-555555555555","attributes":{"name":"Build"}}]
		}
	}}`

	// orbListPage is the collection shape: references.namespace has an id only,
	// and there are no timestamps or usage counts.
	orbListPage = `{"data":[{
		"id":"33333333-3333-3333-3333-333333333333",
		"attributes":{"name":"acme/node","is_private":false,"is_listed":true},
		"references":{
			"namespace":{"id":"11111111-1111-1111-1111-111111111111"},
			"orb_versions":[{"id":"44444444-4444-4444-4444-444444444444","attributes":{"version":"1.2.3","created_at":"2026-02-02T00:00:00Z"}}],
			"orb_categories":[{"id":"55555555-5555-5555-5555-555555555555","attributes":{"name":"Build"}}]
		}
	}],"page":{"next":null,"prev":null}}`

	orbVersionEntity = `{"data":{
		"id":"44444444-4444-4444-4444-444444444444",
		"attributes":{"version":"1.2.3","created_at":"2026-02-02T00:00:00Z"},
		"references":{"orb_package":{"id":"33333333-3333-3333-3333-333333333333","attributes":{"name":"acme/node"}}}
	}}`

	orbEmptyList = `{"data":[],"page":{"next":null,"prev":null}}`
)

func TestCreateOrbPackageSendsDataEnvelope(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(orbDetailEntity))

	pkg, err := client.CreateOrbPackage(context.Background(), circleci.CreateOrbPackageRequest{
		Name:        "acme/node",
		NamespaceID: orbNsID,
		IsPrivate:   true,
	})
	if err != nil {
		t.Fatalf("CreateOrbPackage returned error: %v", err)
	}

	got := rec.last(t)
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if want := "/api/v3/orb/packages"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	// The orb routes take the v3 data/attributes/references envelope on the way
	// in, and the name is fully qualified even though the namespace is also given
	// by reference.
	want := `{"data":{"attributes":{"name":"acme/node","is_private":true},` +
		`"references":{"namespace":{"id":"11111111-1111-1111-1111-111111111111"}}}}`
	if got.body != want {
		t.Errorf("body = %s, want %s", got.body, want)
	}

	if pkg.Name != "acme/node" || pkg.Namespace != "acme" {
		t.Errorf("package = %+v, want name acme/node in namespace acme", pkg)
	}
}

func TestGetOrbPackageDetailShape(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(orbDetailEntity))

	pkg, err := client.GetOrbPackage(context.Background(), orbID)
	if err != nil {
		t.Fatalf("GetOrbPackage returned error: %v", err)
	}

	if want := "/api/v3/orb/packages/" + orbID; rec.last(t).path != want {
		t.Errorf("path = %q, want %q", rec.last(t).path, want)
	}

	switch {
	case pkg.Namespace != "acme":
		t.Errorf("Namespace = %q, want %q from the detail namespace reference", pkg.Namespace, "acme")
	case pkg.NamespaceID != orbNsID:
		t.Errorf("NamespaceID = %q, want %q", pkg.NamespaceID, orbNsID)
	case pkg.CreatedAt != "2026-01-02T03:04:05Z":
		t.Errorf("CreatedAt = %q, want it populated", pkg.CreatedAt)
	case pkg.HomeURL != "https://example.com":
		t.Errorf("HomeURL = %q, want it populated", pkg.HomeURL)
	case pkg.LatestVersion != "1.2.3":
		t.Errorf("LatestVersion = %q, want %q from the first version reference", pkg.LatestVersion, "1.2.3")
	case pkg.Last30DaysBuildCount != 7 || pkg.Last30DaysProjectCount != 3 || pkg.Last30DaysOrgCount != 2:
		t.Errorf("usage counts = %d/%d/%d, want 7/3/2",
			pkg.Last30DaysBuildCount, pkg.Last30DaysProjectCount, pkg.Last30DaysOrgCount)
	case len(pkg.Categories) != 1 || pkg.Categories[0].Name != "Build":
		t.Errorf("categories = %+v, want one named Build", pkg.Categories)
	}
}

func TestGetOrbPackageEmptyEntityIsNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newOrbClient(t, orbJSON(`{"data":{}}`))

	_, err := client.GetOrbPackage(context.Background(), orbID)
	if !circleci.IsNotFound(err) {
		t.Errorf("err = %v, want it to satisfy IsNotFound", err)
	}
}

// TestListOrbPackagesUsesTheThinnerCollectionShape is the reason orb.go keeps two
// wire types. The collection's namespace reference has no name, so a package that
// came from a listing must report an empty Namespace rather than a wrong one, and
// callers that need the name have to refetch by id.
func TestListOrbPackagesUsesTheThinnerCollectionShape(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(orbListPage))

	certified := true
	pkgs, err := client.ListOrbPackages(context.Background(), circleci.ListOrbPackagesOptions{
		NamespaceID: orbNsID,
		Certified:   &certified,
		Visibility:  circleci.OrbVisibilityPrivate,
		PageLimit:   20,
	})
	if err != nil {
		t.Fatalf("ListOrbPackages returned error: %v", err)
	}

	if len(pkgs) != 1 {
		t.Fatalf("packages = %+v, want one", pkgs)
	}

	if pkgs[0].NamespaceID != orbNsID {
		t.Errorf("NamespaceID = %q, want %q", pkgs[0].NamespaceID, orbNsID)
	}
	if pkgs[0].Namespace != "" {
		t.Errorf("Namespace = %q, want it empty: the collection does not return the namespace name", pkgs[0].Namespace)
	}
	if pkgs[0].CreatedAt != "" || pkgs[0].Last30DaysBuildCount != 0 {
		t.Errorf("package = %+v, want no created_at or usage counts from a listing", pkgs[0])
	}
	// What the collection does return is still decoded.
	if pkgs[0].LatestVersion != "1.2.3" || len(pkgs[0].Categories) != 1 {
		t.Errorf("package = %+v, want the version and category references decoded", pkgs[0])
	}

	query := rec.last(t).query
	for _, want := range []string{
		"filter%5Bnamespace_id%5D=" + orbNsID,
		"filter%5Bcertified%5D=true",
		"filter%5Bvisibility%5D=private",
		"page%5Blimit%5D=20",
	} {
		if !strings.Contains(query, want) {
			t.Errorf("query = %q, want it to contain %q", query, want)
		}
	}
	if strings.Contains(query, "page%5Bcursor%5D") {
		t.Errorf("query = %q, want no page[cursor] on the first page", query)
	}
}

func TestListOrbPackagesOmitsCertifiedWhenUnset(t *testing.T) {
	t.Parallel()

	// Leaving the filter off is a different request from asking for
	// filter[certified]=false, which is why the option is a pointer.
	client, rec := newOrbClient(t, orbJSON(orbEmptyList))

	if _, err := client.ListOrbPackages(context.Background(), circleci.ListOrbPackagesOptions{}); err != nil {
		t.Fatalf("ListOrbPackages returned error: %v", err)
	}

	if got := rec.last(t).query; got != "" {
		t.Errorf("query = %q, want it empty", got)
	}
}

func TestListOrbPackagesDrainsPages(t *testing.T) {
	t.Parallel()

	page1 := `{"data":[{"id":"a","attributes":{"name":"acme/one"},"references":{"namespace":{"id":"` + orbNsID + `"}}}],` +
		`"page":{"next":"cursor-2","prev":null}}`
	page2 := `{"data":[{"id":"b","attributes":{"name":"acme/two"},"references":{"namespace":{"id":"` + orbNsID + `"}}}],` +
		`"page":{"next":null,"prev":null}}`

	client, rec := newOrbClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page[cursor]") == "cursor-2" {
			_, _ = w.Write([]byte(page2))

			return
		}
		_, _ = w.Write([]byte(page1))
	})

	pkgs, err := client.ListOrbPackages(context.Background(), circleci.ListOrbPackagesOptions{})
	if err != nil {
		t.Fatalf("ListOrbPackages returned error: %v", err)
	}

	if len(pkgs) != 2 || pkgs[0].Name != "acme/one" || pkgs[1].Name != "acme/two" {
		t.Errorf("packages = %+v, want both pages accumulated in order", pkgs)
	}

	all := rec.all()
	if len(all) != 2 {
		t.Fatalf("requests = %d, want 2 (one per page)", len(all))
	}
	if all[0].query != "" {
		t.Errorf("first request query = %q, want no cursor", all[0].query)
	}
	if want := "page%5Bcursor%5D=cursor-2"; all[1].query != want {
		t.Errorf("second request query = %q, want %q", all[1].query, want)
	}
}

// TestGetOrbPackageByNameEmptyListIsNotFound covers the v3 collection behaviour
// that a lookup by filter has to translate: nothing matching is a 200 with an
// empty data array, not a 404.
func TestGetOrbPackageByNameEmptyListIsNotFound(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(orbEmptyList))

	_, err := client.GetOrbPackageByName(context.Background(), "acme/missing")
	if !circleci.IsNotFound(err) {
		t.Fatalf("err = %v, want it to satisfy IsNotFound", err)
	}
	if !strings.Contains(err.Error(), "acme/missing") {
		t.Errorf("err = %v, want it to name the orb", err)
	}

	// A private orb is invisible to an unfiltered listing, so the miss is retried
	// asking for private orbs before it is reported as not found.
	all := rec.all()
	if len(all) != 2 {
		t.Fatalf("requests = %d, want 2 (public then private)", len(all))
	}
	if strings.Contains(all[0].query, "visibility") {
		t.Errorf("first query = %q, want no visibility filter", all[0].query)
	}
	if want := "filter%5Bvisibility%5D=private"; !strings.Contains(all[1].query, want) {
		t.Errorf("second query = %q, want it to contain %q", all[1].query, want)
	}
}

func TestGetOrbPackageByNameRefetchesDetail(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/orb/packages") {
			_, _ = w.Write([]byte(orbListPage))

			return
		}
		_, _ = w.Write([]byte(orbDetailEntity))
	})

	pkg, err := client.GetOrbPackageByName(context.Background(), "acme/node")
	if err != nil {
		t.Fatalf("GetOrbPackageByName returned error: %v", err)
	}

	// The namespace name is only available from the detail route, so a by-name
	// lookup has to follow the collection with a by-id fetch.
	if pkg.Namespace != "acme" {
		t.Errorf("Namespace = %q, want %q from the refetched detail", pkg.Namespace, "acme")
	}

	all := rec.all()
	if len(all) != 2 {
		t.Fatalf("requests = %d, want 2 (list then detail)", len(all))
	}
	if want := "/api/v3/orb/packages/" + orbID; all[1].path != want {
		t.Errorf("second request path = %q, want %q", all[1].path, want)
	}
}

func TestGetOrbPackageByNameRejectsAPartialMatch(t *testing.T) {
	t.Parallel()

	// filter[name] is not documented as an exact match, so a record with a
	// different name must not be accepted.
	page := `{"data":[{"id":"x","attributes":{"name":"acme/node-extra"},` +
		`"references":{"namespace":{"id":"` + orbNsID + `"}}}],"page":{"next":null}}`

	client, _ := newOrbClient(t, orbJSON(page))

	_, err := client.GetOrbPackageByName(context.Background(), "acme/node")
	if !circleci.IsNotFound(err) {
		t.Errorf("err = %v, want it to satisfy IsNotFound", err)
	}
}

func TestSetOrbListed(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(orbDetailEntity))

	if _, err := client.SetOrbListed(context.Background(), orbID, false); err != nil {
		t.Fatalf("SetOrbListed returned error: %v", err)
	}

	got := rec.last(t)
	if want := "/api/v3/orb/packages/" + orbID + "/set-listed"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if want := `{"is_listed":false}`; got.body != want {
		t.Errorf("body = %s, want %s", got.body, want)
	}
}

func TestOrbCategoryMembership(t *testing.T) {
	t.Parallel()

	categoryID := "55555555-5555-5555-5555-555555555555"

	tests := []struct {
		name     string
		call     func(context.Context, *circleci.Client) error
		wantPath string
	}{
		{
			name: "add",
			call: func(ctx context.Context, c *circleci.Client) error {
				_, err := c.AddOrbCategory(ctx, orbID, categoryID)

				return err
			},
			wantPath: "/api/v3/orb/packages/" + orbID + "/add-category",
		},
		{
			name: "remove",
			call: func(ctx context.Context, c *circleci.Client) error {
				_, err := c.RemoveOrbCategory(ctx, orbID, categoryID)

				return err
			},
			wantPath: "/api/v3/orb/packages/" + orbID + "/remove-category",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, rec := newOrbClient(t, orbJSON(orbDetailEntity))

			if err := tt.call(context.Background(), client); err != nil {
				t.Fatalf("call returned error: %v", err)
			}

			got := rec.last(t)
			if got.path != tt.wantPath {
				t.Errorf("path = %q, want %q", got.path, tt.wantPath)
			}
			if want := `{"category_id":"` + categoryID + `"}`; got.body != want {
				t.Errorf("body = %s, want %s", got.body, want)
			}
		})
	}
}

func TestValidateOrbYAML(t *testing.T) {
	t.Parallel()

	body := `{"data":{"attributes":{"is_valid":false,"output_yaml":"version: 2.1\n","errors":["boom"]}}}`
	client, rec := newOrbClient(t, orbJSON(body))

	result, err := client.ValidateOrbYAML(context.Background(), "version: 2.1", "22222222-2222-2222-2222-222222222222")
	if err != nil {
		t.Fatalf("ValidateOrbYAML returned error: %v", err)
	}

	got := rec.last(t)
	if want := "/api/v3/orb/packages/validate"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if want := `{"yaml":"version: 2.1","org_id":"22222222-2222-2222-2222-222222222222"}`; got.body != want {
		t.Errorf("body = %s, want %s", got.body, want)
	}

	if result.Valid {
		t.Error("Valid = true, want false")
	}
	if len(result.Errors) != 1 || result.Errors[0] != "boom" {
		t.Errorf("Errors = %v, want [boom]", result.Errors)
	}
}

func TestValidateOrbYAMLOmitsEmptyOrganization(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(`{"data":{"attributes":{"is_valid":true}}}`))

	if _, err := client.ValidateOrbYAML(context.Background(), "version: 2.1", ""); err != nil {
		t.Fatalf("ValidateOrbYAML returned error: %v", err)
	}

	if want := `{"yaml":"version: 2.1"}`; rec.last(t).body != want {
		t.Errorf("body = %s, want %s", rec.last(t).body, want)
	}
}

func TestPublishOrbVersionSendsDataEnvelope(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(orbVersionEntity))

	version, err := client.PublishOrbVersion(context.Background(), circleci.PublishOrbVersionRequest{
		OrbID:   orbID,
		YAML:    "version: 2.1",
		Version: "1.2.3",
	})
	if err != nil {
		t.Fatalf("PublishOrbVersion returned error: %v", err)
	}

	got := rec.last(t)
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if want := "/api/v3/orb/versions"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	want := `{"data":{"attributes":{"orb_id":"` + orbID + `","yaml":"version: 2.1","version":"1.2.3"}}}`
	if got.body != want {
		t.Errorf("body = %s, want %s", got.body, want)
	}

	if version.OrbName != "acme/node" || version.Version != "1.2.3" {
		t.Errorf("version = %+v, want acme/node@1.2.3", version)
	}
}

func TestGetOrbVersion(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(orbVersionEntity))

	version, err := client.GetOrbVersion(context.Background(), orbVerN)
	if err != nil {
		t.Fatalf("GetOrbVersion returned error: %v", err)
	}

	if want := "/api/v3/orb/versions/" + orbVerN; rec.last(t).path != want {
		t.Errorf("path = %q, want %q", rec.last(t).path, want)
	}
	if version.OrbID != orbID {
		t.Errorf("OrbID = %q, want %q from the orb_package reference", version.OrbID, orbID)
	}
}

func TestGetOrbVersionEmptyEntityIsNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newOrbClient(t, orbJSON(`{"data":{}}`))

	_, err := client.GetOrbVersion(context.Background(), orbVerN)
	if !circleci.IsNotFound(err) {
		t.Errorf("err = %v, want it to satisfy IsNotFound", err)
	}
}

func TestGetOrbVersionByRefScopesToTheOrb(t *testing.T) {
	t.Parallel()

	page := `{"data":[{"id":"` + orbVerN + `","attributes":{"version":"1.2.3"},` +
		`"references":{"orb_package":{"id":"` + orbID + `","attributes":{"name":"acme/node"}}}}],` +
		`"page":{"next":null}}`

	client, rec := newOrbClient(t, orbRoutes(map[string]string{
		"/api/v3/orb/versions": page,
		"/api/v3/orb/packages": orbPackageBody(orbID, "acme", "acme/node"),
	}))

	version, err := client.GetOrbVersionByRef(context.Background(), orbID, "1.2.3")
	if err != nil {
		t.Fatalf("GetOrbVersionByRef returned error: %v", err)
	}

	// filter[ref] takes a fully-qualified "namespace/orb@version" reference, and
	// the server ignores filter[orb_id] whenever it is present. Sending a bare
	// version string resolved nothing against production.
	query := rec.last(t).query
	if want := "filter%5Bref%5D=acme%2Fnode%401.2.3"; !strings.Contains(query, want) {
		t.Errorf("query = %q, want it to contain %q", query, want)
	}
	if strings.Contains(query, "filter%5Borb_id%5D") {
		t.Errorf("query = %q, want no filter[orb_id]: the server ignores it when filter[ref] is set", query)
	}

	if version.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", version.Version, "1.2.3")
	}
}

func TestGetOrbVersionByRefEmptyListIsNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newOrbClient(t, orbRoutes(map[string]string{
		"/api/v3/orb/versions": orbEmptyList,
		"/api/v3/orb/packages": orbPackageBody(orbID, "acme", "acme/node"),
	}))

	_, err := client.GetOrbVersionByRef(context.Background(), orbID, "9.9.9")
	if !circleci.IsNotFound(err) {
		t.Errorf("err = %v, want it to satisfy IsNotFound", err)
	}
}

func TestGetOrbVersionByRefAcceptsAnAlias(t *testing.T) {
	t.Parallel()

	// "volatile" resolves to whatever the newest version is, so the resolved
	// version string does not equal the requested ref.
	page := `{"data":[{"id":"` + orbVerN + `","attributes":{"version":"1.2.3"},` +
		`"references":{"orb_package":{"id":"` + orbID + `","attributes":{"name":"acme/node"}}}}],` +
		`"page":{"next":null}}`

	client, _ := newOrbClient(t, orbRoutes(map[string]string{
		"/api/v3/orb/versions": page,
		"/api/v3/orb/packages": orbPackageBody(orbID, "acme", "acme/node"),
	}))

	version, err := client.GetOrbVersionByRef(context.Background(), orbID, "volatile")
	if err != nil {
		t.Fatalf("GetOrbVersionByRef returned error: %v", err)
	}
	if version.Version != "1.2.3" {
		t.Errorf("Version = %q, want the resolved %q", version.Version, "1.2.3")
	}
}

func TestListOrbVersionsDrainsPages(t *testing.T) {
	t.Parallel()

	page1 := `{"data":[{"id":"v1","attributes":{"version":"2.0.0"},"references":{"orb_package":{"id":"` + orbID + `","attributes":{"name":"acme/node"}}}}],` +
		`"page":{"next":"cursor-2"}}`
	page2 := `{"data":[{"id":"v2","attributes":{"version":"1.0.0"},"references":{"orb_package":{"id":"` + orbID + `","attributes":{"name":"acme/node"}}}}],` +
		`"page":{"next":null}}`

	client, rec := newOrbClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page[cursor]") == "cursor-2" {
			_, _ = w.Write([]byte(page2))

			return
		}
		_, _ = w.Write([]byte(page1))
	})

	versions, err := client.ListOrbVersions(context.Background(), circleci.ListOrbVersionsOptions{
		OrbID:   orbID,
		Channel: circleci.OrbChannelStable,
	})
	if err != nil {
		t.Fatalf("ListOrbVersions returned error: %v", err)
	}

	if len(versions) != 2 || versions[0].Version != "2.0.0" || versions[1].Version != "1.0.0" {
		t.Errorf("versions = %+v, want both pages in order", versions)
	}
	if want := "filter%5Bchannel%5D=stable"; !strings.Contains(rec.all()[0].query, want) {
		t.Errorf("query = %q, want it to contain %q", rec.all()[0].query, want)
	}
}

func TestPromoteOrbVersionOmitsTheUnusedField(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(orbVersionEntity))

	_, err := client.PromoteOrbVersion(context.Background(), orbVerN, circleci.PromoteOrbVersionRequest{
		Segment: circleci.OrbSegmentMinor,
	})
	if err != nil {
		t.Fatalf("PromoteOrbVersion returned error: %v", err)
	}

	got := rec.last(t)
	if want := "/api/v3/orb/versions/" + orbVerN + "/promote"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	// The API rejects sending both segment and semantic_version, so the unset one
	// must be omitted rather than sent empty.
	if want := `{"segment":"minor"}`; got.body != want {
		t.Errorf("body = %s, want %s", got.body, want)
	}
}

func TestGetOrbSourceReadsPlainText(t *testing.T) {
	t.Parallel()

	const yaml = "version: 2.1\ndescription: an orb\n"

	client, rec := newOrbClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// The route answers text/plain, which the JSON decoder would reject.
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(yaml))
	})

	source, err := client.GetOrbSource(context.Background(), orbVerN)
	if err != nil {
		t.Fatalf("GetOrbSource returned error: %v", err)
	}

	if want := "/api/v3/orb/versions/" + orbVerN + "/source"; rec.last(t).path != want {
		t.Errorf("path = %q, want %q", rec.last(t).path, want)
	}
	if source != yaml {
		t.Errorf("source = %q, want %q", source, yaml)
	}
}

func TestGetOrbSource404IsNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newOrbClient(t, orbStatus(http.StatusNotFound, `{"error":{"title":"Not Found"}}`))

	_, err := client.GetOrbSource(context.Background(), orbVerN)
	if !circleci.IsNotFound(err) {
		t.Errorf("err = %v, want it to satisfy IsNotFound", err)
	}
}

func TestListOrbCategoriesDrainsPages(t *testing.T) {
	t.Parallel()

	page1 := `{"data":[{"id":"c1","attributes":{"name":"Build"}}],"page":{"next":"cursor-2"}}`
	page2 := `{"data":[{"id":"c2","attributes":{"name":"Notifications"}}],"page":{"next":null}}`

	client, rec := newOrbClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page[cursor]") == "cursor-2" {
			_, _ = w.Write([]byte(page2))

			return
		}
		_, _ = w.Write([]byte(page1))
	})

	categories, err := client.ListOrbCategories(context.Background())
	if err != nil {
		t.Fatalf("ListOrbCategories returned error: %v", err)
	}

	if len(categories) != 2 || categories[0].Name != "Build" || categories[1].Name != "Notifications" {
		t.Errorf("categories = %+v, want both pages", categories)
	}
	if want := "/api/v3/orb/categories"; rec.all()[0].path != want {
		t.Errorf("path = %q, want %q", rec.all()[0].path, want)
	}
}

func TestGetOrbCategoryByName(t *testing.T) {
	t.Parallel()

	body := `{"data":[{"id":"c1","attributes":{"name":"Build"}},{"id":"c2","attributes":{"name":"Notifications"}}],"page":{"next":null}}`
	client, _ := newOrbClient(t, orbJSON(body))

	category, err := client.GetOrbCategoryByName(context.Background(), "Notifications")
	if err != nil {
		t.Fatalf("GetOrbCategoryByName returned error: %v", err)
	}
	if category.ID != "c2" {
		t.Errorf("ID = %q, want %q", category.ID, "c2")
	}
}

func TestGetOrbCategoryByNameNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newOrbClient(t, orbJSON(`{"data":[{"id":"c1","attributes":{"name":"Build"}}],"page":{"next":null}}`))

	_, err := client.GetOrbCategoryByName(context.Background(), "Nope")
	if !circleci.IsNotFound(err) {
		t.Errorf("err = %v, want it to satisfy IsNotFound", err)
	}
}
