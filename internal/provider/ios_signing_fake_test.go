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

	"terraform-provider-circleci/internal/circleci"
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
	// listConfigsStatus, when non-zero, makes GET .../signing/configs answer with
	// that status instead of the collection. It stands in for v3's own way of
	// saying "no" to a filtered collection: a token that has lost access to
	// filter[org_id]'s organization gets an HTTP error there, which is a very
	// different thing from the organization simply having no configurations.
	listConfigsStatus int
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

// iosSigningFakeDecode decodes a v3 request body into dst.
//
// It does NOT reject an unrecognised key, and that is deliberate, not an
// oversight: [NET] against the real API, POST .../signing/certificates and
// POST .../signing/configs both answer 201 for a body carrying an extra key
// at every level tried -- top-level, inside "attributes", and inside one
// entry of "provisioning_profiles" -- with the value under that key simply
// never landing anywhere. An earlier version of this fake rejected such a
// body with 400 "Unexpected field '<name>'.", on the belief that these two
// routes shared a strict binder with the usage export route in
// usage_export_ephemeral_resource_test.go (which really does reject unknown
// fields -- that binder claim is correct there, just not generalisable to
// every v3 route). That belief was never checked against a real account and
// was wrong for these two routes specifically: TestIOSSigningFakeRejectsUnexpectedFields
// used to assert a 400 no real request has ever produced, which is exactly
// backwards from what a fake protecting this package should do -- see
// TestIOSSigningFakeAcceptsUnknownFieldsLikeProduction below for the
// corrected coverage, and the MEASURED FACTS note in this project's own
// review process: unknown keys are silently dropped rather than rejected,
// and a 200 (or 201) does not mean the API read what you sent, so any field
// that matters has to be proven landed by reading it back rather than by the
// create call's status code.
func iosSigningFakeDecode(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer func() { _, _ = io.Copy(io.Discard, r.Body) }()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		iosSigningFakeWriteJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"type": "validation_error", "id": "trace", "title": "Bad Request", "detail": err.Error()},
		})

		return false
	}

	if err := json.Unmarshal(body, dst); err != nil {
		iosSigningFakeWriteJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"type": "validation_error", "id": "trace", "title": "Bad Request", "detail": err.Error()},
		})

		return false
	}

	return true
}

// --- certificates ---

// iosSigningFakeCertTypeMarkers maps a marker a test can put inside a fake
// certificate blob to the cert_type production would derive from a real
// certificate with the corresponding Apple Subject Common Name prefix.
//
// All seven values are reachable, not just the two the fake used to know about:
// three of them (the Developer ID pair and the Mac installer one) change whether
// a signing configuration may carry provisioning profiles at all, so a fake that
// could not produce them could not exercise that at all. The markers are checked
// longest-first so that "DEVELOPER-ID" is not matched by "DEV".
var iosSigningFakeCertTypeMarkers = []struct {
	marker   string
	certType string
}{
	{"DEVELOPER-ID-APPLICATION", circleci.SigningCertificateTypeDeveloperIDApplication},
	{"DEVELOPER-ID-INSTALLER", circleci.SigningCertificateTypeDeveloperIDInstaller},
	{"MAC-INSTALLER-DISTRIBUTION", circleci.SigningCertificateTypeMacInstallerDistribution},
	{"MAC-APP-DISTRIBUTION", circleci.SigningCertificateTypeMacAppDistribution},
	{"MAC-DEVELOPMENT", circleci.SigningCertificateTypeMacDevelopment},
	{"DEV", circleci.SigningCertificateTypeDevelopment},
}

// iosSigningFakeCertType derives cert_type from a marker in the DECODED blob, so
// tests can exercise every value without the fake needing a real X.509 parser.
//
// Production derives this from the certificate's Subject Common Name, i.e. from the
// decoded bytes — so the fake decodes too. Matching against the raw base64 instead
// would only work for blobs whose encoded form happens to contain the marker, which
// is not a property any real caller controls.
func iosSigningFakeCertType(blob string) string {
	decoded, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		// An undecodable blob is not any particular type; the create handler
		// rejects it separately.
		return circleci.SigningCertificateTypeDistribution
	}

	for _, candidate := range iosSigningFakeCertTypeMarkers {
		if strings.Contains(string(decoded), candidate.marker) {
			return candidate.certType
		}
	}

	return circleci.SigningCertificateTypeDistribution
}

