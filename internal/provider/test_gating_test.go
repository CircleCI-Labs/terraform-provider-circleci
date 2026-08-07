// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"go/ast"
	"sort"
	"strings"
	"testing"
)

// TestCredentialFreeTestsUseUnitTest fails when a test that needs no credentials
// is written with resource.Test instead of resource.UnitTest.
//
// This is the highest-value guard in the package after
// TestEveryConstructorIsRegistered, and for the same reason: it catches tests
// that look like they are protecting the provider while silently doing nothing.
//
// resource.Test skips unless TF_ACC=1 is set. That is correct for a test that
// talks to a real CircleCI installation, but most tests here drive an httptest
// fake and need no credentials at all — so gating them behind TF_ACC means a
// developer running "go test ./..." gets a green run from tests that never
// executed. The whole fake-backed suite was in exactly that state: 81 test cases
// across 16 files were written, correct, and never run outside CI. Unskipping
// them moved coverage of this package from 12% to 42% without a line of new test
// code.
//
// resource.UnitTest is the same function with IsUnitTest set, which bypasses the
// TF_ACC requirement. Fake-backed tests must use it.
func TestCredentialFreeTestsUseUnitTest(t *testing.T) {
	t.Parallel()

	files := parseTestFiles(t)
	skipping := skippingHelpers(files)

	var offenders []string

	for _, file := range files {
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Recv != nil || fn.Body == nil {
				continue
			}
			if !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}

			gated, referenced := classifyTestBody(fn.Body, skipping)
			if gated && !referenced {
				offenders = append(offenders, fn.Name.Name)
			}
		}
	}

	sort.Strings(offenders)

	if len(offenders) > 0 {
		t.Errorf(
			"%d test(s) call resource.Test but reference no credential-gated helper:\n  %s\n\n"+
				"resource.Test skips unless TF_ACC=1, so these do not run for a developer "+
				"without a provisioned CircleCI account — and a fake-backed test does not need "+
				"one. Use resource.UnitTest instead. If the test really does talk to a live "+
				"installation, have it obtain its fixtures through the helpers in "+
				"acctest_test.go, which skip cleanly when the environment is unset.",
			len(offenders), strings.Join(offenders, "\n  "),
		)
	}
}

// classifyTestBody reports whether a test body calls resource.Test (rather than
// resource.UnitTest), and whether it reaches any credential-gated helper.
//
// The receiver is matched loosely because the plugin-testing package is imported
// under more than one name in this package: as resource, and as sdkresource where
// a file also imports the framework's own resource package.
func classifyTestBody(body *ast.BlockStmt, skipping map[string]bool) (callsTest, usesCredentials bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return true
		}

		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			pkg, isIdent := fun.X.(*ast.Ident)
			if isIdent && fun.Sel.Name == "Test" && strings.Contains(strings.ToLower(pkg.Name), "resource") {
				callsTest = true
			}
		case *ast.Ident:
			if skipping[fun.Name] {
				usesCredentials = true
			}
		}

		return true
	})

	return callsTest, usesCredentials
}

// skippingHelpers returns the set of package-level test helpers that skip the
// calling test when the environment does not provide what they need.
//
// It is computed rather than hardcoded so that a new fixture helper is picked up
// automatically: a helper qualifies if it calls t.Skip/Skipf/SkipNow, or calls
// another helper that does. Every identifier in acctest_test.go reaches t.Skipf
// through testAccEnv, and testAccPreCheck calls t.Skip directly.
func skippingHelpers(files []*ast.File) map[string]bool {
	bodies := map[string]*ast.BlockStmt{}

	for _, file := range files {
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if isFunc && fn.Recv == nil && fn.Body != nil && !strings.HasPrefix(fn.Name.Name, "Test") {
				bodies[fn.Name.Name] = fn.Body
			}
		}
	}

	skipping := map[string]bool{}

	// Iterate to a fixed point: a helper that calls a skipping helper is itself
	// skipping, and the call graph here is two levels deep in practice.
	for changed := true; changed; {
		changed = false

		for name, body := range bodies {
			if skipping[name] {
				continue
			}

			if bodySkips(body, skipping) {
				skipping[name] = true
				changed = true
			}
		}
	}

	return skipping
}

// bodySkips reports whether a helper body skips the test, directly or by calling
// another known skipping helper.
func bodySkips(body *ast.BlockStmt, skipping map[string]bool) bool {
	skips := false

	ast.Inspect(body, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return true
		}

		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			switch fun.Sel.Name {
			case "Skip", "Skipf", "SkipNow":
				skips = true
			}
		case *ast.Ident:
			if skipping[fun.Name] {
				skips = true
			}
		}

		return true
	})

	return skips
}

// parseTestFiles parses only the _test.go files of this package.
func parseTestFiles(t *testing.T) []*ast.File {
	t.Helper()

	parsed := parsePackageGoFiles(t, func(fileName string) bool {
		return strings.HasSuffix(fileName, "_test.go")
	})

	files := make([]*ast.File, 0, len(parsed))
	for _, file := range parsed {
		files = append(files, file)
	}

	return files
}
