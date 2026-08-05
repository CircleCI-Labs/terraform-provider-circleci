// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                     = &otelExporterResource{}
	_ resource.ResourceWithConfigure        = &otelExporterResource{}
	_ resource.ResourceWithImportState      = &otelExporterResource{}
	_ resource.ResourceWithConfigValidators = &otelExporterResource{}
	_ resource.ResourceWithValidateConfig   = &otelExporterResource{}
	_ resource.ResourceWithModifyPlan       = &otelExporterResource{}
)

// otelExporterTypeName is the Terraform type name.
const otelExporterTypeName = "circleci_otel_exporter"

// otelEndpointPattern matches either form of endpoint the service that validates
// the request accepts: a bare "host:port", or an http/https URL.
//
// It used to be `^[A-Za-z0-9._\-\[\]:]+$`, on the strength of the published
// OpenAPI description — "Don't include https:// or grpc://. Just the hostname and
// port are required." Reading the enforcing service shows that description is
// incomplete in one direction and too loose in another:
//
//   - A URL endpoint IS accepted. ValidateEndpoint parses the value with net/url
//     first and takes that branch whenever the scheme is http or https, and
//     ValidateProtocol has a dedicated error for combining such an endpoint with
//     grpc — a rule that could not exist if the form itself were invalid. The old
//     pattern rejected, at plan time, a configuration the API would have accepted.
//   - The port is NOT optional in the bare form. That branch is net.SplitHostPort,
//     which fails outright on a value with no port, so "otel.example.com" was
//     accepted here and then rejected as malformed by the API.
//
// A scheme other than http or https still matches neither branch there, so
// "grpc://otel.example.com:4317" stays a plan-time error.
var otelEndpointPattern = regexp.MustCompile(
	`^(?:` +
		// http/https URL: host, optional port, optional path.
		`https?://[^\s/?#]+(?:/[^\s?#]*)?` +
		`|` +
		// Bare host and mandatory port, including a bracketed IPv6 literal.
		`(?:[A-Za-z0-9._\-]+|\[[0-9A-Fa-f:.]+\]):[0-9]{1,5}` +
		`)$`,
)

// otelEndpointDescription is the human half of otelEndpointPattern, shared by the
// validator message and the attribute description so the two cannot disagree.
const otelEndpointDescription = "must be either a bare host and port such as " +
	`"otel.example.com:4317" (the port is required), or an http:// or https:// URL such as ` +
	`"https://otel.example.com/v1/traces", which is only valid with protocol = "http"`

// otelExperimentalNote is the experimental warning shared by the OTLP exporter
// resource and data source. CircleCI marks these endpoints experimental, so they
// may change or disappear outside the v2 API's usual deprecation process.
const otelExperimentalNote = "~> **Experimental.** CircleCI flags the OTLP exporter endpoints as " +
	"experimental. Their shape may change, or they may be withdrawn, without the notice the rest of " +
	"the v2 API carries. Pin the provider version if that matters to you."

// otelExporterResourceModel maps the resource schema.
type otelExporterResourceModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	OrgID          types.String `tfsdk:"org_id"`
	Endpoint       types.String `tfsdk:"endpoint"`
	Protocol       types.String `tfsdk:"protocol"`
	Insecure       types.Bool   `tfsdk:"insecure"`
	Headers        types.Map    `tfsdk:"headers"`
	// HeadersWO is always null here. The framework nullifies a write-only
	// attribute in plan and state, so the field exists only to satisfy the schema;
	// the value is read from configuration by resolveOTelHeaders.
	HeadersWO        types.Map   `tfsdk:"headers_wo"`
	HeadersWOVersion types.Int64 `tfsdk:"headers_wo_version"`
	// HeadersWONames is populated only on the write-only path; see
	// otel_exporter_write_only.go.
	HeadersWONames types.Set  `tfsdk:"headers_wo_names"`
	Issues         types.List `tfsdk:"issues"`
}

// NewOTelExporterResource is a helper function to simplify the provider implementation.
func NewOTelExporterResource() resource.Resource {
	return &otelExporterResource{}
}

// otelExporterResource is the resource implementation.
type otelExporterResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *otelExporterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_otel_exporter"
}

