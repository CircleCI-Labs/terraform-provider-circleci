// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// This file is the test half of pipeline_resource_rename.go: the pipeline
// definition resource served under both `circleci_pipeline` (deprecated) and
// `circleci_pipeline_definition`.
//
// The test that matters is TestPipelineResourceRename_MovedBlockDoesNotDestroyTheDefinition.
// A rename that destroys and recreates on migration is worse than the breaking
// change it was meant to avoid: recreating a pipeline definition gives it a new id,
// which every trigger pointing at it then has to be rebuilt around.

// pipelineRenameResourceProvider serves the pipeline definition resource under both of its
// type names.
//
// The provider's own Resources list is wired up separately, so these tests bring
// their own provider rather than depending on that registration — the same reason
// governanceProvider exists (see provider_test.go).
type pipelineRenameResourceProvider struct {
	*CircleCiProvider
}

func (p *pipelineRenameResourceProvider) Resources(_ context.Context) []func() fwresource.Resource {
	return []func() fwresource.Resource{
		NewPipelineDefinitionResource,
		NewDeprecatedPipelineResource,
	}
}

// pipelineRenameResourceFactories mirrors testAccProtoV6ProviderFactories for the
// two pipeline definition type names.
var pipelineRenameResourceFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"circleci": providerserver.NewProtocol6WithError(
		&pipelineRenameResourceProvider{CircleCiProvider: &CircleCiProvider{version: "test"}},
	),
}

// pipelineRenameResourceDefinitionID is the id newFakePipelineDefAPI mints for the
// first definition created against it. Asserting on it is how these tests tell a
// surviving definition from a recreated one.
const pipelineRenameResourceDefinitionID = "11111111-2222-3333-4444-000000000001"

// pipelineRenameResourceConfig renders the same pipeline definition under whichever
// type name is asked for, always at the address "example".
func pipelineRenameResourceConfig(host, typeName string) string {
	return pipelineFakeProviderConfig(host, "cloud") + fmt.Sprintf(`
resource %[1]q "example" {
  project_id                       = %[2]q
  name                             = "pipe-1"
  description                      = "original"
  config_source_provider           = "github_app"
  config_source_file_path          = "config.yml"
  config_source_repo_external_id   = "ext-1"
  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = "ext-2"
}
`, typeName, fakePipelineProjectID)
}

// TestPipelineResourceRename_BothTypeNamesWork proves the rename is additive: the old name
// keeps working while the new one does the same thing, through one implementation
// and one API route.
func TestPipelineResourceRename_BothTypeNamesWork(t *testing.T) {
	for _, typeName := range []string{"circleci_pipeline", "circleci_pipeline_definition"} {
		t.Run(typeName, func(t *testing.T) {
			api, host := newFakePipelineDefAPI(t)
			address := typeName + ".example"

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: pipelineRenameResourceFactories,
				Steps: []resource.TestStep{
					{
						Config: pipelineRenameResourceConfig(host, typeName),
						ConfigStateChecks: []statecheck.StateCheck{
							statecheck.ExpectKnownValue(
								address,
								tfjsonpath.New("id"),
								knownvalue.StringExact(pipelineRenameResourceDefinitionID),
							),
							statecheck.ExpectKnownValue(
								address,
								tfjsonpath.New("description"),
								knownvalue.StringExact("original"),
							),
							// Computed from the create response, so a populated value
							// here is a read-back through the resource's own mapping
							// rather than an echo of the configuration.
							statecheck.ExpectKnownValue(
								address,
								tfjsonpath.New("config_source_repo_full_name"),
								knownvalue.StringExact(resolveFullName("ext-1")),
							),
						},
					},
					{
						// Refresh and re-plan: Read must round-trip under both names.
						Config: pipelineRenameResourceConfig(host, typeName),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{
								plancheck.ExpectResourceAction(address, plancheck.ResourceActionNoop),
							},
						},
					},
				},
			})

			api.lastRequest(t, http.MethodPost, "/api/v2/projects/"+fakePipelineProjectID+"/pipeline-definitions")
			api.lastRequest(t, http.MethodGet, "/api/v2/projects/"+fakePipelineProjectID+
				"/pipeline-definitions/"+pipelineRenameResourceDefinitionID)
		})
	}
}

