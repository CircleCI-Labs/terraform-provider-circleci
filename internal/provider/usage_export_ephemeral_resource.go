// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
// Usage export is v2, which for a long time was taken here to mean it worked
// on CircleCI Server too. It does not, and the API version was the wrong
// thing to reason from: what matters is who owns the route's backend.
// CircleCI Server's gateway does route both usage export paths, but Server
// does not deploy the backend that serves them, and is handed a placeholder
// upstream instead. The routes therefore exist on Server and cannot succeed
// there. See requireUsageExportCloud.
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

// usageExportTypeName is used in diagnostics, including the Cloud-only error.
const usageExportTypeName = "circleci_usage_export"

// requireUsageExportCloud refuses CircleCI Server.
//
// This is not requireCloud from configure.go, for one reason: that function's
// message says the type "is backed by the CircleCI v3 API, which is not available
// on CircleCI Server", and here that would be false in a way that matters. Usage
// export is v2, Server routes it, and a practitioner told otherwise would go
// looking for a version problem that is not there. The real reason is that Server
// does not run the service behind the route, so the message says that instead.
//
// It takes typeName as its second argument even though it serves exactly one
// type, so that availability_test.go's AST scan can read the gated type name out
// of the call site the way it does for the two gates in configure.go and
// standalone_only.go. That test is what stops this type's `## Availability` table
// from drifting back to claiming Server support, and a gate it cannot see is a
// page nothing checks -- which is how the wrong verdict survived here in the
// first place.
func requireUsageExportCloud(client *circleci.Client, typeName string, diags *diag.Diagnostics) bool {
	// An unconfigured client is not a Server client, and it is not this gate's
	// business either: Open dereferences it a few lines later regardless, so
	// answering true here leaves that path exactly as it was rather than turning a
	// provider-wiring bug into a misleading deployment diagnostic.
	if client == nil || client.IsCloud() {
		return true
	}

	diags.AddError(
		typeName+" requires CircleCI Cloud",
		fmt.Sprintf(
			"%s starts a usage export job, which CircleCI Server cannot run: the export is produced "+
				"by a reporting service that CircleCI Server does not deploy. CircleCI Server does "+
				"route the usage export API paths, so the request would be accepted and then fail "+
				"inside CircleCI rather than returning a clean 404 -- which is why this is refused "+
				"here instead.\n\n"+
				"The provider is configured with deployment = %q for host %q. Remove this ephemeral "+
				"resource from configurations that target CircleCI Server.",
			typeName, string(client.Deployment()), client.Host(),
		),
	)

	return false
}

// Ensure the implementation satisfies the expected interfaces.
var (
	_ ephemeral.EphemeralResource                     = &usageExportEphemeralResource{}
	_ ephemeral.EphemeralResourceWithConfigure        = &usageExportEphemeralResource{}
	_ ephemeral.EphemeralResourceWithConfigValidators = &usageExportEphemeralResource{}
)

