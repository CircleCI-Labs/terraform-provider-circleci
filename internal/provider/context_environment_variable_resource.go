// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                     = &contextEnvironmentVariableResource{}
	_ resource.ResourceWithConfigure        = &contextEnvironmentVariableResource{}
	_ resource.ResourceWithImportState      = &contextEnvironmentVariableResource{}
	_ resource.ResourceWithConfigValidators = &contextEnvironmentVariableResource{}
)

// contextEnvironmentVariableResourceModel maps the output schema.
type contextEnvironmentVariableResourceModel struct {
	Name            types.String `tfsdk:"name"`
	Value           types.String `tfsdk:"value"`
	ValueWO         types.String `tfsdk:"value_wo"`
	ValueWOVersion  types.Int64  `tfsdk:"value_wo_version"`
	UpdatedAt       types.String `tfsdk:"updated_at"`
	RemoteUpdatedAt types.String `tfsdk:"remote_updated_at"`
	CreatedAt       types.String `tfsdk:"created_at"`
	ContextId       types.String `tfsdk:"context_id"`
}

// recordWrite pins both timestamps to what CircleCI reported for a write this
// provider just made, which is what makes the two-timestamp comparison in Read
// meaningful: updated_at is only ever a value we put there ourselves.
func (m *contextEnvironmentVariableResourceModel) recordWrite(updatedAt string) {
	m.UpdatedAt = types.StringValue(updatedAt)
	m.RemoteUpdatedAt = types.StringValue(updatedAt)
}

// NewContextEnvironmentVariableResource is a helper function to simplify the provider implementation.
func NewContextEnvironmentVariableResource() resource.Resource {
	return &contextEnvironmentVariableResource{}
}

// contextEnvironmentVariableResource is the resource implementation.
type contextEnvironmentVariableResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *contextEnvironmentVariableResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_context_environment_variable"
}

// Schema defines the schema for the resource.
func (r *contextEnvironmentVariableResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an environment variable stored in a CircleCI context.\n\n" +
			"-> **The value is never read back.** The API does not return a context environment " +
			"variable's value on any route, not even the one that just set it, so `value` is only ever " +
			"the value from configuration or state — it is never overwritten by a refresh, and importing " +
			"this resource requires supplying it afterwards.\n\n" +
			"A change made outside Terraform is still detected, by comparing timestamps rather than " +
			"values: see `updated_at` and `remote_updated_at`.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the environment variable. Changing this value forces a new resource to be created.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"value": schema.StringAttribute{
				MarkdownDescription: "The value of the environment variable. The API never returns this, " +
					"so it always reflects configuration or state rather than a live read.\n\n" +
					"It is recorded in Terraform state in cleartext. Use `value_wo` instead to keep " +
					"it out of state, at the cost of having to bump `value_wo_version` to rotate it. " +
					"Set exactly one of the two.",
				Optional:  true,
				Sensitive: true,
			},
			"value_wo":         writeOnlyValueAttribute(false),
			"value_wo_version": writeOnlyValueVersionAttribute(false),
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp CircleCI reported the last time **Terraform** wrote " +
					"this environment variable. It is the baseline the next refresh compares against, " +
					"so it is deliberately not refreshed from the API.",
				Computed: true,
			},
			"remote_updated_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp CircleCI reported for this environment variable as " +
					"of the last refresh, whoever wrote it. When it is later than `updated_at` the " +
					"value was changed outside Terraform, and the next apply re-asserts the " +
					"configured one.",
				Computed: true,
			},
			"context_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the context that owns this environment variable. Changing this value forces a new resource to be created.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the environment variable was created.",
				Computed:            true,
			},
		},
	}
}

// ConfigValidators requires exactly one of `value` and `value_wo`.
func (r *contextEnvironmentVariableResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{envVarValueConfigValidator()}
}

