// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// circleci_group_membership is served entirely on Client.PrivateHost(), which
// (deliberately — see internal/circleci/private.go) has no provider-schema
// attribute to redirect at a local fake, unlike Client.Host() or RunnerHost().
// So unlike this package's other fake-backed tests, these cannot drive the
// resource through resource.UnitTest and an HCL "provider" block: there would
// be no way to stop the request leaving for https://app.circleci.com for real.
//
// Instead these call Create/Read/Update/Delete/ImportState directly against a
// resource built with circleci.New(circleci.Config{PrivateHost: fake.URL}) —
// the same technique TestIOSSigningWriteOnly_GuardsAgainstMissingCredentials
// and TestCloudOnlyModifyPlanAllowsDestroy use to reach a path a full
// Terraform run cannot. Schema-level behaviour (plan modifiers, requiredness)
// is still asserted directly against the schema, same as before.

const (
	testMembershipOrgID   = "00000000-1111-2222-3333-444444444444"
	testMembershipGroupID = "55555555-6666-7777-8888-999999999999"

	testUserA = "aaaaaaaa-0000-0000-0000-000000000001"
	testUserB = "bbbbbbbb-0000-0000-0000-000000000002"
	testUserC = "cccccccc-0000-0000-0000-000000000003"
	testUserD = "dddddddd-0000-0000-0000-000000000004"
)

// membershipCall is one mutating call the provider made, so that tests can assert
// on the add/remove delta rather than only on the end state.
type membershipCall struct {
	action  string // "add" or "remove"
	userIDs []string
}

// mockMembershipAPI is an in-memory stand-in for the private group membership
// routes (internal/circleci/group_membership.go).
type mockMembershipAPI struct {
	t *testing.T

	mu sync.Mutex
	// members maps group id to the user ids in it, in insertion order.
	members map[string][]string
	// missing marks groups that answer 404, for drift tests.
	missing map[string]bool
	// recorded is every mutating call, in order.
	recorded []membershipCall
}

// newMockMembershipAPI starts a mock membership API and returns it alongside its
// origin.
func newMockMembershipAPI(t *testing.T) (*mockMembershipAPI, string) {
	t.Helper()

	api := &mockMembershipAPI{
		t:       t,
		members: map[string][]string{},
		missing: map[string]bool{},
	}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	return api, srv.URL
}

// client returns a *circleci.Client whose private origin is this fake, and
// whose main host is deliberately unroutable — every group-membership call
// must go to PrivateHost, never Host.
func (m *mockMembershipAPI) client(host string) *circleci.Client {
	return circleci.New(circleci.Config{
		Host:        "http://127.0.0.1:1",
		PrivateHost: host,
		Token:       "fake",
		Deployment:  circleci.DeploymentCloud,
	})
}

// seedMembers puts users into the managed group behind Terraform's back.
func (m *mockMembershipAPI) seedMembers(userIDs ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.members[testMembershipGroupID] = append(m.members[testMembershipGroupID], userIDs...)
}

// removeGroup makes the managed group answer 404, as if it had been deleted
// outside Terraform.
func (m *mockMembershipAPI) removeGroup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.missing[testMembershipGroupID] = true
}

// currentMembers returns the managed group's membership, sorted for comparison.
func (m *mockMembershipAPI) currentMembers() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	members := slices.Clone(m.members[testMembershipGroupID])
	slices.Sort(members)

	return members
}

// calls returns the mutating calls recorded so far.
func (m *mockMembershipAPI) calls() []membershipCall {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.Clone(m.recorded)
}

