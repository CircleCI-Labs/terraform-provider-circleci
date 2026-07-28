// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const namespaceEntity = `{"data":{"id":"11111111-1111-1111-1111-111111111111","attributes":{"name":"acme"}}}`

func TestGetNamespaceByName(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(namespaceEntity))

	ns, err := client.GetNamespace(context.Background(), "acme")
	if err != nil {
		t.Fatalf("GetNamespace returned error: %v", err)
	}

	// The by-name lookup is the collection route scoped by filter[name], not a
	// nested path.
	got := rec.last(t)
	if got.method != http.MethodGet {
		t.Errorf("method = %q, want GET", got.method)
	}
	if want := "/api/v3/namespaces"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if want := "filter%5Bname%5D=acme"; got.query != want {
		t.Errorf("query = %q, want %q", got.query, want)
	}

	if ns.ID != "11111111-1111-1111-1111-111111111111" || ns.Name != "acme" {
		t.Errorf("namespace = %+v, want id 1111... and name acme", ns)
	}
}

func TestGetNamespaceEmptyEntityIsNotFound(t *testing.T) {
	t.Parallel()

	// A 200 with an empty envelope must not read as a namespace with a blank name.
	client, _ := newOrbClient(t, orbJSON(`{"data":{}}`))

	_, err := client.GetNamespace(context.Background(), "nope")
	if !circleci.IsNotFound(err) {
		t.Errorf("err = %v, want it to satisfy IsNotFound", err)
	}
}

func TestGetNamespaceHTTP404IsNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newOrbClient(t, orbStatus(http.StatusNotFound,
		`{"error":{"title":"Not Found","detail":"namespace not found"}}`))

	_, err := client.GetNamespace(context.Background(), "nope")
	if !circleci.IsNotFound(err) {
		t.Errorf("err = %v, want it to satisfy IsNotFound", err)
	}
	if want := "Not Found: namespace not found"; circleci.Detail(err) != want {
		t.Errorf("Detail = %q, want %q", circleci.Detail(err), want)
	}
}

func TestGetNamespaceByID(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(namespaceEntity))

	if _, err := client.GetNamespaceByID(context.Background(), "11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatalf("GetNamespaceByID returned error: %v", err)
	}

	got := rec.last(t)
	if want := "/api/v3/namespaces/11111111-1111-1111-1111-111111111111"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if got.query != "" {
		t.Errorf("query = %q, want it empty", got.query)
	}
}

func TestCreateNamespaceSendsFlatBody(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(namespaceEntity))

	ns, err := client.CreateNamespace(context.Background(), circleci.CreateNamespaceRequest{
		Name:           "acme",
		OrganizationID: "22222222-2222-2222-2222-222222222222",
	})
	if err != nil {
		t.Fatalf("CreateNamespace returned error: %v", err)
	}

	got := rec.last(t)
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if want := "/api/v3/namespaces"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	// The namespace routes take a flat body, unlike the orb routes which take the
	// data/attributes envelope.
	want := `{"name":"acme","org_id":"22222222-2222-2222-2222-222222222222"}`
	if got.body != want {
		t.Errorf("body = %s, want %s", got.body, want)
	}

	if ns.Name != "acme" {
		t.Errorf("name = %q, want %q", ns.Name, "acme")
	}
}

func TestRenameNamespace(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t,
		orbJSON(`{"data":{"id":"11111111-1111-1111-1111-111111111111","attributes":{"name":"acme-two"}}}`))

	ns, err := client.RenameNamespace(context.Background(), "11111111-1111-1111-1111-111111111111", "acme-two")
	if err != nil {
		t.Fatalf("RenameNamespace returned error: %v", err)
	}

	got := rec.last(t)
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if want := "/api/v3/namespaces/11111111-1111-1111-1111-111111111111/rename"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if want := `{"name":"acme-two"}`; got.body != want {
		t.Errorf("body = %s, want %s", got.body, want)
	}

	// Renaming keeps the id: it is an update, not a replacement.
	if ns.ID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("id = %q, want it unchanged", ns.ID)
	}
	if ns.Name != "acme-two" {
		t.Errorf("name = %q, want %q", ns.Name, "acme-two")
	}
}

func TestDeleteNamespace(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbStatus(http.StatusNoContent, ``))

	if err := client.DeleteNamespace(context.Background(), "11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatalf("DeleteNamespace returned error: %v", err)
	}

	got := rec.last(t)
	if got.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", got.method)
	}
	if want := "/api/v3/namespaces/11111111-1111-1111-1111-111111111111"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
}
