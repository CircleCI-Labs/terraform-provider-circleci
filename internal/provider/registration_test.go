// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// TestEveryConstructorIsRegistered fails when a resource or data source is
// implemented but never added to CircleCiProvider.Resources or DataSources.
//
// Without this, a type can be fully implemented and fully tested yet invisible
// to practitioners: acceptance tests construct a provider server from the
// registered lists, so an unregistered type simply never gets exercised and
// nothing goes red. That happened repeatedly while this package was being
// extended.
//
// The check is static — it compares the New*Resource / New*DataSource functions
// declared in this package against the identifiers referenced inside the two
// registration methods.
func TestEveryConstructorIsRegistered(t *testing.T) {
	t.Parallel()

	// Skip test files: constructors must be registered by production code.
	files := parsePackageGoFiles(t, func(fileName string) bool {
		return !strings.HasSuffix(fileName, "_test.go")
	})

	declared := map[string]string{} // constructor name -> "resource" | "datasource"
	registered := map[string]bool{}

	for name, file := range files {
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc {
				continue
			}

			// Registration methods: collect every identifier in their bodies.
			if fn.Recv != nil && (fn.Name.Name == "Resources" ||
				fn.Name.Name == "DataSources" ||
				fn.Name.Name == "EphemeralResources" ||
				fn.Name.Name == "Functions") {
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if ident, isIdent := n.(*ast.Ident); isIdent {
						registered[ident.Name] = true
					}

					return true
				})

				continue
			}

			// Constructors: package-level funcs whose return type is one of the
			// framework's four registrable interfaces.
			//
			// Classification is by RETURN TYPE, not name suffix. An ephemeral
			// resource's constructor is conventionally named New*Resource too, so a
			// suffix check reported it as a resource and would have sent it to the
			// wrong registration list; functions end in Function and were missed
			// entirely, so nothing checked them at all.
			if fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "New") {
				continue
			}
			if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
				continue
			}

			returned, isSelector := fn.Type.Results.List[0].Type.(*ast.SelectorExpr)
			if !isSelector {
				continue
			}

			pkgIdent, isIdent := returned.X.(*ast.Ident)
			if !isIdent {
				continue
			}

			switch pkgIdent.Name + "." + returned.Sel.Name {
			case "resource.Resource":
				declared[fn.Name.Name] = "resource"
			case "datasource.DataSource":
				declared[fn.Name.Name] = "data source"
			case "ephemeral.EphemeralResource":
				declared[fn.Name.Name] = "ephemeral resource"
			case "function.Function":
				declared[fn.Name.Name] = "function"
			default:
				continue
			}

			_ = name
		}
	}

	if len(declared) == 0 {
		t.Fatal("found no New*Resource/New*DataSource constructors; the AST walk is broken, not the provider")
	}

	var missing []string
	for name, kind := range declared {
		if !registered[name] {
			missing = append(missing, name+" ("+kind+")")
		}
	}

	if len(missing) > 0 {
		t.Errorf(
			"%d constructor(s) are implemented but not registered in provider.go:\n  %s\n\n"+
				"Add each to CircleCiProvider.Resources() or DataSources(), otherwise the type is "+
				"invisible to Terraform even though its tests pass.",
			len(missing), strings.Join(missing, "\n  "),
		)
	}
}

// TestRegisteredTypeNamesAreUnique guards against two types claiming the same
// Terraform type name, which would make one of them unreachable.
func TestRegisteredTypeNamesAreUnique(t *testing.T) {
	t.Parallel()

	p := &CircleCiProvider{version: "test"}
	ctx := t.Context()

	seen := map[string]bool{}

	for _, newResource := range p.Resources(ctx) {
		var resp resource.MetadataResponse
		newResource().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "circleci"}, &resp)

		if seen[resp.TypeName] {
			t.Errorf("resource type name %q is registered more than once", resp.TypeName)
		}
		seen[resp.TypeName] = true
	}

	seenDS := map[string]bool{}

	for _, newDataSource := range p.DataSources(ctx) {
		var resp datasource.MetadataResponse
		newDataSource().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "circleci"}, &resp)

		if seenDS[resp.TypeName] {
			t.Errorf("data source type name %q is registered more than once", resp.TypeName)
		}
		seenDS[resp.TypeName] = true
	}

	if len(seen) == 0 || len(seenDS) == 0 {
		t.Fatal("no resources or data sources registered")
	}

	t.Logf("registered %d resources and %d data sources", len(seen), len(seenDS))
}

// parsePackageGoFiles parses the .go files in this package's directory whose
// names keep accepts, returning them keyed by file name.
//
// This stands in for go/parser.ParseDir, deprecated in Go 1.25 because it
// decides which files belong to a package without consulting build tags. This
// package declares no build tags, so walking the directory is equivalent — and
// it drops ParseDir's grouping by package name, which every caller here had to
// unwrap before doing any work anyway.
func parsePackageGoFiles(t *testing.T, keep func(fileName string) bool) map[string]*ast.File {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	fset := token.NewFileSet()
	files := map[string]*ast.File{}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || !keep(name) {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		files[name] = file
	}

	// Every caller asserts something about what it found in these files, so a
	// filter that matches nothing would pass vacuously.
	if len(files) == 0 {
		t.Fatal("the filter matched no .go files; the walk is broken, not the code under test")
	}

	return files
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}
