// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &webhooksDataSource{}
	_ datasource.DataSourceWithConfigure = &webhooksDataSource{}
)

// webhooksDataSourceModel maps the data source schema.
type webhooksDataSourceModel struct {
	ProjectID types.String       `tfsdk:"project_id"`
	Webhooks  []webhookItemModel `tfsdk:"webhooks"`
}

// webhookItemModel maps one webhook in the list. The attribute names match
// `circleci_webhook`, including the flattened scope, so a webhook read here and a
// webhook read there describe themselves the same way.
type webhookItemModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	URL              types.String `tfsdk:"url"`
	Events           types.Set    `tfsdk:"events"`
	VerifyTLS        types.Bool   `tfsdk:"verify_tls"`
	ScopeID          types.String `tfsdk:"scope_id"`
	ScopeType        types.String `tfsdk:"scope_type"`
	HasSigningSecret types.Bool   `tfsdk:"has_signing_secret"`
	CreatedAt        types.String `tfsdk:"created_at"`
	UpdatedAt        types.String `tfsdk:"updated_at"`
}

// NewWebhooksDataSource is a helper function to simplify the provider implementation.
func NewWebhooksDataSource() datasource.DataSource {
	return &webhooksDataSource{}
}

// webhooksDataSource is the data source implementation.
type webhooksDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *webhooksDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webhooks"
}

// Schema defines the schema for the data source.
func (d *webhooksDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches every outbound webhook configured on a CircleCI project, including " +
			"webhooks created outside Terraform. Available on CircleCI Cloud and CircleCI Server.\n\n" +
			"Pagination is followed internally. The webhook service does not paginate today, but the " +
			"response carries the page token, so the whole collection is covered either way.\n\n" +
			"~> **Signing secrets are not returned.** The API masks every signing secret, so this data " +
			"source reports only whether one is set, as `has_signing_secret`. Do not try to read a " +
			"receiver credential back out of it.",
		Attributes: map[string]schema.Attribute{
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the project whose webhooks are listed. " +
					"This is the webhook scope id; `" + circleci.WebhookScopeTypeProject +
					"` is the only scope type the API supports.",
				Required: true,
			},
			"webhooks": schema.ListNestedAttribute{
				MarkdownDescription: "The webhooks on the project, in the order the API returns them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the webhook.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Name of the webhook.",
							Computed:            true,
						},
						"url": schema.StringAttribute{
							MarkdownDescription: "URL webhook payloads are delivered to.",
							Computed:            true,
						},
						// A Set, matching `circleci_webhook`'s own `events`: CircleCI
						// returns a webhook's events in an order of its own choosing
						// rather than the order they were submitted in, so there is no
						// order here worth reporting.
						"events": schema.SetAttribute{
							MarkdownDescription: "Events that trigger delivery, such as `" +
								circleci.WebhookEventWorkflowCompleted + "` and `" +
								circleci.WebhookEventJobCompleted + "`. Unordered: CircleCI does not " +
								"preserve the order events were configured in.",
							ElementType: types.StringType,
							Computed:    true,
						},
						"verify_tls": schema.BoolAttribute{
							MarkdownDescription: "Whether CircleCI verifies the receiver's TLS certificate " +
								"when delivering a payload.",
							Computed: true,
						},
						"scope_id": schema.StringAttribute{
							MarkdownDescription: "Unique identifier (UUID) of the scope the webhook watches, " +
								"which is the project id.",
							Computed: true,
						},
						"scope_type": schema.StringAttribute{
							MarkdownDescription: "Type of the scope the webhook watches. Always `" +
								circleci.WebhookScopeTypeProject + "`.",
							Computed: true,
						},
						"has_signing_secret": schema.BoolAttribute{
							MarkdownDescription: "Whether a signing secret is configured. The secret itself " +
								"is masked by the API and is never available here.",
							Computed: true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "Timestamp the webhook was created, as the API reported it.",
							Computed:            true,
						},
						"updated_at": schema.StringAttribute{
							MarkdownDescription: "Timestamp the webhook was last updated, as the API reported it.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// Read lists the project's webhooks.
func (d *webhooksDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state webhooksDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := state.ProjectID.ValueString()

	webhooks, err := d.client.ListWebhooks(ctx, projectID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list CircleCI webhooks for project "+projectID,
			circleci.Detail(err),
		)

		return
	}

	// An empty, non-null list keeps `for_each` and `length()` working against a
	// project that has no webhooks yet.
	state.Webhooks = make([]webhookItemModel, 0, len(webhooks))
	for _, hook := range webhooks {
		events, diags := types.SetValueFrom(ctx, types.StringType, hook.Events)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}

		state.Webhooks = append(state.Webhooks, webhookItemModel{
			ID:               types.StringValue(hook.ID),
			Name:             types.StringValue(hook.Name),
			URL:              types.StringValue(hook.URL),
			Events:           events,
			VerifyTLS:        types.BoolValue(hook.VerifyTLS),
			ScopeID:          types.StringValue(hook.Scope.ID),
			ScopeType:        types.StringValue(hook.Scope.Type),
			HasSigningSecret: types.BoolValue(hook.HasSigningSecret()),
			CreatedAt:        types.StringValue(hook.CreatedAt),
			UpdatedAt:        types.StringValue(hook.UpdatedAt),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *webhooksDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
