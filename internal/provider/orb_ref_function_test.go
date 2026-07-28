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

func TestOrbRefFunction_Metadata(t *testing.T) {
	t.Parallel()

	var resp function.MetadataResponse
	NewOrbRefFunction().Metadata(context.Background(), function.MetadataRequest{}, &resp)

	if resp.Name != "orb_ref" {
		t.Errorf("Name = %q, want %q", resp.Name, "orb_ref")
	}
}

func TestOrbRefFunction_Definition(t *testing.T) {
	t.Parallel()

	f := NewOrbRefFunction()

	var defResp function.DefinitionResponse
	f.Definition(context.Background(), function.DefinitionRequest{}, &defResp)

	var validateResp function.DefinitionValidateResponse
	defResp.Definition.ValidateImplementation(context.Background(), function.DefinitionValidateRequest{FuncName: "orb_ref"}, &validateResp)
	if validateResp.Diagnostics.HasError() {
		t.Fatalf("definition is invalid: %s", validateResp.Diagnostics)
	}
}

func TestOrbRefFunction_Run(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		namespace     string
		orb           string
		version       string
		want          string
		wantErrArgPos int64
		wantErr       bool
	}{
		{name: "semantic version", namespace: "circleci", orb: "node", version: "1.2.3", want: "circleci/node@1.2.3"},
		{name: "dev release", namespace: "circleci", orb: "node", version: "dev:alpha", want: "circleci/node@dev:alpha"},
		{name: "volatile", namespace: "circleci", orb: "node", version: "volatile", want: "circleci/node@volatile"},
		{name: "namespace with slash", namespace: "acme/evil", orb: "node", version: "1.2.3", wantErr: true, wantErrArgPos: 0},
		{name: "orb with at sign", namespace: "circleci", orb: "node@1", version: "1.2.3", wantErr: true, wantErrArgPos: 1},
		{name: "bad version", namespace: "circleci", orb: "node", version: "latest", wantErr: true, wantErrArgPos: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := NewOrbRefFunction()
			ctx := context.Background()

			req := function.RunRequest{
				Arguments: function.NewArgumentsData([]attr.Value{
					types.StringValue(tt.namespace),
					types.StringValue(tt.orb),
					types.StringValue(tt.version),
				}),
			}
			resp := &function.RunResponse{Result: function.NewResultData(types.StringUnknown())}

			f.Run(ctx, req, resp)

			if tt.wantErr {
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
