// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

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

// The two Terraform type names this resource answers to, used in the Cloud-only
// diagnostic (see cloud_only.go). The singular data source is renamed the same way
// and shares both constants, so neither belongs in a file that a single deprecation
// takes with it.
const (
	pipelineTypeName           = "circleci_pipeline"
	pipelineDefinitionTypeName = "circleci_pipeline_definition"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                   = &pipelineResource{}
	_ resource.ResourceWithConfigure      = &pipelineResource{}
	_ resource.ResourceWithImportState    = &pipelineResource{}
	_ resource.ResourceWithMoveState      = &pipelineResource{}
	_ resource.ResourceWithValidateConfig = &pipelineResource{}
)

// pipelineResourceModel maps the output schema.
type pipelineResourceModel struct {
	Id                           types.String `tfsdk:"id"`
	ProjectId                    types.String `tfsdk:"project_id"`
	Name                         types.String `tfsdk:"name"`
	Description                  types.String `tfsdk:"description"`
	CreatedAt                    types.String `tfsdk:"created_at"`
	ConfigSourceProvider         types.String `tfsdk:"config_source_provider"`
	ConfigSourceFilePath         types.String `tfsdk:"config_source_file_path"`
	ConfigSourceRepoFullName     types.String `tfsdk:"config_source_repo_full_name"`
	ConfigSourceRepoExternalId   types.String `tfsdk:"config_source_repo_external_id"`
	CheckoutSourceProvider       types.String `tfsdk:"checkout_source_provider"`
	CheckoutSourceRepoFullName   types.String `tfsdk:"checkout_source_repo_full_name"`
	CheckoutSourceRepoExternalId types.String `tfsdk:"checkout_source_repo_external_id"`
}

// NewPipelineDefinitionResource is a helper function to simplify the provider
// implementation.
func NewPipelineDefinitionResource() resource.Resource {
	return &pipelineResource{}
}

// pipelineResource is the resource implementation.
type pipelineResource struct {
	client *circleci.Client

	// deprecated marks the copy registered under the old type name. See
	// pipeline_resource_rename.go.
	deprecated bool
}

// Metadata returns the resource type name.
func (r *pipelineResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName +
		renamedTypeName(r.deprecated, "_pipeline", "_pipeline_definition")
}

