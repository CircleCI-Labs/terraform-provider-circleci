// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
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
	_ resource.Resource                = &otelExporterResource{}
	_ resource.ResourceWithConfigure   = &otelExporterResource{}
	_ resource.ResourceWithImportState = &otelExporterResource{}
)

// otelExporterTypeName is the Terraform type name.
const otelExporterTypeName = "circleci_otel_exporter"

// otelEndpointPattern matches a bare OTLP endpoint: a host, optionally
// bracketed for IPv6, optionally followed by a port. It exists to reject a
// scheme or a path, which the API documents as invalid — turning what would be
// an opaque HTTP 400 into a plan-time error.
var otelEndpointPattern = regexp.MustCompile(`^[A-Za-z0-9._\-\[\]:]+$`)

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
	Endpoint       types.String `tfsdk:"endpoint"`
	Protocol       types.String `tfsdk:"protocol"`
	Insecure       types.Bool   `tfsdk:"insecure"`
	Headers        types.Map    `tfsdk:"headers"`
	Issues         types.List   `tfsdk:"issues"`
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
			"organization's pipelines. Available on CircleCI Cloud and CircleCI Server.\n\n" +
			otelExperimentalNote + "\n\n" +
			"~> **The API has no update route.** Every configurable attribute forces a new resource, so " +
			"changing an endpoint, protocol or header destroys the exporter and creates a replacement " +
			"with a new `id`. Traces are not exported during the gap.\n\n" +
			fmt.Sprintf(
				"~> **At most %d exporters per organization.** Creating one beyond the limit is rejected.",
				circleci.OTelExporterLimit,
			),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the exporter, assigned by CircleCI.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"organization_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization the exporter belongs to. " +
					"Changing this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "The OTLP endpoint spans are sent to, as `host:port` — for example " +
					"`otel.example.com:4317`. Do **not** include a scheme: `https://` or `grpc://` is " +
					"rejected. Changing this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(
						otelEndpointPattern,
						"must be a bare host and port with no scheme and no path, "+
							"e.g. \"otel.example.com:4317\"",
					),
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
					"Defaults to `false`. Leave it false unless the collector is reachable only over a " +
					"private network: headers, including any credentials, travel in the clear otherwise. " +
					"Changing this value forces a new resource to be created.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"headers": schema.MapAttribute{
				MarkdownDescription: "Extra headers sent with each export, typically the collector's " +
					"credentials. Changing this value forces a new resource to be created.\n\n" +
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

// Create creates the resource and sets the initial Terraform state.
func (r *otelExporterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan otelExporterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	headers := map[string]string{}
	if !plan.Headers.IsNull() && !plan.Headers.IsUnknown() {
		resp.Diagnostics.Append(plan.Headers.ElementsAs(ctx, &headers, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	exporter, err := r.client.CreateOTelExporter(ctx, circleci.CreateOTelExporterRequest{
		OrgID:    plan.OrganizationID.ValueString(),
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
				plan.OrganizationID.ValueString(), circleci.Detail(err), circleci.OTelExporterLimit,
			),
		)

		return
	}

	// Everything but the headers comes from the response. The headers stay as
	// configured: the API answers with redacted values, and writing those into
	// state would both lose the real values and make the applied state differ
	// from the plan.
	plan.ID = types.StringValue(exporter.ID)
	plan.Endpoint = types.StringValue(exporter.Endpoint)
	plan.Protocol = types.StringValue(exporter.Protocol)
	plan.Insecure = types.BoolValue(exporter.Insecure)

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
	var state otelExporterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	exporter, err := r.client.GetOTelExporter(ctx,
		state.OrganizationID.ValueString(), state.ID.ValueString())
	if err != nil {
		// The API has no single-exporter route, so a missing exporter surfaces as
		// an absence from the organization's list rather than a 404. IsNotFound
		// covers both.
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Error reading CircleCI OTLP exporter",
			fmt.Sprintf(
				"Could not read OTLP exporter %s for organization %s: %s",
				state.ID.ValueString(), state.OrganizationID.ValueString(), circleci.Detail(err),
			),
		)

		return
	}

	state.Endpoint = types.StringValue(exporter.Endpoint)
	state.Protocol = types.StringValue(exporter.Protocol)
	state.Insecure = types.BoolValue(exporter.Insecure)

	headers, diags := otelRefreshHeaders(ctx, state.Headers, exporter.Headers)
	resp.Diagnostics.Append(diags...)

	issues, diags := otelIssuesValue(ctx, exporter.Issues)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Headers = headers
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

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), organizationID)...)
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
