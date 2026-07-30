// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestBranchOverrides is the regression test for pr_only_branch_overrides.
//
// The attribute is a Set rather than a List because CircleCI reports the branches
// back in an order of its own choosing; see the schema in project_resource.go.
//
// The Create and Update paths previously built this slice with
// attr.Value.String(), which renders a value the way Terraform displays it. A
// branch therefore arrived at the API as `"main"` — including the quote
// characters — so the setting never matched a real branch. This is the likely
// cause of issue #59.
func TestBranchOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		set  types.Set
		want []string
	}{
		{
			name: "single branch is unquoted",
			set: types.SetValueMust(types.StringType, []attr.Value{
				types.StringValue("main"),
			}),
			want: []string{"main"},
		},
		{
			name: "multiple branches",
			set: types.SetValueMust(types.StringType, []attr.Value{
				types.StringValue("main"),
				types.StringValue("develop"),
				types.StringValue("release/1.x"),
			}),
			want: []string{"main", "develop", "release/1.x"},
		},
		{
			name: "empty set",
			set:  types.SetValueMust(types.StringType, []attr.Value{}),
			want: []string{},
		},
		{
			name: "null set",
			set:  types.SetNull(types.StringType),
			want: nil,
		},
		{
			name: "unknown set",
			set:  types.SetUnknown(types.StringType),
			want: nil,
		},
		{
			name: "branch containing a quote is not double-escaped",
			set: types.SetValueMust(types.StringType, []attr.Value{
				types.StringValue(`odd"name`),
			}),
			want: []string{`odd"name`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, diags := branchOverrides(context.Background(), tt.set)
			if diags.HasError() {
				t.Fatalf("branchOverrides returned diagnostics: %v", diags)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("branchOverrides() = %#v, want %#v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("branchOverrides()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestBranchOverridesRejectsTheOldApproach documents precisely what was wrong,
// so nobody reintroduces it.
func TestBranchOverridesRejectsTheOldApproach(t *testing.T) {
	t.Parallel()

	branch := types.StringValue("main")

	// This is what the code used to do.
	if got := branch.String(); got != `"main"` {
		t.Fatalf("attr.Value.String() = %q; this test assumes it renders quoted", got)
	}

	// And this is what it must do instead.
	if got := branch.ValueString(); got != "main" {
		t.Errorf("ValueString() = %q, want %q", got, "main")
	}
}

// TestParseProjectSlug covers the guard added for malformed slugs. Indexing the
// split result directly used to panic, crashing the provider process.
func TestParseProjectSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		slug        string
		wantVCS     string
		wantOrg     string
		wantProject string
		wantErr     bool
	}{
		{name: "valid", slug: "gh/acme/repo", wantVCS: "gh", wantOrg: "acme", wantProject: "repo"},
		{name: "valid bitbucket", slug: "bb/acme/repo", wantVCS: "bb", wantOrg: "acme", wantProject: "repo"},
		{name: "valid circleci org", slug: "circleci/abc-123/repo", wantVCS: "circleci", wantOrg: "abc-123", wantProject: "repo"},
		{name: "too few segments", slug: "gh/acme", wantErr: true},
		{name: "single segment", slug: "repo", wantErr: true},
		{name: "too many segments", slug: "gh/acme/repo/extra", wantErr: true},
		{name: "empty", slug: "", wantErr: true},
		{name: "empty middle segment", slug: "gh//repo", wantErr: true},
		{name: "empty last segment", slug: "gh/acme/", wantErr: true},
		{name: "only separators", slug: "//", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vcs, org, project, diags := parseProjectSlug(tt.slug)

			if tt.wantErr {
				if !diags.HasError() {
					t.Fatalf("parseProjectSlug(%q) returned no error, want one", tt.slug)
				}

				return
			}

			if diags.HasError() {
				t.Fatalf("parseProjectSlug(%q) returned diagnostics: %v", tt.slug, diags)
			}
			if vcs != tt.wantVCS || org != tt.wantOrg || project != tt.wantProject {
				t.Errorf("parseProjectSlug(%q) = (%q, %q, %q), want (%q, %q, %q)",
					tt.slug, vcs, org, project, tt.wantVCS, tt.wantOrg, tt.wantProject)
			}
		})
	}
}
