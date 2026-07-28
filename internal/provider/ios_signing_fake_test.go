// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// This file stands up an in-memory fake of the v3 signing/certificates and
// signing/configs routes, so the iOS signing resources can be driven through
// real Terraform plans and applies without a CircleCI account.
//
// It keeps real state, like orb_fake_test.go's fake, because the interesting
// behaviour here is stateful: a second plan after apply must be empty despite
// the fake never echoing cert_blob, cert_password or a profile's blob back --
// exactly like production, which cannot return any of them either.

// iosSigningFakeCert is a stored certificate. Blob and Password are kept only
// so the fake can answer consistently; a real API would not even do that much
// -- see internal/circleci/signing_certificate.go.
type iosSigningFakeCert struct {
	ID        string
	OrgID     string
	FileName  string
	Blob      string
	Password  string
	CertType  string
	CreatedAt string
	ExpiresAt string
}

// iosSigningFakeProfile is one provisioning profile on a fake signing config.
type iosSigningFakeProfile struct {
	FileName string
	Blob     string
}

// iosSigningFakeConfig is a stored signing configuration.
type iosSigningFakeConfig struct {
	ID       string
	OrgID    string
	Name     string
	CertID   string
	Profiles []iosSigningFakeProfile
}

// iosSigningFakeAPI is a fake of the CircleCI v3 signing certificate and
// signing config API.
type iosSigningFakeAPI struct {
	server *httptest.Server

	mu      sync.Mutex
	nextID  int
	certs   map[string]*iosSigningFakeCert
	configs map[string]*iosSigningFakeConfig
}

// newIOSSigningFakeAPI starts a fake v3 API and stops it when the test ends.
func newIOSSigningFakeAPI(t *testing.T) *iosSigningFakeAPI {
	t.Helper()

	api := &iosSigningFakeAPI{
		certs:   map[string]*iosSigningFakeCert{},
		configs: map[string]*iosSigningFakeConfig{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/signing/certificates", api.listCertificates)
	mux.HandleFunc("POST /api/v3/signing/certificates", api.createCertificate)
	mux.HandleFunc("GET /api/v3/signing/certificates/{id}", api.getCertificate)
	mux.HandleFunc("DELETE /api/v3/signing/certificates/{id}", api.deleteCertificate)
	mux.HandleFunc("GET /api/v3/signing/configs", api.listConfigs)
	mux.HandleFunc("POST /api/v3/signing/configs", api.createConfig)
	mux.HandleFunc("DELETE /api/v3/signing/configs/{id}", api.deleteConfig)

	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(api.server.Close)

	return api
}

// URL is the fake's origin, for the provider's host attribute.
func (a *iosSigningFakeAPI) URL() string { return a.server.URL }

func (a *iosSigningFakeAPI) mintID() string {
	a.nextID++

	return fmt.Sprintf("%08d-1111-2222-3333-444444444444", a.nextID)
}

func iosSigningFakeWriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func iosSigningFakeWriteError(w http.ResponseWriter, status int, title string) {
	iosSigningFakeWriteJSON(w, status, map[string]any{
		"error": map[string]any{"id": "trace", "title": title},
	})
}

func iosSigningFakeDecode(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer func() { _, _ = io.Copy(io.Discard, r.Body) }()

	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		iosSigningFakeWriteJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"type": "validation_error", "id": "trace", "title": "Bad Request", "detail": err.Error()},
		})

		return false
	}

	return true
}

// --- certificates ---

// iosSigningFakeCertType derives cert_type from a marker in the DECODED blob
// ("DEV" anywhere in it means development), so tests can exercise both values
// without the fake needing a real X.509 parser.
//
// Production derives this from the certificate's Subject Common Name, i.e. from the
// decoded bytes — so the fake decodes too. Matching against the raw base64 instead
// would only work for blobs whose encoded form happens to contain the marker, which
// is not a property any real caller controls.
func iosSigningFakeCertType(blob string) string {
	decoded, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		// An undecodable blob is not a development certificate; the create handler
		// rejects it separately.
		return "distribution"
	}

	if strings.Contains(string(decoded), "DEV") {
		return "development"
	}

	return "distribution"
}