// Schema defines the schema for the resource.
func (r *otelExporterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an OTLP exporter: where CircleCI sends OpenTelemetry traces for an " +
			"organization's pipelines. **CircleCI Cloud only.** The route is `/api/v2`, but that is not " +
			"enough here: CircleCI's public API service forwards it to a separate service, and a " +
			"CircleCI Server installation neither runs that service nor routes to it, so the endpoint " +
			"does not exist there. This is refused during `terraform plan` rather than at apply.\n\n" +
			otelExperimentalNote + "\n\n" +
			"~> **The API has no update route.** Every configurable attribute forces a new resource, so " +
			"changing an endpoint, protocol or header destroys the exporter and creates a replacement " +
			"with a new `id`. Traces are not exported during the gap.\n\n" +
			fmt.Sprintf(
				"~> **At most %d exporters per organization.** Creating one beyond the limit is rejected.",
				circleci.OTelExporterLimit,
			) + "\n\n" +
			"Headers usually carry the collector's credentials. `headers` records them in " +
			"Terraform state in cleartext; `headers_wo` is the same argument as a write-only " +
			"one, sent to CircleCI and never persisted (Terraform 1.11 or later). Set at most " +
			"one of the two. See the [Managing secrets](../guides/managing-secrets) guide for " +
			"how the two compare, including the drift detection the write-only path gives up.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the exporter, assigned by CircleCI.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// See org_id_deprecation.go for why these are Optional+Computed and why
			// replacement is conditional on being configured.
			"organization_id": deprecatedOrgIDAttribute("this exporter", true),
			"org_id":          orgIDAttribute("this exporter", true),
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Where CircleCI sends spans. Two forms are accepted:\n\n" +
					"- a bare host and port, such as `otel.example.com:4317` — the port is required; or\n" +
					"- an `http://` or `https://` URL, such as `https://otel.example.com/v1/traces`, " +
					"which is only valid together with `protocol = \"http\"`.\n\n" +
					"Any other scheme, `grpc://` included, is rejected. The host must resolve publicly: " +
					"CircleCI refuses an endpoint that resolves to a private, loopback or link-local " +
					"address. Changing this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(otelEndpointPattern, otelEndpointDescription),
				},
			},
			"protocol": schema.StringAttribute{
				MarkdownDescription: "The OTLP transport: `" + circleci.OTelProtocolGRPC + "` (usually " +
					"port 4317) or `" + circleci.OTelProtocolHTTP + "` (usually port 4318). " +
					"Changing this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf(
						circleci.OTelProtocolGRPC,
						circleci.OTelProtocolHTTP,
					),
				},
			},
			"insecure": schema.BoolAttribute{
				MarkdownDescription: "Whether to connect to the endpoint without transport security. " +
					"CircleCI defaults this to `false`. Leave it false unless the collector is reachable only over a " +
					"private network: headers, including any credentials, travel in the clear otherwise. " +
					"Changing this value forces a new resource to be created.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					// RequiresReplaceIfConfigured rather than plain RequiresReplace: plain
					// RequiresReplace fires on any planned-vs-state difference with no
					// exception for an unknown planned value, so it would plan a spurious
					// replacement whenever insecure's configured expression is itself
					// unknown at plan time.
					boolplanmodifier.RequiresReplaceIfConfigured(),

					// UseStateForUnknown, and deliberately NO Default, because the two
					// together crashed apply. With a Default, the framework overwrites the
					// planned value whenever the *config* value is null — it never consults
					// prior state — so removing `insecure = true` from a configuration
					// planned insecure as false. RequiresReplaceIfConfigured then declined
					// to fire (the config value being null is exactly its bail-out
					// condition), so it planned an in-place update; Update is a no-op
					// because the API has no update route, leaving true in state against a
					// plan of false: "Provider produced inconsistent result after apply".
					//
					// Without the Default, an omitted insecure plans as unknown and resolves
					// to the value already in state, which is the ordinary
					// Optional+Computed meaning of "not configured" — leave it alone. The
					// API's own default is still false; that is stated in the description
					// rather than enforced here.
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"headers": schema.MapAttribute{
				MarkdownDescription: "Extra headers sent with each export, typically the collector's " +
					"credentials. Changing this value forces a new resource to be created.\n\n" +
					"They are recorded in Terraform state in cleartext. Use `headers_wo` instead to " +
					"keep them out of state, at the cost of having to bump `headers_wo_version` to " +
					"rotate them and of losing the drift detection described below. Set at most one " +
					"of the two; setting neither sends no headers.\n\n" +
					"~> **Header values cannot be read back.** CircleCI encrypts them at rest and every " +
					"read answers with the placeholder `" + circleci.OTelRedactedHeaderValue + "`, so " +
					"Terraform cannot detect a value changed outside Terraform. A header *added or " +
					"removed* outside Terraform is detected, because the names are returned in full.",
				ElementType: types.StringType,
				Optional:    true,
				Sensitive:   true,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.RequiresReplace(),
				},
				Validators: []validator.Map{
					mapvalidator.KeysAre(stringvalidator.LengthAtLeast(1)),
				},
			},
			// See otel_exporter_write_only.go, including why these two are
			// Conflicting with `headers` rather than ExactlyOneOf against it.
			"headers_wo":         otelHeadersWriteOnlyAttribute(),
			"headers_wo_version": otelHeadersWriteOnlyVersionAttribute(),
			"headers_wo_names":   otelHeadersWriteOnlyNamesAttribute(),
			"issues": schema.ListAttribute{
				MarkdownDescription: "Validation problems CircleCI has detected with this exporter, such " +
					"as an endpoint that no longer resolves. Empty when there are none.",
				ElementType: types.StringType,
				Computed:    true,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names,
// and refuses `headers` and `headers_wo` together.
func (r *otelExporterResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		orgIDConfigValidator(),
		otelHeadersConfigValidator(),
	}
}

