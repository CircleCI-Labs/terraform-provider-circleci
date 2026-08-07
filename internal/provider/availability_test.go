// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// This file guards the `## Availability` table that most documentation pages
// carry, in the same filesystem-only spirit as examples_test.go: it needs no
// credentials, no network and no `terraform` binary, and it catches a class of
// defect that nothing else goes red for.
//
// The defect it exists for was real, caught before release rather than after:
// `circleci_group_membership`'s page (removed — see CHANGELOG.md) carried an
// availability table saying **no** for CircleCI Server directly above a
// paragraph concluding "so this resource works against both Cloud and Server" —
// on a resource the provider rejected outright when `deployment = "server"`.
// Three layers describe availability (this repository's README matrix, the
// summary table on the provider index page, and these per-page tables), and
// prose drifts silently because no compiler reads it.
//
// The one thing that cannot drift is the gate in the code. So that is what these
// tests compare against.

// deploymentGates are the helpers that make a type unavailable on CircleCI
// Server. Both take the type name as their second argument, which is what makes
// the set of gated types recoverable statically.
//
// requireCloud is the v3-and-friends gate; requireStandaloneCapable is the
// `circleci` type organization gate, which also implies Server, because a Server
// installation is always a `github` type organization. Either one means the
// documentation must say Server is unavailable.
//
// requireUsageExportCloud is a third: circleci_usage_export is not v3, and
// CircleCI Server does route its paths, but Server does not deploy the reporting
// service they are proxied to -- so it needs its own diagnostic wording and
// therefore its own helper, and it is listed here so its page is checked like any
// other gated type's. It takes the type name in the same argument position for
// exactly that reason.
var deploymentGates = map[string]string{
	"requireCloud":             "CircleCI Cloud only",
	"requireStandaloneCapable": "requires a standalone organization, which Server can never be",
	"requireUsageExportCloud":  "CircleCI Cloud only; Server does not deploy the reporting service behind the route",
}

// unresolvableGates are the gate call sites whose type-name argument is not a
// string literal or a package-level constant, and so cannot be read statically.
//
// Each must be accounted for by dynamicallyGatedTypes below. The point of
// listing them is that a *new* unresolvable call site fails this test rather than
// silently reducing its coverage: a gate the scan cannot see is a page nothing
// checks.
var unresolvableGates = map[string]string{
	"r.typeName()": "pipelineResource serves both circleci_pipeline_definition and its " +
		"deprecated alias circleci_pipeline, so the name is a method call rather than a constant",
	"typeName": "the pipeline-definition data source, for the same reason",
}

// dynamicallyGatedTypes are the types covered by unresolvableGates. Keep the two
// in step.
var dynamicallyGatedTypes = []string{
	"circleci_pipeline",
	"circleci_pipeline_definition",
}

// availabilityExempt are the documentation pages that deliberately carry no
// `## Availability` table, with the reason each is acceptable.
//
// Like examplelessTypes in examples_test.go this is a ratchet, and the bar is
// high: "nobody has written it yet" is not a reason, it is the finding.
var availabilityExempt = map[string]string{
	// Both pages exist only to say "this name was renamed" and end with "For
	// everything else — arguments, availability, examples — see
	// circleci_pipeline_definition." A second availability table here would be a
	// second thing to keep in step for a name we are asking people to stop using.
	"resources/pipeline":    "deprecated alias; the page defers to circleci_pipeline_definition for availability",
	"data-sources/pipeline": "deprecated alias; the page defers to circleci_pipeline_definition for availability",
}