// usageExportEphemeralModel maps the ephemeral resource schema. It doubles as
// both the Open request's config and the Open response's result: the
// framework pre-populates OpenResponse.Result from OpenRequest.Config, so
// unknown fields (the computed ones) are the only ones this type needs to
// fill in before calling Result.Set.
type usageExportEphemeralModel struct {
	OrganizationID types.String `tfsdk:"organization_id"`
	OrgID          types.String `tfsdk:"org_id"`
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
			"schedule regardless of what this ephemeral resource does.\n\n" +
			"~> **CircleCI Cloud only.** Unlike this provider's other Cloud-only types this one is not " +
			"v3, and CircleCI Server does route its paths — but Server does not run the reporting " +
			"service that produces the export, so a request there is accepted and then fails inside " +
			"CircleCI. Using this with `deployment = \"server\"` reports an error instead.",
		Attributes: map[string]schema.Attribute{
			// An ephemeral resource holds no state, so there is nothing to replace
			// and no prior value to retain: both names are plain Optional, with
			// exactly one required. See org_id_deprecation.go.
			"organization_id": deprecatedOrgIDEphemeralAttribute("the usage data being exported"),
			"org_id":          orgIDEphemeralAttribute("the usage data being exported"),
			"start": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Start of the export window, as an RFC 3339 timestamp (e.g. " +
					"`\"2024-01-01T00:00:00Z\"`). CircleCI refuses a `start` more than 366 days in the " +
					"past, or in the future.",
			},
			"end": schema.StringAttribute{
				Required: true,
				MarkdownDescription: fmt.Sprintf(
					"End of the export window, as an RFC 3339 timestamp. CircleCI caps a single export at "+
						"%s and refuses an `end` before `start` or in the future; both of the first two are "+
						"checked before any request is made. Split a wider range across several exports.",
					circleci.UsageExportMaxWindow,
				),
			},
			"shared_org_ids": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				MarkdownDescription: "UUIDs of additional organizations that share billing with `org_id`, " +
					"to include in the export. CircleCI honours this rather than ignoring it, but rejects " +
					"the whole request if any entry is not a UUID, without saying which — so entries are " +
					"checked here first.",
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
				MarkdownDescription: fmt.Sprintf(
					"Signed URLs the export's data can be downloaded from. Marked sensitive because each "+
						"URL itself grants access to the data — anyone holding the URL can download it, with "+
						"no further authentication. Because this is ephemeral data, these URLs are never "+
						"written to a state or plan file.\n\n"+
						"Each URL is minted at the moment the job was observed to be complete and is valid "+
						"for %s from then, so consume them within the same run rather than passing them on.",
					circleci.UsageExportURLValidity,
				),
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

// ConfigValidators requires exactly one of the two organization attribute names.
func (e *usageExportEphemeralResource) ConfigValidators(_ context.Context) []ephemeral.ConfigValidator {
	return []ephemeral.ConfigValidator{
		orgIDEphemeralConfigValidator(),
	}
}

// Open creates a usage export job and waits for it to reach a terminal state.
//
// There is deliberately no EphemeralResourceWithClose implementation: a usage
// export job cannot be canceled or deleted through the API (there is no DELETE
// route for it), and the exported data's retention is entirely up to CircleCI,
// not to whether this ephemeral resource instance is later closed. Once
// created, the job simply exists; Close would have nothing to do.
func (e *usageExportEphemeralResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	if !requireUsageExportCloud(e.client, usageExportTypeName, &resp.Diagnostics) {
		return
	}

	var config usageExportEphemeralModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	start, ok := parseUsageExportTimestamp(config.Start, "start", &resp.Diagnostics)
	end, endOK := parseUsageExportTimestamp(config.End, "end", &resp.Diagnostics)
	if !ok || !endOK {
		return
	}

	validateUsageExportWindow(start, end, &resp.Diagnostics)
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

		validateUsageExportSharedOrgIDs(sharedOrgIDs, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	// Whichever of the two organization attribute names the configuration set. It
	// is not mirrored onto the other in the result: both are plain Optional rather
	// than Optional+Computed — an ephemeral resource has no state for a retained
	// value to live in — so filling one in would be a result that disagrees with
	// the configuration. See org_id_deprecation.go.
	orgID := effectiveOrgID(config.OrganizationID, config.OrgID)

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

// parseUsageExportTimestamp parses one bound of the export window.
//
// The service binds both bounds into a time.Time and reports any failure as one
// unattributed "malformed request body", so parsing here is what makes it
// possible to say which of the two was wrong.
func parseUsageExportTimestamp(value types.String, name string, diagnostics *diag.Diagnostics) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339, value.ValueString())
	if err != nil {
		diagnostics.AddAttributeError(path.Root(name),
			"Invalid "+name+" timestamp",
			fmt.Sprintf("%s %q is not an RFC 3339 timestamp: %s", name, value.ValueString(), err),
		)

		return time.Time{}, false
	}

	return parsed, true
}

// validateUsageExportWindow rejects the two window shapes the service refuses
// that can be decided from the configuration alone.
//
// The service checks four things, and only these two are wall-clock independent:
// end before start, and a window wider than UsageExportMaxWindow. The other two
// -- start further back than UsageExportMaxAge, and either bound in the future --
// depend on when the apply runs, so they are documented on the attributes rather
// than enforced here. Enforcing them would mean this provider deciding what "now"
// is, and disagreeing with CircleCI by a few seconds either way is worse than
// letting CircleCI answer: a configuration refused here cannot be applied at all,
// where one refused by CircleCI at least says so with its own message.
func validateUsageExportWindow(start, end time.Time, diagnostics *diag.Diagnostics) {
	if end.Before(start) {
		diagnostics.AddAttributeError(path.Root("end"),
			"Export window ends before it starts",
			fmt.Sprintf(
				"end (%s) is before start (%s). CircleCI refuses this window rather than treating it "+
					"as an empty export.",
				end.Format(time.RFC3339), start.Format(time.RFC3339),
			),
		)

		return
	}

	if end.Sub(start) > circleci.UsageExportMaxWindow {
		diagnostics.AddAttributeError(path.Root("end"),
			"Export window is too wide",
			fmt.Sprintf(
				"start (%s) to end (%s) is %s, and CircleCI caps a single usage export at %s. Split "+
					"the range across several exports.",
				start.Format(time.RFC3339), end.Format(time.RFC3339),
				end.Sub(start), circleci.UsageExportMaxWindow,
			),
		)
	}
}

// validateUsageExportSharedOrgIDs rejects a shared organization id that is not a
// UUID.
//
// The service binds this field to a slice of UUIDs, so one bad entry fails the
// whole request body with the same unattributed "malformed request body" a bad
// timestamp gives -- naming neither the field nor the entry. Checking here is the
// only way a practitioner learns which id was wrong.
func validateUsageExportSharedOrgIDs(sharedOrgIDs []string, diagnostics *diag.Diagnostics) {
	for i, id := range sharedOrgIDs {
		if _, err := uuid.Parse(id); err != nil {
			diagnostics.AddAttributeError(path.Root("shared_org_ids").AtListIndex(i),
				"Invalid shared organization id",
				fmt.Sprintf(
					"shared_org_ids[%d] is %q, which is not a UUID. CircleCI rejects the whole request "+
						"body when any entry is not a UUID, without naming the offending one.",
					i, id,
				),
			)
		}
	}
}
