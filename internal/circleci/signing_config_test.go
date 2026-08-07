// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const (
	signingConfigOrgID  = "22222222-2222-2222-2222-222222222222"
	signingConfigCertID = "33333333-3333-3333-3333-333333333333"
	signingConfigID     = "44444444-4444-4444-4444-444444444444"
)

// signingConfigListBody is the GET .../signing/configs shape for one config.
//
// The certificate reference's attributes carry file_name and cert_type. The
// published OpenAPI spec documents that object as empty ("attributes: {}"),
// but production actually populates both fields. This fixture follows
// production, per the project's rule to derive shapes from production source
// rather than the spec.
const signingConfigListBody = `{
  "data": [
    {
      "id": "44444444-4444-4444-4444-444444444444",
      "attributes": {
        "name": "release-config",
        "provisioning_profiles": [
          {"file_name": "release.mobileprovision"}
        ]
      },
      "references": {
        "signing_certificate": {
          "id": "33333333-3333-3333-3333-333333333333",
          "attributes": {
            "file_name": "dist.p12",
            "cert_type": "distribution"
          }
        }
      }
    }
  ]
}`

// TestCreateSigningConfigSendsEnvelopeAndHydratesFromList is the load-bearing
// test for the create path.
//
// POST .../signing/configs answers 201 with only {"data":{"id"}}, and unlike
// a certificate there is no GET .../signing/configs/{id} route at all -- the
// API only supports GET (collection), POST and DELETE. CreateSigningConfig
// must hydrate the result by listing and matching on id instead of a direct
// GET.
func TestCreateSigningConfigSendsEnvelopeAndHydratesFromList(t *testing.T) {
	t.Parallel()

	var (
		postBody      string
		postSeen      bool
		listQuerySeen string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.Method {
		case http.MethodPost:
			postSeen = true
			raw, _ := io.ReadAll(r.Body)
			postBody = string(raw)

			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"` + signingConfigID + `"}}`))
		case http.MethodGet:
			listQuerySeen = r.URL.RawQuery
			_, _ = w.Write([]byte(signingConfigListBody))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	t.Cleanup(srv.Close)

	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	cfg, err := client.CreateSigningConfig(context.Background(), circleci.CreateSigningConfigRequest{
		OrganizationID: signingConfigOrgID,
		CertificateID:  signingConfigCertID,
		Name:           "release-config",
		ProvisioningProfiles: []circleci.CreateSigningProvisioningProfile{
			{FileName: "release.mobileprovision", Blob: "cGxpc3Q...base64..."},
		},
	})
	if err != nil {
		t.Fatalf("CreateSigningConfig returned error: %v", err)
	}

	if !postSeen {
		t.Fatal("no POST request was made")
	}
	if want := "filter%5Borg_id%5D=" + signingConfigOrgID; listQuerySeen != want {
		t.Errorf("follow-up list query = %q, want %q", listQuerySeen, want)
	}

	var body struct {
		Data struct {
			Attributes struct {
				Name                 string `json:"name"`
				ProvisioningProfiles []struct {
					Blob     string `json:"blob"`
					FileName string `json:"file_name"`
				} `json:"provisioning_profiles"`
			} `json:"attributes"`
			References struct {
				Org struct {
					ID string `json:"id"`
				} `json:"org"`
				Certificate struct {
					ID string `json:"id"`
				} `json:"signing_certificate"`
			} `json:"references"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(postBody), &body); err != nil {
		t.Fatalf("POST body %q is not JSON: %v", postBody, err)
	}
	if body.Data.Attributes.Name != "release-config" {
		t.Errorf("name = %q, want %q", body.Data.Attributes.Name, "release-config")
	}
	if body.Data.References.Org.ID != signingConfigOrgID {
		t.Errorf("references.org.id = %q, want %q", body.Data.References.Org.ID, signingConfigOrgID)
	}
	if body.Data.References.Certificate.ID != signingConfigCertID {
		t.Errorf("references.signing_certificate.id = %q, want %q", body.Data.References.Certificate.ID, signingConfigCertID)
	}
	if len(body.Data.Attributes.ProvisioningProfiles) != 1 ||
		body.Data.Attributes.ProvisioningProfiles[0].Blob != "cGxpc3Q...base64..." ||
		body.Data.Attributes.ProvisioningProfiles[0].FileName != "release.mobileprovision" {
		t.Errorf("provisioning_profiles = %+v, want one profile with the configured blob and file_name",
			body.Data.Attributes.ProvisioningProfiles)
	}

	// The hydrated config carries what the follow-up list reported, and has no
	// field capable of carrying the profile blob back.
	if cfg.ID != signingConfigID {
		t.Errorf("ID = %q, want %q", cfg.ID, signingConfigID)
	}
	if cfg.CertificateID != signingConfigCertID {
		t.Errorf("CertificateID = %q, want %q", cfg.CertificateID, signingConfigCertID)
	}
	if cfg.CertificateFileName != "dist.p12" {
		t.Errorf("CertificateFileName = %q, want %q (inlined by the handler despite the spec)", cfg.CertificateFileName, "dist.p12")
	}
	if cfg.CertificateType != circleci.SigningCertificateTypeDistribution {
		t.Errorf("CertificateType = %q, want %q", cfg.CertificateType, circleci.SigningCertificateTypeDistribution)
	}
	if len(cfg.ProvisioningProfiles) != 1 || cfg.ProvisioningProfiles[0].FileName != "release.mobileprovision" {
		t.Errorf("ProvisioningProfiles = %+v, want one profile named release.mobileprovision", cfg.ProvisioningProfiles)
	}
}

