// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const (
	signingCertOrgID = "22222222-2222-2222-2222-222222222222"
	signingCertID    = "11111111-1111-1111-1111-111111111111"
)

// signingCertificateEntityBody is the GET .../signing/certificates/{id} shape:
// id + attributes + a references.org the list endpoint omits.
const signingCertificateEntityBody = `{
  "data": {
    "id": "11111111-1111-1111-1111-111111111111",
    "attributes": {
      "file_name": "dist.p12",
      "cert_type": "distribution",
      "fingerprint": "AA:BB:CC:DD",
      "created_at": "2024-01-02T03:04:05.000Z",
      "expires_at": "2025-01-02T03:04:05.000Z"
    },
    "references": {
      "org": {"id": "22222222-2222-2222-2222-222222222222"}
    }
  }
}`

// TestCreateSigningCertificateSendsEnvelopeAndFollowsUpWithGet is the
// load-bearing test for the create path.
//
// POST .../signing/certificates answers 201 with only {"data":{"id"}} in
// production -- cert_blob and cert_password are never echoed back, on this
// call or any other. CreateSigningCertificate must not mistake the create
// response for the full representation; it has to make the documented
// follow-up GET.
func TestCreateSigningCertificateSendsEnvelopeAndFollowsUpWithGet(t *testing.T) {
	t.Parallel()

	var (
		postBody    string
		postSeen    bool
		getPathSeen string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.Method {
		case http.MethodPost:
			postSeen = true
			raw, _ := io.ReadAll(r.Body)
			postBody = string(raw)

			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"` + signingCertID + `"}}`))
		case http.MethodGet:
			getPathSeen = r.URL.Path
			_, _ = w.Write([]byte(signingCertificateEntityBody))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	t.Cleanup(srv.Close)

	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	cert, err := client.CreateSigningCertificate(context.Background(), circleci.CreateSigningCertificateRequest{
		OrganizationID: signingCertOrgID,
		FileName:       "dist.p12",
		CertBlob:       "cDEy...base64...",
		CertPassword:   "s3cret",
	})
	if err != nil {
		t.Fatalf("CreateSigningCertificate returned error: %v", err)
	}

	if !postSeen {
		t.Fatal("no POST request was made")
	}
	if getPathSeen != "/api/v3/signing/certificates/"+signingCertID {
		t.Errorf("follow-up GET path = %q, want /api/v3/signing/certificates/%s", getPathSeen, signingCertID)
	}

	var body struct {
		Data struct {
			Attributes struct {
				FileName     string `json:"file_name"`
				CertBlob     string `json:"cert_blob"`
				CertPassword string `json:"cert_password"`
			} `json:"attributes"`
			References struct {
				Org struct {
					ID string `json:"id"`
				} `json:"org"`
			} `json:"references"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(postBody), &body); err != nil {
		t.Fatalf("POST body %q is not JSON: %v", postBody, err)
	}
	if body.Data.Attributes.FileName != "dist.p12" {
		t.Errorf("file_name = %q, want %q", body.Data.Attributes.FileName, "dist.p12")
	}
	if body.Data.Attributes.CertBlob != "cDEy...base64..." {
		t.Errorf("cert_blob = %q, want the configured value", body.Data.Attributes.CertBlob)
	}
	if body.Data.Attributes.CertPassword != "s3cret" {
		t.Errorf("cert_password = %q, want the configured value", body.Data.Attributes.CertPassword)
	}
	if body.Data.References.Org.ID != signingCertOrgID {
		t.Errorf("references.org.id = %q, want %q", body.Data.References.Org.ID, signingCertOrgID)
	}

	// The hydrated certificate must carry what the follow-up GET reported, and
	// must have no way to carry back cert_blob or cert_password: there is no
	// field for either on circleci.SigningCertificate.
	if cert.ID != signingCertID {
		t.Errorf("ID = %q, want %q", cert.ID, signingCertID)
	}
	if cert.OrganizationID != signingCertOrgID {
		t.Errorf("OrganizationID = %q, want %q", cert.OrganizationID, signingCertOrgID)
	}
	if cert.CertType != circleci.SigningCertificateTypeDistribution {
		t.Errorf("CertType = %q, want %q", cert.CertType, circleci.SigningCertificateTypeDistribution)
	}
	if cert.Fingerprint != "AA:BB:CC:DD" {
		t.Errorf("Fingerprint = %q, want %q", cert.Fingerprint, "AA:BB:CC:DD")
	}
	if cert.CreatedAt == nil || *cert.CreatedAt != "2024-01-02T03:04:05.000Z" {
		t.Errorf("CreatedAt = %v, want 2024-01-02T03:04:05.000Z", cert.CreatedAt)
	}
	if cert.ExpiresAt == nil || *cert.ExpiresAt != "2025-01-02T03:04:05.000Z" {
		t.Errorf("ExpiresAt = %v, want 2025-01-02T03:04:05.000Z", cert.ExpiresAt)
	}
}