// TestPipelineResourceRename_MovedBlockDoesNotDestroyTheDefinition is the test this
// rename lives or dies by.
//
// Terraform treats a change of resource *type* as a different resource: without
// MoveState the migration is a destroy of circleci_pipeline followed by a create of
// circleci_pipeline_definition, which mints a new definition id and orphans every
// trigger referring to the old one. With MoveState the state is carried across and
// the plan is a no-op.
//
// Three independent things are asserted, because a passing plan check alone would
// not rule out a recreate that happened to be planned as an update:
//
//   - the plan across the move is empty, so nothing is created, replaced or updated;
//   - the definition id in state after the move is the one from before it;
//   - the fake received no DELETE and exactly one create, checked inside the step so
//     that the test framework's own destroy at the end of the case is not counted.
func TestPipelineResourceRename_MovedBlockDoesNotDestroyTheDefinition(t *testing.T) {
	api, host := newFakePipelineDefAPI(t)

	moved := pipelineRenameResourceConfig(host, "circleci_pipeline_definition") + `
moved {
  from = circleci_pipeline.example
  to   = circleci_pipeline_definition.example
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: pipelineRenameResourceFactories,
		Steps: []resource.TestStep{
			{
				// The state a practitioner already has today.
				Config: pipelineRenameResourceConfig(host, "circleci_pipeline"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_pipeline.example",
						tfjsonpath.New("id"),
						knownvalue.StringExact(pipelineRenameResourceDefinitionID),
					),
				},
			},
			{
				Config: moved,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_pipeline_definition.example",
							plancheck.ResourceActionNoop,
						),
						plancheck.ExpectEmptyPlan(),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					// The same definition, not a replacement of it.
					statecheck.ExpectKnownValue(
						"circleci_pipeline_definition.example",
						tfjsonpath.New("id"),
						knownvalue.StringExact(pipelineRenameResourceDefinitionID),
					),
					statecheck.ExpectKnownValue(
						"circleci_pipeline_definition.example",
						tfjsonpath.New("description"),
						knownvalue.StringExact("original"),
					),
					statecheck.ExpectKnownValue(
						"circleci_pipeline_definition.example",
						tfjsonpath.New("config_source_repo_full_name"),
						knownvalue.StringExact(resolveFullName("ext-1")),
					),
				},
				Check: func(*terraform.State) error {
					return assertPipelineDefinitionNotRecreated(api)
				},
			},
			{
				// The old address is gone for good: the migration must not leave the
				// state needing the same moved block on every subsequent plan.
				Config: pipelineRenameResourceConfig(host, "circleci_pipeline_definition"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// assertPipelineDefinitionNotRecreated reports whether the requests the fake has seen so
// far are those of a single, never-destroyed definition.
func assertPipelineDefinitionNotRecreated(api *fakePipelineDefAPI) error {
	creates := 0

	for _, request := range api.recorded() {
		if request.Method == http.MethodDelete {
			return fmt.Errorf(
				"the fake received %s %s: the move destroyed the pipeline definition instead of "+
					"carrying its state across", request.Method, request.Path,
			)
		}

		if request.Method == http.MethodPost {
			creates++
		}
	}

	if creates != 1 {
		return fmt.Errorf("the fake received %d creates, want exactly 1 — the move recreated the definition", creates)
	}

	return nil
}

// TestPipelineResourceRename_OnlyTheOldNameIsDeprecated pins the one difference between the
// two registrations. Deprecating both would nag practitioners who have already
// migrated; deprecating neither leaves the old name with nothing to tell them.
func TestPipelineResourceRename_OnlyTheOldNameIsDeprecated(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	testCases := map[string]struct {
		constructor func() fwresource.Resource
		deprecated  bool
	}{
		"circleci_pipeline":            {constructor: NewDeprecatedPipelineResource, deprecated: true},
		"circleci_pipeline_definition": {constructor: NewPipelineDefinitionResource, deprecated: false},
	}

	for typeName, testCase := range testCases {
		t.Run(typeName, func(t *testing.T) {
			t.Parallel()

			implementation := testCase.constructor()

			var metadata fwresource.MetadataResponse
			implementation.Metadata(ctx, fwresource.MetadataRequest{ProviderTypeName: "circleci"}, &metadata)

			if metadata.TypeName != typeName {
				t.Errorf("Metadata reported type name %q, want %q", metadata.TypeName, typeName)
			}

			var schemaResponse fwresource.SchemaResponse
			implementation.Schema(ctx, fwresource.SchemaRequest{}, &schemaResponse)

			message := schemaResponse.Schema.DeprecationMessage

			if !testCase.deprecated {
				if message != "" {
					t.Errorf("%s carries a deprecation message it should not: %q", typeName, message)
				}

				return
			}

			if message == "" {
				t.Fatalf("%s carries no deprecation message, so nothing tells practitioners to migrate", typeName)
			}
			// The message is the only migration instruction most practitioners will
			// see, so it has to name the replacement.
			if !strings.Contains(message, "circleci_pipeline_definition") {
				t.Errorf("the deprecation message does not name circleci_pipeline_definition: %q", message)
			}
		})
	}
}

// TestPipelineResourceRename_BothNamesShareOneSchema is what makes the state move a
// straight copy rather than a transformation. If the two schemas ever diverge,
// MoveState silently starts producing state that does not match the target schema, so
// the divergence has to fail here instead.
//
// The comparison is on the schemas' object types, not the attribute maps: plan
// modifiers hold functions, which are never reflect.DeepEqual to anything, and the
// type is the thing the move actually depends on — a raw state written against one
// schema is only readable through the other if their types are identical.
func TestPipelineResourceRename_BothNamesShareOneSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	var deprecated, current fwresource.SchemaResponse
	NewDeprecatedPipelineResource().Schema(ctx, fwresource.SchemaRequest{}, &deprecated)
	NewPipelineDefinitionResource().Schema(ctx, fwresource.SchemaRequest{}, &current)

	if !deprecated.Schema.Type().Equal(current.Schema.Type()) {
		t.Errorf(
			"the two type names expose different attributes, so the state move is no longer a "+
				"straight copy:\n  circleci_pipeline:            %s\n  circleci_pipeline_definition: %s",
			deprecated.Schema.Type(), current.Schema.Type(),
		)
	}

	if deprecated.Schema.Version != current.Schema.Version {
		t.Errorf(
			"schema versions differ (%d and %d); a move across versions needs a transformation, not a copy",
			deprecated.Schema.Version, current.Schema.Version,
		)
	}
}

// TestPipelineResourceRename_StateMoverMatchesOnTypeNameAlone covers the two decisions in
// the mover that the acceptance-style test above cannot reach: that a source
// provider address the practitioner never chose does not block the move, and that
// an unrelated source type is skipped rather than mangled.
//
// The addresses below are all real possibilities — the registry, a network mirror,
// and a locally built binary — and the practitioner has no way to influence which one
// Terraform reports. The test harness itself makes the point: the moved-block test
// above passes while Terraform reports the source provider as
// registry.terraform.io/hashicorp/circleci, which is not an address this provider is
// ever published under.
func TestPipelineResourceRename_StateMoverMatchesOnTypeNameAlone(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	target, isMover := NewPipelineDefinitionResource().(fwresource.ResourceWithMoveState)
	if !isMover {
		t.Fatal("circleci_pipeline_definition does not implement ResourceWithMoveState, " +
			"so no moved block can migrate anything off the old name")
	}

	var schemaResponse fwresource.SchemaResponse
	target.Schema(ctx, fwresource.SchemaRequest{}, &schemaResponse)

	movers := target.MoveState(ctx)
	if len(movers) != 1 {
		t.Fatalf("MoveState returned %d movers, want 1", len(movers))
	}

	sourceState := tfsdk.State{Schema: schemaResponse.Schema}
	if diags := sourceState.Set(ctx, pipelineResourceModel{
		Id:                           types.StringValue(pipelineRenameResourceDefinitionID),
		ProjectId:                    types.StringValue(fakePipelineProjectID),
		Name:                         types.StringValue("pipe-1"),
		Description:                  types.StringValue("original"),
		CreatedAt:                    types.StringValue("2024-03-01T00:00:00.000Z"),
		ConfigSourceProvider:         types.StringValue("github_app"),
		ConfigSourceFilePath:         types.StringValue("config.yml"),
		ConfigSourceRepoFullName:     types.StringValue(resolveFullName("ext-1")),
		ConfigSourceRepoExternalId:   types.StringValue("ext-1"),
		CheckoutSourceProvider:       types.StringValue("github_app"),
		CheckoutSourceRepoFullName:   types.StringValue(resolveFullName("ext-2")),
		CheckoutSourceRepoExternalId: types.StringValue("ext-2"),
	}); diags.HasError() {
		t.Fatalf("building the source state: %v", diags)
	}

	testCases := map[string]struct {
		sourceTypeName string
		providerAddr   string
		wantMoved      bool
	}{
		"registry": {
			sourceTypeName: "circleci_pipeline",
			providerAddr:   "registry.terraform.io/circleci/circleci",
			wantMoved:      true,
		},
		"network mirror": {
			sourceTypeName: "circleci_pipeline",
			providerAddr:   "terraform.example.com/circleci/circleci",
			wantMoved:      true,
		},
		"locally built provider": {
			sourceTypeName: "circleci_pipeline",
			providerAddr:   "hashicorp.com/edu/circleci",
			wantMoved:      true,
		},
		"unrelated source type is skipped": {
			sourceTypeName: "circleci_trigger",
			providerAddr:   "registry.terraform.io/circleci/circleci",
			wantMoved:      false,
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// The framework hands the implementation a target state that is null but
			// typed, and treats it still being null as "skipped".
			null := tftypes.NewValue(schemaResponse.Schema.Type().TerraformType(ctx), nil)
			response := fwresource.MoveStateResponse{
				TargetState: tfsdk.State{Schema: schemaResponse.Schema, Raw: null},
			}

			movers[0].StateMover(ctx, fwresource.MoveStateRequest{
				SourceTypeName:        testCase.sourceTypeName,
				SourceProviderAddress: testCase.providerAddr,
				SourceState:           &sourceState,
			}, &response)

			if response.Diagnostics.HasError() {
				t.Fatalf("the mover reported errors: %v", response.Diagnostics)
			}

			if moved := !response.TargetState.Raw.Equal(null); moved != testCase.wantMoved {
				t.Fatalf("state moved = %t, want %t", moved, testCase.wantMoved)
			}

			if !testCase.wantMoved {
				return
			}

			var moved pipelineResourceModel
			if diags := response.TargetState.Get(ctx, &moved); diags.HasError() {
				t.Fatalf("reading the moved state: %v", diags)
			}

			if moved.Id.ValueString() != pipelineRenameResourceDefinitionID {
				t.Errorf("moved id = %q, want %q", moved.Id.ValueString(), pipelineRenameResourceDefinitionID)
			}
			if moved.ProjectId.ValueString() != fakePipelineProjectID {
				t.Errorf("moved project_id = %q, want %q", moved.ProjectId.ValueString(), fakePipelineProjectID)
			}
		})
	}
}
