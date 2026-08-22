// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Routes for audit log streaming configs: where CircleCI streams an
// organization's audit log events, as JSON objects written to a
// customer-owned S3 (or S3-compatible) bucket.
//
// These are "/api/v2/organizations/{org_id}/audit-log/configs" and
// "/api/v2/audit-log/configs[/{id}]", per an internal route table — NOT
// confirmed by an executed request. See DESIGN.md for why that distinction
// matters: the public API's own tests use fabricated placeholder bodies
// ("s3"/"bucket"/"name") that do not reflect the real wire shape, if this
// route table is even still current.
//
// [NET] It is not current, or not reachable this way: probing all seven
// routes in this file against four real Cloud organizations (org-admin
// token, one mid-Scale-trial) got back a routing-layer 404
// (`{"message" : "Not Found"}` under /api/v2, `{"message":"Route Not
// Found."}` under /api/v3) — the same shape a deliberately nonexistent path
// gets, and unlike this API's actual resource-level 404/403 bodies
// elsewhere. See DESIGN.md's "A pass-through route's wire shape belongs to
// whatever is behind it" addendum for the full comparison. Treat every claim
// in this file as UNVALIDATED against production, not merely
// plan-gated — the failure precedes any plan-tier check.
//
// Unlike the collection routes, the single-config routes (get, update,
// delete) are NOT nested under /organizations/{org_id}/...: they address a
// config by its bare id. Update is a PUT to the bare collection path with the
// id carried in the body, not a per-id PUT. (Also per the route table, also
// unconfirmed.)
const (
	auditLogConfigsRoute      = "/organizations/%s/audit-log/configs"
	auditLogConfigRoute       = "/audit-log/configs/%s"
	auditLogConfigUpdateRoute = "/audit-log/configs"
	// auditLogAccessRoute reports whether an organization is entitled to audit
	// log streaming at all (its plan tier), as distinct from whether it has
	// configured a destination. See GetAuditLogAccess.
	auditLogAccessRoute = "/organizations/%s/audit-log/access"
)

// Audit log streaming destination types.
const (
	// AuditLogTargetTypeS3 delivers to an AWS S3 bucket. Config.Region is
	// required and Config.Endpoint must be empty.
	AuditLogTargetTypeS3 = "S3"
	// AuditLogTargetTypeS3Compatible delivers to an S3-compatible endpoint,
	// such as MinIO. Config.Endpoint is required; Config.Region is optional
	// and defaults server-side to "us-east-1" when omitted.
	AuditLogTargetTypeS3Compatible = "S3_COMPATIBLE"
)

// Connection statuses the API reports for a config's most recent delivery
// attempts. These are report-only: none of them can be set through the API.
const (
	// AuditLogConnectionStatusConnected means recent deliveries succeeded, or
	// none have been attempted yet.
	AuditLogConnectionStatusConnected = "CONNECTED"
	// AuditLogConnectionStatusDisconnected means every recent delivery failed.
	AuditLogConnectionStatusDisconnected = "DISCONNECTED"
	// AuditLogConnectionStatusUnknown means the status could not be
	// calculated.
	AuditLogConnectionStatusUnknown = "UNKNOWN"
	// AuditLogConnectionStatusDisabled means the config is disabled, so no
	// deliveries are attempted.
	AuditLogConnectionStatusDisabled = "DISABLED"
)

// auditLogConfigPurpose is sent as the required "purpose" field on every
// create and update. It exists purely for the backend's own metrics and
// audit trail — other consumers of the same underlying API use different
// values — but this client only ever manages audit log streaming, so it is
// not exposed as configurable.
const auditLogConfigPurpose = "audit-logs"