func iosSigningFakeFingerprint(blob string) string {
	sum := sha256.Sum256([]byte(blob))

	return fmt.Sprintf("%X", sum[:8])
}

func (a *iosSigningFakeAPI) certEntity(c *iosSigningFakeCert) map[string]any {
	return map[string]any{
		"id": c.ID,
		"attributes": map[string]any{
			"file_name":   c.FileName,
			"cert_type":   c.CertType,
			"fingerprint": iosSigningFakeFingerprint(c.Blob),
			"created_at":  c.CreatedAt,
			"expires_at":  emptyToNilJSON(c.ExpiresAt),
		},
		"references": map[string]any{
			"org": map[string]any{"id": c.OrgID},
		},
	}
}

func (a *iosSigningFakeAPI) certItem(c *iosSigningFakeCert) map[string]any {
	// The list endpoint omits "references" entirely in production; see
	// signing_certificate_test.go's TestListSigningCertificatesOmitsReferences.
	return map[string]any{
		"id": c.ID,
		"attributes": map[string]any{
			"file_name":   c.FileName,
			"cert_type":   c.CertType,
			"fingerprint": iosSigningFakeFingerprint(c.Blob),
			"created_at":  c.CreatedAt,
			"expires_at":  emptyToNilJSON(c.ExpiresAt),
		},
	}
}

func emptyToNilJSON(s string) any {
	if s == "" {
		return nil
	}

	return s
}

func (a *iosSigningFakeAPI) createCertificate(w http.ResponseWriter, r *http.Request) {
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
	if !iosSigningFakeDecode(w, r, &body) {
		return
	}

	if body.Data.References.Org.ID == "" {
		iosSigningFakeWriteJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{
				"type": "validation_error", "id": "trace", "title": "Validation Error",
				"detail": "Field 'org.id' is required.",
				"source": map[string]any{"pointer": "/data/references/org/id"},
			},
		})

		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	c := &iosSigningFakeCert{
		ID:        a.mintID(),
		OrgID:     body.Data.References.Org.ID,
		FileName:  body.Data.Attributes.FileName,
		Blob:      body.Data.Attributes.CertBlob,
		Password:  body.Data.Attributes.CertPassword,
		CertType:  iosSigningFakeCertType(body.Data.Attributes.CertBlob),
		CreatedAt: iosSigningFakeCreatedAt,
		ExpiresAt: iosSigningFakeExpiresAt,
	}
	a.certs[c.ID] = c

	w.Header().Set("Location", "/api/v3/signing/certificates/"+c.ID)
	// Production returns only {"data":{"id"}} on create; see
	// signingCertificateCreated in internal/circleci/signing_certificate.go.
	iosSigningFakeWriteJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"id": c.ID}})
}

func (a *iosSigningFakeAPI) getCertificate(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	c, ok := a.certs[r.PathValue("id")]
	if !ok {
		iosSigningFakeWriteError(w, http.StatusNotFound, "Not Found")

		return
	}

	iosSigningFakeWriteJSON(w, http.StatusOK, map[string]any{"data": a.certEntity(c)})
}