// ValidateConfig refuses a URL-style endpoint combined with `protocol = "grpc"`.
//
// The rule belongs to the API — "protocol must be 'http' when endpoint starts with
// http:// or https://" — and cannot be expressed as an attribute validator,
// because it depends on two attributes at once. ValidateConfig runs before the
// plan and needs no client, so this is the earliest place it can be caught; the
// alternative is an HTTP 400 mid-apply.
func (r *otelExporterResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config otelExporterResourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A value that is null or still unknown (an interpolation of something not yet
	// created) cannot be checked here; the API rejects it if it turns out wrong.
	if config.Endpoint.IsNull() || config.Endpoint.IsUnknown() ||
		config.Protocol.IsNull() || config.Protocol.IsUnknown() {
		return
	}

	endpoint := config.Endpoint.ValueString()
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return
	}

	if config.Protocol.ValueString() == circleci.OTelProtocolHTTP {
		return
	}

	resp.Diagnostics.AddAttributeError(
		path.Root("protocol"),
		"Invalid protocol for a URL endpoint",
		fmt.Sprintf(
			"protocol must be %q when endpoint is an http:// or https:// URL, but it is %q and "+
				"endpoint is %q.\n\nUse a bare host and port such as \"otel.example.com:4317\" for "+
				"%s, or set protocol = %q for this endpoint.",
			circleci.OTelProtocolHTTP, config.Protocol.ValueString(), endpoint,
			circleci.OTelProtocolGRPC, circleci.OTelProtocolHTTP,
		),
	)
}

// ModifyPlan gates the resource on CircleCI Cloud at plan time.
//
// The route is /api/v2, but v2 is not sufficient here: /api/v2/otel is a
// pass-through to a backend CircleCI Server neither deploys nor routes to —
// see otelExportersRoute in internal/circleci. Without this gate
// `terraform plan` succeeds against a Server host and the create fails with a
// 404 mid-apply. Destroy is exempt, so a resource stranded in state by a
// deployment change stays removable.
//
// requireCloud's message says "backed by the CircleCI v3 API", which is not the
// reason in this case; `circleci_audit_log_config` is gated with the same
// approximation for the same kind of reason.
func (r *otelExporterResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, otelExporterTypeName, &resp.Diagnostics)
}