func TestGetSigningCertificateDecodesEntityWithOrgReference(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(signingCertificateEntityBody))

	cert, err := client.GetSigningCertificate(context.Background(), signingCertID)
	if err != nil {
		t.Fatalf("GetSigningCertificate returned error: %v", err)
	}

	got := rec.last(t)
	if want := "/api/v3/signing/certificates/" + signingCertID; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	if cert.FileName != "dist.p12" {
		t.Errorf("FileName = %q, want %q", cert.FileName, "dist.p12")
	}
	if cert.OrganizationID != signingCertOrgID {
		t.Errorf("OrganizationID = %q, want %q", cert.OrganizationID, signingCertOrgID)
	}
}

func TestGetSigningCertificateNotFound(t *testing.T) {
	t.Parallel()

	// v3 404s answer {"error":{"id","title"}} with no "detail". Detail must
	// still render something sensible from the title alone.
	client, _ := newOrbClient(t, orbStatus(http.StatusNotFound,
		`{"error":{"id":"trace-1","title":"Not Found"}}`))

	_, err := client.GetSigningCertificate(context.Background(), "missing")
	if !circleci.IsNotFound(err) {
		t.Errorf("err = %v, want it to satisfy IsNotFound", err)
	}
	if want := "Not Found\nerror id: trace-1"; circleci.Detail(err) != want {
		t.Errorf("Detail(err) = %q, want %q", circleci.Detail(err), want)
	}
}

// TestListSigningCertificatesOmitsReferences pins the production shape where
// the list endpoint's items carry no "references" object at all -- the org is
// already known from filter[org_id], so the org reference used by the
// single-entity GET has no counterpart on the list route's items. A client
// that expected references on list items would silently read a zero value;
// this fixture is written in the production shape so a regression in either
// direction is caught.
func TestListSigningCertificatesOmitsReferences(t *testing.T) {
	t.Parallel()

	const body = `{
	  "data": [
	    {
	      "id": "11111111-1111-1111-1111-111111111111",
	      "attributes": {
	        "file_name": "dist.p12",
	        "cert_type": "distribution",
	        "fingerprint": "AA:BB",
	        "created_at": "2024-01-02T03:04:05.000Z",
	        "expires_at": null
	      }
	    }
	  ]
	}`

	client, rec := newOrbClient(t, orbJSON(body))

	certs, err := client.ListSigningCertificates(context.Background(), signingCertOrgID)
	if err != nil {
		t.Fatalf("ListSigningCertificates returned error: %v", err)
	}

	got := rec.last(t)
	if want := "/api/v3/signing/certificates"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if want := "filter%5Borg_id%5D=" + signingCertOrgID; got.query != want {
		t.Errorf("query = %q, want %q", got.query, want)
	}

	if len(certs) != 1 {
		t.Fatalf("got %d certificates, want 1", len(certs))
	}
	// OrganizationID is filled in from the filter, since the item itself carries
	// no org reference.
	if certs[0].OrganizationID != signingCertOrgID {
		t.Errorf("OrganizationID = %q, want %q (filled in from the filter)", certs[0].OrganizationID, signingCertOrgID)
	}
	if certs[0].FileName != "dist.p12" {
		t.Errorf("FileName = %q, want %q", certs[0].FileName, "dist.p12")
	}
	if certs[0].ExpiresAt != nil {
		t.Errorf("ExpiresAt = %v, want nil for a certificate with no known expiry", certs[0].ExpiresAt)
	}
}

// TestListSigningCertificatesPaginates covers the v3 cursor, and that a single
// page (the common case) omits "page" entirely rather than sending
// {"next":null,"prev":null}.
func TestListSigningCertificatesPaginates(t *testing.T) {
	t.Parallel()

	// Both pages share a path and are told apart only by page[cursor], which
	// orbRoutes (keyed on path prefix) cannot express, so this builds the
	// two-page sequence directly.
	var page int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		page++
		if r.URL.Query().Get("page[cursor]") == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"cert-1","attributes":{"file_name":"a.p12","cert_type":"distribution","fingerprint":"AA","created_at":null,"expires_at":null}}],"page":{"next":"cursor-2"}}`))

			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"cert-2","attributes":{"file_name":"b.p12","cert_type":"development","fingerprint":"BB","created_at":null,"expires_at":null}}]}`))
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	certs, err := c.ListSigningCertificates(context.Background(), signingCertOrgID)
	if err != nil {
		t.Fatalf("ListSigningCertificates returned error: %v", err)
	}

	if len(certs) != 2 {
		t.Fatalf("got %d certificates, want 2 (both pages)", len(certs))
	}
	if certs[0].ID != "cert-1" || certs[1].ID != "cert-2" {
		t.Errorf("ids = %q, %q, want cert-1, cert-2 in page order", certs[0].ID, certs[1].ID)
	}
	if page != 2 {
		t.Errorf("server saw %d requests, want 2", page)
	}
}