func (a *iosSigningFakeAPI) listCertificates(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	orgID := r.URL.Query().Get("filter[org_id]")

	data := make([]map[string]any, 0, len(a.certs))
	for _, c := range a.certs {
		if orgID != "" && c.OrgID != orgID {
			continue
		}
		data = append(data, a.certItem(c))
	}

	// A single-page v3 collection omits "page" entirely rather than sending null
	// cursors.
	iosSigningFakeWriteJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (a *iosSigningFakeAPI) deleteCertificate(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	id := r.PathValue("id")
	if _, ok := a.certs[id]; !ok {
		iosSigningFakeWriteError(w, http.StatusNotFound, "Not Found")

		return
	}

	delete(a.certs, id)
	w.WriteHeader(http.StatusNoContent)
}

// --- signing configs ---

func (a *iosSigningFakeAPI) configItem(cfg *iosSigningFakeConfig) map[string]any {
	profiles := make([]map[string]any, 0, len(cfg.Profiles))
	for _, p := range cfg.Profiles {
		profiles = append(profiles, map[string]any{"file_name": p.FileName})
	}

	cert := a.certs[cfg.CertID]
	certFileName, certType := "", ""
	if cert != nil {
		certFileName, certType = cert.FileName, cert.CertType
	}

	return map[string]any{
		"id": cfg.ID,
		"attributes": map[string]any{
			"name":                  cfg.Name,
			"provisioning_profiles": profiles,
		},
		"references": map[string]any{
			"signing_certificate": map[string]any{
				"id": cfg.CertID,
				// Populated despite the published spec documenting this object as
				// empty -- see signingCertRefAttrs in circleci/the API and the
				// comment on signingConfigListBody in signing_config_test.go.
				"attributes": map[string]any{
					"file_name": certFileName,
					"cert_type": certType,
				},
			},
		},
	}
}

func (a *iosSigningFakeAPI) createConfig(w http.ResponseWriter, r *http.Request) {
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
	if !iosSigningFakeDecode(w, r, &body) {
		return
	}

	if body.Data.References.Org.ID == "" || body.Data.References.Certificate.ID == "" {
		iosSigningFakeWriteJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{
				"type": "validation_error", "id": "trace", "title": "Validation Error",
				"detail": "org.id and signing_certificate.id are required.",
			},
		})

		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.certs[body.Data.References.Certificate.ID]; !ok {
		iosSigningFakeWriteError(w, http.StatusBadRequest, "Bad Request")

		return
	}

	profiles := make([]iosSigningFakeProfile, len(body.Data.Attributes.ProvisioningProfiles))
	for i, p := range body.Data.Attributes.ProvisioningProfiles {
		profiles[i] = iosSigningFakeProfile{FileName: p.FileName, Blob: p.Blob}
	}

	cfg := &iosSigningFakeConfig{
		ID:       a.mintID(),
		OrgID:    body.Data.References.Org.ID,
		Name:     body.Data.Attributes.Name,
		CertID:   body.Data.References.Certificate.ID,
		Profiles: profiles,
	}
	a.configs[cfg.ID] = cfg

	// There is no GET .../signing/configs/{id} route; the Location header points
	// at the list, exactly like post_signing_config_v3.go in circleci/the API.
	w.Header().Set("Location", "/api/v3/signing/configs")
	iosSigningFakeWriteJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"id": cfg.ID}})
}

func (a *iosSigningFakeAPI) listConfigs(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	orgID := r.URL.Query().Get("filter[org_id]")

	data := make([]map[string]any, 0, len(a.configs))
	for _, cfg := range a.configs {
		if orgID != "" && cfg.OrgID != orgID {
			continue
		}
		data = append(data, a.configItem(cfg))
	}

	iosSigningFakeWriteJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (a *iosSigningFakeAPI) deleteConfig(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	id := r.PathValue("id")
	if _, ok := a.configs[id]; !ok {
		iosSigningFakeWriteError(w, http.StatusNotFound, "Not Found")

		return
	}

	delete(a.configs, id)
	w.WriteHeader(http.StatusNoContent)
}

// --- provider wiring ---

const (
	iosSigningFakeCreatedAt = "2026-01-02T03:04:05.000Z"
	iosSigningFakeExpiresAt = "2027-01-02T03:04:05.000Z"
)

// iosSigningProviderConfig renders a provider block pointing at the fake.
func iosSigningProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
`, host)
}

// iosSigningServerProviderConfig renders a provider block that claims to be
// CircleCI Server, for the tests that assert these types refuse to run there.
func iosSigningServerProviderConfig() string {
	return `
provider "circleci" {
  host       = "https://circleci.example.com"
  key        = "fake"
  deployment = "server"
}
`
}