func (m *mockMembershipAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// /private/ciam/orgs/{org_id}/groups/{group_id}/{users|add-users|delete-users}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 7 || parts[0] != "private" || parts[1] != "ciam" ||
		parts[2] != "orgs" || parts[4] != "groups" {
		m.write(w, http.StatusNotFound, map[string]string{"message": "Not Found: " + r.URL.Path})

		return
	}

	groupID, action := parts[5], parts[6]

	if m.missing[groupID] {
		m.write(w, http.StatusNotFound, map[string]string{"message": "Group not found"})

		return
	}

	switch {
	case action == "users" && r.Method == http.MethodGet:
		m.list(w, groupID)
	case action == "add-users" && r.Method == http.MethodPost:
		m.mutate(w, r, groupID, "add")
	case action == "delete-users" && r.Method == http.MethodPost:
		m.mutate(w, r, groupID, "remove")
	default:
		m.write(w, http.StatusMethodNotAllowed, map[string]string{"message": "Method Not Allowed"})
	}
}

func (m *mockMembershipAPI) list(w http.ResponseWriter, groupID string) {
	type member struct {
		UserID    string `json:"user_id"`
		Username  string `json:"username"`
		AvatarURL string `json:"avatar_url"`
		Email     string `json:"email"`
		GroupID   string `json:"group_id"`
		CreatedAt string `json:"created_at"`
	}

	items := make([]member, 0, len(m.members[groupID]))
	for _, id := range m.members[groupID] {
		items = append(items, member{
			UserID:    id,
			Username:  "user-" + id[:4],
			AvatarURL: "https://avatars.example/" + id[:4] + ".png",
			Email:     id[:4] + "@example.com",
			GroupID:   groupID,
			CreatedAt: "2024-01-02T03:04:05.000000Z",
		})
	}

	// No "count" field: [NET] confirms the real route answers only
	// {"items": [...]}, for both an empty and a populated group. See
	// groupMembersResponse in internal/circleci/group_membership.go.
	m.write(w, http.StatusOK, struct {
		Items []member `json:"items"`
	}{Items: items})
}

func (m *mockMembershipAPI) mutate(w http.ResponseWriter, r *http.Request, groupID, action string) {
	var body struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.write(w, http.StatusBadRequest, map[string]string{"message": "invalid body"})

		return
	}
	// The real API rejects an empty list, so the provider must never send one.
	if len(body.UserIDs) == 0 {
		m.write(w, http.StatusBadRequest, map[string]string{"message": "No valid user_ids provided"})

		return
	}

	m.recorded = append(m.recorded, membershipCall{action: action, userIDs: slices.Clone(body.UserIDs)})

	for _, id := range body.UserIDs {
		switch action {
		case "add":
			if !slices.Contains(m.members[groupID], id) {
				m.members[groupID] = append(m.members[groupID], id)
			}
		case "remove":
			if i := slices.Index(m.members[groupID], id); i >= 0 {
				m.members[groupID] = slices.Delete(m.members[groupID], i, i+1)
			}
		}
	}

	m.write(w, http.StatusOK, map[string]string{"message": action + "ed user(s)"})
}

func (m *mockMembershipAPI) write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		m.t.Errorf("encoding mock response: %v", err)
	}
}

// assertCall checks that exactly one call with the given action was made, with
// want as its user ids in any order. Set ordering is not guaranteed, so the
// comparison is order-insensitive.
func assertCall(t *testing.T, calls []membershipCall, action string, want []string) {
	t.Helper()

	var matches []membershipCall
	for _, call := range calls {
		if call.action == action {
			matches = append(matches, call)
		}
	}

	if len(want) == 0 {
		if len(matches) != 0 {
			t.Errorf("got %d %q calls (%v), want none", len(matches), action, matches)
		}

		return
	}

	if len(matches) != 1 {
		t.Fatalf("got %d %q calls (%v), want exactly 1", len(matches), action, matches)
	}

	got := slices.Clone(matches[0].userIDs)
	slices.Sort(got)
	wantSorted := slices.Clone(want)
	slices.Sort(wantSorted)

	if !slices.Equal(got, wantSorted) {
		t.Errorf("%q call user_ids = %v, want %v", action, got, wantSorted)
	}
}

// --- direct-invocation helpers ------------------------------------------------

