// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// circleci_usage_export is an ephemeral resource, not a managed resource or a
// data source, because a usage export job fits neither shape:
//
//   - It is not a resource: there is nothing to converge a plan onto (a usage
//     export cannot be "updated" in place) and nothing meaningful to destroy —
//     the job is a fait accompli the moment it exists, and deleting the
//     Terraform resource cannot un-generate it or revoke the signed URLs.
//   - It is not a data source: creating the job is a side-effecting POST, and
//     data sources are documented (and expected by practitioners) to be
//     read-only.
//
// It is a textbook ephemeral resource instead: Open both creates the job and
// waits for it, and the signed download URLs it returns are exactly the kind
// of value that must never be written to a state file, because a state file
// artifact typically outlives the signed URL's validity window and is stored,
// diffed and often shared far more casually than anyone would share a
// bearer credential. Ephemeral values are never persisted to state or plan
// files by Terraform itself — that guarantee is the entire reason this shape
// exists, and it is also why there is no Close: see below.
//
// Usage export is v2 (the CircleCI API in the API, proxying to
// the API) and is served on both CircleCI Cloud and CircleCI
// Server, so unlike the v3-only resources in this provider it is not gated by
// requireCloud.
const (
	// usageExportPollInterval is how often Open re-checks a job's status.
	//
	// GET .../usage_export_job/{id} is rate limited to 10 requests per minute,
	// per path, according to CircleCI's own routing configuration. Polling
	// every 10 seconds caps this resource at 6 requests per minute — enough
	// headroom to poll from a single Open call while leaving room for other
	// activity against the same organization, without ever tripping the limit
	// itself.
	usageExportPollInterval = 10 * time.Second

	// usageExportDefaultPollTimeout bounds how long Open will wait before
	// giving up, so that a stuck or unusually large export cannot hang a plan
	// or apply forever. Ten minutes at a 10-second interval is 60 requests,
	// comfortably covering most exports (small date ranges typically complete
	// within seconds to low minutes) while still failing fast enough that a
	// genuinely stuck job does not tie up a CI run indefinitely. Practitioners
	// exporting unusually large ranges can raise poll_timeout.
	usageExportDefaultPollTimeout = 10 * time.Minute
)

// usageExportPollIntervalVar is the mutable form of usageExportPollInterval:
// tests shorten it so the poll-loop-terminates test does not have to run for
// several real minutes.
var usageExportPollIntervalVar = usageExportPollInterval

// Ensure the implementation satisfies the expected interfaces.
var (
	_ ephemeral.EphemeralResource              = &usageExportEphemeralResource{}
	_ ephemeral.EphemeralResourceWithConfigure = &usageExportEphemeralResource{}
)

// usageExportEphemeralModel maps the ephemeral resource schema. It doubles as
// both the Open request's config and the Open response's result: the
// framework pre-populates OpenResponse.Result from OpenRequest.Config, so
// unknown fields (the computed ones) are the only ones this type needs to
// fill in before calling Result.Set.
type usageExportEphemeralModel struct {
	OrganizationID types.String `tfsdk:"organization_id"`
	Start          types.String `tfsdk:"start"`
	End            types.String `tfsdk:"end"`
	SharedOrgIDs   types.List   `tfsdk:"shared_org_ids"`
	PollTimeout    types.String `tfsdk:"poll_timeout"`
	ID             types.String `tfsdk:"id"`
	State          types.String `tfsdk:"state"`
	ErrorReason    types.String `tfsdk:"error_reason"`
	DownloadURLs   types.List   `tfsdk:"download_urls"`
}

// NewUsageExportEphemeralResource is a helper function to simplify the provider implementation.
func NewUsageExportEphemeralResource() ephemeral.EphemeralResource {
	return &usageExportEphemeralResource{}
}

type usageExportEphemeralResource struct {
	client *circleci.Client
}

// Metadata returns the ephemeral resource type name.
func (e *usageExportEphemeralResource) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_usage_export"
}

