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
	_ datasource.DataSource              = &userDataSource{}
	_ datasource.DataSourceWithConfigure = &userDataSource{}
)

// userDataSourceModel maps the data source schema.
//
// ID is both an optional input and the identity of the result: supplying it looks
// that user up, and omitting it resolves whoever the configured token belongs to
// and then reports their id. That is why it is Optional and Computed rather than
// one or the other.
type userDataSourceModel struct {
	ID        types.String `tfsdk:"id"`
	Login     types.String `tfsdk:"login"`
	Name      types.String `tfsdk:"name"`
	AvatarURL types.String `tfsdk:"avatar_url"`
}

// NewUserDataSource is a helper function to simplify the provider implementation.
func NewUserDataSource() datasource.DataSource {
	return &userDataSource{}
}

// userDataSource reads a CircleCI user.
type userDataSource struct {
	client *circleci.Client
}

// Metadata returns the data source type name.
func (d *userDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

// Schema defines the schema for the data source.
func (d *userDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches a CircleCI user.\n\n" +
			"With no `id`, this reports the user the configured API token authenticates as, which is the " +
			"usual use: confirming which account a pipeline's token belongs to, or tagging resources with " +
			"the identity that created them. With an `id`, it looks that user up instead.\n\n" +
			"There is no matching resource. CircleCI has no API for creating, updating or deleting a user " +
			"account, and organization membership is managed in the web UI.\n\n" +
			"~> **A user token is required either way.** Both routes authorize the caller rather than the " +
			"subject, so a project or organization token is rejected with a permissions error rather than " +
			"returning nothing.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier (UUID) of the user to fetch. Omit it to fetch the user " +
					"the configured token authenticates as, in which case this reports that user's ID.\n\n" +
					"This is a CircleCI user ID, not a VCS user ID. It is the value that appears as the actor " +
					"on a pipeline or workflow.",
				Optional: true,
				Computed: true,
			},
			"login": schema.StringAttribute{
				MarkdownDescription: "The user's login on their VCS.",
				Computed:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The user's display name.",
				Computed:            true,
			},
			"avatar_url": schema.StringAttribute{
				MarkdownDescription: "URL of the user's avatar on their VCS.",
				Computed:            true,
			},
		},
	}
}

// Read fetches the user.
func (d *userDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		return
	}

	var state userDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// An unknown id is treated as absent as well as a null one. That matters when
	// the id comes from another resource that has not been created yet: the read is
	// deferred by Terraform in that case, but being defensive here keeps an unknown
	// from being sent as a literal path segment.
	userID := state.ID.ValueString()
	lookupByID := !state.ID.IsNull() && !state.ID.IsUnknown() && userID != ""

	var (
		user *circleci.User
		err  error
	)
	if lookupByID {
		user, err = d.client.Users().Get(ctx, userID)
	} else {
		user, err = d.client.Users().Current(ctx)
	}

	if err != nil {
		summary := "Unable to read the current CircleCI user"
		if lookupByID {
			summary = "Unable to read CircleCI user " + userID
		}

		// A 403 here almost always means the token is not a user token, which is
		// worth saying outright: the routes authorize the caller, so the failure has
		// nothing to do with the user being looked up.
		detail := circleci.Detail(err)
		if circleci.IsUnauthorized(err) {
			detail += "\n\nBoth /me and /user/{id} require a personal API token. A project or organization " +
				"token cannot read either, whichever user is being requested."
		}

		resp.Diagnostics.AddError(summary, detail)

		return
	}

	state.ID = types.StringValue(user.ID)
	state.Login = types.StringValue(user.Login)
	state.Name = types.StringValue(user.Name)
	state.AvatarURL = types.StringValue(user.AvatarURL)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Configure adds the provider configured client to the data source.
func (d *userDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := apiClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}

	d.client = client
}