// groupMembershipResourceSchemaForTest returns the resource's schema, the same
// way ios_signing_write_only_test.go's helper of the same shape does.
func groupMembershipResourceSchemaForTest(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	(&groupMembershipResource{}).Schema(t.Context(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}

	return resp.Schema
}

// membershipStateForTest builds a tfsdk.State (or, read as a Plan/Config, the
// identical raw value) from a fully-populated model — see configForTest in
// environment_variable_write_only_test.go for the same technique applied to a
// Config specifically.
func membershipStateForTest(t *testing.T, schema rschema.Schema, model groupMembershipResourceModel) tfsdk.State {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a state value: %+v", diags)
	}

	return state
}

// membershipModel builds a model for testMembershipOrgID/testMembershipGroupID
// and the given userIDs, leaving computed attributes null — the shape a
// practitioner's configuration takes.
func membershipModel(t *testing.T, userIDs ...string) groupMembershipResourceModel {
	t.Helper()

	ids, diags := types.SetValueFrom(t.Context(), types.StringType, userIDs)
	if diags.HasError() {
		t.Fatalf("could not build user_ids: %+v", diags)
	}

	return groupMembershipResourceModel{
		OrganizationId: types.StringValue(testMembershipOrgID),
		OrgId:          types.StringNull(),
		GroupId:        types.StringValue(testMembershipGroupID),
		UserIds:        ids,
	}
}

func TestGroupMembershipResourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwresource.SchemaResponse{}
	NewGroupMembershipResource().Schema(ctx, fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// The two identifiers address the membership, so a change to either must
	// replace the resource rather than silently re-point it at another group.
	for _, name := range []string{"organization_id", "group_id"} {
		attribute, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("schema is missing the %q attribute", name)
		}

		stringAttribute, ok := attribute.(rschema.StringAttribute)
		if !ok {
			t.Fatalf("attribute %q is %T, want rschema.StringAttribute", name, attribute)
		}

		var forcesReplacement bool
		for _, modifier := range stringAttribute.PlanModifiers {
			if strings.Contains(strings.ToLower(modifier.Description(ctx)), "destroy and recreate") {
				forcesReplacement = true
			}
		}
		if !forcesReplacement {
			t.Errorf("attribute %q has no plan modifier forcing replacement", name)
		}
	}

	// user_ids must be required: the resource owns the complete membership, and an
	// optional list would make "no members" indistinguishable from "unmanaged".
	userIDs, ok := resp.Schema.Attributes["user_ids"]
	if !ok {
		t.Fatal("schema is missing the \"user_ids\" attribute")
	}
	if !userIDs.IsRequired() {
		t.Error("attribute \"user_ids\" is not required, but the resource owns the full member list")
	}
}

func TestGroupMembershipResourceImportStateRejectsMalformedID(t *testing.T) {
	t.Parallel()

	// The import ID must carry both ids, because a group id is only meaningful
	// within its organization.
	for _, id := range []string{
		"",
		"only-a-group-id",
		"/group-id",
		"organization-id/",
	} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			r, ok := NewGroupMembershipResource().(*groupMembershipResource)
			if !ok {
				t.Fatal("NewGroupMembershipResource did not return a *groupMembershipResource")
			}

			resp := &fwresource.ImportStateResponse{}
			r.ImportState(t.Context(), fwresource.ImportStateRequest{ID: id}, resp)

			if !resp.Diagnostics.HasError() {
				t.Errorf("import of %q produced no error, want one", id)
			}
		})
	}
}