// TestGatedTypesDocumentThatServerIsUnavailable fails when the provider refuses a
// type on CircleCI Server but the type's documentation page does not say so.
//
// This is the direction that matters. A page claiming Server support for a type
// that errors at plan time sends somebody to build a Server configuration around
// a resource that cannot work there, and they find out from a diagnostic rather
// than from the docs.
//
// The reverse direction is deliberately not asserted: a page may honestly say
// "no" for a type the provider does not gate. `circleci_project_group` is exactly
// that — its route is not served on a CircleCI Server installation, which is
// something the provider cannot detect from configuration, so the page states
// it and the request simply fails. Requiring a gate for every documented "no"
// would be requiring the provider to guess.
func TestGatedTypesDocumentThatServerIsUnavailable(t *testing.T) {
	t.Parallel()

	gated := gatedTypeNames(t)

	// A floor, not a target. If the AST scan silently stops matching, this is what
	// notices — otherwise the test would pass by checking nothing at all.
	const minimumGatedTypes = 20

	if len(gated) < minimumGatedTypes {
		t.Fatalf(
			"found only %d gated type(s), expected at least %d: the AST scan is broken, "+
				"not the provider. Check deploymentGates against the helpers in configure.go "+
				"and standalone_only.go.",
			len(gated), minimumGatedTypes,
		)
	}

	// A gate is passed its type name as a plain string, which nothing checks
	// against the registration lists. A typo there produces a diagnostic naming a
	// resource that does not exist, and no test would notice.
	registered := registeredTypeNames(t)

	var unknown []string

	for _, typeName := range sortedKeys(gated) {
		if !registered[typeName] {
			unknown = append(unknown, typeName)
		}
	}

	if len(unknown) > 0 {
		t.Errorf(
			"%d gate(s) name a type that is not registered: %s\n\n"+
				"The name is what the diagnostic shows a practitioner, so a typo there names a "+
				"resource they cannot find in the documentation. Correct the argument to "+
				"requireCloud or requireStandaloneCapable, or register the type.",
			len(unknown), strings.Join(unknown, ", "),
		)
	}

	var (
		problems []string
		settled  []string
		checked  int
	)

	for _, typeName := range sortedKeys(gated) {
		for _, page := range documentationPagesFor(t, typeName) {
			_, exempt := availabilityExempt[page.key]

			section, hasSection := availabilitySection(t, page.file)

			switch {
			case exempt && hasSection:
				settled = append(settled, page.key)

				continue
			case exempt:
				continue
			case !hasSection:
				problems = append(problems, page.template+
					"\n      has no `## Availability` section, but the provider gates "+
					typeName+" ("+gated[typeName]+")")

				continue
			}

			checked++

			verdict, cell := serverVerdict(section)

			switch verdict {
			case "":
				problems = append(problems, page.template+
					"\n      its `## Availability` section has no readable CircleCI Server verdict"+
					shapeHint(cell))
			case "no":
				// Correct.
			default:
				problems = append(problems, page.template+
					"\n      says CircleCI Server is "+strconv.Quote(verdict)+
					", but the provider gates "+typeName+" ("+gated[typeName]+")"+
					"\n      the cell reads: "+strconv.Quote(truncate(cell, 120)))
			}
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)

		t.Errorf(
			"%d documentation page(s) disagree with the provider's own deployment gating:\n  %s\n\n"+
				"Either correct the page — the gate is the fact — or, if the gate itself is wrong, "+
				"remove it and say why. Do not weaken this test to accommodate prose. The README "+
				"compatibility matrix and the summary table on the provider index page must agree "+
				"with the per-page table too; the page is authoritative for its own type.",
			len(problems), strings.Join(problems, "\n  "),
		)
	}

	if len(settled) > 0 {
		sort.Strings(settled)

		t.Logf(
			"%d entry/entries in availabilityExempt now have an `## Availability` section "+
				"and can be deleted from it: %s",
			len(settled), strings.Join(settled, ", "),
		)
	}

	t.Logf("checked the CircleCI Server verdict on %d page(s) of %d gated type(s)",
		checked, len(gated))
}

