// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Same constraint as group_membership_resource_test.go: circleci_group_membership
// (the data source too) is served on Client.PrivateHost(), which has no
// provider-schema override, so these drive Read directly rather than through
// resource.UnitTest and an HCL "data" block.

// groupMembershipDataSourceSchemaForTest returns the data source's schema.
func groupMembershipDataSourceSchemaForTest(t *testing.T) dschema.Schema {
	t.Helper()

	resp := &datasource.SchemaResponse{}
	(&groupMembershipDataSource{}).Schema(t.Context(), datasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}

	return resp.Schema
}

func TestGroupMembershipDataSourceRead(t *testing.T) {
	t.Parallel()

	api, host := newMockMembershipAPI(t)
	api.seedMembers(testUserA, testUserB)
	schema := groupMembershipDataSourceSchemaForTest(t)

	d := &groupMembershipDataSource{client: (&mockMembershipAPI{}).client(host)}

	cfgState := tfsdk.State{Schema: schema}
	if diags := cfgState.Set(t.Context(), groupMembershipDataSourceModel{
		OrganizationId: types.StringValue(testMembershipOrgID),
		OrgId:          types.StringNull(),
		GroupId:        types.StringValue(testMembershipGroupID),
		UserIds:        types.SetUnknown(types.StringType),
		Members:        nil,
	}); diags.HasError() {
		t.Fatalf("could not build a config value: %+v", diags)
	}

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schema}}
	d.Read(t.Context(), datasource.ReadRequest{Config: tfsdk.Config{Schema: schema, Raw: cfgState.Raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned diagnostics: %v", resp.Diagnostics)
	}

	var out groupMembershipDataSourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}

	if len(out.Members) != 2 {
		t.Fatalf("members = %v, want 2", out.Members)
	}

	byID := map[string]groupMemberItemModel{}
	for _, m := range out.Members {
		byID[m.UserId.ValueString()] = m
	}

	a, ok := byID[testUserA]
	if !ok {
		t.Fatalf("members does not include %s: %v", testUserA, out.Members)
	}
	if want := "user-" + testUserA[:4]; a.Username.ValueString() != want {
		t.Errorf("username = %q, want %q", a.Username.ValueString(), want)
	}
	if want := testUserA[:4] + "@example.com"; a.Email.ValueString() != want {
		t.Errorf("email = %q, want %q", a.Email.ValueString(), want)
	}
	if want := "https://avatars.example/" + testUserA[:4] + ".png"; a.AvatarUrl.ValueString() != want {
		t.Errorf("avatar_url = %q, want %q", a.AvatarUrl.ValueString(), want)
	}

	var gotIDs []string
	if diags := out.UserIds.ElementsAs(t.Context(), &gotIDs, false); diags.HasError() {
		t.Fatalf("reading back user_ids: %v", diags)
	}
	if len(gotIDs) != 2 {
		t.Errorf("user_ids = %v, want 2 entries", gotIDs)
	}
}

// TestGroupMembershipDataSourceRead_emptyGroup covers that an empty group
// reports an empty, non-null members list and user_ids set, so `for_each` and
// `length()` keep working against it rather than erroring on a null value.
func TestGroupMembershipDataSourceRead_emptyGroup(t *testing.T) {
	t.Parallel()

	_, host := newMockMembershipAPI(t)
	schema := groupMembershipDataSourceSchemaForTest(t)

	d := &groupMembershipDataSource{client: (&mockMembershipAPI{}).client(host)}

	cfgState := tfsdk.State{Schema: schema}
	if diags := cfgState.Set(t.Context(), groupMembershipDataSourceModel{
		OrganizationId: types.StringValue(testMembershipOrgID),
		OrgId:          types.StringNull(),
		GroupId:        types.StringValue(testMembershipGroupID),
		UserIds:        types.SetUnknown(types.StringType),
		Members:        nil,
	}); diags.HasError() {
		t.Fatalf("could not build a config value: %+v", diags)
	}

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schema}}
	d.Read(t.Context(), datasource.ReadRequest{Config: tfsdk.Config{Schema: schema, Raw: cfgState.Raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned diagnostics: %v", resp.Diagnostics)
	}

	var out groupMembershipDataSourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}

	if out.Members == nil || len(out.Members) != 0 {
		t.Errorf("members = %#v, want a non-nil empty slice", out.Members)
	}
	if out.UserIds.IsNull() {
		t.Error("user_ids is null, want a non-null empty set")
	}
	var gotIDs []string
	if diags := out.UserIds.ElementsAs(t.Context(), &gotIDs, false); diags.HasError() {
		t.Fatalf("reading back user_ids: %v", diags)
	}
	if len(gotIDs) != 0 {
		t.Errorf("user_ids = %v, want none", gotIDs)
	}
}

// TestGroupMembershipDataSourceRead_requiresStandaloneOrganization mirrors the
// resource's gate: deployment = "server" must be rejected with the same
// message, before any request reaches the API.
func TestGroupMembershipDataSourceRead_requiresStandaloneOrganization(t *testing.T) {
	t.Parallel()

	api, host := newMockMembershipAPI(t)
	schema := groupMembershipDataSourceSchemaForTest(t)

	serverClient := circleci.New(circleci.Config{
		Host:        "http://127.0.0.1:1",
		PrivateHost: host,
		Token:       "fake",
		Deployment:  circleci.DeploymentServer,
	})
	d := &groupMembershipDataSource{client: serverClient}

	cfgState := tfsdk.State{Schema: schema}
	if diags := cfgState.Set(t.Context(), groupMembershipDataSourceModel{
		OrganizationId: types.StringValue(testMembershipOrgID),
		OrgId:          types.StringNull(),
		GroupId:        types.StringValue(testMembershipGroupID),
		UserIds:        types.SetUnknown(types.StringType),
		Members:        nil,
	}); diags.HasError() {
		t.Fatalf("could not build a config value: %+v", diags)
	}

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schema}}
	d.Read(t.Context(), datasource.ReadRequest{Config: tfsdk.Config{Schema: schema, Raw: cfgState.Raw}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("Read on a server deployment produced no diagnostics, want one")
	}
	found := false
	for _, diag := range resp.Diagnostics {
		if diag.Summary() == "circleci_group_membership requires a standalone CircleCI organization" {
			found = true
		}
	}
	if !found {
		t.Errorf("diagnostics = %v, want a %q summary", resp.Diagnostics, "circleci_group_membership requires a standalone CircleCI organization")
	}

	if len(api.calls()) != 0 {
		t.Errorf("server deployment reached the fake API: %v, want no requests at all", api.calls())
	}
}
