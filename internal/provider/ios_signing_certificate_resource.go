// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                     = &iosSigningCertificateResource{}
	_ resource.ResourceWithConfigure        = &iosSigningCertificateResource{}
	_ resource.ResourceWithImportState      = &iosSigningCertificateResource{}
	_ resource.ResourceWithConfigValidators = &iosSigningCertificateResource{}
)

// iosSigningCertificateTypeName is used in diagnostics, including the
// Cloud-only error.
const iosSigningCertificateTypeName = "circleci_ios_signing_certificate"

// iosSigningCertificateResourceModel maps the resource schema.
//
// CertificateBlob and CertificatePassword are the only two attributes with no
// corresponding value ever returned by the API: see the Schema doc comment
// below ("Security") for why they are still ordinary Sensitive attributes
// rather than some other shape, and what that means for Terraform state.
type iosSigningCertificateResourceModel struct {
	Id                  types.String `tfsdk:"id"`
	OrganizationId      types.String `tfsdk:"organization_id"`
	OrgId               types.String `tfsdk:"org_id"`
	FileName            types.String `tfsdk:"file_name"`
	CertificateBlob     types.String `tfsdk:"certificate_blob"`
	CertificatePassword types.String `tfsdk:"certificate_password"`
	CertType            types.String `tfsdk:"cert_type"`
	Fingerprint         types.String `tfsdk:"fingerprint"`
	CreatedAt           types.String `tfsdk:"created_at"`
	ExpiresAt           types.String `tfsdk:"expires_at"`
}

// NewIOSSigningCertificateResource is a helper function to simplify the provider implementation.
func NewIOSSigningCertificateResource() resource.Resource {
	return &iosSigningCertificateResource{}
}

// iosSigningCertificateResource is the resource implementation.
type iosSigningCertificateResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *iosSigningCertificateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ios_signing_certificate"
}

