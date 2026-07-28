// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/CircleCI-Public/circleci-sdk-go/runner"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Runner administration is served by a separate origin (https://runner.circleci.com
// on Cloud, the Server host on CircleCI Server) and is plumbed through the
// provider's runner_host attribute into runner.NewServiceWithBaseURL.
//
// Canonical surface: every runner resource and data source in this provider
// deliberately targets the established `{runner_host}/api/v3/runner/...` surface
// (`/runner/resource`, `/runner/token`, `/runner/tasks`). A newer surface exists at
// `circleci.com/api/v3/runner/resource-classes` (plural, with an `/update` action),
// but it is being actively reshaped (the API PRs #1115/#1137), so it is
// intentionally NOT used here. Do not migrate until that surface is stable and the
// SDK exposes it; the existing surface is the one CircleCI Server also serves.
//
// Limitation: the circleci-sdk-go runner service returns untyped errors, so
// internal/circleci.IsNotFound cannot classify them. Runner code therefore cannot
// distinguish a 404 from any other failure, and must not string-match error text.
// A missing resource class is detected by its absence from a list response instead.

// runnerOrgIDPattern recognises the UUID that the runner API expects for
// organization identifiers. The runner API takes a UUID only — it does not accept
// an organization slug — so catching the wrong shape in the plan gives a better
// error than the API's 400.
var runnerOrgIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// runnerResourceClassPattern recognises a "namespace/name" resource class.
var runnerResourceClassPattern = regexp.MustCompile(`^[^/]+/[^/]+$`)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &runnerResourceClassResource{}
	_ resource.ResourceWithConfigure   = &runnerResourceClassResource{}
	_ resource.ResourceWithImportState = &runnerResourceClassResource{}
)

// runnerResourceClassResourceModel maps the resource schema.
type runnerResourceClassResourceModel struct {
	Id             types.String `tfsdk:"id"`
	OrganizationId types.String `tfsdk:"organization_id"`
	ResourceClass  types.String `tfsdk:"resource_class"`
	Description    types.String `tfsdk:"description"`
	ForceDelete    types.Bool   `tfsdk:"force_delete"`
}

// NewRunnerResourceClassResource is a helper function to simplify the provider implementation.
func NewRunnerResourceClassResource() resource.Resource {
	return &runnerResourceClassResource{}
}

// runnerResourceClassResource is the resource implementation.
type runnerResourceClassResource struct {
	client *runner.Service
}

// Metadata returns the resource type name.
func (r *runnerResourceClassResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_runner_resource_class"
}

// Schema defines the schema for the resource.
func (r *runnerResourceClassResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI runner resource class.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the runner resource class.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"organization_id": schema.StringAttribute{
				MarkdownDescription: "The UUID of the organization that owns the resource class.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(runnerOrgIDPattern, "must be an organization UUID"),
				},
			},
			"resource_class": schema.StringAttribute{
				MarkdownDescription: "The resource class name in `namespace/name` format (e.g. `myorg/myrunner`). Changing this value forces a new resource to be created.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(runnerResourceClassPattern, "must be in the format 'namespace/name'"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description of the runner resource class.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"force_delete": schema.BoolAttribute{
				MarkdownDescription: "If true, deletes the resource class even if it has associated tokens.",
				Optional:            true,
				Computed:            false,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *runnerResourceClassResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan runnerResourceClassResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// organization_id is Required in the schema and the API rejects a create
	// without org_id, so it has to be sent here.
	createReq := runner.CreateResourceClassRequest{
		OrganizationID: plan.OrganizationId.ValueString(),
		ResourceClass:  plan.ResourceClass.ValueString(),
		Description:    plan.Description.ValueString(),
	}

	rc, err := r.client.CreateResourceClass(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI runner resource class",
			"Could not create runner resource class, unexpected error: "+err.Error(),
		)
		return
	}

	plan.Id = types.StringValue(rc.Id)
	plan.ResourceClass = types.StringValue(rc.ResourceClass)
	plan.Description = types.StringValue(rc.Description)

	diags = resp.State.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
}

// Read refreshes the Terraform state with the latest data.
func (r *runnerResourceClassResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state runnerResourceClassResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	rcName := state.ResourceClass.ValueString()
	slashIdx := strings.Index(rcName, "/")
	if slashIdx == -1 {
		resp.Diagnostics.AddError(
			"Invalid resource_class format",
			fmt.Sprintf("Expected namespace/name format, got: %s", rcName),
		)
		return
	}
	namespace := rcName[:slashIdx]

	// Scope the list by organization as well as namespace. organization_id is null
	// immediately after an import (it is not part of the import ID), in which case
	// the namespace filter alone is used.
	classes, err := r.client.ListResourceClasses(ctx, namespace, state.OrganizationId.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading CircleCI runner resource classes",
			"Could not list runner resource classes for namespace "+namespace+": "+err.Error(),
		)
		return
	}

	var found *runner.ResourceClass
	for i := range classes.Items {
		if classes.Items[i].ResourceClass == rcName {
			found = &classes.Items[i]
			break
		}
	}

	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.Id = types.StringValue(found.Id)
	state.ResourceClass = types.StringValue(found.ResourceClass)
	state.Description = types.StringValue(found.Description)
	// ForceDelete is not returned by the API — preserve value from state.

	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

// Update persists plan values (such as organization_id and force_delete) into
// state. The runner API has no update endpoint, so no API call is made.
func (r *runnerResourceClassResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan runnerResourceClassResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *runnerResourceClassResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state runnerResourceClassResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteResourceClass(ctx, state.Id.ValueString(), state.ForceDelete.ValueBool())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error deleting CircleCI runner resource class",
			"Could not delete runner resource class "+state.Id.ValueString()+": "+err.Error(),
		)
	}
}

// Configure adds the provider configured client to the resource.
func (r *runnerResourceClassResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*CircleCiClientWrapper)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *CircleCiClientWrapper, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = client.RunnerService
}

// ImportState imports an existing resource class into Terraform state.
// The import ID is the resource_class string (e.g. "myorg/myrunner").
// After import, Read is called to populate the full state.
func (r *runnerResourceClassResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_class"), req.ID)...)
}