// Schema defines the schema for the ephemeral resource.
func (e *usageExportEphemeralResource) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Starts a CircleCI usage export job and waits for it to finish, returning " +
			"signed URLs the job's data can be downloaded from.\n\n" +
			"This is an ephemeral resource rather than a managed resource or a data source: creating the " +
			"job is a side-effecting POST (ruling out a data source), there is nothing to converge a plan " +
			"onto or meaningfully destroy (ruling out a managed resource), and its result is a signed URL " +
			"that grants data access — exactly the kind of value that should never be written to a state " +
			"file, which is what an ephemeral resource guarantees.\n\n" +
			"`Open` creates the job and polls it to completion; there is no `Close`, because there is " +
			"nothing to clean up — the job and its export artifacts are retained by CircleCI on their own " +
			"schedule regardless of what this ephemeral resource does.",
		Attributes: map[string]schema.Attribute{
			"organization_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "UUID of the organization to export usage data for.",
			},
			"start": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Start of the export window, as an RFC 3339 timestamp (e.g. `\"2024-01-01T00:00:00Z\"`).",
			},
			"end": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "End of the export window, as an RFC 3339 timestamp.",
			},
			"shared_org_ids": schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "UUIDs of additional organizations that share billing with `organization_id`, to include in the export.",
			},
			"poll_timeout": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: fmt.Sprintf(
					"How long to wait for the export job to reach a terminal state, as a Go duration "+
						"string (e.g. `\"20m\"`). Defaults to `%s`. Raise this for an unusually large date "+
						"range; CircleCI's own rate limit on checking a job's status (10 requests per minute) "+
						"means this resource polls no more often than every %s regardless of this value.",
					usageExportDefaultPollTimeout, usageExportPollInterval,
				),
			},
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The usage export job's id.",
			},
			"state": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The job's terminal state: `\"completed\"` or `\"failed\"`. `Open` only " +
					"returns once the job has reached one of these — it never returns `\"created\"` or `\"processing\"`.",
			},
			"error_reason": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Why the job failed. Empty unless `state` is `\"failed\"`.",
			},
			"download_urls": schema.ListAttribute{
				ElementType: types.StringType,
				Computed:    true,
				Sensitive:   true,
				MarkdownDescription: "Signed URLs the export's data can be downloaded from. Marked sensitive " +
					"because each URL itself grants access to the data — anyone holding the URL can download " +
					"it, with no further authentication. Because this is ephemeral data, these URLs are never " +
					"written to a state or plan file.",
			},
		},
	}
}

// Configure adds the provider configured client to the ephemeral resource.
func (e *usageExportEphemeralResource) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	client, ok := ephemeralAPIClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	e.client = client
}