func TestGetSigningConfigFindsMatchInList(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(signingConfigListBody))

	cfg, err := client.GetSigningConfig(context.Background(), signingConfigOrgID, signingConfigID)
	if err != nil {
		t.Fatalf("GetSigningConfig returned error: %v", err)
	}

	got := rec.last(t)
	if want := "/api/v3/signing/configs"; got.path != want {
		t.Errorf("path = %q, want %q (there is no single-entity route)", got.path, want)
	}

	if cfg.Name != "release-config" {
		t.Errorf("Name = %q, want %q", cfg.Name, "release-config")
	}
}

// TestGetSigningConfigNotFoundWhenAbsentFromList covers the id genuinely not
// being present in the organization's list -- the v3 style of "not found" for
// a filtered collection, as opposed to an HTTP 404.
func TestGetSigningConfigNotFoundWhenAbsentFromList(t *testing.T) {
	t.Parallel()

	client, _ := newOrbClient(t, orbJSON(`{"data":[]}`))

	_, err := client.GetSigningConfig(context.Background(), signingConfigOrgID, "does-not-exist")
	if !circleci.IsNotFound(err) {
		t.Errorf("err = %v, want it to satisfy IsNotFound", err)
	}
}

// TestGetSigningConfigDistinguishesTransport404FromEmptyList pins the
// distinction a caller needs to tell a deleted configuration from an
// organization the token has lost access to.
//
// There is no single-config route, so GetSigningConfig always works by
// listing and matching on id. circleci.IsNotFound says yes to both a
// transport 404 from that list call and the ErrNotFound sentinel this package
// wraps around a successful-but-empty match -- but only the sentinel means
// the configuration itself is gone. A caller that checks the broader
// IsNotFound, rather than errors.Is(err, circleci.ErrNotFound), cannot tell
// the two apart, and that is exactly the defect this pins: v3 answers 404 for
// filter[org_id] both when the organization does not exist and when the token
// can no longer manage it.
func TestGetSigningConfigDistinguishesTransport404FromEmptyList(t *testing.T) {
	t.Parallel()

	client, _ := newOrbClient(t, orbStatus(http.StatusNotFound, `{"error":{"title":"Org not found"}}`))

	_, err := client.GetSigningConfig(context.Background(), signingConfigOrgID, signingConfigID)
	if err == nil {
		t.Fatal("GetSigningConfig returned no error for a 404 from the list, want one")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true: a transport 404 still satisfies it", err)
	}
	if errors.Is(err, circleci.ErrNotFound) {
		t.Errorf("errors.Is(%v, ErrNotFound) = true, want false: a 404 from the list call itself "+
			"is not the same as the configuration being absent from a successful list", err)
	}
}

func TestListSigningConfigsSendsOrgFilter(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbJSON(signingConfigListBody))

	configs, err := client.ListSigningConfigs(context.Background(), signingConfigOrgID)
	if err != nil {
		t.Fatalf("ListSigningConfigs returned error: %v", err)
	}

	got := rec.last(t)
	if want := "filter%5Borg_id%5D=" + signingConfigOrgID; got.query != want {
		t.Errorf("query = %q, want %q", got.query, want)
	}

	if len(configs) != 1 {
		t.Fatalf("got %d configs, want 1", len(configs))
	}
	if configs[0].OrganizationID != signingConfigOrgID {
		t.Errorf("OrganizationID = %q, want %q (filled in from the filter)", configs[0].OrganizationID, signingConfigOrgID)
	}
}

func TestDeleteSigningConfigIsNoContent(t *testing.T) {
	t.Parallel()

	client, rec := newOrbClient(t, orbStatus(http.StatusNoContent, ``))

	if err := client.DeleteSigningConfig(context.Background(), signingConfigID); err != nil {
		t.Fatalf("DeleteSigningConfig returned error: %v", err)
	}

	got := rec.last(t)
	if got.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", got.method)
	}
	if want := "/api/v3/signing/configs/" + signingConfigID; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
}
