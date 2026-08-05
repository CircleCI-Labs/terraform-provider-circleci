// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Signing certificate cert_type values, as reported by the API.
//
// None of these is ever sent by a caller: the server classifies the uploaded
// certificate by matching its X.509 Subject Common Name against a fixed set of
// Apple prefixes, and a common name matching none of them is rejected outright
// rather than stored with some fallback type. So cert_type is read-only here,
// and the set below is closed -- there are seven values, not two, and three of
// them change what circleci_ios_signing_config will accept (see
// SigningCertificateTypeRequiresProvisioningProfile).
const (
	// SigningCertificateTypeDistribution is an "iPhone Distribution:" /
	// "Apple Distribution:" certificate, used to sign builds for release.
	SigningCertificateTypeDistribution = "distribution"
	// SigningCertificateTypeDevelopment is an "iPhone Developer:" /
	// "Apple Development:" certificate, used to sign builds for testing on
	// registered devices.
	SigningCertificateTypeDevelopment = "development"
	// SigningCertificateTypeDeveloperIDApplication is a "Developer ID
	// Application:" certificate, for distributing a macOS app outside the App
	// Store. Apple's workflow has no provisioning profile for it.
	SigningCertificateTypeDeveloperIDApplication = "developer-id-application"
	// SigningCertificateTypeDeveloperIDInstaller is a "Developer ID Installer:"
	// certificate, for signing a macOS .pkg distributed outside the App Store.
	// Apple's workflow has no provisioning profile for it.
	SigningCertificateTypeDeveloperIDInstaller = "developer-id-installer"
	// SigningCertificateTypeMacDevelopment is a "Mac Developer:" certificate.
	SigningCertificateTypeMacDevelopment = "mac-development"
	// SigningCertificateTypeMacAppDistribution is a "3rd Party Mac Developer
	// Application:" certificate, for a Mac App Store app.
	SigningCertificateTypeMacAppDistribution = "mac-app-distribution"
	// SigningCertificateTypeMacInstallerDistribution is a "3rd Party Mac
	// Developer Installer:" certificate, for signing a Mac App Store .pkg.
	// Apple's workflow has no provisioning profile for it.
	SigningCertificateTypeMacInstallerDistribution = "mac-installer-distribution"
)

// SigningCertificateTypes is every cert_type the API can report, in the order
// the service's own classifier tests them.
//
// It exists so that a caller documenting or validating the value does not have
// to restate the list, and so a test can assert the set has not silently grown.
var SigningCertificateTypes = []string{
	SigningCertificateTypeDistribution,
	SigningCertificateTypeDevelopment,
	SigningCertificateTypeDeveloperIDApplication,
	SigningCertificateTypeDeveloperIDInstaller,
	SigningCertificateTypeMacDevelopment,
	SigningCertificateTypeMacAppDistribution,
	SigningCertificateTypeMacInstallerDistribution,
}

// SigningCertificateTypeRequiresProvisioningProfile reports whether a signing
// configuration paired with a certificate of this type must carry at least one
// provisioning profile.
//
// The rule is two-sided and enforced by the service, not merely advisory: for a
// type that requires profiles, a configuration with none is a 400 ("at least one
// provisioning profile is required for this certificate type"); for a type that
// does not, a configuration with any profile at all is also a 400
// ("provisioning profiles are not allowed for this certificate type"). The three
// exempt types are the ones with no provisioning profile in Apple's own workflow:
// both Developer ID variants (direct distribution outside the App Store) and the
// 3rd Party Mac Developer Installer certificate (Mac App Store .pkg signing).
//
// An unrecognized certType reports true, matching the service's own default.
func SigningCertificateTypeRequiresProvisioningProfile(certType string) bool {
	switch certType {
	case SigningCertificateTypeDeveloperIDApplication,
		SigningCertificateTypeDeveloperIDInstaller,
		SigningCertificateTypeMacInstallerDistribution:
		return false
	}

	return true
}