// AuditLogS3Config is the S3 (or S3-compatible) destination for an audit log
// streaming config.
type AuditLogS3Config struct {
	// ARN is the AWS IAM role CircleCI assumes, via OIDC, to write to the
	// bucket. Must match an AWS or MinIO IAM role ARN shape, and at most 128
	// characters.
	ARN string `json:"arn"`
	// Region is required when TargetType is AuditLogTargetTypeS3, and must be
	// empty when TargetType is AuditLogTargetTypeS3Compatible unless the
	// endpoint requires a specific region: the API defaults it to "us-east-1"
	// server-side in that case.
	Region string `json:"region,omitempty"`
	// BucketName is the destination bucket: 3-63 characters, lowercase
	// letters, digits, dots and hyphens only. A trailing "/" is stripped
	// server-side.
	BucketName string `json:"bucket_name"`
	// BucketPrefix is an optional key prefix under the bucket. Leading and
	// trailing "/" are stripped server-side.
	BucketPrefix string `json:"bucket_prefix,omitempty"`
	// Endpoint is required when TargetType is AuditLogTargetTypeS3Compatible,
	// and must be empty when TargetType is AuditLogTargetTypeS3.
	Endpoint string `json:"endpoint,omitempty"`
}

// AuditLogConfig is an audit log streaming configuration, as returned by
// every audit-log/configs route.
type AuditLogConfig struct {
	// ID is the config's identifier, assigned by CircleCI.
	ID string `json:"id"`
	// OrgID is the organization the config belongs to.
	OrgID string `json:"org_id"`
	// TargetType is AuditLogTargetTypeS3 or AuditLogTargetTypeS3Compatible.
	TargetType string `json:"target_type"`
	// IsDisabled reports whether streaming is turned off. A disabled config
	// is kept, not deleted, and can be re-enabled in place.
	IsDisabled bool `json:"is_disabled"`
	// Config is the S3 destination.
	Config AuditLogS3Config `json:"config"`
	// CreatedBy is the id of the user who created the config.
	CreatedBy string `json:"created_by"`
	// CreatedAt is an RFC 3339 timestamp, kept as the string CircleCI sent.
	CreatedAt string `json:"created_at"`
	// UpdatedAt is an RFC 3339 timestamp, kept as the string CircleCI sent.
	UpdatedAt string `json:"updated_at"`
	// ConnectionStatus reports the outcome of the most recent delivery
	// attempts. See the AuditLogConnectionStatus* constants.
	ConnectionStatus string `json:"connection_status"`
}

// listAuditLogConfigsResponse is the v2 envelope for the list route:
//
//	{"items": [...]}
//
// with no next_page_token: an organization has at most one config per target
// type, so there are never enough rows to paginate.
type listAuditLogConfigsResponse struct {
	Items []AuditLogConfig `json:"items"`
}

// ListAuditLogConfigs returns every audit log streaming config for an
// organization — at most one per target type.
func (c *Client) ListAuditLogConfigs(ctx context.Context, orgID string) ([]AuditLogConfig, error) {
	var resp listAuditLogConfigsResponse

	if err := c.GetV2(ctx, auditLogConfigsRoute, &resp, RouteParams(orgID)); err != nil {
		return nil, err
	}

	return resp.Items, nil
}

// GetAuditLogConfig returns a single audit log streaming config by id.
//
// A config that does not exist, and one the caller lacks permission on,
// answer identically: HTTP 404 with {"message": "Resource does not exist or
// unauthorized"}. That is an anti-enumeration measure — the opposite of the
// 403-for-missing behaviour some other CircleCI routes use — but it means
// IsNotFound alone cannot distinguish "gone" from "never had access".
func (c *Client) GetAuditLogConfig(ctx context.Context, id string) (*AuditLogConfig, error) {
	var config AuditLogConfig

	if err := c.GetV2(ctx, auditLogConfigRoute, &config, RouteParams(id)); err != nil {
		return nil, err
	}

	return &config, nil
}

// CreateAuditLogConfigRequest is the input to CreateAuditLogConfig.
type CreateAuditLogConfigRequest struct {
	// OrgID is the organization to create the config for.
	OrgID string
	// TargetType is AuditLogTargetTypeS3 or AuditLogTargetTypeS3Compatible.
	TargetType string
	// IsDisabled creates the config already disabled. Note that the
	// connection is still verified at create time even when this is true —
	// unlike an update, create has no path that skips the connectivity
	// check.
	IsDisabled bool
	// Config is the S3 destination.
	Config AuditLogS3Config
}

// createAuditLogConfigBody is the POST body. Field names and nesting mirror
// the backend's own create request type exactly.
type createAuditLogConfigBody struct {
	TargetType string           `json:"target_type"`
	IsDisabled bool             `json:"is_disabled"`
	Config     AuditLogS3Config `json:"config"`
	Purpose    string           `json:"purpose"`
}

