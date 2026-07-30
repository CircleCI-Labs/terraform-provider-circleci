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
// The certificate content and its password are the only two values with no
// corresponding value ever returned by the API, and each can be supplied under
// two names: CertificateBlob/CertificatePassword, which are recorded in
// Terraform state, or CertificateBlobWO/CertificatePasswordWO, which are not.
// See ios_signing_write_only.go, and the Schema doc comment below ("Security")
// for what the choice means.
//
// The two WO fields are null in the plan and in state always -- the framework
// nullifies a write-only attribute in both -- so nothing reads them from this
// model; resolveIOSSigningCertificateCredentials reads them from the
// configuration instead. They are declared because every schema attribute needs
// a field.
type iosSigningCertificateResourceModel struct {
	Id                    types.String `tfsdk:"id"`
	OrganizationId        types.String `tfsdk:"organization_id"`
	OrgId                 types.String `tfsdk:"org_id"`
	FileName              types.String `tfsdk:"file_name"`
	CertificateBlob       types.String `tfsdk:"certificate_blob"`
	CertificatePassword   types.String `tfsdk:"certificate_password"`
	CertificateBlobWO     types.String `tfsdk:"certificate_blob_wo"`
	CertificatePasswordWO types.String `tfsdk:"certificate_password_wo"`
	CertificateWOVersion  types.Int64  `tfsdk:"certificate_wo_version"`
	CertType              types.String `tfsdk:"cert_type"`
	Fingerprint           types.String `tfsdk:"fingerprint"`
	CreatedAt             types.String `tfsdk:"created_at"`
	ExpiresAt             types.String `tfsdk:"expires_at"`
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
			"certificate: `organization_id`, `file_name`, `certificate_blob`, " +
			"`certificate_password` and `certificate_wo_version` all force a new resource to be " +
			"created when changed, which uploads a replacement certificate and deletes the old " +
			"one. There is also no read route for the certificate content or password, so this " +
			"is mechanical, not a design choice: nothing else is possible without an update " +
			"endpoint.\n\n" +
			"## Security\n\n" +
			"The certificate content and its password are genuine credentials: the private key " +
			"half of a code-signing identity, and the password protecting it. Anyone who can " +
			"read them can sign iOS builds as your organization. There are two ways to supply " +
			"them, and they differ in exactly one respect -- whether Terraform keeps a copy.\n\n" +
			"**Prefer `certificate_blob_wo` and `certificate_password_wo`** (Terraform 1.11 or " +
			"later). These are write-only arguments: the provider sends them to CircleCI and " +
			"nothing is persisted, not to state and not to a plan file. They can be fed from an " +
			"`ephemeral` block, which Terraform also refuses to persist, so the certificate need " +
			"not exist in state, in a plan file, or in a `.tfvars` file at any point. What it " +
			"costs: because nothing derived from the certificate is stored, Terraform cannot see " +
			"that it changed, so you rotate by incrementing `certificate_wo_version` -- one " +
			"counter for the pair, since a `.p12` and its password rotate together. Forget to " +
			"bump it and the new certificate is simply never sent.\n\n" +
			"`certificate_blob` and `certificate_password` remain supported and undeprecated, " +
			"and rotate on their own without a counter. Both are marked `Sensitive`, which keeps " +
			"them out of Terraform's CLI output, **but Sensitive does not encrypt state**: on " +
			"this path both values are written to Terraform state in cleartext, because that is " +
			"where Terraform keeps the copy `terraform plan` compares against. CircleCI stores " +
			"them once and never returns them again, in any response, so state is the only copy " +
			"there is.\n\n" +
			"Either way:\n\n" +
			"- Use a state backend that encrypts at rest (e.g. Terraform Cloud, or an S3 backend " +
			"with SSE and a restrictive bucket policy), and restrict who can read it. This is " +
			"the whole mitigation on the state-backed path, and still worth doing on the " +
			"write-only one.\n" +
			"- Source the `.p12` and its password from a secret manager (Vault, AWS Secrets " +
			"Manager, etc.), never from a file committed to the repository the configuration " +
			"lives in. With the write-only attributes, use the secret manager's `ephemeral` " +
			"resource rather than a data source, so the value is not persisted on the way " +
			"through.\n" +
			"- Rotating the certificate is a full replace of this resource (see above), so a " +
			"leaked certificate can be revoked by deleting the resource -- which deletes it from " +
			"CircleCI too -- rather than by an in-place update.\n" +
			"- Moving an existing certificate to the write-only attributes does not scrub it " +
			"from state versions already stored. Completing the move means rotating the " +
			"certificate as well as pruning old state.\n\n" +
			"Note also what this resource deliberately does **not** expose: there is no computed " +
			"attribute that echoes back a masked form of the certificate or its password " +
			"(compare `circleci_webhook`'s `has_signing_secret`, which exists because that API " +
			"genuinely reports whether a secret is set). A certificate cannot exist without " +
			"content and a password -- one of the two spellings is always configured -- so there " +
			"is no \"configured or not\" ambiguity for a boolean to resolve, and a `Sensitive` " +
			"string that only ever held a fixed mask would look like a credential without being " +
			"one.\n\n" +
			"See the [Managing secrets](../guides/managing-secrets) guide for how this compares " +
			"with the provider's other secret-bearing resources.",
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
			// certificate_blob and certificate_password are Optional rather than
			// Required because either may be replaced by its `_wo` spelling below.
			// iosSigningCertificateBlobConfigValidator requires exactly one of the two
			// blobs, and the AlsoRequires validators here keep a password paired with
			// its blob, which is what "both Required" used to guarantee.
			"certificate_blob": schema.StringAttribute{
				MarkdownDescription: "The certificate's `.p12` file, base64-encoded (standard " +
					"encoding), for example `filebase64(\"distribution.p12\")`. CircleCI never " +
					"returns this value, so it cannot be read back into state on import or drift " +
					"detection, and **it is stored in Terraform state in cleartext** -- prefer " +
					"`certificate_blob_wo`, and see the resource-level \"Security\" section. " +
					"Changing this value forces a new resource to be created.\n\n" +
					"Set exactly one of `certificate_blob` and `certificate_blob_wo`. " +
					"`certificate_password` is required alongside this one.",
				Optional:  true,
				Sensitive: true,
				Validators: []validator.String{
					stringvalidator.AlsoRequires(path.MatchRoot("certificate_password")),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"certificate_password": schema.StringAttribute{
				MarkdownDescription: "The password that unlocks `certificate_blob`. Stored in " +
					"Terraform state in cleartext, for the same reason as `certificate_blob`; " +
					"prefer `certificate_password_wo`. Changing this value forces a new resource " +
					"to be created.\n\n" +
					"Required alongside `certificate_blob`, and only valid with it: use " +
					"`certificate_password_wo` with `certificate_blob_wo`.",
				Optional:  true,
				Sensitive: true,
				Validators: []validator.String{
					// Both directions, so that neither half of the pair can be set alone.
					// See ios_signing_write_only.go on why one-directional AlsoRequires is
					// not enough.
					stringvalidator.AlsoRequires(path.MatchRoot("certificate_blob")),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			// The write-only pair and the one version counter that governs both. See
			// ios_signing_write_only.go.
			"certificate_blob_wo":     iosSigningCertificateBlobWriteOnlyAttribute(),
			"certificate_password_wo": iosSigningCertificatePasswordWriteOnlyAttribute(),
			"certificate_wo_version":  iosSigningCertificateWriteOnlyVersionAttribute(),
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

// ConfigValidators requires exactly one of the two organization attribute names,
// and exactly one of the two certificate attribute names.
func (r *iosSigningCertificateResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		orgIDConfigValidator(),
		iosSigningCertificateBlobConfigValidator(),
	}
}

// Create uploads the certificate and sets the initial Terraform state.
//
// The certificate content and password come from
// resolveIOSSigningCertificateCredentials rather than straight off the plan,
// because on the write-only path the plan holds nulls by design. Create is the
// only method that needs them: there is no Update, and no route returns them.
func (r *iosSigningCertificateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireCloud(r.client, iosSigningCertificateTypeName, &resp.Diagnostics) {
		return
	}

	var plan iosSigningCertificateResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	certBlob, certPassword, ok := resolveIOSSigningCertificateCredentials(
		ctx, req.Config, plan.CertificateBlob, plan.CertificatePassword, plan.CertificateWOVersion, &resp.Diagnostics,
	)
	if !ok {
		return
	}

	cert, err := r.client.CreateSigningCertificate(ctx, circleci.CreateSigningCertificateRequest{
		OrganizationID: effectiveOrgID(plan.OrganizationId, plan.OrgId),
		FileName:       plan.FileName.ValueString(),
		CertBlob:       certBlob,
		CertPassword:   certPassword,
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
// certificate_blob, certificate_password and certificate_wo_version are left
// untouched: the API has nothing to report for any of them, and if Read tried to
// clear or recompute them every plan after apply would show a permanent diff
// against the configuration. Leaving them alone is what keeps that plan empty.
// The two `_wo` attributes are not written either -- the framework requires them
// to be null in state, and setting one would be an error.
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
//
// This is why `certificate_wo_version` carries RequiresReplace rather than
// driving an in-place rewrite the way `value_wo_version` does on the environment
// variable resources: an update that reached here would silently do nothing while
// reporting success.
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
//
// An import into a configuration using the write-only pair behaves better, not
// worse: `certificate_blob_wo` is null in state whether imported or created, so
// only `certificate_wo_version` shows the one-time diff.
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