// TestAvailabilityTablesStateACircleCIServerVerdict fails when a page has an
// `## Availability` section that does not answer the question it exists to
// answer.
//
// The section's whole job is "does this work on my installation". A table that
// omits the CircleCI Server row, or writes the verdict in a form no reader can
// scan — anything not opening with Yes, No, Unverified or Depends — is a section
// that looks complete and answers nothing. Two shapes are accepted because two
// are in use: the five-row Cloud/Server/API/Organization type/Token table, which
// is the established one, and an older two-column Cloud|Server table.
//
// "Unverified" is a first-class answer here, not a failure. No CircleCI Server
// installation has been available to test this provider against, so several
// verdicts are honestly unknown, and the repository's convention is to say so
// rather than to infer. In particular, "the route is v2, so Server serves it" is
// not sound — `circleci_pipeline_definition` is v2 and unavailable on Server.
func TestAvailabilityTablesStateACircleCIServerVerdict(t *testing.T) {
	t.Parallel()

	var (
		malformed []string
		missing   []string
		found     int
	)

	for _, page := range allDocumentationPages(t) {
		section, hasSection := availabilitySection(t, page.file)
		if !hasSection {
			if _, exempt := availabilityExempt[page.key]; !exempt {
				missing = append(missing, page.template)
			}

			continue
		}

		found++

		if verdict, cell := serverVerdict(section); verdict == "" {
			malformed = append(malformed, page.template+shapeHint(cell))
		}
	}

	if found == 0 {
		t.Fatal("found no `## Availability` sections under templates/; the walk is broken, not the templates")
	}

	if len(malformed) > 0 {
		sort.Strings(malformed)

		t.Errorf(
			"%d `## Availability` section(s) do not state a readable CircleCI Server verdict:\n  %s\n\n"+
				"Use the five-row shape and open the Server cell with Yes, No, Unverified or "+
				"Depends:\n\n"+
				"    | | |\n"+
				"    | --- | --- |\n"+
				"    | **CircleCI Cloud** | Yes |\n"+
				"    | **CircleCI Server** | No — <why> |\n"+
				"    | **API** | `GET /api/v2/...` |\n"+
				"    | **Organization type** | Any. |\n"+
				"    | **Token** | Any valid API token. |\n",
			len(malformed), strings.Join(malformed, "\n  "),
		)
	}

	// Reported, not failed. Roughly half the pages have no availability section
	// yet, and writing them is ongoing work that should not be blocked on this
	// file; the ones that matter most — every type the provider gates — are
	// covered as errors by the test above.
	if len(missing) > 0 {
		sort.Strings(missing)

		t.Logf("%d page(s) have no `## Availability` section yet:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}

	t.Logf("checked %d `## Availability` section(s)", found)
}

// documentationPage locates one type's page.
type documentationPage struct {
	// key is "<kind>/<name without the circleci_ prefix>", the form used by
	// availabilityExempt.
	key string
	// template is the repository-relative template path, for failure output.
	template string
	// file is the on-disk path to read.
	file string
}

// documentationPagesFor returns the pages that document a type: a resource page,
// a data source page, or both, whichever exist.
func documentationPagesFor(t *testing.T, typeName string) []documentationPage {
	t.Helper()

	bare := strings.TrimPrefix(typeName, "circleci_")

	var pages []documentationPage

	for _, kind := range []string{"resources", "data-sources", "ephemeral-resources"} {
		template := path.Join("templates", kind, bare+".md.tmpl")
		file := filepath.Join(repoRoot, template)

		if _, err := os.Stat(file); err != nil {
			continue
		}

		pages = append(pages, documentationPage{
			key:      kind + "/" + bare,
			template: template,
			file:     file,
		})
	}

	return pages
}

// allDocumentationPages returns every per-type page. Guides are excluded: they
// are prose about a workflow rather than a statement about one type, and the
// self-hosted runner guide's availability discussion is deliberately narrative.
func allDocumentationPages(t *testing.T) []documentationPage {
	t.Helper()

	var pages []documentationPage

	for _, kind := range []string{"resources", "data-sources", "ephemeral-resources", "functions"} {
		dir := filepath.Join(repoRoot, "templates", kind)

		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}

		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md.tmpl") {
				continue
			}

			bare := strings.TrimSuffix(entry.Name(), ".md.tmpl")

			pages = append(pages, documentationPage{
				key:      kind + "/" + bare,
				template: path.Join("templates", kind, entry.Name()),
				file:     filepath.Join(dir, entry.Name()),
			})
		}
	}

	if len(pages) == 0 {
		t.Fatal("found no templates under templates/; the walk is broken")
	}

	return pages
}

