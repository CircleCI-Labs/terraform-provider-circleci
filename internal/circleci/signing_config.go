// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"fmt"
)

// SigningProvisioningProfile is one Apple provisioning profile attached to a
// signing configuration, as reported back by the API.
//
// The profile's content (its .mobileprovision file) is write-only, exactly
// like a certificate's cert_blob: it is accepted on create and never appears
// in any response, so there is no field for it here. See
// CreateSigningProvisioningProfile.
//
// ID is the profile's own identifier, distinct from the signing
// configuration's id. [NET] against the real API: this project's
// provisioning-profile struct used to carry only FileName, on the belief
// that file_name was the whole of what a profile entry reports -- checked
// against neither the OpenAPI spec (silent on the point) nor a real account
// until this was measured. Production's GET .../signing/configs answers each
// provisioning_profiles entry as {"id", "file_name"}, not {"file_name"}
// alone. Nothing in this provider currently looks a profile up by this id --
// there is no per-profile route, only whole-configuration create/delete --
// but it is real, server-assigned data, so it is captured here rather than
// silently discarded the way an untagged struct field would discard it.
type SigningProvisioningProfile struct {
	ID       string
	FileName string
}

// CreateSigningProvisioningProfile is one provisioning profile supplied when
// creating a signing configuration.
type CreateSigningProvisioningProfile struct {
	// FileName is capped at 40 characters by the API.
	FileName string
	// Blob is the profile's .mobileprovision file, base64-encoded with standard
	// encoding.
	Blob string
}

// SigningConfig pairs a signing certificate with the provisioning profiles it
// signs. It is what an iOS build step references to sign an app.
//
// CertificateID, CertificateFileName and CertificateType describe the paired
// certificate rather than embedding a SigningCertificate: the API's own
// "signing_certificate" reference on a config carries only this reduced set
// (id, file_name, cert_type), not fingerprint or the timestamps, so there is
// nothing more to report without a second call the caller can make itself via
// GetSigningCertificate.
type SigningConfig struct {
	ID                   string
	OrganizationID       string
	Name                 string
	ProvisioningProfiles []SigningProvisioningProfile
	CertificateID        string
	CertificateFileName  string
	CertificateType      string
}

// CreateSigningConfigRequest is the input to CreateSigningConfig.
//
// ProvisioningProfiles is capped at 100 entries by the API's request binding.
// Its lower bound is not fixed: it depends on the referenced certificate's
// cert_type, which the caller does not send and cannot choose. See
// SigningCertificateTypeRequiresProvisioningProfile -- for four of the seven
// types at least one profile is required, and for the other three any profile at
// all is refused. Both refusals are a 400 carrying the service's own wording.
//
// Each profile is also really parsed (the .mobileprovision CMS wrapper is
// unpacked and its plist read) and cross-checked against the certificate: a
// profile that does not list the certificate's SHA-1 fingerprint among its
// DeveloperCertificates is a 400, and two profiles sharing a bundle identifier
// and profile type are a 409 -- which means the list is a set keyed on parsed
// contents, not on FileName.
type CreateSigningConfigRequest struct {
	OrganizationID string
	CertificateID  string
	// Name may only contain letters, numbers and hyphens (^[A-Za-z0-9-]+$), and is
	// capped at 50 characters by the API.
	// Name must be unique within the organization: a repeat is a 409, not an
	// overwrite.
	Name                 string
	ProvisioningProfiles []CreateSigningProvisioningProfile
}

// signingConfigCertRef is the "signing_certificate" reference carried by a
// config's list representation: the certificate's id plus the minimal
// attributes the service inlines (file_name, cert_type), so that listing
// configs does not require a second call per certificate.
type signingConfigCertRef struct {
	ID         string `json:"id"`
	Attributes struct {
		FileName string `json:"file_name"`
		CertType string `json:"cert_type"`
	} `json:"attributes"`
}

// signingConfigProfileAttributes is one provisioning profile as reported
// back: its own id and its file name. The blob supplied on create is
// write-only and never echoed, exactly like a certificate's cert_blob. See
// the [NET] note on SigningProvisioningProfile.ID for why "id" is here at
// all -- an earlier version of this struct carried only file_name.
type signingConfigProfileAttributes struct {
	ID       string `json:"id"`
	FileName string `json:"file_name"`
}

// signingConfigItem is the v3 wire shape of one entry of GET
// .../signing/configs.
//
// There is no GET .../signing/configs/{id}: the only routes for signing configs
// are GET (collection), POST and DELETE. A single configuration is
// therefore looked up by listing an organization's configs and filtering
// client-side by id -- see getSigningConfigByID.
type signingConfigItem struct {
	ID         string `json:"id"`
	Attributes struct {
		Name                 string                           `json:"name"`
		ProvisioningProfiles []signingConfigProfileAttributes `json:"provisioning_profiles"`
	} `json:"attributes"`
	References struct {
		Certificate signingConfigCertRef `json:"signing_certificate"`
	} `json:"references"`
}

