// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"terraform-provider-circleci/internal/circleci"
)

const orbTestYAML = "version: 2.1\ndescription: an orb\n"

// TestOrbFakeVersionEntityOmitsTheOrbName asserts the bytes the fake sends for an
// orb version, not what the provider makes of them.
//
// No orb version route sends references.orb_package.attributes: the API renders
// that object only when it has an orb name, and the version records it renders
// from never carry one. The fake used to send it anyway, so every orb_name
// assertion in this file passed while the attribute was permanently empty against
// the real API. This test is what stops that being reintroduced — with it in
// place, orb_name can only be satisfied by the client resolving the name from the
// orb package, which is what TestAccOrbVersionResource now proves it does.
func TestOrbFakeVersionEntityOmitsTheOrbName(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID)
	// A source distinct from every other fixture, so that finding it in the entity
	// can only mean the entity leaked it.
	const source = "version: 2.1\ndescription: only-in-this-test\n"

	version := api.seedVersion(orb.ID, "1.0.0", source)

	entity, err := json.Marshal(api.versionEntity(version))
	if err != nil {
		t.Fatalf("marshalling the version entity: %v", err)
	}

	want := `"references":{"orb_package":{"id":"` + orb.ID + `"}}`
	if !strings.Contains(string(entity), want) {
		t.Errorf("version entity = %s,\nwant it to contain %s and nothing else under orb_package", entity, want)
	}
	if strings.Contains(string(entity), "acme/node") {
		t.Errorf("version entity = %s, want no orb name: the API never sends one here", entity)
	}
	// attributes.source is only sent by the by-id route when explicitly asked for
	// with ?include=source, which the client never does.
	if strings.Contains(string(entity), "only-in-this-test") {
		t.Errorf("version entity = %s, want no source outside the dedicated source route", entity)
	}
}

// TestAccOrbVersionResourceResolvesTheOrbName proves orb_name is populated by a
// lookup against the orb package rather than by reading it off the version.
func TestAccOrbVersionResourceResolvesTheOrbName(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_version" "test" {
  orb_id  = %q
  version = "1.0.0"
  yaml    = %q
}
`, orb.ID, orbTestYAML)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("circleci_orb_version.test", "orb_name", "acme/node"),
				func(*terraform.State) error {
					if got := api.requestsFor(http.MethodGet, "/orb/packages/"+orb.ID); len(got) == 0 {
						return fmt.Errorf(
							"orb_name was set without reading the orb package; requests were %+v",
							api.allRequests(),
						)
					}

					return nil
				},
			),
		}},
	})
}

// TestAccOrbVersionResource publishes a version and then destroys the resource.
//
// The destroy is the interesting half: a published orb version cannot be deleted,
// so the provider must remove it from state, warn, and make no API call at all.
func TestAccOrbVersionResource(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_version" "test" {
  orb_id  = %q
  version = "1.0.0"
  yaml    = %q
}
`, orb.ID, orbTestYAML)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("circleci_orb_version.test", "id"),
					resource.TestCheckResourceAttr("circleci_orb_version.test", "orb_id", orb.ID),
					resource.TestCheckResourceAttr("circleci_orb_version.test", "orb_name", "acme/node"),
					resource.TestCheckResourceAttr("circleci_orb_version.test", "version", "1.0.0"),
					resource.TestCheckResourceAttr("circleci_orb_version.test", "yaml", orbTestYAML),
					resource.TestCheckResourceAttr("circleci_orb_version.test", "source", orbTestYAML),
					resource.TestCheckResourceAttr("circleci_orb_version.test", "created_at", orbFakeCreatedAt),
				),
			},
			{
				Config:            config,
				ResourceName:      "circleci_orb_version.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					res, ok := state.RootModule().Resources["circleci_orb_version.test"]
					if !ok {
						return "", fmt.Errorf("resource not found in state")
					}

					return res.Primary.Attributes["id"], nil
				},
			},
		},
	})

	if got := api.requestsFor("POST", "/api/v3/orb/versions"); len(got) != 1 {
		t.Errorf("publish requests = %d, want exactly 1", len(got))
	}
	// Nothing may be deleted: the version stays published forever.
	for _, request := range api.allRequests() {
		if request.Method == "DELETE" {
			t.Errorf("the provider sent %s %s, want no delete for a published orb version",
				request.Method, request.Path)
		}
	}
}