// SigningCertificate is an Apple code-signing certificate (a .p12 file and its
// password) uploaded to a CircleCI organization for signing iOS builds.
//
// The .p12 content and password are write-only: the API stores them and never
// sends them back in any response, so there is no field for them here. See
// CreateSigningCertificateRequest.
//
// CreatedAt and ExpiresAt are kept as the RFC 3339 strings the API sent (rather
// than parsed into time.Time), matching the convention used for every other
// timestamp in this package, so they round-trip into Terraform state exactly as
// received. Either may be nil: ExpiresAt when the server could not parse an
// expiry out of the certificate, CreatedAt in principle for the same reason,
// though every certificate observed in practice has one.
type SigningCertificate struct {
	ID             string
	OrganizationID string
	FileName       string
	// CertType is one of SigningCertificateTypes, server-derived; see the
	// package-level constants.
	CertType string
	// Fingerprint is the SHA-1 hex digest of the certificate's DER bytes, which
	// is what Apple uses to identify a signing identity. It is also the key the
	// service deduplicates on -- see CreateSigningCertificate.
	Fingerprint string
	CreatedAt   *string
	ExpiresAt   *string
}

// CreateSigningCertificateRequest is the input to CreateSigningCertificate.
//
// CertBlob is the certificate's .p12 file, base64-encoded with standard
// encoding (production callers use e.g. Terraform's filebase64() to produce
// it), and CertPassword is the passphrase that unlocks it. Both are write-only
// credentials: CircleCI stores them to sign builds and never returns them
// again, in any response, from any route -- see the "Security" section of the
// circleci_ios_signing_certificate resource documentation for what that means
// for Terraform state.
type CreateSigningCertificateRequest struct {
	OrganizationID string
	// FileName is capped at 40 characters by the API.
	FileName     string
	CertBlob     string
	CertPassword string
}

// signingCertificateAttributes is the v3 "attributes" object, shared by the
// list and single-entity representations: file_name, cert_type, fingerprint,
// created_at and expires_at. cert_blob and cert_password are accepted on
// create but never appear in any response, so they have no place here.
type signingCertificateAttributes struct {
	FileName    string  `json:"file_name"`
	CertType    string  `json:"cert_type"`
	Fingerprint string  `json:"fingerprint"`
	CreatedAt   *string `json:"created_at"`
	ExpiresAt   *string `json:"expires_at"`
}

// signingCertificateReferences is the "references" object on the single-entity
// representation (GET .../signing/certificates/{id}). The list representation
// (GET .../signing/certificates) omits references entirely: the org is already
// known from the filter[org_id] query parameter that scoped the request.
type signingCertificateReferences struct {
	Org struct {
		ID string `json:"id"`
	} `json:"org"`
}

// signingCertificateEntity is the wire shape of the single-entity GET.
type signingCertificateEntity struct {
	ID         string                       `json:"id"`
	Attributes signingCertificateAttributes `json:"attributes"`
	References signingCertificateReferences `json:"references"`
}

func (e signingCertificateEntity) toSigningCertificate() *SigningCertificate {
	return &SigningCertificate{
		ID:             e.ID,
		OrganizationID: e.References.Org.ID,
		FileName:       e.Attributes.FileName,
		CertType:       e.Attributes.CertType,
		Fingerprint:    e.Attributes.Fingerprint,
		CreatedAt:      e.Attributes.CreatedAt,
		ExpiresAt:      e.Attributes.ExpiresAt,
	}
}

// signingCertificateItem is the wire shape of one entry of the list GET. It
// carries the same attributes as signingCertificateEntity but, per production,
// no references object.
type signingCertificateItem struct {
	ID         string                       `json:"id"`
	Attributes signingCertificateAttributes `json:"attributes"`
}

func (i signingCertificateItem) toSigningCertificate(organizationID string) *SigningCertificate {
	return &SigningCertificate{
		ID:             i.ID,
		OrganizationID: organizationID,
		FileName:       i.Attributes.FileName,
		CertType:       i.Attributes.CertType,
		Fingerprint:    i.Attributes.Fingerprint,
		CreatedAt:      i.Attributes.CreatedAt,
		ExpiresAt:      i.Attributes.ExpiresAt,
	}
}

// signingCertificateCreated is the wire shape of the 201 response to POST
// .../signing/certificates: the new id, and nothing else. The service
// deliberately does not echo the certificate back (a read-after-write there
// would risk masking a successful create as a failure on a transient read
// error), so CreateSigningCertificate makes its own follow-up GET.
type signingCertificateCreated struct {
	ID string `json:"id"`
}