// Create creates the resource and sets the initial Terraform state.
func (r *otelExporterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	// r.client != nil rather than the usual "r.client == nil || !requireCloud":
	// returning early on an unconfigured client would make Create a silent no-op,
	// and the schema-level diagnostics below are more use than that.
	if r.client != nil && !requireCloud(r.client, otelExporterTypeName, &resp.Diagnostics) {
		return
	}

	var plan otelExporterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// From configuration, because `headers_wo` is null in the plan. See
	// otel_exporter_write_only.go.
	headers, ok := resolveOTelHeaders(
		ctx, req.Config, plan.Headers, plan.HeadersWOVersion, &resp.Diagnostics,
	)
	if !ok {
		return
	}

	orgID := effectiveOrgID(plan.OrganizationID, plan.OrgID)

	exporter, err := r.client.CreateOTelExporter(ctx, circleci.CreateOTelExporterRequest{
		OrgID:    orgID,
		Endpoint: plan.Endpoint.ValueString(),
		Protocol: plan.Protocol.ValueString(),
		Insecure: plan.Insecure.ValueBool(),
		Headers:  headers,
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI OTLP exporter",
			fmt.Sprintf(
				"Could not create an OTLP exporter for organization %s: %s\n\n"+
					"An organization may have at most %d exporters, and the endpoint must be a bare "+
					"host:port with no scheme.",
				orgID, circleci.Detail(err), circleci.OTelExporterLimit,
			),
		)

		return
	}

	// Everything but the headers comes from the response. The headers stay as
	// planned — the configured value on the `headers` path, null on the
	// `headers_wo` one: the API answers with redacted values, and writing those
	// into state would both lose the real values and make the applied state differ
	// from the plan.
	plan.ID = types.StringValue(exporter.ID)
	setOrgIDs(&plan.OrganizationID, &plan.OrgID, orgID)
	plan.Endpoint = types.StringValue(exporter.Endpoint)
	plan.Protocol = types.StringValue(exporter.Protocol)
	plan.Insecure = types.BoolValue(exporter.Insecure)

	headerNames, diags := otelHeadersWriteOnlyNamesAfterCreate(ctx, plan.HeadersWOVersion, exporter.Headers)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.HeadersWONames = headerNames

	issues, diags := otelIssuesValue(ctx, exporter.Issues)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.Issues = issues

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *otelExporterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client != nil && !requireCloud(r.client, otelExporterTypeName, &resp.Diagnostics) {
		return
	}

	var state otelExporterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := effectiveOrgID(state.OrganizationID, state.OrgID)

	exporter, err := r.client.GetOTelExporter(ctx, orgID, state.ID.ValueString())
	if err != nil {
		// The API has no single-exporter route, so a missing exporter surfaces as an
		// absence from the organization's list. Only that absence — the ErrNotFound
		// sentinel — may drop the resource from state.
		//
		// A *transport* 404 from the list must not, even though IsNotFound would say
		// yes to it: the list route answers 404 "Org not found" both for an
		// organization that does not exist and for a token that cannot manage the one
		// that does. Dropping state there would have the next apply create a second
		// exporter alongside the live one, against a limit of
		// circleci.OTelExporterLimit — so a token losing a permission would silently
		// duplicate an exporter and then start failing at the limit. Report it
		// instead and leave state alone.
		if errors.Is(err, circleci.ErrNotFound) {
			resp.State.RemoveResource(ctx)

			return
		}

		if circleci.HasStatus(err, http.StatusNotFound) {
			resp.Diagnostics.AddError(
				"Error reading CircleCI OTLP exporter",
				fmt.Sprintf(
					"CircleCI answered 404 for organization %s while reading OTLP exporter %s. The "+
						"exporter itself is read by listing the organization's exporters, so this is "+
						"about the organization, not the exporter: either organization %s does not "+
						"exist, or the API token cannot manage it.\n\n"+
						"%s has been left in Terraform state, because removing it would create a "+
						"second exporter on the next apply while the first one is still live.",
					orgID, state.ID.ValueString(), orgID, otelExporterTypeName,
				),
			)

			return
		}

		resp.Diagnostics.AddError(
			"Error reading CircleCI OTLP exporter",
			fmt.Sprintf(
				"Could not read OTLP exporter %s for organization %s: %s",
				state.ID.ValueString(), orgID, circleci.Detail(err),
			),
		)

		return
	}

	setOrgIDs(&state.OrganizationID, &state.OrgID, orgID)
	state.Endpoint = types.StringValue(exporter.Endpoint)
	state.Protocol = types.StringValue(exporter.Protocol)
	state.Insecure = types.BoolValue(exporter.Insecure)

	headers, headerNames, diags := otelHeadersAfterRead(
		ctx, state.Headers, state.HeadersWONames, state.HeadersWOVersion, exporter.Headers,
	)
	resp.Diagnostics.Append(diags...)

	issues, diags := otelIssuesValue(ctx, exporter.Issues)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Headers = headers
	state.HeadersWONames = headerNames
	state.Issues = issues

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is a no-op. The CircleCI API has no endpoint for updating an OTLP
// exporter, so every configurable attribute is marked RequiresReplace and
// Terraform destroys and recreates the exporter instead of ever calling this. It
// exists only to satisfy the resource.Resource interface.
func (r *otelExporterResource) Update(_ context.Context, _ resource.UpdateRequest, _ *resource.UpdateResponse) {
}

// Delete deletes the resource and removes the Terraform state on success.
//
// Deliberately not gated on requireCloud, unlike Create and Read: an exporter
// left in state after `deployment` changed to "server" must stay removable. See
// DESIGN.md, "Cloud-only gating happens at plan time".
func (r *otelExporterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state otelExporterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The delete route takes only the exporter ID; the organization is implied.
	if err := r.client.DeleteOTelExporter(ctx, state.ID.ValueString()); err != nil {
		// An exporter someone already removed outside Terraform is not a failure:
		// the desired end state is reached either way.
		if circleci.IsNotFound(err) {
			return
		}

		resp.Diagnostics.AddError(
			"Error deleting CircleCI OTLP exporter",
			fmt.Sprintf(
				"Could not delete OTLP exporter %s: %s",
				state.ID.ValueString(), circleci.Detail(err),
			),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *otelExporterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing OTLP exporter into Terraform state.
// Expected import ID format: "organization_id/exporter_id". Both are needed
// because reading an exporter means listing its organization's exporters.
func (r *otelExporterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	organizationID, exporterID, found := strings.Cut(req.ID, "/")
	if !found || organizationID == "" || exporterID == "" || strings.Contains(exporterID, "/") {
		resp.Diagnostics.AddError(
			"Invalid Import ID Format",
			fmt.Sprintf(
				"Expected an import ID in the format \"organization_id/exporter_id\", got: %q.\n\n"+
					"Both parts are required: the API has no route for a single exporter, so reading one "+
					"means listing its organization's exporters.\n\n"+
					"For example:\n  terraform import %s.example "+
					"\"00000000-0000-0000-0000-000000000000/11111111-1111-1111-1111-111111111111\"",
				req.ID, otelExporterTypeName,
			),
		)

		return
	}

	// Both organization attribute names are set, so a configuration written
	// against either one imports cleanly. See org_id_deprecation.go.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), organizationID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("org_id"), organizationID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), exporterID)...)
}

// otelRefreshHeaders folds the API's header map back into state.
//
// Header values are never disclosed: a read answers with
// circleci.OTelRedactedHeaderValue for every value, so adopting them would
// destroy the configured values and then force a replacement on the next plan.
// The header *names* are returned in full, though, so the key sets can be
// compared: when they match, the prior values are kept and no drift is reported;
// when they differ, someone added or removed a header outside Terraform and the
// remote map is adopted so the change is visible.
func otelRefreshHeaders(ctx context.Context, prior types.Map, remote map[string]string) (types.Map, diag.Diagnostics) {
	var diags diag.Diagnostics

	priorElements := prior.Elements()

	if len(remote) == 0 {
		if len(priorElements) == 0 {
			// Keep a null map null rather than turning it into an empty one.
			return prior, diags
		}

		return types.MapNull(types.StringType), diags
	}

	if len(priorElements) == len(remote) {
		sameKeys := true
		for name := range remote {
			if _, ok := priorElements[name]; !ok {
				sameKeys = false

				break
			}
		}

		if sameKeys {
			return prior, diags
		}
	}

	return types.MapValueFrom(ctx, types.StringType, remote)
}

// otelIssuesValue converts the API's issues list into an attribute value,
// normalizing an absent list to an empty one so practitioners can always iterate
// over it.
func otelIssuesValue(ctx context.Context, issues []string) (types.List, diag.Diagnostics) {
	if issues == nil {
		issues = []string{}
	}

	return types.ListValueFrom(ctx, types.StringType, issues)
}
