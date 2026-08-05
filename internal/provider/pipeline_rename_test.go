// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// This file is the test half of pipeline_rename.go: one data source served under both
// circleci_pipeline and circleci_pipeline_definition.
//
// What matters is that the two names are interchangeable. A rename implemented by
// copying an implementation, or by a wrapper that forgets one of the optional
// datasource interfaces, produces two type names that read subtly different data or
// reject different configurations — and a practitioner mid-migration would then get
// different results depending on which name they happened to use. So the pair is read
// side by side in one configuration and required to agree.

const (
	pipelineRenameOldTypeName = "circleci_pipeline"
	pipelineRenameNewTypeName = "circleci_pipeline_definition"
)

// pipelineRenameDataSourceProvider serves the renamed data source under both of its
// type names.
//
// The provider's own DataSources list is wired up separately, so these tests bring
// their own provider rather than depending on that registration — the same reason
// observabilityProvider exists.
type pipelineRenameDataSourceProvider struct {
	*CircleCiProvider
}

func (p *pipelineRenameDataSourceProvider) Resources(_ context.Context) []func() fwresource.Resource {
	return nil
}

func (p *pipelineRenameDataSourceProvider) DataSources(_ context.Context) []func() fwdatasource.DataSource {
	return []func() fwdatasource.DataSource{
		NewPipelineDefinitionDataSource,
		NewDeprecatedPipelineDataSource,
	}
}

// pipelineRenameDataSourceFactories mirrors testAccProtoV6ProviderFactories for the
// two registrations above.
var pipelineRenameDataSourceFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"circleci": providerserver.NewProtocol6WithError(
		&pipelineRenameDataSourceProvider{CircleCiProvider: &CircleCiProvider{version: "test"}},
	),
}

// Identifiers the fake below serves.
const (
	pipelineRenameProjectID    = "aaaaaaaa-0000-1111-2222-333333333333"
	pipelineRenameDefinitionID = "bbbbbbbb-0000-1111-2222-333333333333"
)

// newPipelineRenameAPI serves the one route the renamed data source reads.
// Anything else is a 404 naming the path, so a data source that asks for the
// wrong id fails loudly instead of reading an empty result.
func newPipelineRenameAPI(t *testing.T) string {
	t.Helper()

	definition := fmt.Sprintf(`{
	  "id": %[1]q,
	  "name": "nightly",
	  "description": "Nightly build",
	  "created_at": "2024-05-01T10:00:00Z",
	  "config_source": {
	    "provider": "github_app",
	    "file_path": ".circleci/config.yml",
	    "repo": {"full_name": "acme/api", "external_id": "123456"}
	  },
	  "checkout_source": {
	    "provider": "github_app",
	    "repo": {"full_name": "acme/api", "external_id": "123456"}
	  }
	}`, pipelineRenameDefinitionID)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v2/projects/{projectID}/pipeline-definitions/{definitionID}",
		func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("projectID") != pipelineRenameProjectID ||
				r.PathValue("definitionID") != pipelineRenameDefinitionID {
				pipelineRenameNotFound(w, r)

				return
			}

			pipelineRenameJSON(w, http.StatusOK, definition)
		})

	mux.HandleFunc("/", pipelineRenameNotFound)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv.URL
}

func pipelineRenameJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func pipelineRenameNotFound(w http.ResponseWriter, r *http.Request) {
	pipelineRenameJSON(w, http.StatusNotFound,
		fmt.Sprintf(`{"message":"Not Found: %s %s"}`, r.Method, r.URL.Path))
}

// pipelineRenameDataSourceConfig renders the provider plus one block per type name,
// both at the same arguments.
func pipelineRenameDataSourceConfig(host string) string {
	arguments := fmt.Sprintf("id = %q\n  project_id = %q",
		pipelineRenameDefinitionID, pipelineRenameProjectID)

	return fmt.Sprintf(`
provider "circleci" {
  host       = %[1]q
  key        = "fake-token"
  deployment = "cloud"
}

data %[2]q "deprecated" {
  %[4]s
}

data %[3]q "current" {
  %[4]s
}
`, host, pipelineRenameOldTypeName, pipelineRenameNewTypeName, arguments)
}

// TestPipelineDataSourceRename_BothTypeNamesReadTheSameData reads the pair side by
// side in one configuration and requires the results to be identical.
//
// One configuration rather than two runs, because "the new name works" is the weaker
// half of the claim: what a migrating practitioner needs is that switching names
// changes nothing about what they get back.
func TestPipelineDataSourceRename_BothTypeNamesReadTheSameData(t *testing.T) {
	host := newPipelineRenameAPI(t)

	deprecated := "data." + pipelineRenameOldTypeName + ".deprecated"
	current := "data." + pipelineRenameNewTypeName + ".current"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: pipelineRenameDataSourceFactories,
		Steps: []resource.TestStep{{
			Config: pipelineRenameDataSourceConfig(host),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(deprecated, tfjsonpath.New("name"),
					knownvalue.StringExact("nightly")),
				statecheck.ExpectKnownValue(current, tfjsonpath.New("name"),
					knownvalue.StringExact("nightly")),
				statecheck.CompareValuePairs(
					deprecated, tfjsonpath.New("name"),
					current, tfjsonpath.New("name"),
					compare.ValuesSame(),
				),
			},
		}},
	})
}