// createSigningCertificateBody is the POST .../signing/certificates request
// envelope: data.attributes carries file_name/cert_blob/cert_password,
// data.references.org carries the owning organization.
type createSigningCertificateBody struct {
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

// CreateSigningCertificate uploads an iOS signing certificate to an
// organization and returns its representation.
//
// POST .../signing/certificates answers 201 with only {"data":{"id"}}, so this
// method makes the documented follow-up GET .../signing/certificates/{id} to
// return the full representation -- CertBlob and CertPassword are never in it,
// because the API never sends them back.
//
// The upload is validated and it is idempotent, in a way callers have to plan
// for:
//
//   - The .p12 is really parsed (PKCS#12 decode with CertPassword, then the
//     X.509 Subject Common Name matched against Apple's prefixes). An
//     undecodable blob, a wrong password, or a certificate that is not an Apple
//     signing certificate are all one 400, "invalid certificate".
//   - Storage upserts on (organization, fingerprint). Uploading the same
//     certificate to the same organization twice returns the id of the first
//     upload and does *not* overwrite its FileName. Two Terraform resources
//     holding the same .p12 under different file names therefore collide onto one
//     id, and the second one's Create sees the first one's FileName come back --
//     which Terraform reports as a provider producing an inconsistent result.
//     One resource per distinct certificate.
//
// CircleCI Cloud only; the certificate itself is only useful to a macOS
// executor building and signing an iOS app.
func (c *Client) CreateSigningCertificate(ctx context.Context, req CreateSigningCertificateRequest) (*SigningCertificate, error) {
	var body createSigningCertificateBody
	body.Data.Attributes.FileName = req.FileName
	body.Data.Attributes.CertBlob = req.CertBlob
	body.Data.Attributes.CertPassword = req.CertPassword
	body.Data.References.Org.ID = req.OrganizationID

	var env Entity[signingCertificateCreated]
	if err := c.PostV3(ctx, "/signing/certificates", body, &env); err != nil {
		return nil, err
	}

	return c.GetSigningCertificate(ctx, env.Data.ID)
}

// GetSigningCertificate retrieves one signing certificate by id. A missing
// certificate yields an error satisfying IsNotFound.
func (c *Client) GetSigningCertificate(ctx context.Context, id string) (*SigningCertificate, error) {
	var env Entity[signingCertificateEntity]
	if err := c.GetV3(ctx, "/signing/certificates/%s", &env, RouteParams(id)); err != nil {
		return nil, err
	}

	return env.Data.toSigningCertificate(), nil
}

// ListSigningCertificates returns every signing certificate belonging to an
// organization, following the v3 cursor to the last page.
//
// filter[org_id] is required, not optional: the handler resolves it before doing
// anything else and answers 400 without it. The drain is defensive rather than
// load-bearing today -- the handler renders the whole collection with no cursor
// set, so the "page" object is omitted and there is only ever one page -- but a
// client that stops after one page cannot tell "no cursor" from "cursor ignored",
// and the cost of following one that does appear is a single extra request.
func (c *Client) ListSigningCertificates(ctx context.Context, organizationID string) ([]SigningCertificate, error) {
	return DrainV3(ctx, func(ctx context.Context, cursor string) (List[SigningCertificate], error) {
		var page List[signingCertificateItem]
		err := c.GetV3(ctx, "/signing/certificates", &page,
			Filter("org_id", organizationID),
			PageCursor(cursor),
		)
		if err != nil {
			return List[SigningCertificate]{}, err
		}

		out := List[SigningCertificate]{Meta: page.Meta, Page: page.Page}
		for _, item := range page.Data {
			out.Data = append(out.Data, *item.toSigningCertificate(organizationID))
		}

		return out, nil
	})
}

// DeleteSigningCertificate deletes a signing certificate by id.
//
// There is no update route for a signing certificate: uploading a new .p12 or
// changing its password is always a delete-and-recreate.
//
// It answers 204 with no body. A certificate that does not exist is a 404, since
// the handler resolves the certificate (to find its organization, for the
// permission check) before deleting it; a caller who can view but not manage that
// organization gets 403.
//
// A certificate still referenced by a signing configuration is a 409, not a 204
// or a cascade: the store refuses to orphan a configuration. Terraform's own
// dependency ordering handles this whenever the configuration references the
// certificate's id, which is the only way to write the pair; a configuration
// created outside Terraform has to be deleted by hand first.
func (c *Client) DeleteSigningCertificate(ctx context.Context, id string) error {
	return c.DeleteV3(ctx, "/signing/certificates/%s", RouteParams(id))
}
