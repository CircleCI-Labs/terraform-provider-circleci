// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                     = &auditLogConfigResource{}
	_ resource.ResourceWithConfigure        = &auditLogConfigResource{}
	_ resource.ResourceWithImportState      = &auditLogConfigResource{}
	_ resource.ResourceWithModifyPlan       = &auditLogConfigResource{}
	_ resource.ResourceWithConfigValidators = &auditLogConfigResource{}
)

// auditLogConfigTypeName is the Terraform type name, used both for Metadata
// and for the Cloud-only error.
const auditLogConfigTypeName = "circleci_audit_log_config"

// auditLogARNPattern mirrors the API's own validation (an AWS or
// MinIO IAM role ARN), turning what would otherwise be an opaque 400 at apply
// time into a plan-time error.
var auditLogARNPattern = regexp.MustCompile(
	`^(?:arn:(?:aws|aws-us-gov|aws-cn):iam::\d{12}:role/[a-zA-Z0-9+=,.@_-]+` +
		`|arn:(?:aws|minio):iam::(?:\d{12})?:?role/[a-zA-Z0-9+=,.@_-]+)$`,
)

// auditLogBucketNamePattern mirrors the S3 bucket naming rules the owning
// service enforces server-side.
var auditLogBucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`)

// auditLogBucketPrefixPattern rejects a leading or trailing "/". The owning
// service silently strips both server-side, which would otherwise leave the
// applied state permanently different from a configuration that included
// them.
var auditLogBucketPrefixPattern = regexp.MustCompile(`^[^/].*[^/]$|^[^/]$|^$`)

// auditLogConfigResourceModel maps the resource schema.
type auditLogConfigResourceModel struct {
	ID               types.String `tfsdk:"id"`
	OrganizationID   types.String `tfsdk:"organization_id"`
	OrgID            types.String `tfsdk:"org_id"`
	TargetType       types.String `tfsdk:"target_type"`
	IsDisabled       types.Bool   `tfsdk:"is_disabled"`
	ARN              types.String `tfsdk:"arn"`
	Region           types.String `tfsdk:"region"`
	BucketName       types.String `tfsdk:"bucket_name"`
	BucketPrefix     types.String `tfsdk:"bucket_prefix"`
	Endpoint         types.String `tfsdk:"endpoint"`
	ConnectionStatus types.String `tfsdk:"connection_status"`
	CreatedBy        types.String `tfsdk:"created_by"`
	CreatedAt        types.String `tfsdk:"created_at"`
	UpdatedAt        types.String `tfsdk:"updated_at"`
}

// NewAuditLogConfigResource is a helper function to simplify the provider
// implementation.
func NewAuditLogConfigResource() resource.Resource {
	return &auditLogConfigResource{}
}

// auditLogConfigResource is the resource implementation.
type auditLogConfigResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *auditLogConfigResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_audit_log_config"
}

// Schema defines the schema for the resource.
func (r *auditLogConfigResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI audit log streaming config: where an organization's audit " +
			"log events are delivered, as JSON objects written to a customer-owned S3 (or S3-compatible) " +
			"bucket.\n\n" +
			"~> **CircleCI Cloud only, and only on a Scale plan.** This is a v2 API, but the API " +
			"gates it on a Cloud billing plan tier that CircleCI Server installations do not have; " +
			"CircleCI's own docs describe audit log streaming as a Scale-plan feature " +
			"(https://circleci.com/changelog/audit-log-streaming). The provider could not confirm from " +
			"CircleCI Server's routes served whether the underlying route even exists there, so this is " +
			"gated the same way the v3-only resources are, out of caution.\n\n" +
			"~> **Creating a config verifies connectivity to the bucket, even when `is_disabled = true`.** " +
			"CircleCI assumes `arn` via OIDC and writes a probe object; a role that cannot be assumed, or a " +
			"bucket that cannot be written to, fails the create. Updating an existing config to " +
			"`is_disabled = true` does *not* re-verify connectivity, so a config can be disabled even once " +
			"its destination has become unreachable.\n\n" +
			"An organization may have at most one config per `target_type`: a second `S3` (or `S3_COMPATIBLE`) " +
			"config for the same organization is rejected.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the config, assigned by CircleCI.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// See org_id_deprecation.go for why these are Optional+Computed and why
			// replacement is conditional on being configured.
			"organization_id": deprecatedOrgIDAttribute("this audit log streaming config", true),
			"org_id":          orgIDAttribute("this audit log streaming config", true),
			"target_type": schema.StringAttribute{
				MarkdownDescription: "The destination type: `" + circleci.AuditLogTargetTypeS3 + "` (AWS S3; " +
					"`region` is required and `endpoint` must be omitted) or `" +
					circleci.AuditLogTargetTypeS3Compatible + "` (an S3-compatible endpoint such as MinIO; " +
					"`endpoint` is required and `region` defaults to `us-east-1` if omitted). Updatable in " +
					"place.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(circleci.AuditLogTargetTypeS3, circleci.AuditLogTargetTypeS3Compatible),
				},
			},
			"is_disabled": schema.BoolAttribute{
				MarkdownDescription: "Whether streaming is turned off. A disabled config is kept, not " +
					"deleted, and can be re-enabled in place. Defaults to `false`. Updatable in place.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"arn": schema.StringAttribute{
				MarkdownDescription: "The AWS IAM role CircleCI assumes, via OIDC, to write audit log " +
					"objects to the bucket. Must be an AWS or MinIO IAM role ARN. Updatable in place.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtMost(128),
					stringvalidator.RegexMatches(
						auditLogARNPattern,
						"must be an AWS or MinIO IAM role ARN, e.g. "+
							"\"arn:aws:iam::123456789012:role/circleci-audit-logs\"",
					),
				},
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "The bucket's AWS region. Required when `target_type = \"" +
					circleci.AuditLogTargetTypeS3 + "\"`. Optional when `target_type = \"" +
					circleci.AuditLogTargetTypeS3Compatible + "\"`, where CircleCI defaults it to " +
					"`us-east-1` if omitted. Updatable in place.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"bucket_name": schema.StringAttribute{
				MarkdownDescription: "The destination bucket. 3-63 characters: lowercase letters, digits, " +
					"dots and hyphens only. Updatable in place.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(3, 63),
					stringvalidator.RegexMatches(
						auditLogBucketNamePattern,
						"must contain only lowercase letters, numbers, dots, and hyphens",
					),
				},
			},
			"bucket_prefix": schema.StringAttribute{
				MarkdownDescription: "An optional key prefix under the bucket. Do not include a leading or " +
					"trailing `/`: CircleCI strips them server-side, which would otherwise leave every plan " +
					"showing a difference. Updatable in place.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						auditLogBucketPrefixPattern,
						"must not have a leading or trailing \"/\"",
					),
				},
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "The S3-compatible endpoint URL. Required when `target_type = \"" +
					circleci.AuditLogTargetTypeS3Compatible + "\"`, and must be omitted when " +
					"`target_type = \"" + circleci.AuditLogTargetTypeS3 + "\"`. Updatable in place.",
				Optional: true,
			},
			"connection_status": schema.StringAttribute{
				MarkdownDescription: "CircleCI's report of the most recent delivery attempts: `CONNECTED`, " +
					"`DISCONNECTED`, `UNKNOWN` or `DISABLED`. This can change between applies with no " +
					"configuration change, reflecting real delivery outcomes rather than desired state.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_by": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the user who created the config.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the config was created, as an RFC 3339 timestamp.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "When the config was last updated, as an RFC 3339 timestamp.",
				Computed:            true,
			},
		},
	}
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (r *auditLogConfigResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		orgIDConfigValidator(),
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *auditLogConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil || !requireCloud(r.client, auditLogConfigTypeName, &resp.Diagnostics) {
		return
	}

	var plan auditLogConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID := effectiveOrgID(plan.OrganizationID, plan.OrgID)

	config, err := r.client.CreateAuditLogConfig(ctx, circleci.CreateAuditLogConfigRequest{
		OrgID:      orgID,
		TargetType: plan.TargetType.ValueString(),
		IsDisabled: plan.IsDisabled.ValueBool(),
		Config:     auditLogS3ConfigFromModel(plan),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI audit log config",
			fmt.Sprintf(
				"Could not create an audit log streaming config for organization %s: %s\n\n"+
					"This requires the organization to be on a CircleCI Cloud Scale plan, requires a "+
					"connectable destination (create verifies connectivity even when is_disabled is set), "+
					"and allows at most one config per target_type.",
				orgID, circleci.Detail(err),
			),
		)

		return
	}

	applyAuditLogConfig(&plan, config)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *auditLogConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil || !requireCloud(r.client, auditLogConfigTypeName, &resp.Diagnostics) {
		return
	}

	var state auditLogConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config, err := r.client.GetAuditLogConfig(ctx, state.ID.ValueString())
	if err != nil {
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		resp.Diagnostics.AddError(
			"Error reading CircleCI audit log config",
			fmt.Sprintf("Could not read audit log config %s: %s", state.ID.ValueString(), circleci.Detail(err)),
		)

		return
	}

	applyAuditLogConfig(&state, config)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update applies a full replacement via the PUT route, which the API supports
// for every attribute except organization_id.
func (r *auditLogConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil || !requireCloud(r.client, auditLogConfigTypeName, &resp.Diagnostics) {
		return
	}

	var plan, state auditLogConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config, err := r.client.UpdateAuditLogConfig(ctx, circleci.UpdateAuditLogConfigRequest{
		ID:         state.ID.ValueString(),
		OrgID:      effectiveOrgID(plan.OrganizationID, plan.OrgID),
		TargetType: plan.TargetType.ValueString(),
		IsDisabled: plan.IsDisabled.ValueBool(),
		Config:     auditLogS3ConfigFromModel(plan),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Error updating CircleCI audit log config",
			fmt.Sprintf(
				"Could not update audit log config %s: %s\n\n"+
					"Unless is_disabled is being set to true, this also requires a connectable destination.",
				state.ID.ValueString(), circleci.Detail(err),
			),
		)

		return
	}

	plan.ID = state.ID
	applyAuditLogConfig(&plan, config)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *auditLogConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil || !requireCloud(r.client, auditLogConfigTypeName, &resp.Diagnostics) {
		return
	}

	var state auditLogConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteAuditLogConfig(ctx, state.ID.ValueString()); err != nil && !circleci.IsNotFound(err) {
		resp.Diagnostics.AddError(
			"Error deleting CircleCI audit log config",
			fmt.Sprintf("Could not delete audit log config %s: %s", state.ID.ValueString(), circleci.Detail(err)),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *auditLogConfigResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing audit log config by its id. Unlike
// circleci_otel_exporter, no organization_id is needed alongside it: the
// single-config route is not scoped by organization, and the response
// includes org_id.
func (r *auditLogConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// ModifyPlan rejects CircleCI Server at plan time rather than at apply time.
// Destroy is exempt so a resource stranded in state by a deployment change
// stays removable.
func (r *auditLogConfigResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.Plan.Raw.IsNull() {
		return
	}

	requireCloud(r.client, auditLogConfigTypeName, &resp.Diagnostics)
}

// auditLogS3ConfigFromModel builds the API's S3 destination shape from the
// resource's flat attributes.
func auditLogS3ConfigFromModel(model auditLogConfigResourceModel) circleci.AuditLogS3Config {
	return circleci.AuditLogS3Config{
		ARN:          model.ARN.ValueString(),
		Region:       model.Region.ValueString(),
		BucketName:   model.BucketName.ValueString(),
		BucketPrefix: model.BucketPrefix.ValueString(),
		Endpoint:     model.Endpoint.ValueString(),
	}
}

// applyAuditLogConfig copies an API config into the model.
func applyAuditLogConfig(model *auditLogConfigResourceModel, config *circleci.AuditLogConfig) {
	model.ID = types.StringValue(config.ID)
	// Both organization attribute names are written from the one value the API
	// reports. See org_id_deprecation.go.
	setOrgIDs(&model.OrganizationID, &model.OrgID, config.OrgID)
	model.TargetType = types.StringValue(config.TargetType)
	model.IsDisabled = types.BoolValue(config.IsDisabled)
	model.ARN = types.StringValue(config.Config.ARN)
	model.BucketName = types.StringValue(config.Config.BucketName)
	model.ConnectionStatus = types.StringValue(config.ConnectionStatus)
	model.CreatedBy = types.StringValue(config.CreatedBy)
	model.CreatedAt = types.StringValue(config.CreatedAt)
	model.UpdatedAt = types.StringValue(config.UpdatedAt)

	if config.Config.Region == "" {
		model.Region = types.StringNull()
	} else {
		model.Region = types.StringValue(config.Config.Region)
	}

	if config.Config.BucketPrefix == "" {
		model.BucketPrefix = types.StringNull()
	} else {
		model.BucketPrefix = types.StringValue(config.Config.BucketPrefix)
	}

	if config.Config.Endpoint == "" {
		model.Endpoint = types.StringNull()
	} else {
		model.Endpoint = types.StringValue(config.Config.Endpoint)
	}
}