// TestGroupMembershipResourceImportState_RoundTripsWithRead proves the round
// trip `terraform import` actually drives for this resource: ImportState sets
// only organization_id/org_id/group_id/id (see groupMembershipResource.
// ImportState), and the framework's own post-import refresh then calls Read
// against that state. This asserts the combination reproduces exactly the
// state Create left behind, including user_ids — the condition that makes
// the plan following an import empty. There is no secret attribute here to
// complicate that: user_ids is a set of UUIDs, and Read always reports the
// group's live membership in full.
func TestGroupMembershipResourceImportState_RoundTripsWithRead(t *testing.T) {
	t.Parallel()

	api, host := newMockMembershipAPI(t)
	schema := groupMembershipResourceSchemaForTest(t)
	r := &groupMembershipResource{client: api.client(host)}

	createPlan := membershipStateForTest(t, schema, membershipModel(t, testUserA, testUserB))
	createResp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: createPlan.Raw}}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create returned diagnostics: %v", createResp.Diagnostics)
	}

	var created groupMembershipResourceModel
	if diags := createResp.State.Get(t.Context(), &created); diags.HasError() {
		t.Fatalf("reading back created state: %v", diags)
	}

	// ImportState only ever receives a bare "organization_id/group_id" id and an
	// empty (all-null-but-typed) state to write into, the same as a real
	// `terraform import` call.
	importResp := &fwresource.ImportStateResponse{
		State: membershipStateForTest(t, schema, groupMembershipResourceModel{
			Id:             types.StringNull(),
			OrganizationId: types.StringNull(),
			OrgId:          types.StringNull(),
			GroupId:        types.StringNull(),
			UserIds:        types.SetNull(types.StringType),
		}),
	}
	r.ImportState(t.Context(), fwresource.ImportStateRequest{
		ID: testMembershipOrgID + "/" + testMembershipGroupID,
	}, importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState returned diagnostics: %v", importResp.Diagnostics)
	}

	readResp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema}}
	r.Read(t.Context(), fwresource.ReadRequest{State: importResp.State}, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read returned diagnostics: %v", readResp.Diagnostics)
	}

	var imported groupMembershipResourceModel
	if diags := readResp.State.Get(t.Context(), &imported); diags.HasError() {
		t.Fatalf("reading back imported state: %v", diags)
	}

	if imported.Id.ValueString() != created.Id.ValueString() {
		t.Errorf("imported id = %q, want %q (the value Create produced)", imported.Id.ValueString(), created.Id.ValueString())
	}
	if imported.OrganizationId.ValueString() != created.OrganizationId.ValueString() {
		t.Errorf("imported organization_id = %q, want %q", imported.OrganizationId.ValueString(), created.OrganizationId.ValueString())
	}
	if imported.OrgId.ValueString() != created.OrgId.ValueString() {
		t.Errorf("imported org_id = %q, want %q", imported.OrgId.ValueString(), created.OrgId.ValueString())
	}
	if imported.GroupId.ValueString() != created.GroupId.ValueString() {
		t.Errorf("imported group_id = %q, want %q", imported.GroupId.ValueString(), created.GroupId.ValueString())
	}

	var gotIDs, wantIDs []string
	if diags := imported.UserIds.ElementsAs(t.Context(), &gotIDs, false); diags.HasError() {
		t.Fatalf("reading back imported user_ids: %v", diags)
	}
	if diags := created.UserIds.ElementsAs(t.Context(), &wantIDs, false); diags.HasError() {
		t.Fatalf("reading back created user_ids: %v", diags)
	}
	slices.Sort(gotIDs)
	slices.Sort(wantIDs)
	if !slices.Equal(gotIDs, wantIDs) {
		t.Errorf("imported user_ids = %v, want %v (the value Create produced)", gotIDs, wantIDs)
	}
}

// TestGroupMembershipResourceCreate covers: an empty group gets exactly one add
// call and no remove call, and the resulting state carries the right id and
// user_ids.
func TestGroupMembershipResourceCreate(t *testing.T) {
	t.Parallel()

	api, host := newMockMembershipAPI(t)
	schema := groupMembershipResourceSchemaForTest(t)
	r := &groupMembershipResource{client: api.client(host)}

	plan := membershipStateForTest(t, schema, membershipModel(t, testUserA, testUserB))
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: plan.Raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create returned diagnostics: %v", resp.Diagnostics)
	}

	assertCall(t, api.calls(), "add", []string{testUserA, testUserB})
	assertCall(t, api.calls(), "remove", nil)

	if got := api.currentMembers(); !slices.Equal(got, []string{testUserA, testUserB}) {
		t.Errorf("members = %v, want [%s %s]", got, testUserA, testUserB)
	}

	var out groupMembershipResourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if want := testMembershipOrgID + "/" + testMembershipGroupID; out.Id.ValueString() != want {
		t.Errorf("id = %q, want %q", out.Id.ValueString(), want)
	}
}