// iosSigningFakeFingerprint stands in for the server's SHA-1-of-DER-bytes
// fingerprint.
//
// The format matches what [NET] testing measured against the real API: 40
// lowercase hex characters, no colons and no separators. An earlier version
// of the test fixtures in signing_certificate_test.go showed
// "AA:BB:CC:DD" -- colon-separated and uppercase -- which was never checked
// against a real certificate and is not the real shape; nothing in this
// package parses the field, so the wrong shape caused no behavioural bug, but
// it was still a false belief worth correcting once measured. This fake uses
// SHA-256 rather than a real SHA-1-of-DER computation (there is no real
// certificate to hash), truncated and re-encoded to the same 40-character
// lowercase hex shape production reports.
func iosSigningFakeFingerprint(blob string) string {
	sum := sha256.Sum256([]byte(blob))

	return fmt.Sprintf("%x", sum[:20])
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

	// Storage upserts on (organization, fingerprint): re-uploading the same
	// certificate returns the first upload's id and leaves its file_name alone.
	// Modelled here because it is the difference between "two resources holding one
	// certificate work" and "the second apply fails with an inconsistent result",
	// and a fake that minted a fresh id every time would report the first.
	fingerprint := iosSigningFakeFingerprint(body.Data.Attributes.CertBlob)
	for _, existing := range a.certs {
		if existing.OrgID == body.Data.References.Org.ID &&
			iosSigningFakeFingerprint(existing.Blob) == fingerprint {
			w.Header().Set("Location", "/api/v3/signing/certificates/"+existing.ID)
			iosSigningFakeWriteJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"id": existing.ID}})

			return
		}
	}

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

	// A certificate still referenced by a signing configuration is a 409: the store
	// refuses to orphan the configuration rather than cascading. Terraform's own
	// dependency ordering avoids this whenever the configuration references the
	// certificate's id, which is the only way to write the pair -- but the fake has
	// to refuse it for that ordering to be worth anything.
	for _, cfg := range a.configs {
		if cfg.CertID == id {
			iosSigningFakeWriteError(w, http.StatusConflict,
				"certificate is in use by one or more signing configurations")

			return
		}
	}

	delete(a.certs, id)
	w.WriteHeader(http.StatusNoContent)
}

// --- signing configs ---

func (a *iosSigningFakeAPI) configItem(cfg *iosSigningFakeConfig) map[string]any {
	profiles := make([]map[string]any, 0, len(cfg.Profiles))
	for i, p := range cfg.Profiles {
		// Production reports {"id", "file_name"} per profile, not "file_name"
		// alone -- see the [NET] note on circleci.SigningProvisioningProfile.ID.
		// iosSigningFakeProfile itself carries no id field, deliberately: several
		// tests compare a whole []iosSigningFakeProfile against a literal built
		// from FileName and Blob alone (see
		// TestIOSSigningConfigWriteOnly_SameRequestAsProvisioningProfiles), and a
		// stored, minted id would break that comparison for no benefit -- nothing
		// in this fake's own logic needs to look a profile up by this id. The id
		// is synthesized here, at response-rendering time only, so the wire shape
		// is realistic without changing what the fake stores.
		profiles = append(profiles, map[string]any{
			"id":        fmt.Sprintf("%s-profile-%d", cfg.ID, i),
			"file_name": p.FileName,
		})
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
				// empty.
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

	cert, ok := a.certs[body.Data.References.Certificate.ID]
	if !ok {
		iosSigningFakeWriteError(w, http.StatusBadRequest, "Bad Request")

		return
	}

	// The profile count is validated against the certificate's type, in both
	// directions, exactly as the store does: required for a type whose Apple
	// workflow has a provisioning profile, refused for the three that do not. This
	// is not a nicety -- it is the rule that makes a signing configuration
	// impossible to create for a Developer ID or Mac installer certificate, and a
	// fake that accepted any count would report that combination as working.
	profileCount := len(body.Data.Attributes.ProvisioningProfiles)
	if circleci.SigningCertificateTypeRequiresProvisioningProfile(cert.CertType) {
		if profileCount == 0 {
			iosSigningFakeWriteError(w, http.StatusBadRequest,
				"at least one provisioning profile is required for this certificate type")

			return
		}
	} else if profileCount > 0 {
		iosSigningFakeWriteError(w, http.StatusBadRequest,
			"provisioning profiles are not allowed for this certificate type")

		return
	}

	// A name is unique within the organization: a repeat is a 409, not an
	// overwrite.
	for _, existing := range a.configs {
		if existing.OrgID == body.Data.References.Org.ID && existing.Name == body.Data.Attributes.Name {
			iosSigningFakeWriteError(w, http.StatusConflict,
				"a signing configuration with this name already exists")

			return
		}
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
	// at the list, matching the real API's create response.
	w.Header().Set("Location", "/api/v3/signing/configs")
	iosSigningFakeWriteJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"id": cfg.ID}})
}