// availabilityHeading matches the section and captures its body, which runs to
// the next second-level heading or the end of the file.
var availabilityHeading = regexp.MustCompile(`(?ms)^## Availability\r?\n(.*?)(?:\n## |\z)`)

// availabilitySection returns a page's `## Availability` body.
func availabilitySection(t *testing.T, file string) (body string, ok bool) {
	t.Helper()

	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}

	match := availabilityHeading.FindStringSubmatch(string(content))
	if match == nil {
		return "", false
	}

	return match[1], true
}

// fiveRowServerCell matches the CircleCI Server row of the established five-row
// table.
var fiveRowServerCell = regexp.MustCompile(`(?m)^\|\s*\*\*CircleCI Server\*\*\s*\|(.*)$`)

// twoColumnHeader matches the header of the older two-column shape.
var twoColumnHeader = regexp.MustCompile(`(?m)^\|\s*CircleCI Cloud\s*\|\s*CircleCI Server\s*\|\s*$`)

// tableSeparator matches a markdown header separator row, e.g. `|---|---|`.
var tableSeparator = regexp.MustCompile(`^\|[\s:|-]+\|$`)

// serverVerdict reads the CircleCI Server verdict out of an availability
// section, returning the normalised verdict and the raw cell it came from. The
// verdict is "" when no cell could be found or the cell opens with something
// unrecognised; the raw cell is returned either way so failure output can show
// what was actually there.
func serverVerdict(section string) (verdict, cell string) {
	cell = serverCell(section)

	trimmed := strings.ToLower(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(cell), "*")))

	for _, known := range []string{"yes", "no", "unverified", "depends"} {
		if strings.HasPrefix(trimmed, known) {
			return known, cell
		}
	}

	return "", cell
}

// serverCell extracts the raw CircleCI Server cell from either accepted table
// shape.
func serverCell(section string) string {
	if match := fiveRowServerCell.FindStringSubmatch(section); match != nil {
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(match[1]), "|"))
	}

	header := twoColumnHeader.FindStringIndex(section)
	if header == nil {
		return ""
	}

	for _, line := range strings.Split(section[header[1]:], "\n") {
		line = strings.TrimSpace(line)

		if line == "" || tableSeparator.MatchString(line) {
			continue
		}

		if !strings.HasPrefix(line, "|") {
			return ""
		}

		columns := strings.Split(strings.Trim(line, "|"), "|")
		if len(columns) < 2 {
			return ""
		}

		return strings.TrimSpace(columns[1])
	}

	return ""
}

// shapeHint appends the offending cell to a failure line, or says there was none.
func shapeHint(cell string) string {
	if strings.TrimSpace(cell) == "" {
		return "\n      no CircleCI Server row was found in either accepted table shape"
	}

	return "\n      the cell reads: " + strconv.Quote(truncate(cell, 120))
}

// truncate shortens a cell for failure output, which only needs enough of it to
// identify the line to edit.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	return s[:limit] + "…"
}

