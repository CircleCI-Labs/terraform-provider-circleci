// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &iosSigningConfigResource{}
	_ resource.ResourceWithConfigure   = &iosSigningConfigResource{}
	_ resource.ResourceWithImportState = &iosSigningConfigResource{}
)

// iosSigningConfigTypeName is used in diagnostics, including the Cloud-only error.
const iosSigningConfigTypeName = "circleci_ios_signing_config"

// iosSigningConfigNamePattern mirrors the API's own validation (see
// signingConfigNameRE in circleci/the API's post_signing_config_v3.go):
// letters, numbers and hyphens only. Rejecting anything else in the schema
// turns a plan-time error into what would otherwise be a 400 at apply.
var iosSigningConfigNamePattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// iosSigningConfigResourceModel maps the resource schema.
type iosSigningConfigResourceModel struct {
	Id                   types.String                   `tfsdk:"id"`
	OrganizationId       types.String                   `tfsdk:"organization_id"`
	Name                 types.String                   `tfsdk:"name"`
	CertificateId        types.String                   `tfsdk:"certificate_id"`
	CertificateFileName  types.String                   `tfsdk:"certificate_file_name"`
	CertificateType      types.String                   `tfsdk:"certificate_type"`
	ProvisioningProfiles []iosSigningConfigProfileModel `tfsdk:"provisioning_profiles"`
}

// iosSigningConfigProfileModel maps one provisioning profile in the config.
//
// Blob is write-only, exactly like circleci_ios_signing_certificate's
// certificate_blob: the API never returns a profile's content, only its
// file_name (see signingConfigProfileAttributes in
// internal/circleci/signing_config.go).
type iosSigningConfigProfileModel struct {
	FileName types.String `tfsdk:"file_name"`
	Blob     types.String `tfsdk:"blob"`
}

// NewIOSSigningConfigResource is a helper function to simplify the provider implementation.
func NewIOSSigningConfigResource() resource.Resource {
	return &iosSigningConfigResource{}
}

// iosSigningConfigResource is the resource implementation.
type iosSigningConfigResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *iosSigningConfigResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ios_signing_config"
}

