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
	_ resource.Resource                = &projectEnvironmentVariableResource{}
	_ resource.ResourceWithConfigure   = &projectEnvironmentVariableResource{}
	_ resource.ResourceWithImportState = &projectEnvironmentVariableResource{}
)

// projectEnvironmentVariableResourceModel maps the resource schema.
type projectEnvironmentVariableResourceModel struct {
	Name        types.String `tfsdk:"name"`
	Value       types.String `tfsdk:"value"`
	ProjectSlug types.String `tfsdk:"project_slug"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// NewProjectEnvironmentVariableResource is a helper function to simplify the provider implementation.
func NewProjectEnvironmentVariableResource() resource.Resource {
	return &projectEnvironmentVariableResource{}
}

// projectEnvironmentVariableResource is the resource implementation.
type projectEnvironmentVariableResource struct {
	client *circleci.Client
}

// Metadata returns the resource type name.
func (r *projectEnvironmentVariableResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_environment_variable"
}

// Schema defines the schema for the resource.
func (r *projectEnvironmentVariableResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an environment variable stored in a CircleCI project.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the environment variable. Changing this value forces a new resource to be created.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"value": schema.StringAttribute{
				MarkdownDescription: "The value of the environment variable. Changing this value forces a new resource to be created.",
				Required:            true,
				Sensitive:           true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"project_slug": schema.StringAttribute{
				MarkdownDescription: "The project slug in the format `vcs-type/org-name/repo-name`. Changing this value forces a new resource to be created.",
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

// Create creates the resource and sets the initial Terraform state.
func (r *projectEnvironmentVariableResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	// Retrieve values from plan
	var plan projectEnvironmentVariableResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Create new project environment variable. The returned Value is masked (the
	// create response is read back through the same masking view as the list and
	// single-get routes), so it is never mapped back onto plan.Value here.
	newEnvVar, err := r.client.CreateProjectEnvironmentVariable(ctx, plan.ProjectSlug.ValueString(),
		circleci.ProjectEnvironmentVariableInput{
			Name:  plan.Name.ValueString(),
			Value: plan.Value.ValueString(),
		},
	)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI project environment variable",
			circleci.Detail(err),
		)
		return
	}

	// Map response body to schema and populate Computed attribute values
	plan.CreatedAt = types.StringValue(newEnvVar.CreatedAt)

	// Set state to fully populated data
	diags = resp.State.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Read refreshes the Terraform state with the latest data.
func (r *projectEnvironmentVariableResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectEnvironmentVariableResourceModel
	diags := req.State.Get(ctx, &state)
	if diags != nil {
		resp.Diagnostics.Append(diags...)
		return
	}

	envVar, err := r.client.GetProjectEnvironmentVariable(ctx, state.ProjectSlug.ValueString(), state.Name.ValueString())
	// A variable deleted outside Terraform must drop out of state so the next
	// plan recreates it. Absence is tested with circleci.IsNotFound rather than
	// by string-matching the error: matching "404" also matches a 5xx whose body
	// happens to mention it, which silently removed live resources from state.
	if circleci.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Error Reading CircleCI Project Environment Variable",
			"Could not read project environment variable "+state.Name.ValueString()+": "+circleci.Detail(err),
		)
		return
	}

	state.Name = types.StringValue(envVar.Name)
	// Preserve Value from state: the API only ever returns a masked value (e.g.
	// xxxx1234), and reading that into state would produce a permanent diff
	// against the configured value on every refresh.
	state.CreatedAt = types.StringValue(envVar.CreatedAt)

	// Set state
	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *projectEnvironmentVariableResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *projectEnvironmentVariableResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Retrieve values from state
	var state projectEnvironmentVariableResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Delete existing project environment variable. One already gone is the
	// desired end state, so absence is not an error.
	err := r.client.DeleteProjectEnvironmentVariable(ctx, state.ProjectSlug.ValueString(), state.Name.ValueString())
	if err != nil && !circleci.IsNotFound(err) {
		resp.Diagnostics.AddError(
			"Error Deleting CircleCi Project Environment Variable",
			circleci.Detail(err),
		)
		return
	}
}

// Configure adds the provider configured client to the resource.
func (r *projectEnvironmentVariableResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

// ImportState imports an existing resource into Terraform state.
// Expected import ID format: "project_slug/env_var_name".
// e.g. "circleci/org_id/project_id/MY_VAR".
func (r *projectEnvironmentVariableResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// The project slug contains slashes (e.g. "circleci/org/project"),
	// so split from the right to extract the env var name.
	lastSlash := strings.LastIndex(req.ID, "/")
	if lastSlash == -1 || lastSlash == 0 || lastSlash == len(req.ID)-1 {
		resp.Diagnostics.AddError(
			"Invalid Import ID Format",
			fmt.Sprintf("Expected import ID format: 'project_slug/env_var_name' (e.g. 'circleci/org_id/project_id/MY_VAR'). Got: %s", req.ID),
		)
		return
	}

	projectSlug := req.ID[:lastSlash]
	name := req.ID[lastSlash+1:]

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_slug"), projectSlug)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}