func (i signingConfigItem) toSigningConfig(organizationID string) *SigningConfig {
	profiles := make([]SigningProvisioningProfile, 0, len(i.Attributes.ProvisioningProfiles))
	for _, p := range i.Attributes.ProvisioningProfiles {
		profiles = append(profiles, SigningProvisioningProfile(p))
	}

	return &SigningConfig{
		ID:                   i.ID,
		OrganizationID:       organizationID,
		Name:                 i.Attributes.Name,
		ProvisioningProfiles: profiles,
		CertificateID:        i.References.Certificate.ID,
		CertificateFileName:  i.References.Certificate.Attributes.FileName,
		CertificateType:      i.References.Certificate.Attributes.CertType,
	}
}

// signingConfigCreated is the wire shape of the 201 response to POST
// .../signing/configs: the new id, and nothing else, for the same reason as a
// certificate's create response. See CreateSigningConfig.
type signingConfigCreated struct {
	ID string `json:"id"`
}

// createSigningConfigProfile is one provisioning profile in the create
// request body.
type createSigningConfigProfile struct {
	Blob     string `json:"blob"`
	FileName string `json:"file_name"`
}

// createSigningConfigBody is the POST .../signing/configs request envelope:
// data.attributes carries name/provisioning_profiles, data.references carries
// the owning organization and the paired certificate.
type createSigningConfigBody struct {
	Data struct {
		Attributes struct {
			Name                 string                       `json:"name"`
			ProvisioningProfiles []createSigningConfigProfile `json:"provisioning_profiles"`
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

// CreateSigningConfig creates an iOS signing configuration pairing a
// certificate with one or more provisioning profiles, and returns its
// representation.
//
// POST .../signing/configs answers 201 with only {"data":{"id"}}, and unlike a
// certificate there is no GET .../signing/configs/{id} route at all to follow
// it with, so this method hydrates the result with ListSigningConfigs and a
// client-side match on id.
//
// CircleCI Cloud only; the configuration itself is only useful to a macOS
// executor building and signing an iOS app.
func (c *Client) CreateSigningConfig(ctx context.Context, req CreateSigningConfigRequest) (*SigningConfig, error) {
	var body createSigningConfigBody
	body.Data.Attributes.Name = req.Name
	body.Data.References.Org.ID = req.OrganizationID
	body.Data.References.Certificate.ID = req.CertificateID

	body.Data.Attributes.ProvisioningProfiles = make([]createSigningConfigProfile, len(req.ProvisioningProfiles))
	for i, p := range req.ProvisioningProfiles {
		body.Data.Attributes.ProvisioningProfiles[i] = createSigningConfigProfile{
			Blob:     p.Blob,
			FileName: p.FileName,
		}
	}

	var env Entity[signingConfigCreated]
	if err := c.PostV3(ctx, "/signing/configs", body, &env); err != nil {
		return nil, err
	}

	return c.getSigningConfigByID(ctx, req.OrganizationID, env.Data.ID)
}

// GetSigningConfig retrieves one signing configuration by id.
//
// There is no single-entity route for a signing config (see
// CreateSigningConfig), so this lists every configuration owned by
// organizationID and returns the one whose id matches. A missing configuration
// yields an error satisfying IsNotFound. Both organizationID and id are
// required for exactly this reason: a caller that only kept the id could not
// resolve it.
func (c *Client) GetSigningConfig(ctx context.Context, organizationID, id string) (*SigningConfig, error) {
	return c.getSigningConfigByID(ctx, organizationID, id)
}

func (c *Client) getSigningConfigByID(ctx context.Context, organizationID, id string) (*SigningConfig, error) {
	configs, err := c.ListSigningConfigs(ctx, organizationID)
	if err != nil {
		return nil, err
	}

	for _, cfg := range configs {
		if cfg.ID == id {
			return &cfg, nil
		}
	}

	return nil, fmt.Errorf("signing config %q: %w", id, ErrNotFound)
}

// ListSigningConfigs returns every signing configuration belonging to an
// organization, following the v3 cursor to the last page.
//
// filter[org_id] is required, exactly as on ListSigningCertificates, and the
// drain is defensive for the same reason: the handler sets no cursor, so the
// response carries no "page" object and there is only ever one page today.
func (c *Client) ListSigningConfigs(ctx context.Context, organizationID string) ([]SigningConfig, error) {
	return DrainV3(ctx, func(ctx context.Context, cursor string) (List[SigningConfig], error) {
		var page List[signingConfigItem]
		err := c.GetV3(ctx, "/signing/configs", &page,
			Filter("org_id", organizationID),
			PageCursor(cursor),
		)
		if err != nil {
			return List[SigningConfig]{}, err
		}

		out := List[SigningConfig]{Meta: page.Meta, Page: page.Page}
		for _, item := range page.Data {
			out.Data = append(out.Data, *item.toSigningConfig(organizationID))
		}

		return out, nil
	})
}

// DeleteSigningConfig deletes a signing configuration by id.
//
// There is no update route for a signing configuration: adding, removing or
// renewing a provisioning profile, renaming the config, or repointing it at a
// different certificate is always a delete-and-recreate.
func (c *Client) DeleteSigningConfig(ctx context.Context, id string) error {
	return c.DeleteV3(ctx, "/signing/configs/%s", RouteParams(id))
}