// gatedTypeNames returns every type name the provider refuses on CircleCI
// Server, mapped to why, read statically from the gate call sites.
func gatedTypeNames(t *testing.T) map[string]string {
	t.Helper()

	// Production code only: a gate that exists solely in a test gates nothing.
	files := parsePackageGoFiles(t, func(fileName string) bool {
		return !strings.HasSuffix(fileName, "_test.go")
	})

	constants := stringConstants(files)

	gated := map[string]string{}
	unresolved := map[string]bool{}

	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall {
				return true
			}

			gate, isIdent := call.Fun.(*ast.Ident)
			if !isIdent {
				return true
			}

			reason, isGate := deploymentGates[gate.Name]
			if !isGate || len(call.Args) < 2 {
				return true
			}

			name, resolved := resolveTypeName(call.Args[1], constants)
			if !resolved {
				unresolved[types.ExprString(call.Args[1])] = true

				return true
			}

			gated[name] = reason

			return true
		})
	}

	var unexplained []string

	for expression := range unresolved {
		if _, known := unresolvableGates[expression]; !known {
			unexplained = append(unexplained, expression)
		}
	}

	if len(unexplained) > 0 {
		sort.Strings(unexplained)

		t.Errorf(
			"%d gate call site(s) name their type with an expression this scan cannot resolve:\n  %s\n\n"+
				"Pass a string literal or a package-level constant, so the set of Cloud-only types "+
				"stays readable statically. If the name genuinely has to be dynamic, add the "+
				"expression to unresolvableGates and the types it covers to dynamicallyGatedTypes, "+
				"both in this file.",
			len(unexplained), strings.Join(unexplained, "\n  "),
		)
	}

	for _, name := range dynamicallyGatedTypes {
		if _, already := gated[name]; !already {
			gated[name] = "CircleCI Cloud only"
		}
	}

	return gated
}

// resolveTypeName reads a type name out of a gate's second argument, which is
// either a string literal or an identifier bound to one.
func resolveTypeName(argument ast.Expr, constants map[string]string) (string, bool) {
	switch expression := argument.(type) {
	case *ast.BasicLit:
		if expression.Kind != token.STRING {
			return "", false
		}

		value, err := strconv.Unquote(expression.Value)
		if err != nil {
			return "", false
		}

		return value, true

	case *ast.Ident:
		value, known := constants[expression.Name]

		return value, known
	}

	return "", false
}

// stringConstants collects every package-level string constant, so a gate called
// with `orbTypeName` resolves to "circleci_orb".
//
// Takes the file map rather than the *ast.Package it came from: ast.Package is
// deprecated, and nothing here needs anything else off it.
func stringConstants(files map[string]*ast.File) map[string]string {
	constants := map[string]string{}

	for _, file := range files {
		for _, declaration := range file.Decls {
			generic, isGeneric := declaration.(*ast.GenDecl)
			if !isGeneric || generic.Tok != token.CONST {
				continue
			}

			for _, spec := range generic.Specs {
				value, isValue := spec.(*ast.ValueSpec)
				if !isValue {
					continue
				}

				for i, name := range value.Names {
					if i >= len(value.Values) {
						continue
					}

					literal, isLiteral := value.Values[i].(*ast.BasicLit)
					if !isLiteral || literal.Kind != token.STRING {
						continue
					}

					unquoted, err := strconv.Unquote(literal.Value)
					if err != nil {
						continue
					}

					constants[name.Name] = unquoted
				}
			}
		}
	}

	return constants
}

// registeredTypeNames returns every type name Terraform can actually address, so
// a gate naming a type that does not exist can be told apart from one naming a
// type that does.
func registeredTypeNames(t *testing.T) map[string]bool {
	t.Helper()

	p := &CircleCiProvider{version: "test"}
	ctx := t.Context()

	names := map[string]bool{}

	for _, newResource := range p.Resources(ctx) {
		var resp resource.MetadataResponse
		newResource().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "circleci"}, &resp)
		names[resp.TypeName] = true
	}

	for _, newDataSource := range p.DataSources(ctx) {
		var resp datasource.MetadataResponse
		newDataSource().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "circleci"}, &resp)
		names[resp.TypeName] = true
	}

	for _, newEphemeral := range p.EphemeralResources(ctx) {
		var resp ephemeral.MetadataResponse
		newEphemeral().Metadata(ctx, ephemeral.MetadataRequest{ProviderTypeName: "circleci"}, &resp)
		names[resp.TypeName] = true
	}

	if len(names) == 0 {
		t.Fatal("no types registered; the provider is broken, not the documentation")
	}

	return names
}