// Open creates a usage export job and waits for it to reach a terminal state.
//
// There is deliberately no EphemeralResourceWithClose implementation: a usage
// export job cannot be canceled or deleted through the API (there is no DELETE
// route for it), and the exported data's retention is entirely up to CircleCI,
// not to whether this ephemeral resource instance is later closed. Once
// created, the job simply exists; Close would have nothing to do.
func (e *usageExportEphemeralResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var config usageExportEphemeralModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := time.Parse(time.RFC3339, config.Start.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("start"),
			"Invalid start timestamp",
			fmt.Sprintf("start %q is not an RFC 3339 timestamp: %s", config.Start.ValueString(), err),
		)
	}
	if _, err := time.Parse(time.RFC3339, config.End.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("end"),
			"Invalid end timestamp",
			fmt.Sprintf("end %q is not an RFC 3339 timestamp: %s", config.End.ValueString(), err),
		)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	pollTimeout := usageExportDefaultPollTimeout
	if !config.PollTimeout.IsNull() && config.PollTimeout.ValueString() != "" {
		parsed, err := time.ParseDuration(config.PollTimeout.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("poll_timeout"),
				"Invalid poll_timeout",
				fmt.Sprintf("poll_timeout %q is not a valid duration: %s", config.PollTimeout.ValueString(), err),
			)

			return
		}
		pollTimeout = parsed
	}

	var sharedOrgIDs []string
	if !config.SharedOrgIDs.IsNull() {
		resp.Diagnostics.Append(config.SharedOrgIDs.ElementsAs(ctx, &sharedOrgIDs, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	orgID := config.OrganizationID.ValueString()

	job, err := e.client.UsageExports().Create(ctx, orgID, circleci.CreateUsageExportJobRequest{
		Start:        config.Start.ValueString(),
		End:          config.End.ValueString(),
		SharedOrgIDs: sharedOrgIDs,
	})
	if err != nil {
		resp.Diagnostics.AddError("Error creating CircleCI usage export job", circleci.Detail(err))

		return
	}

	status, err := e.awaitTerminal(ctx, orgID, job.UsageExportJobID, job.State, pollTimeout)
	if err != nil {
		resp.Diagnostics.AddError("Timed out waiting for CircleCI usage export job", err.Error())

		return
	}

	if status.State == circleci.UsageExportJobStateFailed {
		resp.Diagnostics.AddError(
			"CircleCI usage export job failed",
			fmt.Sprintf("Usage export job %s failed: %s", status.UsageExportJobID, status.ErrorReason),
		)

		return
	}

	downloadURLs, diags := types.ListValueFrom(ctx, types.StringType, status.DownloadURLs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	config.ID = types.StringValue(status.UsageExportJobID)
	config.State = types.StringValue(status.State)
	config.ErrorReason = types.StringValue(status.ErrorReason)
	config.DownloadURLs = downloadURLs

	resp.Diagnostics.Append(resp.Result.Set(ctx, &config)...)
}

// awaitTerminal polls a usage export job until it reaches "completed" or
// "failed", respecting ctx cancellation and a bounded, hard timeout so that a
// stuck job cannot hang a Terraform plan or apply forever.
func (e *usageExportEphemeralResource) awaitTerminal(
	ctx context.Context, orgID, jobID, initialState string, timeout time.Duration,
) (*circleci.UsageExportJobStatus, error) {
	state := initialState
	if isTerminalUsageExportState(state) {
		return e.client.UsageExports().Get(ctx, orgID, jobID)
	}

	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// pollCtxTimeoutErr renders the diagnostic for pollCtx having expired,
	// distinguishing an outer cancellation from the poll_timeout cap. It is
	// called from two places: expiry can be observed either as pollCtx.Done()
	// winning the select below, or as the in-flight Get call below failing
	// because pollCtx (which it is called with) expired out from under it —
	// both must produce this message, not the generic "polling ... failed"
	// one, or the timeout case would unpredictably lose its useful detail
	// depending on exactly when the deadline lands relative to a request.
	pollCtxTimeoutErr := func() error {
		if ctx.Err() != nil {
			return fmt.Errorf(
				"context canceled while waiting for usage export job %s (last known state %q): %w",
				jobID, state, ctx.Err(),
			)
		}

		return fmt.Errorf(
			"usage export job %s did not reach a terminal state within %s (last known state %q). "+
				"The job may still complete on CircleCI's side; check it with "+
				"GET /api/v2/organizations/%s/usage_export_job/%s, or raise poll_timeout",
			jobID, timeout, state, orgID, jobID,
		)
	}

	ticker := time.NewTicker(usageExportPollIntervalVar)
	defer ticker.Stop()

	for {
		select {
		case <-pollCtx.Done():
			return nil, pollCtxTimeoutErr()
		case <-ticker.C:
			status, err := e.client.UsageExports().Get(pollCtx, orgID, jobID)
			if err != nil {
				if pollCtx.Err() != nil {
					return nil, pollCtxTimeoutErr()
				}

				return nil, fmt.Errorf("polling usage export job %s: %w", jobID, err)
			}

			state = status.State
			if isTerminalUsageExportState(state) {
				return status, nil
			}
		}
	}
}

// isTerminalUsageExportState reports whether state is one Open should stop
// polling at.
func isTerminalUsageExportState(state string) bool {
	return state == circleci.UsageExportJobStateCompleted || state == circleci.UsageExportJobStateFailed
}