// TestPipelineDataSourceRename_OnlyTheOldNameIsDeprecated pins the one difference
// between the two registrations. Deprecating both would nag practitioners who have
// already migrated; deprecating neither leaves the old name with nothing to tell them.
//
// It also checks that Metadata reports the name the constructor is registered for: a
// pair wired to the same type name would leave one of the two unreachable, which
// TestRegisteredTypeNamesAreUnique only catches once provider.go registers them.
func TestPipelineDataSourceRename_OnlyTheOldNameIsDeprecated(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	deprecated := pipelineRenameSchema(ctx, t, NewDeprecatedPipelineDataSource, pipelineRenameOldTypeName)
	current := pipelineRenameSchema(ctx, t, NewPipelineDefinitionDataSource, pipelineRenameNewTypeName)

	if message := current.DeprecationMessage; message != "" {
		t.Errorf("%s carries a deprecation message it should not: %q", pipelineRenameNewTypeName, message)
	}

	message := deprecated.DeprecationMessage
	if message == "" {
		t.Fatalf(
			"%s carries no deprecation message, so nothing tells practitioners to migrate",
			pipelineRenameOldTypeName,
		)
	}
	// The message is the only migration instruction most practitioners will see, so it
	// has to name the replacement.
	if !strings.Contains(message, pipelineRenameNewTypeName) {
		t.Errorf("the deprecation message does not name %s: %q", pipelineRenameNewTypeName, message)
	}
}

// TestPipelineDataSourceRename_BothNamesShareOneSchema is what makes migrating a
// matter of editing the type name and nothing else. If the two schemas diverge, a
// practitioner who renames a block finds their arguments no longer accepted, which
// the state comparison above cannot catch — it only ever sends arguments both names
// already accept.
//
// The attributes are compared field by field rather than with reflect.DeepEqual: an
// attribute may carry validators or plan modifiers built from closures, and two
// function values are never DeepEqual even when they are the same function, so a
// DeepEqual here would fail for reasons that have nothing to do with the rename.
func TestPipelineDataSourceRename_BothNamesShareOneSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	deprecated := pipelineRenameSchema(ctx, t, NewDeprecatedPipelineDataSource, pipelineRenameOldTypeName)
	current := pipelineRenameSchema(ctx, t, NewPipelineDefinitionDataSource, pipelineRenameNewTypeName)

	if len(deprecated.Attributes) != len(current.Attributes) {
		t.Fatalf(
			"%s exposes %d attributes and %s exposes %d, so renaming the block is not enough",
			pipelineRenameOldTypeName, len(deprecated.Attributes),
			pipelineRenameNewTypeName, len(current.Attributes),
		)
	}

	for name, attribute := range deprecated.Attributes {
		counterpart, ok := current.Attributes[name]
		if !ok {
			t.Errorf("%s has no %s attribute, which %s accepts",
				pipelineRenameNewTypeName, name, pipelineRenameOldTypeName)

			continue
		}

		if got, want := pipelineRenameAttribute(counterpart), pipelineRenameAttribute(attribute); got != want {
			t.Errorf("%s differs between the two names: %s has %s, %s has %s",
				name, pipelineRenameOldTypeName, want, pipelineRenameNewTypeName, got)
		}
	}
}

// pipelineRenameAttribute renders everything about an attribute a configuration can
// notice: its type, and whether it is required, optional, computed or sensitive.
func pipelineRenameAttribute(attribute dsschema.Attribute) string {
	return fmt.Sprintf("type %s (required=%t optional=%t computed=%t sensitive=%t)",
		attribute.GetType(), attribute.IsRequired(), attribute.IsOptional(),
		attribute.IsComputed(), attribute.IsSensitive())
}

// pipelineRenameSchema returns a constructed data source's schema, having checked that
// it reports the type name it is expected to serve.
func pipelineRenameSchema(
	ctx context.Context,
	t *testing.T,
	constructor func() fwdatasource.DataSource,
	wantTypeName string,
) dsschema.Schema {
	t.Helper()

	implementation := constructor()

	var metadata fwdatasource.MetadataResponse
	implementation.Metadata(ctx, fwdatasource.MetadataRequest{ProviderTypeName: "circleci"}, &metadata)

	if metadata.TypeName != wantTypeName {
		t.Errorf("Metadata reported type name %q, want %q", metadata.TypeName, wantTypeName)
	}

	var schemaResponse fwdatasource.SchemaResponse
	implementation.Schema(ctx, fwdatasource.SchemaRequest{}, &schemaResponse)

	if schemaResponse.Diagnostics.HasError() {
		t.Fatalf("%s schema returned diagnostics: %v", wantTypeName, schemaResponse.Diagnostics)
	}
	if diags := schemaResponse.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("%s schema validation returned diagnostics: %v", wantTypeName, diags)
	}

	return schemaResponse.Schema
}