// Schema defines the schema for the resource.
func (r *pipelineResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a CircleCI pipeline definition. A pipeline definition specifies where to find the pipeline configuration and where to check out code from.\n\n" +
			"!> **CircleCI Cloud only.** Pipeline definitions live under `/api/v2` but are served by the " +
			"public API service, which CircleCI Server does not route.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The unique identifier of the pipeline.",
				Computed:            true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the project this pipeline belongs to. Changing this value forces a new resource to be created.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					// A pipeline definition does not exist outside the project it was
					// created under, so an in-place "update" of project_id would send
					// its PATCH to the new project carrying the old (and, there,
					// nonexistent) definition id. See DESIGN.md's characterization test
					// notes.
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the pipeline. Changing this value forces a new resource to be created.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					// *** This tells Terraform to replace if 'name' changes ***
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "A description of the pipeline.",
				Required:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The timestamp when the pipeline was created.",
				Computed:            true,
			},
			"config_source_provider": schema.StringAttribute{
				MarkdownDescription: "Where the pipeline's configuration is read from: " +
					markdownValueList(circleci.PipelineConfigSourceProviders()) + ". `github_app` and " +
					"`github_server` read configuration from a VCS repository, named by " +
					"`config_source_repo_external_id`. `circleci` is a CircleCI-hosted configuration: " +
					"there is no repository, and `config_source_repo_external_id` must be omitted — the " +
					"API rejects a repo on this branch outright rather than ignoring it.\n\n" +
					"~> **Changing this value forces a new resource to be created.** The update endpoint's " +
					"`config_source` accepts only `file_path`; provider is immutable after creation.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(circleci.PipelineConfigSourceProviders()...),
				},
				PlanModifiers: []planmodifier.String{
					// The update endpoint's config_source accepts only file_path — provider
					// (like repo, see config_source_repo_external_id below) is immutable
					// after creation. Without RequiresReplace a change here plans an
					// in-place update whose PATCH silently omits it: state would record
					// the new provider while the API kept the old one, and apply would
					// report success.
					stringplanmodifier.RequiresReplace(),
				},
			},
			"config_source_file_path": schema.StringAttribute{
				MarkdownDescription: "The path to the pipeline configuration file. Required for every " +
					"config_source_provider, including `circleci`, which still requires `file_path` " +
					"even though it has no repository to be relative to.",
				Required: true,
			},
			"config_source_repo_full_name": schema.StringAttribute{
				MarkdownDescription: "The full name of the repository containing the pipeline configuration. " +
					"Empty when config_source_provider is `circleci`, which has no repository.",
				Computed: true,
			},
			"config_source_repo_external_id": schema.StringAttribute{
				MarkdownDescription: "The external ID of the repository containing the pipeline " +
					"configuration: the VCS provider's own numeric repository id, not its name. Required " +
					"when config_source_provider is `github_app` or `github_server`; must be omitted when " +
					"it is `circleci`, which has no repository.\n\n" +
					"~> **Changing this value forces a new resource to be created.**",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					// The update endpoint's config_source accepts only file_path —
					// provider and repo are immutable after creation. Without
					// RequiresReplace a change here plans an in-place update whose
					// PATCH silently omits it: state would record the new value while
					// the API kept the old one, and apply would report success. See
					// DESIGN.md's characterization test notes.
					stringplanmodifier.RequiresReplace(),
				},
			},
			"checkout_source_provider": schema.StringAttribute{
				MarkdownDescription: "The VCS provider for the pipeline's checkout source: " +
					markdownValueList(circleci.PipelineCheckoutSourceProviders()) + ". Unlike " +
					"config_source_provider, this has no `circleci` (repo-less) option: the API " +
					"requires a real repository unconditionally, even when the pipeline's " +
					"configuration is hosted by CircleCI itself — a definition always checks out " +
					"code from somewhere.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(circleci.PipelineCheckoutSourceProviders()...),
				},
			},
			"checkout_source_repo_full_name": schema.StringAttribute{
				MarkdownDescription: "The full name of the repository used for code checkout.",
				Computed:            true,
			},
			"checkout_source_repo_external_id": schema.StringAttribute{
				MarkdownDescription: "The external ID of the repository to check out code from: the VCS " +
					"provider's own numeric repository id, not its name. Always required — " +
					"checkout_source has no repo-less provider.",
				Required: true,
			},
		},
	}

	if r.deprecated {
		resp.Schema.DeprecationMessage = pipelineRenameDeprecationMessage
		resp.Schema.MarkdownDescription = "~> **Deprecated in favour of " +
			"[`circleci_pipeline_definition`](pipeline_definition)**, which is what this " +
			"resource has always managed. Both names work and are the same resource; move " +
			"existing state with a `moved` block, which does not destroy anything.\n\n" +
			resp.Schema.MarkdownDescription
	}
}

// pipelineResourceModelFromAPI maps an API pipeline definition onto the
// resource model. projectID is threaded through rather than read from the API
// response because the definition itself carries no project_id field.
//
// config_source_repo_external_id is Optional rather than Computed (see the schema),
// so it must come back exactly null when the API sent no repo — a "circleci"
// config source always has none — or Terraform reports an inconsistent result after
// apply: the plan carries null (the practitioner omitted it), and a bare
// types.StringValue("") would not match that.
func pipelineResourceModelFromAPI(projectID types.String, definition circleci.PipelineDefinition) pipelineResourceModel {
	configSourceRepoExternalID := types.StringNull()
	if definition.ConfigSource.Repo.ExternalID != "" {
		configSourceRepoExternalID = types.StringValue(definition.ConfigSource.Repo.ExternalID)
	}

	return pipelineResourceModel{
		Id:                           types.StringValue(definition.ID),
		ProjectId:                    projectID,
		Name:                         types.StringValue(definition.Name),
		Description:                  types.StringValue(definition.Description),
		CreatedAt:                    types.StringValue(definition.CreatedAt),
		ConfigSourceProvider:         types.StringValue(definition.ConfigSource.Provider),
		ConfigSourceFilePath:         types.StringValue(definition.ConfigSource.FilePath),
		ConfigSourceRepoFullName:     types.StringValue(definition.ConfigSource.Repo.FullName),
		ConfigSourceRepoExternalId:   configSourceRepoExternalID,
		CheckoutSourceProvider:       types.StringValue(definition.CheckoutSource.Provider),
		CheckoutSourceRepoFullName:   types.StringValue(definition.CheckoutSource.Repo.FullName),
		CheckoutSourceRepoExternalId: types.StringValue(definition.CheckoutSource.Repo.ExternalID),
	}
}

// pipelineConfigSourceRepoInput builds the create body's config_source.repo,
// omitting it entirely for the "circleci" provider: that branch of the API's
// config_source oneOf has no repo property at all, and sending one fails the oneOf
// rather than being ignored. See circleci.PipelineConfigSourceProviderNeedsRepo.
func pipelineConfigSourceRepoInput(provider string, externalID types.String) *circleci.RepoInput {
	if !circleci.PipelineConfigSourceProviderNeedsRepo(provider) {
		return nil
	}

	return &circleci.RepoInput{ExternalID: externalID.ValueString()}
}

