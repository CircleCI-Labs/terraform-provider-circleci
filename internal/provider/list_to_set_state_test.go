// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// This file answers one question, empirically rather than by assertion in a
// comment: does turning a resource attribute from a ListAttribute into a
// SetAttribute require a schema version bump and a resource.ResourceWithUpgradeState
// implementation?
//
// These attributes needed that change, because CircleCI stores them as unordered
// collections and reports them back in an order of its own choosing, so Terraform
// planned a change on every run with nothing to apply:
//
//   - circleci_webhook.events
//   - circleci_project.pr_only_branch_overrides
//   - circleci_project_settings.pr_only_branch_overrides
//   - circleci_oidc_custom_claims.audience
//
// The answer is no, and the reason is that a list and a set of the same element
// type share one JSON encoding — both are a JSON array. Terraform stores resource
// state as JSON and hands it back to the provider through UpgradeResourceState,
// which the framework answers by re-reading those raw bytes against the *current*
// schema type whenever the stored version matches the current one. So state
// written by an older provider under `"events": ["b","a"]` decodes as a set
// without anything having to rewrite it.
//
// That is easy to believe and cheap to get wrong, hence the tests below: they feed
// real prior-state JSON through the provider's own UpgradeResourceState RPC and
// assert the value that comes back is a set holding the right elements.

// listToSetCase is one attribute that changed from a list to a set.
type listToSetCase struct {
	// typeName is the Terraform resource type.
	typeName string
	// attribute is the attribute that changed type.
	attribute string
	// elements are the values the prior state holds for it, in an order chosen so
	// that no correct implementation can depend on it.
	elements []string
}

func listToSetCases() []listToSetCase {
	return []listToSetCase{
		{
			typeName:  "circleci_webhook",
			attribute: "events",
			elements:  []string{"workflow-completed", "job-completed"},
		},
		{
			typeName:  "circleci_project",
			attribute: "pr_only_branch_overrides",
			elements:  []string{"zebra", "alpha", "main", "beta"},
		},
		{
			typeName:  "circleci_project_settings",
			attribute: "pr_only_branch_overrides",
			elements:  []string{"zebra", "alpha", "main", "beta"},
		},
		{
			typeName:  "circleci_oidc_custom_claims",
			attribute: "audience",
			elements:  []string{"sts.amazonaws.com", "vault.example.com"},
		},
	}
}

// TestListToSetNeedsNoStateUpgrade is the evidence that no state upgrade is
// required.
//
// It drives the provider's real UpgradeResourceState RPC — the one Terraform calls
// with whatever JSON is in the state file — with prior state written under the old
// list schema, and asserts the upgraded value is a set holding the same elements.
// If a state upgrade were needed, this is where it would show up: the RPC would
// answer with an error diagnostic instead.
func TestListToSetNeedsNoStateUpgrade(t *testing.T) {
	t.Parallel()

	for _, tc := range listToSetCases() {
		t.Run(tc.typeName+"."+tc.attribute, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			server := newProviderServerForTest(t)
			schema := resourceSchemaFromServer(t, server, tc.typeName)

			// The stored schema version is the current one. A provider that needed a
			// state upgrade here would have had to bump this, so asserting it is
			// asserting the conclusion of this whole file.
			if schema.Version != 0 {
				t.Errorf("schema version for %s is %d, want 0: no version bump was needed for the "+
					"list-to-set change, so a non-zero version means someone added one without "+
					"updating this test", tc.typeName, schema.Version)
			}

			currentType, isObject := schema.ValueType().(tftypes.Object)
			if !isObject {
				t.Fatalf("schema type for %s is %T, want tftypes.Object", tc.typeName, schema.ValueType())
			}

			priorState := priorStateJSON(t, currentType, tc.attribute, tc.elements)

			// Half the point of this test: the very same bytes are valid state under
			// the OLD schema, where the attribute was a list. That shared encoding is
			// the reason no upgrade is needed, so it is worth proving rather than
			// asserting.
			oldType := currentType
			oldType.AttributeTypes = map[string]tftypes.Type{}
			for name, attrType := range currentType.AttributeTypes {
				if name == tc.attribute {
					attrType = tftypes.List{ElementType: tftypes.String}
				}
				oldType.AttributeTypes[name] = attrType
			}
			if _, err := priorState.Unmarshal(oldType); err != nil {
				t.Fatalf("the prior state this test feeds in is not valid under the old list schema, "+
					"so it does not represent state an older provider would have written: %v", err)
			}

			resp, err := server.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
				TypeName: tc.typeName,
				Version:  schema.Version,
				RawState: priorState,
			})
			if err != nil {
				t.Fatalf("UpgradeResourceState returned an error: %v", err)
			}
			for _, diagnostic := range resp.Diagnostics {
				if diagnostic.Severity == tfprotov6.DiagnosticSeverityError {
					t.Fatalf("UpgradeResourceState reported an error reading state written under the "+
						"old list schema, so a state upgrade IS required after all: %s: %s",
						diagnostic.Summary, diagnostic.Detail)
				}
			}

			if resp.UpgradedState == nil {
				t.Fatal("UpgradeResourceState returned no upgraded state")
			}

			upgraded, err := resp.UpgradedState.Unmarshal(currentType)
			if err != nil {
				t.Fatalf("the upgraded state does not match the current schema type: %v", err)
			}

			assertSetOfStrings(t, upgraded, tc.attribute, tc.elements)
		})
	}
}

