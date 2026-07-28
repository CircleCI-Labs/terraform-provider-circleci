// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CircleCI-Public/circleci-sdk-go/runner"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// circleci_ephemeral_runner_token exists alongside the circleci_runner_token
// managed resource rather than replacing it: a Terraform type name must be
// unique, so a second, differently named type is the only way to offer both
// shapes.
//
// circleci_runner_token is a poor fit for a value the API hands out exactly
// once: every subsequent `terraform plan` reads it back out of state, so the
// token — a long-lived credential capable of registering a self-hosted runner
// against this resource class — sits in the state file for as long as the
// resource exists, readable by anyone who can read that state. This ephemeral
// resource instead creates a token for the duration of a single Terraform run
// (typically to bootstrap a runner in the same apply, e.g. via a
// provisioner or an API call to whatever stands up the runner fleet) and
// deletes it again in Close, so nothing outlives the run. Use
// circleci_runner_token when the token genuinely needs to persist — for
// example when nothing in this Terraform run will consume it, and it will be
// handed to a runner provisioned entirely outside Terraform later.
const ephemeralRunnerTokenPrivateKey = "token_id"

// Ensure the implementation satisfies the expected interfaces.
var (
	_ ephemeral.EphemeralResource              = &ephemeralRunnerTokenResource{}
	_ ephemeral.EphemeralResourceWithConfigure = &ephemeralRunnerTokenResource{}
	_ ephemeral.EphemeralResourceWithClose     = &ephemeralRunnerTokenResource{}
)

// ephemeralRunnerTokenModel maps the ephemeral resource schema.
type ephemeralRunnerTokenModel struct {
	OrganizationID types.String `tfsdk:"organization_id"`
	ResourceClass  types.String `tfsdk:"resource_class"`
	Nickname       types.String `tfsdk:"nickname"`
	ID             types.String `tfsdk:"id"`
	Token          types.String `tfsdk:"token"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

// NewEphemeralRunnerTokenResource is a helper function to simplify the provider implementation.
func NewEphemeralRunnerTokenResource() ephemeral.EphemeralResource {
	return &ephemeralRunnerTokenResource{}
}

type ephemeralRunnerTokenResource struct {
	client *runner.Service
}

// Metadata returns the ephemeral resource type name.
func (e *ephemeralRunnerTokenResource) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ephemeral_runner_token"
}

// Schema defines the schema for the ephemeral resource.
func (e *ephemeralRunnerTokenResource) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a CircleCI self-hosted runner authentication token for the duration " +
			"of a single Terraform run, deleting it again when the run ends.\n\n" +
			"Unlike `circleci_runner_token`, the token value here is never written to a state file: `Open` " +
			"creates it and `Close` deletes it, so the credential exists only for as long as this run needs " +
			"it. Use this to bootstrap a runner within the same `terraform apply` that consumes the token " +
			"(for example, passing it to a provisioner or to an external call that registers the runner " +
			"agent); use the `circleci_runner_token` resource instead when the token must outlive this run.\n\n" +
			"**Best-effort deletion.** The underlying runner admin API (circleci-sdk-go's runner client) " +
			"returns untyped errors, so a delete failure because the token was already gone cannot be told " +
			"apart from a genuine failure. `Close` still calls delete and, if it errors, raises a warning " +
			"naming the token id rather than silently leaving a possibly-live credential unaccounted for — " +
			"see that id in the warning if one appears, and check/delete it by hand.",
		Attributes: map[string]schema.Attribute{
			"organization_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The UUID of the organization that owns the resource class.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(runnerOrgIDPattern, "must be an organization UUID"),
				},
			},
			"resource_class": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The resource class this token grants access to, in `namespace/name` format (e.g. `myorg/myrunner`).",
				Validators: []validator.String{
					stringvalidator.RegexMatches(runnerResourceClassPattern, "must be in the format 'namespace/name'"),
				},
			},
			"nickname": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "A human-readable label for the token.",
			},
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Unique identifier (UUID) of the runner token.",
			},
			"token": schema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "The token value used to authenticate a runner agent. Never persisted to state.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The time at which the token was created.",
			},
		},
	}
}

// Configure adds the provider configured client to the ephemeral resource.
func (e *ephemeralRunnerTokenResource) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	client, ok := ephemeralRunnerService(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	e.client = client
}

// Open creates a runner token and records its id in private state, so Close
// can delete it again without needing the token value itself.
func (e *ephemeralRunnerTokenResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var config ephemeralRunnerTokenModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	t, err := e.client.CreateToken(ctx, runner.CreateTokenRequest{
		OrganizationID: config.OrganizationID.ValueString(),
		ResourceClass:  config.ResourceClass.ValueString(),
		Nickname:       config.Nickname.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI runner token",
			"Could not create runner token, unexpected error: "+err.Error(),
		)

		return
	}

	config.ID = types.StringValue(t.Id)
	config.Token = types.StringValue(t.Token)
	config.CreatedAt = types.StringValue(t.CreatedAt)

	resp.Diagnostics.Append(resp.Result.Set(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		// The token was created but its id was never handed back to Terraform,
		// so there is no route (short of a warning naming it here) by which
		// Close could ever be told to clean it up.
		resp.Diagnostics.AddWarning(
			"CircleCI runner token may not be cleaned up automatically",
			fmt.Sprintf(
				"Runner token %s was created, but the provider could not record its id for later cleanup. "+
					"Delete it manually if it is no longer needed.", t.Id,
			),
		)

		return
	}

	idJSON, err := json.Marshal(t.Id)
	if err != nil {
		// A plain UUID string always marshals; this is defensive.
		resp.Diagnostics.AddWarning(
			"CircleCI runner token may not be cleaned up automatically",
			fmt.Sprintf(
				"Runner token %s was created, but its id could not be saved for later cleanup (%s). "+
					"Delete it manually if it is no longer needed.", t.Id, err,
			),
		)

		return
	}

	resp.Diagnostics.Append(resp.Private.SetKey(ctx, ephemeralRunnerTokenPrivateKey, idJSON)...)
}

// Close deletes the token Open created.
//
// circleci-sdk-go's runner client returns untyped errors (see the package
// comment on runner_resource_class_resource.go), so a delete failure because
// the token was already gone cannot be distinguished from one where it is
// still live. Rather than guess, a failed delete raises a warning that names
// the token id, so a practitioner can check and, if necessary, delete it by
// hand instead of it being silently unaccounted for.
func (e *ephemeralRunnerTokenResource) Close(ctx context.Context, req ephemeral.CloseRequest, resp *ephemeral.CloseResponse) {
	raw, diags := req.Private.GetKey(ctx, ephemeralRunnerTokenPrivateKey)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if len(raw) == 0 {
		// Open never got far enough to record a token id (it may have failed
		// before creating one, or failed to save it — already warned about at
		// that point), so there is nothing this Close can do.
		return
	}

	var tokenID string
	if err := json.Unmarshal(raw, &tokenID); err != nil {
		resp.Diagnostics.AddError("Error reading CircleCI runner token private state", err.Error())

		return
	}

	if err := e.client.DeleteToken(ctx, tokenID); err != nil {
		resp.Diagnostics.AddWarning(
			"CircleCI runner token may not have been deleted",
			fmt.Sprintf(
				"Deleting runner token %s failed: %s. This token was created for a single Terraform run and "+
					"is not stored in state, so it will not be retried automatically. Verify whether it still "+
					"exists (list tokens for its resource class) and delete it by hand if so.",
				tokenID, err,
			),
		)
	}
}
