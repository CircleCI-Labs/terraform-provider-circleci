// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Signing certificate cert_type values, as reported by the API.
//
// Neither is ever sent by a caller: the server derives it from the uploaded
// certificate's X.509 Subject Common Name (see the cert-type classification
// comment in circleci/the API's cmd/genfakecerts), so it is read-only
// here too.
const (
	// SigningCertificateTypeDistribution is an "iPhone Distribution" /
	// "Apple Distribution" certificate, used to sign builds for release.
	SigningCertificateTypeDistribution = "distribution"
	// SigningCertificateTypeDevelopment is an "iPhone Developer" /
	// "Apple Development" certificate, used to sign builds for testing on
	// registered devices.
	SigningCertificateTypeDevelopment = "development"
)

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
	// CertType is SigningCertificateTypeDistribution or
	// SigningCertificateTypeDevelopment, server-derived; see the package-level
	// constants.
	CertType    string
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
func (c *Client) DeleteSigningCertificate(ctx context.Context, id string) error {
	return c.DeleteV3(ctx, "/signing/certificates/%s", RouteParams(id))
}