// CreateAuditLogConfig creates an audit log streaming config for an
// organization.
//
// The backend verifies connectivity to the destination bucket as part
// of create — using the same OIDC-assumed role that will later write to it —
// so a create can fail with a connectivity or permission error even when
// IsDisabled is true. It also requires the organization to be on a CircleCI
// Cloud Scale plan, answering 403 ("Feature only available for scale orgs")
// otherwise, and allows at most one config per (org, target type) pair,
// answering 409 for a second one.
func (c *Client) CreateAuditLogConfig(ctx context.Context, req CreateAuditLogConfigRequest) (*AuditLogConfig, error) {
	body := createAuditLogConfigBody{
		TargetType: req.TargetType,
		IsDisabled: req.IsDisabled,
		Config:     req.Config,
		Purpose:    auditLogConfigPurpose,
	}

	var config AuditLogConfig
	// The handler answers 200, not 201, on a successful create.
	if err := c.PostV2(ctx, auditLogConfigsRoute, body, &config, RouteParams(req.OrgID)); err != nil {
		return nil, err
	}

	return &config, nil
}

// UpdateAuditLogConfigRequest is the input to UpdateAuditLogConfig.
type UpdateAuditLogConfigRequest struct {
	// ID is the config to update.
	ID string
	// OrgID is sent for completeness, but the API ignores it: the
	// existing config's organization always wins, so a config cannot change
	// which organization owns it through this route.
	OrgID string
	// TargetType is AuditLogTargetTypeS3 or AuditLogTargetTypeS3Compatible.
	// The API allows changing this in place; it does not re-run the
	// create-time conflict check against the organization's other configs.
	TargetType string
	// IsDisabled updates whether streaming is turned off. Connectivity is
	// verified on update only when this is false: setting IsDisabled to true
	// skips the check, unlike create.
	IsDisabled bool
	// Config is the S3 destination.
	Config AuditLogS3Config
}

// updateAuditLogConfigBody is the PUT body. Field names mirror
// the backend's own update request type exactly.
type updateAuditLogConfigBody struct {
	ID         string           `json:"id"`
	OrgID      string           `json:"org_id"`
	TargetType string           `json:"target_type"`
	IsDisabled bool             `json:"is_disabled"`
	Config     AuditLogS3Config `json:"config"`
	Purpose    string           `json:"purpose"`
}

// UpdateAuditLogConfig applies a full replacement of an existing config's
// target type, disabled flag and S3 destination.
//
// This is a PUT to the bare collection route, not a per-id route: the id
// travels in the body. There is no route that updates a subset of fields.
func (c *Client) UpdateAuditLogConfig(ctx context.Context, req UpdateAuditLogConfigRequest) (*AuditLogConfig, error) {
	body := updateAuditLogConfigBody{
		ID:         req.ID,
		OrgID:      req.OrgID,
		TargetType: req.TargetType,
		IsDisabled: req.IsDisabled,
		Config:     req.Config,
		Purpose:    auditLogConfigPurpose,
	}

	var config AuditLogConfig
	if err := c.PutV2(ctx, auditLogConfigUpdateRoute, body, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// DeleteAuditLogConfig deletes an audit log streaming config by id.
func (c *Client) DeleteAuditLogConfig(ctx context.Context, id string) error {
	return c.DeleteV2(ctx, auditLogConfigRoute, RouteParams(id))
}

// auditLogAccessResponse is GetFileConfigsAccess's response shape:
// {"has_access": bool}.
type auditLogAccessResponse struct {
	HasAccess bool `json:"has_access"`
}

// GetAuditLogAccess reports whether an organization is entitled to audit log
// streaming at all — i.e. whether it is on a plan tier the feature is gated
// behind — as distinct from whether it has actually configured a
// destination. A create against an ineligible organization fails with a 403
// ("Feature only available for scale orgs"); this route lets a caller check
// that ahead of time.
func (c *Client) GetAuditLogAccess(ctx context.Context, orgID string) (bool, error) {
	var resp auditLogAccessResponse

	if err := c.GetV2(ctx, auditLogAccessRoute, &resp, RouteParams(orgID)); err != nil {
		return false, err
	}

	return resp.HasAccess, nil
}
