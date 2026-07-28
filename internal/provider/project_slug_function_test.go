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

func TestProjectSlugFunction_Metadata(t *testing.T) {
	t.Parallel()

	var resp function.MetadataResponse
	NewProjectSlugFunction().Metadata(context.Background(), function.MetadataRequest{}, &resp)

	if resp.Name != "project_slug" {
		t.Errorf("Name = %q, want %q", resp.Name, "project_slug")
	}
}

func TestProjectSlugFunction_Definition(t *testing.T) {
	t.Parallel()

	f := NewProjectSlugFunction()

	var defResp function.DefinitionResponse
	f.Definition(context.Background(), function.DefinitionRequest{}, &defResp)

	var validateResp function.DefinitionValidateResponse
	defResp.Definition.ValidateImplementation(context.Background(), function.DefinitionValidateRequest{FuncName: "project_slug"}, &validateResp)
	if validateResp.Diagnostics.HasError() {
		t.Fatalf("definition is invalid: %s", validateResp.Diagnostics)
	}
}

func TestProjectSlugFunction_Run(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		vcsType       string
		org           string
		project       string
		want          string
		wantErrArgPos int64
		wantErrHasArg bool
	}{
		{name: "github classic", vcsType: "gh", org: "my-org", project: "my-repo", want: "gh/my-org/my-repo"},
		{name: "bitbucket", vcsType: "bb", org: "my-org", project: "my-repo", want: "bb/my-org/my-repo"},
		{
			name: "circleci with UUIDs", vcsType: "circleci",
			org: "11111111-1111-1111-1111-111111111111", project: "22222222-2222-2222-2222-222222222222",
			want: "circleci/11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222",
		},
		{
			name: "circleci rejects org name instead of UUID", vcsType: "circleci",
			org: "my-org", project: "22222222-2222-2222-2222-222222222222",
			wantErrHasArg: true, wantErrArgPos: 1,
		},
		{
			name: "circleci rejects project name instead of UUID", vcsType: "circleci",
			org: "11111111-1111-1111-1111-111111111111", project: "my-repo",
			wantErrHasArg: true, wantErrArgPos: 2,
		},
		{name: "unrecognized vcs_type", vcsType: "gitlab", org: "my-org", project: "my-repo", wantErrHasArg: true, wantErrArgPos: 0},
		{name: "org with slash", vcsType: "gh", org: "my/org", project: "my-repo", wantErrHasArg: true, wantErrArgPos: 1},
		{name: "empty project", vcsType: "gh", org: "my-org", project: "", wantErrHasArg: true, wantErrArgPos: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := NewProjectSlugFunction()
			ctx := context.Background()

			req := function.RunRequest{
				Arguments: function.NewArgumentsData([]attr.Value{
					types.StringValue(tt.vcsType),
					types.StringValue(tt.org),
					types.StringValue(tt.project),
				}),
			}
			resp := &function.RunResponse{Result: function.NewResultData(types.StringUnknown())}

			f.Run(ctx, req, resp)

			if tt.wantErrHasArg {
				if resp.Error == nil {
					t.Fatal("Error = nil, want an error")
				}
				if resp.Error.FunctionArgument == nil || *resp.Error.FunctionArgument != tt.wantErrArgPos {
					t.Errorf("FunctionArgument = %v, want %d", resp.Error.FunctionArgument, tt.wantErrArgPos)
				}

				return
			}

			if resp.Error != nil {
				t.Fatalf("Error = %v, want none", resp.Error)
			}

			want := function.NewResultData(types.StringValue(tt.want))
			if !resp.Result.Equal(want) {
				t.Errorf("Result = %#v, want %#v", resp.Result, want)
			}
		})
	}
}
