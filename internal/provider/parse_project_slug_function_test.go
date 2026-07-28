// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestParseProjectSlugFunction_Metadata(t *testing.T) {
	t.Parallel()

	var resp function.MetadataResponse
	NewParseProjectSlugFunction().Metadata(context.Background(), function.MetadataRequest{}, &resp)

	if resp.Name != "parse_project_slug" {
		t.Errorf("Name = %q, want %q", resp.Name, "parse_project_slug")
	}
}

func TestParseProjectSlugFunction_Definition(t *testing.T) {
	t.Parallel()

	f := NewParseProjectSlugFunction()

	var defResp function.DefinitionResponse
	f.Definition(context.Background(), function.DefinitionRequest{}, &defResp)

	var validateResp function.DefinitionValidateResponse
	defResp.Definition.ValidateImplementation(context.Background(), function.DefinitionValidateRequest{FuncName: "parse_project_slug"}, &validateResp)
	if validateResp.Diagnostics.HasError() {
		t.Fatalf("definition is invalid: %s", validateResp.Diagnostics)
	}
}

func TestParseProjectSlugFunction_Run(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		slug    string
		want    map[string]attr.Value
		wantErr bool
	}{
		{
			name: "classic github",
			slug: "gh/my-org/my-repo",
			want: map[string]attr.Value{
				"vcs_type": types.StringValue("gh"),
				"org":      types.StringValue("my-org"),
				"project":  types.StringValue("my-repo"),
			},
		},
		{
			name: "circleci uuid form",
			slug: "circleci/11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222",
			want: map[string]attr.Value{
				"vcs_type": types.StringValue("circleci"),
				"org":      types.StringValue("11111111-1111-1111-1111-111111111111"),
				"project":  types.StringValue("22222222-2222-2222-2222-222222222222"),
			},
		},
		{name: "too few segments", slug: "gh/my-org", wantErr: true},
		{name: "too many segments", slug: "gh/my-org/my-repo/extra", wantErr: true},
		{name: "empty segment", slug: "gh//my-repo", wantErr: true},
		{name: "empty string", slug: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := NewParseProjectSlugFunction()
			ctx := context.Background()

			req := function.RunRequest{
				Arguments: function.NewArgumentsData([]attr.Value{types.StringValue(tt.slug)}),
			}
			resp := &function.RunResponse{
				Result: function.NewResultData(types.ObjectUnknown(parseProjectSlugAttrTypes)),
			}

			f.Run(ctx, req, resp)

			if tt.wantErr {
				if resp.Error == nil {
					t.Fatal("Error = nil, want an error")
				}

				return
			}

			if resp.Error != nil {
				t.Fatalf("Error = %v, want none", resp.Error)
			}

			wantObj, diags := types.ObjectValue(parseProjectSlugAttrTypes, tt.want)
			if diags.HasError() {
				t.Fatalf("building expected object: %s", diags)
			}

			want := function.NewResultData(wantObj)
			if !resp.Result.Equal(want) {
				t.Errorf("Result = %#v, want %#v", resp.Result, want)
			}
		})
	}
}