// setListConfigsStatus makes GET .../signing/configs fail with status instead
// of answering the collection, or restores it when status is zero. See
// listConfigsStatus.
func (a *iosSigningFakeAPI) setListConfigsStatus(status int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.listConfigsStatus = status
}

func (a *iosSigningFakeAPI) listConfigs(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.listConfigsStatus != 0 {
		iosSigningFakeWriteError(w, a.listConfigsStatus, "Org not found")

		return
	}

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

// TestIOSSigningFakeAcceptsUnknownFieldsLikeProduction proves the fake matches
// what [NET] testing actually measured: POST .../signing/certificates and
// POST .../signing/configs both answer 201 for a body carrying an unrecognised
// key, at the top level, inside "attributes", and inside one entry of
// "provisioning_profiles" -- they do not reject it the way this package's
// earlier belief (and an earlier version of this very test, then named
// TestIOSSigningFakeRejectsUnexpectedFields) assumed. That assumption borrowed
// a claim from the usage-export route's binder (real there, per
// usage_export_ephemeral_resource_test.go) and generalised it to every v3
// route without checking, which was exactly backwards for these two: a fake
// that rejected an unknown field here was stricter than production, not
// looser, and would have failed a real client-body typo differently from how
// production actually fails it (silently, not with a 400).
//
// Per the MEASURED FACTS this review project holds itself to -- "a 200 does
// not mean the API read what you sent" -- accepting the extra key is not
// enough on its own: each case also reads the created object back and checks
// that the field sent under the *correct* name landed unharmed, so a decode
// bug that let an unknown key clobber a real one would still be caught here,
// just by the read-back rather than by a 400.
//
// The bodies are posted directly rather than through Terraform, because the
// point is a field name no schema can produce.
func TestIOSSigningFakeAcceptsUnknownFieldsLikeProduction(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	t.Run("certificate attribute and reference", func(t *testing.T) {
		body := `{"data":{"attributes":{"file_name":"a.p12","cert_blob":"YQ==","cert_password":"p",` +
			`"cert_blob_base64":"YQ=="},"references":{"org":{"id":"` + iosSigningTestOrgID + `"},"organization":{"id":"x"}}}}`

		resp, err := http.Post(api.URL()+"/api/v3/signing/certificates", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST signing/certificates: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d (%s), want 201: production does not reject an unrecognised field here", resp.StatusCode, raw)
		}

		var created struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &created); err != nil {
			t.Fatalf("decoding create response %s: %v", raw, err)
		}

		// Read it back: file_name (sent under its correct name, alongside the
		// unrecognised cert_blob_base64) must have landed.
		getResp, err := http.Get(api.URL() + "/api/v3/signing/certificates/" + created.Data.ID)
		if err != nil {
			t.Fatalf("GET certificate: %v", err)
		}
		defer func() { _ = getResp.Body.Close() }()

		getRaw, _ := io.ReadAll(getResp.Body)
		if !strings.Contains(string(getRaw), `"file_name":"a.p12"`) {
			t.Errorf("GET response %s does not show file_name=a.p12 landed", getRaw)
		}
	})

	t.Run("config attribute, reference and profile entry", func(t *testing.T) {
		certBody := `{"data":{"attributes":{"file_name":"cfg-test.p12","cert_blob":"YWJj","cert_password":"p"},` +
			`"references":{"org":{"id":"` + iosSigningTestOrgID + `"}}}}`
		certResp, err := http.Post(api.URL()+"/api/v3/signing/certificates", "application/json", strings.NewReader(certBody))
		if err != nil {
			t.Fatalf("POST signing/certificates: %v", err)
		}
		var cert struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(certResp.Body).Decode(&cert); err != nil {
			t.Fatalf("decoding certificate create response: %v", err)
		}
		_ = certResp.Body.Close()

		body := `{"data":{"attributes":{"name":"unknown-field-config","provisioning_profiles":[` +
			`{"file_name":"a.mobileprovision","blob":"YQ==","content":"YQ=="}],"profiles":[]},` +
			`"references":{"org":{"id":"` + iosSigningTestOrgID + `"},"signing_certificate":{"id":"` + cert.Data.ID + `"}},` +
			`"unexpected_top":"z"}}`

		resp, err := http.Post(api.URL()+"/api/v3/signing/configs", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST signing/configs: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d (%s), want 201: production does not reject an unrecognised field "+
				"at the top level, inside attributes, or inside a provisioning_profiles entry", resp.StatusCode, raw)
		}

		// Read it back via the list (there is no single-entity GET): name and the
		// profile's file_name, both sent under their correct names alongside
		// unrecognised siblings, must have landed.
		listResp, err := http.Get(api.URL() + "/api/v3/signing/configs?filter%5Borg_id%5D=" + iosSigningTestOrgID)
		if err != nil {
			t.Fatalf("GET signing/configs: %v", err)
		}
		defer func() { _ = listResp.Body.Close() }()

		listRaw, _ := io.ReadAll(listResp.Body)
		if !strings.Contains(string(listRaw), `"name":"unknown-field-config"`) {
			t.Errorf("list response %s does not show name=unknown-field-config landed", listRaw)
		}
		if !strings.Contains(string(listRaw), `"file_name":"a.mobileprovision"`) {
			t.Errorf("list response %s does not show the profile's file_name landed", listRaw)
		}
	})
}