// TestGroupMembershipResourceCreate_takesOverExistingMembers covers exclusive
// ownership on create: a group that already has members not listed in the
// configuration has them removed rather than merged with.
func TestGroupMembershipResourceCreate_takesOverExistingMembers(t *testing.T) {
	t.Parallel()

	api, host := newMockMembershipAPI(t)
	api.seedMembers(testUserA, testUserB)
	schema := groupMembershipResourceSchemaForTest(t)
	r := &groupMembershipResource{client: api.client(host)}

	plan := membershipStateForTest(t, schema, membershipModel(t, testUserA, testUserC))
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: plan.Raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create returned diagnostics: %v", resp.Diagnostics)
	}

	assertCall(t, api.calls(), "remove", []string{testUserB})
	assertCall(t, api.calls(), "add", []string{testUserC})

	want := []string{testUserA, testUserC}
	slices.Sort(want)
	if got := api.currentMembers(); !slices.Equal(got, want) {
		t.Errorf("members = %v, want %v", got, want)
	}
}

// TestGroupMembershipResourceCreate_emptySetEmptiesTheGroup covers that an empty
// user_ids is a valid desired state ("no members") rather than "unmanaged", and
// that emptying a group issues only a remove call — the real API rejects an
// empty user_ids array on add-users, so an unconditional add call here would
// break every empty-group apply.
func TestGroupMembershipResourceCreate_emptySetEmptiesTheGroup(t *testing.T) {
	t.Parallel()

	api, host := newMockMembershipAPI(t)
	api.seedMembers(testUserA, testUserB)
	schema := groupMembershipResourceSchemaForTest(t)
	r := &groupMembershipResource{client: api.client(host)}

	plan := membershipStateForTest(t, schema, membershipModel(t))
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: plan.Raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create returned diagnostics: %v", resp.Diagnostics)
	}

	assertCall(t, api.calls(), "remove", []string{testUserA, testUserB})
	assertCall(t, api.calls(), "add", nil)

	if got := api.currentMembers(); len(got) != 0 {
		t.Errorf("members = %v, want none", got)
	}
}

// TestGroupMembershipResourceUpdate covers the delta: {a,b} -> {a,c,d} must
// remove only b and add only c and d. Re-adding a, or removing and re-adding
// it, would be wrong — and would also fail against the real API's rejection of
// a no-op-sized request only by accident, not by design, so this is asserted
// directly on the calls made.
func TestGroupMembershipResourceUpdate(t *testing.T) {
	t.Parallel()

	api, host := newMockMembershipAPI(t)
	api.seedMembers(testUserA, testUserB)
	schema := groupMembershipResourceSchemaForTest(t)
	r := &groupMembershipResource{client: api.client(host)}

	plan := membershipStateForTest(t, schema, membershipModel(t, testUserA, testUserC, testUserD))
	resp := &fwresource.UpdateResponse{State: tfsdk.State{Schema: schema}}
	r.Update(t.Context(), fwresource.UpdateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: plan.Raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Update returned diagnostics: %v", resp.Diagnostics)
	}

	assertCall(t, api.calls(), "remove", []string{testUserB})
	assertCall(t, api.calls(), "add", []string{testUserC, testUserD})

	want := []string{testUserA, testUserC, testUserD}
	slices.Sort(want)
	if got := api.currentMembers(); !slices.Equal(got, want) {
		t.Errorf("members = %v, want %v", got, want)
	}
}