// TestAccOrbVersionResource_ImportIDMustBeUUID rejects a "ns/orb@version"
// reference, which is not something the API can look a version up by directly.
func TestAccOrbVersionResource_ImportIDMustBeUUID(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID)
	api.seedVersion(orb.ID, "1.0.0", orbTestYAML)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_version" "test" {
  orb_id  = %q
  version = "1.0.0"
  yaml    = %q
}
`, orb.ID, orbTestYAML)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:        config,
			ResourceName:  "circleci_orb_version.test",
			ImportState:   true,
			ImportStateId: "acme/node@1.0.0",
			ExpectError:   regexp.MustCompile(`(?s)Invalid import ID.*UUID`),
		}},
	})
}

// TestAccOrbVersionResource_RepublishingStableVersionFails is the failure mode the
// documentation warns about: changing yaml plans a replacement, and republishing
// an existing stable version is rejected. The error has to say why.
func TestAccOrbVersionResource_RepublishingStableVersionFails(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID)

	config := func(yaml string) string {
		return orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_version" "test" {
  orb_id  = %q
  version = "1.0.0"
  yaml    = %q
}
`, orb.ID, yaml)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config(orbTestYAML)},
			{
				Config:      config(orbTestYAML + "# changed\n"),
				ExpectError: regexp.MustCompile(`(?s)already exists.*immutable`),
			},
		},
	})
}

// TestAccOrbVersionResource_DevVersionCanBeRepublished is the documented
// exception: a dev version is mutable, so replacing the resource works.
func TestAccOrbVersionResource_DevVersionCanBeRepublished(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID)

	config := func(yaml string) string {
		return orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_version" "test" {
  orb_id  = %q
  version = "dev:alpha"
  yaml    = %q
}
`, orb.ID, yaml)
	}

	updated := orbTestYAML + "# changed\n"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config(orbTestYAML)},
			{
				Config: config(updated),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_orb_version.test", "version", "dev:alpha"),
					resource.TestCheckResourceAttr("circleci_orb_version.test", "source", updated),
				),
			},
		},
	})
}

// orbVersionResourceSchemaForTest returns the resource's schema, the same way
// budgetResourceSchemaForTest does.
func orbVersionResourceSchemaForTest(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	(&orbVersionResource{}).Schema(t.Context(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}

	return resp.Schema
}

// orbVersionStateForTest builds a tfsdk.State (or, read as a Plan's raw value,
// the identical thing) from a fully-populated model.
func orbVersionStateForTest(t *testing.T, schema rschema.Schema, model orbVersionResourceModel) tfsdk.State {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a state value: %+v", diags)
	}

	return state
}

// TestOrbVersionResourceUnit_CreateWritesStateWhenSourceFetchFails is a
// regression test for issue #37.
//
// PublishOrbVersion is irreversible: once it succeeds, the version is
// published at CircleCI forever, and a stable version can never be
// republished. attributes.source is never present on a publish response in
// practice (see orbVersionWire's comment), so Create always follows a publish
// with GetOrbSource. Before the fix, a failure there returned before
// resp.State.Set, so a version CircleCI had just created permanently had no
// record in Terraform state at all — the next apply would try to publish it
// again and fail with "already exists", with no way to recover except
// importing by hand or bumping the version.
//
// Driven directly through Create, rather than resource.UnitTest, because the
// plugin-testing framework does not make "still in state after a failed
// apply" easy to assert on — see project_resource_unit_test.go's
// TestProjectResourceUnit_UpdateRejectsBadSlug for the same rationale.
func TestOrbVersionResourceUnit_CreateWritesStateWhenSourceFetchFails(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID)
	api.setFailGetOrbSourceStatus(http.StatusInternalServerError)

	schema := orbVersionResourceSchemaForTest(t)
	client := circleci.New(circleci.Config{Host: api.URL(), Token: "fake"})
	r := &orbVersionResource{client: client}

	plan := orbVersionStateForTest(t, schema, orbVersionResourceModel{
		Id:        types.StringUnknown(),
		OrbId:     types.StringValue(orb.ID),
		OrbName:   types.StringUnknown(),
		Version:   types.StringValue("1.0.0"),
		Yaml:      types.StringValue(orbTestYAML),
		Source:    types.StringUnknown(),
		CreatedAt: types.StringUnknown(),
	})

	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: plan.Raw}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("Create returned no diagnostics for a failed source fetch, want one")
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("Create left state empty after the publish succeeded; the published version now exists " +
			"permanently at CircleCI with nothing in Terraform tracking it")
	}

	var out orbVersionResourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if out.Id.ValueString() == "" {
		t.Error("state id is empty, want the id the publish response returned")
	}
	if out.Version.ValueString() != "1.0.0" {
		t.Errorf("state version = %q, want 1.0.0", out.Version.ValueString())
	}
	if out.OrbId.ValueString() != orb.ID {
		t.Errorf("state orb_id = %q, want %q", out.OrbId.ValueString(), orb.ID)
	}

	if published := api.requestsFor("POST", "/api/v3/orb/versions"); len(published) != 1 {
		t.Errorf("publish requests = %d, want exactly 1: the failure was in reading the source back, "+
			"not in publishing, and publishing must not be retried on that account", len(published))
	}
}

// TestAccOrbVersionResource_RejectsBadVersion guards the validator, so that a
// typo is caught at plan time rather than by publishing something permanent.
func TestAccOrbVersionResource_RejectsBadVersion(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	orb := api.seedOrb("acme/node", ns.ID)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_version" "test" {
  orb_id  = %q
  version = "v1.0"
  yaml    = %q
}
`, orb.ID, orbTestYAML)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`semantic version`),
		}},
	})
}