// Create creates the resource and sets the initial Terraform state.
func (r *contextEnvironmentVariableResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan contextEnvironmentVariableResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	value, ok := resolveEnvVarValue(ctx, req.Config, plan.Value, plan.ValueWOVersion, &resp.Diagnostics)
	if !ok {
		return
	}

	created, err := r.client.UpsertContextEnvironmentVariable(
		ctx, plan.ContextId.ValueString(), plan.Name.ValueString(), value,
	)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI context environment variable",
			circleci.Detail(err),
		)

		return
	}

	plan.CreatedAt = types.StringValue(created.CreatedAt)
	plan.recordWrite(created.UpdatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Read refreshes the Terraform state with the latest data.
//
// value is deliberately left untouched: the API never returns it (see the
// resource's MarkdownDescription), so overwriting it here would either wipe out
// the configured value with nothing, or — if some future route ever did start
// returning a mask — write that mask back as if it were the real value. Only
// the metadata a list actually discloses is refreshed.
//
// The one thing it does with the value is remove it, when the timestamps say
// somebody else wrote it — see detectContextEnvVarDrift.
func (r *contextEnvironmentVariableResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state contextEnvironmentVariableResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vars, err := r.client.ListContextEnvironmentVariables(ctx, state.ContextId.ValueString())
	if err != nil {
		if circleci.IsNotFound(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		// See context_resource.go's Read for why 403 is not folded into
		// IsNotFound: this route resolves the context id the same way, so a
		// context that no longer exists and a token that lost permission look
		// identical.
		if circleci.IsUnauthorized(err) {
			resp.Diagnostics.AddError(
				"Unable to read CircleCI context environment variable "+state.Name.ValueString(),
				fmt.Sprintf(
					"The API denied access to context %s. It has either been deleted outside "+
						"Terraform, or the configured token lacks permission — the API returns the same "+
						"response for both and does not distinguish them.\n\n%s",
					state.ContextId.ValueString(), circleci.Detail(err),
				),
			)

			return
		}

		resp.Diagnostics.AddError(
			"Unable to read CircleCI context environment variable "+state.Name.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	var found bool
	for _, elem := range vars {
		if elem.Variable == state.Name.ValueString() {
			state.CreatedAt = types.StringValue(elem.CreatedAt)
			state.ContextId = types.StringValue(elem.ContextID)
			detectContextEnvVarDrift(&state, elem.UpdatedAt)
			found = true

			break
		}
	}

	if !found {
		resp.State.RemoveResource(ctx)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// detectContextEnvVarDrift reports a value changed outside Terraform, by
// comparing the timestamp CircleCI reports now against the one recorded after
// this provider's own last write.
//
// Timestamps are the only signal available. The API returns no value on any
// route, and truncated_value cannot substitute: it is the last four characters
// of the value, so rotating a secret while keeping its suffix leaves it
// identical. (A community provider compares it and silently misses those
// rotations.) updated_at, meanwhile, bumps on *every* PUT even when the value
// written is byte-identical — which is exactly why the comparison has to be
// against a timestamp we put in state ourselves, never against the previous
// value of the same remote field.
//
// Drift is reported by removing the value from state rather than by a
// diagnostic, so the next plan shows a change and the next apply re-asserts
// what the configuration says. Which attribute to blank depends on the path in
// use: on the write-only path `value` is null anyway, and `value_wo_version` is
// the persisted thing whose diff drives an update.
func detectContextEnvVarDrift(state *contextEnvironmentVariableResourceModel, remoteUpdatedAt string) {
	state.RemoteUpdatedAt = types.StringValue(remoteUpdatedAt)

	// Nothing of our own to compare against: the resource was just imported, or
	// its state predates these attributes. Adopt the API's timestamp as the
	// baseline instead of reporting drift that cannot be judged — otherwise the
	// first read after every import would blank the value.
	if state.UpdatedAt.IsNull() || state.UpdatedAt.IsUnknown() {
		state.UpdatedAt = types.StringValue(remoteUpdatedAt)

		return
	}

	if !envVarWrittenOutsideTerraform(state.UpdatedAt.ValueString(), remoteUpdatedAt) {
		return
	}

	if state.ValueWOVersion.IsNull() {
		state.Value = types.StringNull()

		return
	}

	state.ValueWOVersion = types.Int64Null()
}

// Update calls the PUT upsert route, which atomically overwrites the value in
// place — there is no separate create route to fall back to.
func (r *contextEnvironmentVariableResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan contextEnvironmentVariableResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	value, ok := resolveEnvVarValue(ctx, req.Config, plan.Value, plan.ValueWOVersion, &resp.Diagnostics)
	if !ok {
		return
	}

	updated, err := r.client.UpsertContextEnvironmentVariable(
		ctx, plan.ContextId.ValueString(), plan.Name.ValueString(), value,
	)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error updating CircleCI context environment variable",
			circleci.Detail(err),
		)

		return
	}

	plan.CreatedAt = types.StringValue(updated.CreatedAt)
	plan.recordWrite(updated.UpdatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *contextEnvironmentVariableResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state contextEnvironmentVariableResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteContextEnvironmentVariable(ctx, state.ContextId.ValueString(), state.Name.ValueString())
	// Already gone is the desired end state. 403 counts, for the same reason as
	// circleci_context's Delete: this route resolves the context id the same
	// way, so it answers 403 for a context that no longer exists.
	if err != nil && !circleci.IsNotFound(err) && !circleci.IsUnauthorized(err) {
		resp.Diagnostics.AddError(
			"Error deleting CircleCI context environment variable",
			circleci.Detail(err),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *contextEnvironmentVariableResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing resource into Terraform state.
// Expected import ID format: "context_id/env_var_name".
func (r *contextEnvironmentVariableResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID Format",
			fmt.Sprintf("Expected import ID format: 'context_id/env_var_name'. Got: %s", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("context_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), parts[1])...)
	resp.Diagnostics.AddWarning(
		"Context environment variable value cannot be read from API",
		"CircleCI does not expose context environment variable values. Ensure the resource's "+
			"'value' (or 'value_wo' plus 'value_wo_version') is defined in your Terraform configuration.",
	)
}