// TestGroupMembershipResourceUpdate_correctsDriftAgainstLiveMembership proves
// the delta in Update is computed against what the API reports right now, not
// against prior Terraform state: a member added outside Terraform since the
// last apply is corrected in this same call rather than surviving because state
// did not know about it.
func TestGroupMembershipResourceUpdate_correctsDriftAgainstLiveMembership(t *testing.T) {
	t.Parallel()

	api, host := newMockMembershipAPI(t)
	// The live group has A (from a prior apply) and B (added outside Terraform).
	api.seedMembers(testUserA, testUserB)
	schema := groupMembershipResourceSchemaForTest(t)
	r := &groupMembershipResource{client: api.client(host)}

	// The configuration still only wants A: B must be removed even though the
	// prior state (not consulted here) never recorded it.
	plan := membershipStateForTest(t, schema, membershipModel(t, testUserA))
	resp := &fwresource.UpdateResponse{State: tfsdk.State{Schema: schema}}
	r.Update(t.Context(), fwresource.UpdateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: plan.Raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Update returned diagnostics: %v", resp.Diagnostics)
	}

	assertCall(t, api.calls(), "remove", []string{testUserB})
	assertCall(t, api.calls(), "add", nil)

	if got := api.currentMembers(); !slices.Equal(got, []string{testUserA}) {
		t.Errorf("members = %v, want [%s]", got, testUserA)
	}
}

// TestGroupMembershipResourceRead covers the normal path: Read reports the
// group's current membership and re-derives id and the org attribute pair.
func TestGroupMembershipResourceRead(t *testing.T) {
	t.Parallel()

	api, host := newMockMembershipAPI(t)
	api.seedMembers(testUserA, testUserB)
	schema := groupMembershipResourceSchemaForTest(t)
	r := &groupMembershipResource{client: api.client(host)}

	prior := membershipStateForTest(t, schema, groupMembershipResourceModel{
		Id:             types.StringValue(testMembershipOrgID + "/" + testMembershipGroupID),
		OrganizationId: types.StringValue(testMembershipOrgID),
		OrgId:          types.StringValue(testMembershipOrgID),
		GroupId:        types.StringValue(testMembershipGroupID),
		UserIds:        mustSetValue(t, testUserA),
	})

	resp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema}}
	r.Read(t.Context(), fwresource.ReadRequest{State: prior}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned diagnostics: %v", resp.Diagnostics)
	}

	var out groupMembershipResourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}

	var gotIDs []string
	if diags := out.UserIds.ElementsAs(t.Context(), &gotIDs, false); diags.HasError() {
		t.Fatalf("reading back user_ids: %v", diags)
	}
	slices.Sort(gotIDs)

	// Read must report the live membership (A and B), not the state it was
	// handed (which only knew about A) — this is the mechanism that lets
	// Terraform surface a member added outside Terraform as drift.
	want := []string{testUserA, testUserB}
	if !slices.Equal(gotIDs, want) {
		t.Errorf("user_ids = %v, want %v (Read must reflect live membership, not prior state)", gotIDs, want)
	}
}

// TestGroupMembershipResourceRead_deletedGroupRemovesFromState covers a group
// deleted outside Terraform: Read must drop the resource from state (by
// leaving resp.State empty) rather than erroring, so the next plan recreates
// it instead of jamming on a permanent error.
func TestGroupMembershipResourceRead_deletedGroupRemovesFromState(t *testing.T) {
	t.Parallel()

	api, host := newMockMembershipAPI(t)
	api.removeGroup()
	schema := groupMembershipResourceSchemaForTest(t)
	r := &groupMembershipResource{client: api.client(host)}

	prior := membershipStateForTest(t, schema, groupMembershipResourceModel{
		Id:             types.StringValue(testMembershipOrgID + "/" + testMembershipGroupID),
		OrganizationId: types.StringValue(testMembershipOrgID),
		OrgId:          types.StringValue(testMembershipOrgID),
		GroupId:        types.StringValue(testMembershipGroupID),
		UserIds:        mustSetValue(t, testUserA),
	})

	resp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema}}
	r.Read(t.Context(), fwresource.ReadRequest{State: prior}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned diagnostics for a deleted group, want a clean drop from state: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Errorf("Read left state populated for a deleted group, want it removed")
	}
}

