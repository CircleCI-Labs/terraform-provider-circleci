// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// storageRetentionRoute is relative to the private origin, not Host — see
// private.go. There is exactly one route: it both reads and writes the same
// record, unlike the v3 settings pattern (organization_settings.go) which
// splits a GET and a POST across two paths.
const storageRetentionRoute = "/private/orgs/%s/storage-retention-controls"

// StorageRetentionControls holds the per-organization retention period, in
// days, for cache, workspace and job artifacts.
//
// Unlike OrganizationSettings, this is not a partial update: the PUT route
// replaces all three values in one request, so every field is a plain int64
// rather than a pointer. There is no way to change one and leave the other two
// alone at the wire level — SetStorageRetention always sends all three.
type StorageRetentionControls struct {
	// CacheDays is the retention period, in days, for build cache.
	CacheDays int64 `json:"retention_days_cache"`
	// WorkspaceDays is the retention period, in days, for workspace data passed
	// between jobs.
	WorkspaceDays int64 `json:"retention_days_workspace"`
	// ArtifactDays is the retention period, in days, for job artifacts.
	ArtifactDays int64 `json:"retention_days_artifact"`
}

// StorageRetentionBound is the plan-enforced [Min, Max] range for one of the
// three retention controls. It is reported by the API and never sent: there is
// no route that changes a plan's limits, only one that reports them.
type StorageRetentionBound struct {
	Min int64 `json:"min"`
	Max int64 `json:"max"`
}

// StorageRetentionLimits mirrors the organization's plan-enforced bounds for
// each of the three retention controls.
type StorageRetentionLimits struct {
	Cache     StorageRetentionBound `json:"retention_days_cache"`
	Workspace StorageRetentionBound `json:"retention_days_workspace"`
	Artifact  StorageRetentionBound `json:"retention_days_artifact"`
}

// StorageRetention is the full response from GET
// .../storage-retention-controls: the organization's current controls, and the
// plan's enforced bounds on them.
//
// [NET] measurement shows the real response also carries a third top-level
// key, storage_retention_defaults, shaped like Controls, holding what the
// organization's plan defaults to. Nothing here currently needs it — this
// resource always sends all three fields explicitly rather than relying on a
// default — so it is not modelled; Go's decoder drops the unrecognised key
// silently rather than erroring, which is the behaviour this type relies on.
type StorageRetention struct {
	Controls StorageRetentionControls `json:"storage_retention_controls"`
	Limits   StorageRetentionLimits   `json:"storage_retention_limits"`
}

// GetStorageRetention reads an organization's current storage-retention
// controls and the plan-enforced bounds on them.
//
// Private route, Cloud only — see private.go. Callers must gate this on
// Client.IsCloud themselves; nothing here does it, the same as every other
// method behind GetPrivate/PutPrivate.
func (c *Client) GetStorageRetention(ctx context.Context, orgID string) (*StorageRetention, error) {
	var retention StorageRetention

	if err := c.GetPrivate(ctx, storageRetentionRoute, &retention, RouteParams(orgID)); err != nil {
		return nil, err
	}

	return &retention, nil
}

// SetStorageRetention writes an organization's storage-retention controls and
// returns what CircleCI actually stored.
//
// The PUT route answers 204 No Content on success. An earlier version of this
// comment claimed it clamps an out-of-bounds value to the nearest plan limit
// rather than rejecting it; [NET] measurement against gitlab-test
// (2026-08-21) shows that is false. Every value outside the reported [min,
// max] range — one field over its max, one field under its min, a negative
// number, even an in-range request missing one of the three required fields —
// answered 400 {"error":"Invalid value given"} instead, and the write did not
// apply any of the three fields: a follow-up GET showed the record completely
// unchanged. A value exactly at a reported bound (e.g. artifact_days at its
// max) is accepted normally. So this route's write is, on this evidence,
// atomic and rejecting rather than partial and clamping.
//
// The read-back GET performed here is kept anyway, for two reasons unrelated
// to clamping: it is the only way to learn each control's current value at
// all (the PUT response carries none of them), and it is a cheap defence if
// this route's behaviour ever changes back — see
// warnClampedStorageRetention in the provider package, which compares
// requested against applied and would start firing again if it did.
//
// One more thing [NET] measurement found and this client does not need to
// react to: a request body containing a key the schema does not recognise
// (tested: a bogus extra field alongside all three valid ones) answers 500,
// not the silent-drop behaviour this codebase generally assumes for unknown
// keys elsewhere. This client never sends an extra key, so it is not acted on
// here, but it means "unknown keys are dropped" is not a safe assumption to
// port to this specific route without checking again.
func (c *Client) SetStorageRetention(
	ctx context.Context, orgID string, controls StorageRetentionControls,
) (*StorageRetention, error) {
	if err := c.PutPrivate(ctx, storageRetentionRoute, controls, nil, RouteParams(orgID)); err != nil {
		return nil, err
	}

	return c.GetStorageRetention(ctx, orgID)
}