// Schema defines the schema for the resource.
func (r *iosSigningConfigResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Pairs a `circleci_ios_signing_certificate` with one or more Apple " +
			"provisioning profiles, for signing iOS builds.\n\n" +
			"~> **CircleCI Cloud only.** Signing configurations are served by the CircleCI v3 " +
			"API, which CircleCI Server does not route. The configuration is only useful to a " +
			"macOS executor building and signing an iOS app.\n\n" +
			"!> **This resource is immutable.** The API has no update route for a signing " +
			"configuration -- adding, removing or renewing a provisioning profile, renaming the " +
			"configuration, or repointing it at a different certificate are all a new resource. " +
			"Every attribute is therefore `RequiresReplace`.\n\n" +
			"## Security\n\n" +
			"`provisioning_profiles[*].blob` is write-only, for the same reason and with the same " +
			"consequences as `circleci_ios_signing_certificate`'s `certificate_blob`: CircleCI " +
			"never returns a profile's content, so the only copy this provider can compare " +
			"against on the next `terraform plan` lives in Terraform state, in cleartext. See " +
			"that resource's \"Security\" section for how to source it and protect state " +
			"accordingly. A provisioning profile is less sensitive than a certificate's private " +
			"key -- it authorizes rather than signs -- but it still identifies devices and app " +
			"identifiers and is not intended to be public.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the signing configuration.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"organization_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the organization the " +
					"configuration belongs to. Changing this value forces a new resource to be " +
					"created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The configuration's name. May only contain letters, " +
					"numbers and hyphens, and is limited to 50 characters by the API. Changing " +
					"this value forces a new resource to be created.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 50),
					stringvalidator.RegexMatches(
						iosSigningConfigNamePattern,
						"must only contain letters, numbers and hyphens",
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"certificate_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the " +
					"`circleci_ios_signing_certificate` this configuration signs with. Changing " +
					"this value forces a new resource to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"certificate_file_name": schema.StringAttribute{
				MarkdownDescription: "The paired certificate's display name, as CircleCI " +
					"reports it back on this configuration.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"certificate_type": schema.StringAttribute{
				MarkdownDescription: "The paired certificate's type, `distribution` or " +
					"`development`, as CircleCI reports it back on this configuration.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"provisioning_profiles": schema.ListNestedAttribute{
				MarkdownDescription: "The provisioning profiles paired with the certificate. " +
					"Changing this list in any way -- adding, removing or reordering a profile -- " +
					"forces a new resource to be created, since there is no update route.",
				Required: true,
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
				},
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"file_name": schema.StringAttribute{
							MarkdownDescription: "A display name for the profile, for example " +
								"`release.mobileprovision`. Limited to 40 characters by the API.",
							Required: true,
							Validators: []validator.String{
								stringvalidator.LengthBetween(1, 40),
							},
						},
						"blob": schema.StringAttribute{
							MarkdownDescription: "The profile's `.mobileprovision` file, " +
								"base64-encoded (standard encoding), for example " +
								"`filebase64(\"release.mobileprovision\")`. Write-only: CircleCI " +
								"never returns this value -- see the resource-level \"Security\" " +
								"section.",
							Required:  true,
							Sensitive: true,
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *iosSigningConfigResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// Create creates the signing configuration and sets the initial Terraform state.
func (r *iosSigningConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireCloud(r.client, iosSigningConfigTypeName, &resp.Diagnostics) {
		return
	}

	var plan iosSigningConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	profiles := make([]circleci.CreateSigningProvisioningProfile, len(plan.ProvisioningProfiles))
	for i, p := range plan.ProvisioningProfiles {
		profiles[i] = circleci.CreateSigningProvisioningProfile{
			FileName: p.FileName.ValueString(),
			Blob:     p.Blob.ValueString(),
		}
	}

	cfg, err := r.client.CreateSigningConfig(ctx, circleci.CreateSigningConfigRequest{
		OrganizationID:       plan.OrganizationId.ValueString(),
		CertificateID:        plan.CertificateId.ValueString(),
		Name:                 plan.Name.ValueString(),
		ProvisioningProfiles: profiles,
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to create CircleCI iOS signing configuration",
			circleci.Detail(err),
		)

		return
	}

	setIOSSigningConfigState(&plan, cfg)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
//
// provisioning_profiles[*].blob is left untouched, for the same reason
// certificate_blob is left untouched on circleci_ios_signing_certificate: the
// API has nothing to report for it, and touching it would make every plan
// after apply show a permanent diff.
func (r *iosSigningConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireCloud(r.client, iosSigningConfigTypeName, &resp.Diagnostics) {
		return
	}

	var state iosSigningConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cfg, err := r.client.GetSigningConfig(ctx, state.OrganizationId.ValueString(), state.Id.ValueString())
	if circleci.IsNotFound(err) {
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI iOS signing configuration",
			circleci.Detail(err),
		)

		return
	}

	setIOSSigningConfigState(&state, cfg)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is a no-op. Every configurable attribute is RequiresReplace, because
// the API has no update route for a signing configuration, so Terraform never
// calls this. It exists only to satisfy the resource.Resource interface.
func (r *iosSigningConfigResource) Update(_ context.Context, _ resource.UpdateRequest, _ *resource.UpdateResponse) {
}

// Delete deletes the signing configuration.
func (r *iosSigningConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireCloud(r.client, iosSigningConfigTypeName, &resp.Diagnostics) {
		return
	}

	var state iosSigningConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteSigningConfig(ctx, state.Id.ValueString())
	if circleci.IsNotFound(err) {
		// Already gone; deleting is still a success.
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to delete CircleCI iOS signing configuration",
			circleci.Detail(err),
		)
	}
}

// ImportState imports an existing signing configuration.
//
// There is no GET .../signing/configs/{id} route (see
// internal/circleci/signing_config.go), so resolving a configuration by id
// alone requires knowing which organization to list. The import id is
// therefore "<organization_id>/<config_id>", and provisioning_profiles[*].blob
// is left null for the same reason certificate_blob is on
// circleci_ios_signing_certificate: the API never returns it.
func (r *iosSigningConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	organizationID, configID, ok := strings.Cut(req.ID, "/")
	if !ok || organizationID == "" || configID == "" || strings.Contains(configID, "/") {
		resp.Diagnostics.AddError(
			"Invalid import ID for circleci_ios_signing_config",
			fmt.Sprintf(
				"Expected \"<organization_id>/<config_id>\", got %q. The organization id is "+
					"required because there is no route to look a signing configuration up by id "+
					"alone.",
				req.ID,
			),
		)

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), organizationID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), configID)...)
}

// setIOSSigningConfigState copies an API response into the resource model.
// provisioning_profiles is intentionally not overwritten with the API's
// answer: the server only reports file_name, and overwriting the whole list
// with entries that carry a null blob would conflict with the Required,
// non-null blob every element of the configured list carries.
func setIOSSigningConfigState(model *iosSigningConfigResourceModel, cfg *circleci.SigningConfig) {
	model.Id = types.StringValue(cfg.ID)
	model.OrganizationId = types.StringValue(cfg.OrganizationID)
	model.Name = types.StringValue(cfg.Name)
	model.CertificateId = types.StringValue(cfg.CertificateID)
	model.CertificateFileName = types.StringValue(cfg.CertificateFileName)
	model.CertificateType = types.StringValue(cfg.CertificateType)
}