// TestGroupMembershipResourceDelete covers that Delete removes only the users
// recorded in state, tolerating a group that is already gone.
func TestGroupMembershipResourceDelete(t *testing.T) {
	t.Parallel()

	api, host := newMockMembershipAPI(t)
	// B was added outside Terraform after the last apply; state only knows A.
	api.seedMembers(testUserA, testUserB)
	schema := groupMembershipResourceSchemaForTest(t)
	r := &groupMembershipResource{client: api.client(host)}

	state := membershipStateForTest(t, schema, groupMembershipResourceModel{
		Id:             types.StringValue(testMembershipOrgID + "/" + testMembershipGroupID),
		OrganizationId: types.StringValue(testMembershipOrgID),
		OrgId:          types.StringValue(testMembershipOrgID),
		GroupId:        types.StringValue(testMembershipGroupID),
		UserIds:        mustSetValue(t, testUserA),
	})

	resp := &fwresource.DeleteResponse{State: state}
	r.Delete(t.Context(), fwresource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete returned diagnostics: %v", resp.Diagnostics)
	}

	assertCall(t, api.calls(), "remove", []string{testUserA})
	assertCall(t, api.calls(), "add", nil)

	// B survives: Delete only clears what this resource put there.
	if got := api.currentMembers(); !slices.Equal(got, []string{testUserB}) {
		t.Errorf("members = %v, want [%s] (only A, from state, should have been removed)", got, testUserB)
	}
}

func TestGroupMembershipResourceDelete_toleratesAlreadyDeletedGroup(t *testing.T) {
	t.Parallel()

	api, host := newMockMembershipAPI(t)
	api.removeGroup()
	schema := groupMembershipResourceSchemaForTest(t)
	r := &groupMembershipResource{client: api.client(host)}

	state := membershipStateForTest(t, schema, groupMembershipResourceModel{
		Id:             types.StringValue(testMembershipOrgID + "/" + testMembershipGroupID),
		OrganizationId: types.StringValue(testMembershipOrgID),
		OrgId:          types.StringValue(testMembershipOrgID),
		GroupId:        types.StringValue(testMembershipGroupID),
		UserIds:        mustSetValue(t, testUserA),
	})

	resp := &fwresource.DeleteResponse{State: state}
	r.Delete(t.Context(), fwresource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("Delete of an already-deleted group returned diagnostics, want none: %v", resp.Diagnostics)
	}
}

// --- the gate ------------------------------------------------------------------

// TestGroupMembershipResourceRequiresStandaloneOrganization is the guard test:
// deployment = "server" must be rejected before any request reaches the API,
// with the same message circleci_group and circleci_project_group use.
func TestGroupMembershipResourceRequiresStandaloneOrganization(t *testing.T) {
	t.Parallel()

	api, host := newMockMembershipAPI(t)
	schema := groupMembershipResourceSchemaForTest(t)

	serverClient := circleci.New(circleci.Config{
		Host:        "http://127.0.0.1:1",
		PrivateHost: host,
		Token:       "fake",
		Deployment:  circleci.DeploymentServer,
	})
	r := &groupMembershipResource{client: serverClient}

	plan := membershipStateForTest(t, schema, membershipModel(t, testUserA))
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: plan.Raw}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("Create on a server deployment produced no diagnostics, want one")
	}
	found := false
	for _, d := range resp.Diagnostics {
		if d.Summary() == "circleci_group_membership requires a standalone CircleCI organization" {
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

// mustSetValue is a small helper for building a types.Set literal in a state
// fixture, panicking (via t.Fatal) rather than returning an error a caller
// might ignore.
func mustSetValue(t *testing.T, values ...string) types.Set {
	t.Helper()

	set, diags := types.SetValueFrom(t.Context(), types.StringType, values)
	if diags.HasError() {
		t.Fatalf("building set value: %v", diags)
	}

	return set
}