// TestIOSSigningFakeAcceptsExactlyTheDocumentedBodies is the counterpart: the
// bodies the client really sends must not trip the guard above. Without this a
// too-strict allow-list would look like a passing test suite right up to the point
// a real apply fails.
func TestIOSSigningFakeAcceptsExactlyTheDocumentedBodies(t *testing.T) {
	api := newIOSSigningFakeAPI(t)

	certBody := `{"data":{"attributes":{"file_name":"a.p12","cert_blob":"YQ==","cert_password":"p"},` +
		`"references":{"org":{"id":"` + iosSigningTestOrgID + `"}}}}`

	resp, err := http.Post(api.URL()+"/api/v3/signing/certificates", "application/json", strings.NewReader(certBody))
	if err != nil {
		t.Fatalf("POST certificates: %v", err)
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("certificate create status = %d, want 201", resp.StatusCode)
	}

	configBody := `{"data":{"attributes":{"name":"a","provisioning_profiles":[` +
		`{"file_name":"a.mobileprovision","blob":"YQ=="}]},` +
		`"references":{"org":{"id":"` + iosSigningTestOrgID + `"},"signing_certificate":{"id":"` +
		created.Data.ID + `"}}}}`

	configResp, err := http.Post(api.URL()+"/api/v3/signing/configs", "application/json", strings.NewReader(configBody))
	if err != nil {
		t.Fatalf("POST configs: %v", err)
	}
	defer func() { _ = configResp.Body.Close() }()

	if configResp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(configResp.Body)
		t.Errorf("config create status = %d (%s), want 201", configResp.StatusCode, raw)
	}
}