// Create creates the resource and sets the initial Terraform state.
func (r *pipelineResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireCloud(r.client, r.typeName(), &resp.Diagnostics) {
		return
	}

	var plan pipelineResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	input := circleci.CreatePipelineDefinitionInput{
		Name:        plan.Name.ValueString(),
		Description: plan.Description.ValueString(),
		ConfigSource: circleci.PipelineConfigSourceInput{
			Provider: plan.ConfigSourceProvider.ValueString(),
			Repo:     pipelineConfigSourceRepoInput(plan.ConfigSourceProvider.ValueString(), plan.ConfigSourceRepoExternalId),
			FilePath: plan.ConfigSourceFilePath.ValueString(),
		},
		CheckoutSource: circleci.PipelineCheckoutSourceInput{
			Provider: plan.CheckoutSourceProvider.ValueString(),
			Repo:     circleci.RepoInput{ExternalID: plan.CheckoutSourceRepoExternalId.ValueString()},
		},
	}

	created, err := r.client.CreatePipelineDefinition(ctx, plan.ProjectId.ValueString(), input)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating CircleCI pipeline",
			circleci.Detail(err),
		)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, pipelineResourceModelFromAPI(plan.ProjectId, *created))...)
}

// Read refreshes the Terraform state with the latest data.
func (r *pipelineResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireCloud(r.client, r.typeName(), &resp.Diagnostics) {
		return
	}

	var state pipelineResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	definition, err := r.client.GetPipelineDefinition(ctx, state.ProjectId.ValueString(), state.Id.ValueString())
	// A pipeline definition deleted outside Terraform must drop out of state so
	// the next plan recreates it, rather than becoming a permanent refresh
	// error. Absence is tested with circleci.IsNotFound rather than by
	// string-matching the error: matching "404" also matches a 5xx whose body
	// happens to mention it, which would silently remove live resources from
	// state. See DESIGN.md's characterization test notes.
	if circleci.IsNotFound(err) {
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Read CircleCI pipeline with id "+state.Id.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, pipelineResourceModelFromAPI(state.ProjectId, *definition))...)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *pipelineResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !requireCloud(r.client, r.typeName(), &resp.Diagnostics) {
		return
	}

	var plan pipelineResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state pipelineResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	input := circleci.UpdatePipelineDefinitionInput{
		Name:        plan.Name.ValueString(),
		Description: plan.Description.ValueString(),
		ConfigSource: circleci.PipelineConfigSourceUpdateInput{
			FilePath: plan.ConfigSourceFilePath.ValueString(),
		},
		CheckoutSource: circleci.PipelineCheckoutSourceInput{
			Provider: plan.CheckoutSourceProvider.ValueString(),
			Repo:     circleci.RepoInput{ExternalID: plan.CheckoutSourceRepoExternalId.ValueString()},
		},
	}

	updated, err := r.client.UpdatePipelineDefinition(ctx, plan.ProjectId.ValueString(), state.Id.ValueString(), input)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Update CircleCI pipeline definition with id "+state.Id.ValueString()+" and project id "+state.ProjectId.ValueString(),
			circleci.Detail(err),
		)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, pipelineResourceModelFromAPI(plan.ProjectId, *updated))...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *pipelineResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state pipelineResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A definition already gone is the desired end state, so absence is not an
	// error.
	err := r.client.DeletePipelineDefinition(ctx, state.ProjectId.ValueString(), state.Id.ValueString())
	if err != nil && !circleci.IsNotFound(err) {
		resp.Diagnostics.AddError(
			"Error Deleting CircleCI pipeline",
			circleci.Detail(err),
		)

		return
	}
}

// Configure adds the provider configured client to the resource.
func (r *pipelineResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	r.client = client
}

func (r *pipelineResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Expected format: "PROJECT_ID/PIPELINE_ID"
	parts := strings.SplitN(req.ID, "/", 2)

	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid Import ID Format",
			fmt.Sprintf("Expected import ID format: 'project_id/pipeline_id'. Got: %s", req.ID),
		)
		return
	}

	projectId := parts[0]
	pipelineId := parts[1]

	// 1. Set the primary key 'id'
	resp.Diagnostics.Append(resp.State.SetAttribute(
		ctx, path.Root("id"), pipelineId,
	)...)

	// 2. Set the required but unreadable 'project_id'
	resp.Diagnostics.Append(resp.State.SetAttribute(
		ctx, path.Root("project_id"), projectId,
	)...)

	if resp.Diagnostics.HasError() {
		return
	}
}