// TestListToSetToleratesDuplicatesInPriorState covers the one way the shared
// encoding is not quite an identity: a list could hold the same element twice,
// and a set cannot.
//
// Terraform itself would not have produced such state from a valid configuration
// for `events` or `pr_only_branch_overrides` — nothing deduplicated them, but
// nothing sensible configured a duplicate either — so this documents the
// behaviour rather than demanding a particular one. It is here so that a real
// upgrade path can be added deliberately if this ever turns out to matter, rather
// than being discovered by a practitioner mid-apply.
func TestListToSetToleratesDuplicatesInPriorState(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	server := newProviderServerForTest(t)
	schema := resourceSchemaFromServer(t, server, "circleci_webhook")

	currentType, isObject := schema.ValueType().(tftypes.Object)
	if !isObject {
		t.Fatalf("schema type is %T, want tftypes.Object", schema.ValueType())
	}

	priorState := priorStateJSON(t, currentType, "events",
		[]string{"job-completed", "job-completed", "workflow-completed"})

	resp, err := server.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
		TypeName: "circleci_webhook",
		Version:  schema.Version,
		RawState: priorState,
	})
	if err != nil {
		t.Fatalf("UpgradeResourceState returned an error: %v", err)
	}

	for _, diagnostic := range resp.Diagnostics {
		if diagnostic.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("prior list state holding a duplicate element cannot be read as a set: %s: %s\n\n"+
				"If this ever fires, circleci_webhook needs SchemaVersion bumped and an "+
				"UpgradeState that deduplicates events.", diagnostic.Summary, diagnostic.Detail)
		}
	}
}

// newProviderServerForTest starts the provider as Terraform would talk to it.
func newProviderServerForTest(t *testing.T) tfprotov6.ProviderServer {
	t.Helper()

	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatalf("could not start the provider server: %v", err)
	}

	return server
}

// resourceSchemaFromServer returns one resource's schema as the protocol reports
// it, which is where both the schema version and the state's wire type come from.
func resourceSchemaFromServer(t *testing.T, server tfprotov6.ProviderServer, typeName string) *tfprotov6.Schema {
	t.Helper()

	resp, err := server.GetProviderSchema(t.Context(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema returned an error: %v", err)
	}

	schema, found := resp.ResourceSchemas[typeName]
	if !found {
		t.Fatalf("the provider serves no resource named %q", typeName)
	}

	return schema
}

// priorStateJSON builds the JSON Terraform would have stored for a resource whose
// named attribute was still a list.
//
// Every other attribute is written as JSON null, which is what the shape of the
// state file requires: an object's attributes are all present, whatever their
// values. The collection is written as a plain JSON array, because that is the
// encoding a list and a set share — which is the whole reason this decodes as a
// set without an upgrade.
func priorStateJSON(t *testing.T, schemaType tftypes.Object, attribute string, elements []string) *tfprotov6.RawState {
	t.Helper()

	if _, found := schemaType.AttributeTypes[attribute]; !found {
		t.Fatalf("the schema has no %q attribute", attribute)
	}

	attributes := map[string]any{}
	for name := range schemaType.AttributeTypes {
		attributes[name] = nil
	}

	// A plain JSON array. Under the old schema this was a list; under the new one
	// it is a set; the bytes are identical either way, which is the point.
	attributes[attribute] = elements

	encoded, err := json.Marshal(attributes)
	if err != nil {
		t.Fatalf("could not encode prior state: %v", err)
	}

	return &tfprotov6.RawState{JSON: encoded}
}

// assertSetOfStrings checks that an attribute of an upgraded state value is a set
// holding exactly the expected strings, in any order.
func assertSetOfStrings(t *testing.T, state tftypes.Value, attribute string, want []string) {
	t.Helper()

	attributes := map[string]tftypes.Value{}
	if err := state.As(&attributes); err != nil {
		t.Fatalf("upgraded state is not an object: %v", err)
	}

	value, found := attributes[attribute]
	if !found {
		t.Fatalf("upgraded state has no %q attribute", attribute)
	}

	if !value.Type().Is(tftypes.Set{ElementType: tftypes.String}) {
		t.Fatalf("upgraded %q has type %s, want a set of strings — the attribute was converted to a "+
			"SetAttribute, so prior state must come back as a set", attribute, value.Type())
	}

	var elements []tftypes.Value
	if err := value.As(&elements); err != nil {
		t.Fatalf("upgraded %q is not a collection: %v", attribute, err)
	}

	got := map[string]bool{}
	for _, element := range elements {
		var s string
		if err := element.As(&s); err != nil {
			t.Fatalf("element of %q is not a string: %v", attribute, err)
		}
		got[s] = true
	}

	if len(got) != len(want) {
		t.Fatalf("upgraded %q holds %d distinct elements (%v), want %d (%v)",
			attribute, len(got), elements, len(want), want)
	}
	for _, expected := range want {
		if !got[expected] {
			t.Errorf("upgraded %q is missing %q; it holds %v", attribute, expected, elements)
		}
	}
}