func TestDeleteSigningCertificateIsNoContent(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbStatus(http.StatusNoContent, ``))

	if err := client.DeleteSigningCertificate(context.Background(), signingCertID); err != nil {
		t.Fatalf("DeleteSigningCertificate returned error: %v", err)
	}

	got := rec.last(t)
	if got.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", got.method)
	}
	if want := "/api/v3/signing/certificates/" + signingCertID; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
}

// TestSigningCertificateTypes pins the cert_type value set.
//
// This exists because the set was wrong: two constants were declared and the
// documentation said "distribution or development", where the service classifies
// an uploaded certificate into one of seven types by matching its X.509 Subject
// Common Name against a fixed list of Apple prefixes. Five values were simply
// missing, and three of the five change whether a signing configuration may
// carry provisioning profiles at all -- so an incomplete set was not a cosmetic
// documentation gap, it hid a combination the provider cannot express.
//
// Only a source read can settle this: no response from an account that happens to
// hold two iOS certificates would show the other five.
func TestSigningCertificateTypes(t *testing.T) {
	t.Parallel()

	want := []string{
		"distribution",
		"development",
		"developer-id-application",
		"developer-id-installer",
		"mac-development",
		"mac-app-distribution",
		"mac-installer-distribution",
	}

	if !slices.Equal(circleci.SigningCertificateTypes, want) {
		t.Errorf("SigningCertificateTypes = %v, want %v", circleci.SigningCertificateTypes, want)
	}

	// Every constant appears in the slice, so neither can drift from the other.
	for _, certType := range []string{
		circleci.SigningCertificateTypeDistribution,
		circleci.SigningCertificateTypeDevelopment,
		circleci.SigningCertificateTypeDeveloperIDApplication,
		circleci.SigningCertificateTypeDeveloperIDInstaller,
		circleci.SigningCertificateTypeMacDevelopment,
		circleci.SigningCertificateTypeMacAppDistribution,
		circleci.SigningCertificateTypeMacInstallerDistribution,
	} {
		if !slices.Contains(circleci.SigningCertificateTypes, certType) {
			t.Errorf("SigningCertificateTypes is missing the constant %q", certType)
		}
	}
}

// TestSigningCertificateTypeRequiresProvisioningProfile pins the two-sided rule
// the create-signing-config route enforces from the referenced certificate's
// type.
//
// The client used to document the opposite of half of it -- "an empty list is
// accepted" -- which is true for exactly the three types below that report false
// and false for the four that report true, where an empty list is a 400. Getting
// it backwards mattered: circleci_ios_signing_config requires at least one
// profile, so the three false cases cannot be used with that resource at all.
func TestSigningCertificateTypeRequiresProvisioningProfile(t *testing.T) {
	t.Parallel()

	want := map[string]bool{
		circleci.SigningCertificateTypeDistribution:             true,
		circleci.SigningCertificateTypeDevelopment:              true,
		circleci.SigningCertificateTypeMacDevelopment:           true,
		circleci.SigningCertificateTypeMacAppDistribution:       true,
		circleci.SigningCertificateTypeDeveloperIDApplication:   false,
		circleci.SigningCertificateTypeDeveloperIDInstaller:     false,
		circleci.SigningCertificateTypeMacInstallerDistribution: false,
	}

	for certType, requires := range want {
		if got := circleci.SigningCertificateTypeRequiresProvisioningProfile(certType); got != requires {
			t.Errorf("SigningCertificateTypeRequiresProvisioningProfile(%q) = %t, want %t", certType, got, requires)
		}
	}

	// Every declared type is covered, so a new one cannot be added without
	// deciding which side of the rule it falls on.
	if len(want) != len(circleci.SigningCertificateTypes) {
		t.Errorf("covered %d types, want all %d", len(want), len(circleci.SigningCertificateTypes))
	}

	// An unrecognized type follows the service's own default rather than being
	// treated as exempt, so a value this package has not caught up with fails
	// closed towards requiring a profile.
	if !circleci.SigningCertificateTypeRequiresProvisioningProfile("something-new") {
		t.Error("SigningCertificateTypeRequiresProvisioningProfile(unknown) = false, want true")
	}
}
