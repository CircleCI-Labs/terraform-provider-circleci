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
				MarkdownDescription: "The timestamp when the pipeline was created. Empty for an " +
					"**implicit** pipeline definition — one CircleCI creates automatically for an " +
					"OAuth-backed project rather than through this resource's create route — which the " +
					"API never assigns a creation timestamp to. This resource can only create explicit " +
					"definitions (always timestamped), but an implicit one can still end up here through " +
					"`terraform import`, since the singular pipeline-definition route serves it.",
				Computed: true,
			},
			"config_source_provider": schema.StringAttribute{
				MarkdownDescription: "Where the pipeline's configuration is read from: " +
					markdownValueList(circleci.PipelineConfigSourceProviders()) + ", both of which read " +
					"configuration from a VCS repository named by `config_source_repo_external_id`.\n\n" +
					"~> **`circleci` is not an accepted value here.** It names a CircleCI-internal, " +
					"repo-less configuration source that this provider has never been able to create: " +
					"the create endpoint 400s on it for every file path a customer configuration would " +
					"plausibly use. A configuration written before this restriction was made explicit " +
					"gets a plan-time error explaining why, rather than a plain \"must be one of\".\n\n" +
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
				MarkdownDescription: "The path to the pipeline configuration file, relative to the " +
					"repository named by `config_source_repo_external_id`. Required for every accepted " +
					"config_source_provider.",
				Required: true,
			},
			"config_source_repo_full_name": schema.StringAttribute{
				MarkdownDescription: "The full name of the repository containing the pipeline configuration.",
				Computed:            true,
			},
			"config_source_repo_external_id": schema.StringAttribute{
				MarkdownDescription: "The external ID of the repository containing the pipeline " +
					"configuration: the VCS provider's own numeric repository id, not its name. Required " +
					"for both accepted config_source_provider values.\n\n" +
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
					markdownValueList(circleci.PipelineCheckoutSourceProviders()) + ". checkout_source has " +
					"no repo-less option at all — the API requires a real repository unconditionally, " +
					"even for a definition whose configuration is hosted by CircleCI itself — so, unlike " +
					"config_source_provider, there is nothing here that was ever removed.",
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
//
// created_at, by contrast, is left as a bare types.StringValue even when the API
// omitted it — which it always does for an implicit pipeline definition (see
// circleci.PipelineDefinition.CreatedAt). Unlike config_source_repo_external_id,
// nothing in config can ever supply created_at (it is Computed, not Optional), so
// there is no null-shaped plan value it needs to match, and the empty string is
// stable: this same implicit definition returns no created_at on every future
// read too, so no diff and no inconsistent-result error follows. See the
// attribute's MarkdownDescription in Schema, which is what actually needed fixing
// here — it used to promise a timestamp implicit definitions never get.
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
	//
	// Note that a deleted definition does NOT produce a 404 on the wire — the
	// singular route answers 400, with a body it also returns for definitions
	// that are alive and well on GitLab. Deciding "gone" from that response is
	// what made this resource unrecoverable after an out-of-band delete. The
	// client resolves the ambiguity against the plural list before reporting
	// IsNotFound at all, and reports a plain error when it cannot; see
	// circleci.GetPipelineDefinition. This code sees only the settled answer, so
	// IsNotFound here really does mean gone.
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
	// error. In practice the route never says so: measured over the network,
	// DELETE of an already-deleted definition answers 200 with the ordinary
	// success body. The IsNotFound tolerance stays as a guard in case that
	// changes.
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

// ImportState imports a pipeline definition from a "PROJECT_ID/PIPELINE_ID"
// address.
//
// An OAuth-based (github_oauth) project carries an *implicit* pipeline
// definition that CircleCI synthesizes rather than one a practitioner created.
// [NET] GET answers 200 for it — it looks importable — but PATCH answers 400
// "Failed to update pipeline definition." and DELETE answers 500 "Internal
// server error" and the definition survives. Importing one would build a
// Terraform resource that can never be updated or destroyed: every future
// `terraform destroy` would error forever. This fetches the definition before
// writing any state, and refuses the import outright when
// circleci.PipelineDefinitionIsImplicit says it is implicit — see that
// function for how "implicit" is told apart from an explicit definition,
// including one whose own config_source_provider happens to read
// "github_oauth" or "circleci".
func (r *pipelineResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !requireCloud(r.client, r.typeName(), &resp.Diagnostics) {
		return
	}

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

	definition, err := r.client.GetPipelineDefinition(ctx, projectId, pipelineId)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Read CircleCI pipeline definition with id "+pipelineId,
			circleci.Detail(err),
		)

		return
	}

	if circleci.PipelineDefinitionIsImplicit(*definition) {
		resp.Diagnostics.AddError(
			"Cannot Import Implicit Pipeline Definition",
			"The pipeline definition with id "+pipelineId+" on project "+projectId+" is the implicit "+
				"definition CircleCI manages for an OAuth-based (github_oauth) project, not one a "+
				"practitioner created. It can be read, but CircleCI's API rejects both PATCH (\"Failed "+
				"to update pipeline definition.\") and DELETE (the request fails with an internal server "+
				"error, and the definition survives) — so this provider has no way to update or destroy "+
				"it once imported, and `terraform destroy` would error forever.\n\n"+
				"It cannot be managed by "+pipelineDefinitionTypeName+". If this project needs an "+
				"explicit, manageable pipeline definition, create one with "+pipelineDefinitionTypeName+
				" instead of importing the implicit one.",
		)

		return
	}

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
