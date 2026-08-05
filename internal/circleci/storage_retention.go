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
// The PUT route answers 204 No Content on success, and — this is the part that
// matters to callers — it clamps any value outside the organization's plan
// bounds rather than rejecting it. So a 2xx response does not mean the values
// requested are the values now in effect. Rather than let every caller
// rediscover that, this always reads the record back after writing it, and
// returns the read-back value. Compare it against controls to detect clamping.
func (c *Client) SetStorageRetention(
	ctx context.Context, orgID string, controls StorageRetentionControls,
) (*StorageRetention, error) {
	if err := c.PutPrivate(ctx, storageRetentionRoute, controls, nil, RouteParams(orgID)); err != nil {
		return nil, err
	}

	return c.GetStorageRetention(ctx, orgID)
}