// Schema defines the schema for the resource.
func (r *iosSigningCertificateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Uploads an Apple code-signing certificate (a `.p12` file and its " +
			"password) to a CircleCI organization, for signing iOS builds. Pair it with one or " +
			"more provisioning profiles using `circleci_ios_signing_config`.\n\n" +
			"~> **CircleCI Cloud only.** Signing certificates are served by the CircleCI v3 API, " +
			"which CircleCI Server does not route. The certificate is only useful to a macOS " +
			"executor building and signing an iOS app.\n\n" +
			"!> **This resource is immutable.** The API has no update route for a signing " +
			"certificate: `organization_id`, `file_name`, `certificate_blob` and " +
			"`certificate_password` all force a new resource to be created when changed, " +
			"which uploads a replacement certificate and deletes the old one. There is also no " +
			"read route for the certificate content or password, so this is mechanical, not a " +
			"design choice: nothing else is possible without an update endpoint.\n\n" +
			"## Security\n\n" +
			"`certificate_blob` and `certificate_password` are genuine credentials: the private " +
			"key half of a code-signing identity, and the password protecting it. Both are " +
			"marked `Sensitive`, which keeps them out of Terraform's CLI output, **but Sensitive " +
			"does not encrypt state.** CircleCI stores them once and never returns them again, " +
			"in any response -- there is no route that reads a certificate's content or password " +
			"back -- which means the only place this provider can keep a copy for `terraform " +
			"plan` to compare against is Terraform state itself, in cleartext.\n\n" +
			"Given that:\n\n" +
			"- Use a state backend that encrypts at rest (e.g. Terraform Cloud, or an S3 backend " +
			"with SSE and a restrictive bucket policy), and restrict who can read it.\n" +
			"- Source `certificate_blob` and `certificate_password` from a secret manager (Vault, " +
			"AWS Secrets Manager, etc.) via a data source, never from a `.p12` file committed to " +
			"the repository the configuration lives in.\n" +
			"- Rotating either value is a full replace of this resource (see above), so a leaked " +
			"certificate can be revoked by deleting the resource -- which deletes it from CircleCI " +
			"too -- rather than by an in-place update.\n\n" +
			"Note also what this resource deliberately does **not** expose: there is no computed " +
			"attribute that echoes back a masked form of `certificate_blob` or " +
			"`certificate_password` (compare `circleci_webhook`'s `has_signing_secret`, which " +
			"exists because that API genuinely reports whether a secret is set). A signing " +
			"certificate's content and password are Required to create the resource in the " +
			"first place, so there is no \"configured or not\" ambiguity for a boolean to resolve, " +
			"and a `Sensitive` string that only ever held a fixed mask would look like a " +
			"credential without being one.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the certificate.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// See org_id_deprecation.go for why these are Optional+Computed and why
			// replacement is conditional on being configured.
			"organization_id": deprecatedOrgIDAttribute("this signing certificate", true),
			"org_id":          orgIDAttribute("this signing certificate", true),
			"file_name": schema.StringAttribute{
				MarkdownDescription: "A display name for the certificate, for example " +
					"`distribution.p12`. This is a label only; it does not have to match the " +
					"file the `certificate_blob` bytes came from. Limited to 40 characters by " +
					"the API. Changing this value forces a new resource to be created.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 40),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"certificate_blob": schema.StringAttribute{
				MarkdownDescription: "The certificate's `.p12` file, base64-encoded (standard " +
					"encoding), for example `filebase64(\"distribution.p12\")`. Write-only: " +
					"CircleCI never returns this value, so it cannot be read back into state on " +
					"import or drift detection -- see the resource-level \"Security\" section. " +
					"Changing this value forces a new resource to be created.",
				Required:  true,
				Sensitive: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"certificate_password": schema.StringAttribute{
				MarkdownDescription: "The password that unlocks `certificate_blob`. Write-only, " +
					"for the same reason as `certificate_blob`. Changing this value forces a new " +
					"resource to be created.",
				Required:  true,
				Sensitive: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cert_type": schema.StringAttribute{
				MarkdownDescription: "The certificate's type, `distribution` or `development`. " +
					"CircleCI derives this from the certificate itself (its X.509 Subject Common " +
					"Name) rather than accepting it as input, so it cannot be set and is always " +
					"Computed.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"fingerprint": schema.StringAttribute{
				MarkdownDescription: "The certificate's fingerprint, as reported by CircleCI.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the certificate was uploaded, as an RFC 3339 " +
					"timestamp with millisecond precision.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"expires_at": schema.StringAttribute{
				MarkdownDescription: "When the certificate expires, as an RFC 3339 timestamp " +
					"with millisecond precision, or an empty string if CircleCI could not " +
					"determine an expiry from the certificate.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *iosSigningCertificateResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ConfigValidators requires exactly one of the two organization attribute names.
func (r *iosSigningCertificateResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		orgIDConfigValidator(),
	}
}

// Create uploads the certificate and sets the initial Terraform state.
func (r *iosSigningCertificateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireCloud(r.client, iosSigningCertificateTypeName, &resp.Diagnostics) {
		return
	}

	var plan iosSigningCertificateResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cert, err := r.client.CreateSigningCertificate(ctx, circleci.CreateSigningCertificateRequest{
		OrganizationID: effectiveOrgID(plan.OrganizationId, plan.OrgId),
		FileName:       plan.FileName.ValueString(),
		CertBlob:       plan.CertificateBlob.ValueString(),
		CertPassword:   plan.CertificatePassword.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to create CircleCI iOS signing certificate",
			circleci.Detail(err),
		)

		return
	}

	setIOSSigningCertificateState(&plan, cert)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
//
// certificate_blob and certificate_password are left untouched: the API has
// nothing to report for either, and if Read tried to clear or recompute them
// every plan after apply would show a permanent diff against the
// configuration. Leaving them alone is what keeps that plan empty.
func (r *iosSigningCertificateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireCloud(r.client, iosSigningCertificateTypeName, &resp.Diagnostics) {
		return
	}

	var state iosSigningCertificateResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cert, err := r.client.GetSigningCertificate(ctx, state.Id.ValueString())
	if circleci.IsNotFound(err) {
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read CircleCI iOS signing certificate",
			circleci.Detail(err),
		)

		return
	}

	setIOSSigningCertificateState(&state, cert)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is a no-op. Every configurable attribute is RequiresReplace, because
// the API has no update route for a signing certificate, so Terraform never
// calls this. It exists only to satisfy the resource.Resource interface.
func (r *iosSigningCertificateResource) Update(_ context.Context, _ resource.UpdateRequest, _ *resource.UpdateResponse) {
}

// Delete deletes the certificate.
func (r *iosSigningCertificateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireCloud(r.client, iosSigningCertificateTypeName, &resp.Diagnostics) {
		return
	}

	var state iosSigningCertificateResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteSigningCertificate(ctx, state.Id.ValueString())
	if circleci.IsNotFound(err) {
		// Already gone; deleting is still a success.
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to delete CircleCI iOS signing certificate",
			circleci.Detail(err),
		)
	}
}

// ImportState imports an existing certificate by id.
//
// certificate_blob and certificate_password cannot be recovered on import --
// the API never returns them -- so they are left null. A subsequent plan will
// show them changing from null to the configured value the first time this
// resource appears in a configuration; that one-time diff is expected and
// does not by itself force a replacement, since Create is never called for an
// import.
func (r *iosSigningCertificateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// setIOSSigningCertificateState copies an API response into the resource model.
func setIOSSigningCertificateState(model *iosSigningCertificateResourceModel, cert *circleci.SigningCertificate) {
	model.Id = types.StringValue(cert.ID)
	// Both organization attribute names are written from the one value the API
	// reports. See org_id_deprecation.go.
	setOrgIDs(&model.OrganizationId, &model.OrgId, cert.OrganizationID)
	model.FileName = types.StringValue(cert.FileName)
	model.CertType = types.StringValue(cert.CertType)
	model.Fingerprint = types.StringValue(cert.Fingerprint)
	model.CreatedAt = types.StringValue(stringOrEmpty(cert.CreatedAt))
	model.ExpiresAt = types.StringValue(stringOrEmpty(cert.ExpiresAt))
}

// stringOrEmpty dereferences an optional string, so a nil timestamp becomes ""
// rather than requiring a nullable Terraform attribute for a value that is
// only ever absent for one field (expires_at) on certificates with no expiry
// CircleCI could parse.
func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}
