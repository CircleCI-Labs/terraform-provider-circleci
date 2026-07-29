// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const testPipelineValuesRunID = "5034460f-c7c4-4c43-9457-de07e2029e7b"

// newMockPipelineValuesAPI serves GET /api/v2/pipeline/{id}/values with a
// fixture matching
// the CircleCI API's "200 response with
// pipeline values" case: a flat object mixing strings and a JSON number
// (pipeline.number).
func newMockPipelineValuesAPI(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v2/pipeline/" + testPipelineValuesRunID + "/values"
		if r.URL.Path != wantPath {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, `{"message":"unexpected path %s"}`, r.URL.Path)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "pipeline.id": "` + testPipelineValuesRunID + `",
  "pipeline.number": 42,
  "pipeline.project.git_url": "https://github.com/circleci/example",
  "pipeline.git.branch": "main",
  "pipeline.git.tag": ""
}`))
	}))
	t.Cleanup(srv.Close)

	return srv.URL
}

func TestPipelineValuesDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewPipelineRunValuesDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// run_id is the sole identifier: without it there is nothing to read, so it has
	// to be Required rather than left to a validator.
	runID, ok := resp.Schema.Attributes["run_id"]
	if !ok {
		t.Fatal("run_id must be present, as it is the lookup key")
	}
	if !runID.IsRequired() {
		t.Error("run_id is not required, but the data source cannot read anything without it")
	}

	values, ok := resp.Schema.Attributes["values"]
	if !ok || !values.IsComputed() {
		t.Error("values must be present and computed")
	}
}

func TestAccPipelineValuesDataSource(t *testing.T) {
	host := newMockPipelineValuesAPI(t)

	config := discoveryProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_pipeline_run_values" "test" {
  run_id = %[1]q
}
`, testPipelineValuesRunID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_run_values.test",
						tfjsonpath.New("values").AtMapKey("pipeline.id"),
						knownvalue.StringExact(testPipelineValuesRunID),
					),
					// The API sends this as a JSON number, not a string; it must still
					// come back rendered as "42", not "4.2e+01" or similar.
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_run_values.test",
						tfjsonpath.New("values").AtMapKey("pipeline.number"),
						knownvalue.StringExact("42"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_run_values.test",
						tfjsonpath.New("values").AtMapKey("pipeline.git.branch"),
						knownvalue.StringExact("main"),
					),
					// An empty value must survive as an empty string element, not vanish
					// from the map.
					statecheck.ExpectKnownValue(
						"data.circleci_pipeline_run_values.test",
						tfjsonpath.New("values").AtMapKey("pipeline.git.tag"),
						knownvalue.StringExact(""),
					),
				},
			},
		},
	})
}

func TestAccPipelineValuesDataSource_notFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"pipeline not found"}`))
	}))
	t.Cleanup(srv.Close)

	config := discoveryProviderConfig(srv.URL, "cloud") + fmt.Sprintf(`
data "circleci_pipeline_run_values" "test" {
  run_id = %[1]q
}
`, testPipelineValuesRunID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			// The diagnostic names the pipeline that could not be read, so assert on
			// the id as well as the summary: an error that omitted it would leave a
			// practitioner with several pipeline_values data sources and no idea
			// which one failed.
			ExpectError: regexp.MustCompile(
				`Unable to read CircleCI pipeline values for pipeline ` + testPipelineValuesRunID,
			),
		}},
	})
}
