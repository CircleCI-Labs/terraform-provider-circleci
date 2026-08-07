// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// repoRoot is where the examples/ and templates/ trees live, relative to this
// package's directory, which is where `go test` runs.
const repoRoot = "../.."

// exampleEntryFile maps an examples/ subtree to the one filename tfplugindocs will
// look for inside each of its directories.
//
// These names are not a convention this repository chose — they are hardcoded in
// terraform-plugin-docs' internal/provider/generate.go, one per registrable kind.
// Note that ephemeral resources use a third spelling, and that none of the four is
// the plain "example.tf" a reader might reasonably guess at.
var exampleEntryFile = map[string]string{
	"resources":           "resource.tf",
	"data-sources":        "data-source.tf",
	"ephemeral-resources": "ephemeral-resource.tf",
	"functions":           "function.tf",
}

// TestEveryExampleDirectoryHasTheEntryFileTfplugindocsLooksFor fails when an
// examples/ directory holds Terraform that documentation generation will never see.
//
// tfplugindocs discovers an example at exactly one path per directory — the name in
// exampleEntryFile — and silently ignores every other file. A directory named after
// a type but containing, say, context.tf therefore produces no example on the page
// at all, and nothing anywhere goes red: the file is valid HCL, `terraform fmt`
// checks it happily, and the generated page simply has a gap that only a human
// reading the rendered registry docs would notice.
//
// Eight directories were in exactly that state, holding orphaned Terraform while
// their templates carried a second, unvalidated copy inline. The inline copies had
// drifted: they still used the pre-deprecation `organization_id`, and the trigger's
// still named `pipeline_id`.
//
// Extra files alongside the entry file are fine and are the intended way to show
// several configurations — the template calls `tffile` once per file. This only
// requires that the entry file is one of them.
func TestEveryExampleDirectoryHasTheEntryFileTfplugindocsLooksFor(t *testing.T) {
	t.Parallel()

	var missing []string

	for _, kind := range sortedKeys(exampleEntryFile) {
		entryFile := exampleEntryFile[kind]
		kindDir := filepath.Join(repoRoot, "examples", kind)

		entries, err := os.ReadDir(kindDir)
		if os.IsNotExist(err) {
			continue
		}

		if err != nil {
			t.Fatalf("reading %s: %v", kindDir, err)
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			path := filepath.Join(kindDir, entry.Name(), entryFile)
			if _, err := os.Stat(path); err == nil {
				continue
			}

			// Name the likeliest candidate to rename, so the fix is a mv rather than
			// a hunt: the sole .tf file already in the directory, if there is one.
			hint := ""
			if orphan, single := singleTerraformFile(t, filepath.Join(kindDir, entry.Name())); single {
				hint = " (rename " + orphan + ")"
			}

			missing = append(missing,
				"examples/"+kind+"/"+entry.Name()+"/"+entryFile+hint)
		}
	}

	if len(missing) > 0 {
		t.Errorf(
			"%d example directory/directories have no file tfplugindocs will read:\n  %s\n\n"+
				"Create or rename each path above. tfplugindocs looks for exactly one filename "+
				"per kind — resource.tf, data-source.tf, ephemeral-resource.tf, function.tf — and "+
				"ignores every other file without a word, so Terraform under any other name is "+
				"dead weight that never reaches the documentation.",
			len(missing), strings.Join(missing, "\n  "),
		)
	}
}

// exampleReference matches a `{{ tffile "..." }}` or `{{ codefile "lang" "..." }}`
// call in a documentation template. The path is the last quoted argument in both.
var exampleReference = regexp.MustCompile(`\{\{-?\s*(?:tffile|codefile)\s([^}]*?)-?\}\}`)

// quotedArgument matches one double-quoted template argument.
var quotedArgument = regexp.MustCompile(`"([^"]*)"`)

// TestEveryTemplateExampleReferenceResolvesToAFile fails when a template includes an
// example file that is not there.
//
// This is the other half of the wiring, and it fails in the opposite direction: a
// `tffile` pointing at a path that does not exist makes tfplugindocs error out and
// abort generation, so it is louder than a missing entry file — but only when
// somebody runs generation. Checking it in the unit suite means a template edit and
// an examples/ rename cannot be committed half-done.
func TestEveryTemplateExampleReferenceResolvesToAFile(t *testing.T) {
	t.Parallel()

	referencedBy := templateExampleReferences(t)

	var (
		dangling []string
		checked  int
	)

	for _, referenced := range sortedKeys(referencedBy) {
		checked++

		if _, err := os.Stat(filepath.Join(repoRoot, referenced)); err == nil {
			continue
		}

		for _, template := range referencedBy[referenced] {
			dangling = append(dangling, referenced+"  (referenced by "+template+")")
		}
	}

	if checked == 0 {
		t.Fatal("found no tffile/codefile calls in templates/; the regexp is broken, not the templates")
	}

	if len(dangling) > 0 {
		sort.Strings(dangling)

		t.Errorf(
			"%d template example reference(s) point at a file that does not exist:\n  %s\n\n"+
				"Create each path above, or correct the reference in the named template. "+
				"tfplugindocs aborts generation on a missing include, so leaving one behind "+
				"breaks `task generate-doc` for everybody.",
			len(dangling), strings.Join(dangling, "\n  "),
		)
	}

	t.Logf("checked %d distinct example reference(s) across templates/", checked)
}

// templateExampleReferences returns every example path the templates include,
// mapped to the templates that include it. Both the keys and the template names
// are repository-relative and slash-separated, so they compare directly against a
// path built with path.Join and read the same way in failure output.
func templateExampleReferences(t *testing.T) map[string][]string {
	t.Helper()

	referencedBy := map[string][]string{}
	templatesDir := filepath.Join(repoRoot, "templates")

	err := filepath.WalkDir(templatesDir, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		body, err := os.ReadFile(file)
		if err != nil {
			return err
		}

		relative, err := filepath.Rel(repoRoot, file)
		if err != nil {
			return err
		}

		template := filepath.ToSlash(relative)

		for _, call := range exampleReference.FindAllStringSubmatch(string(body), -1) {
			arguments := quotedArgument.FindAllStringSubmatch(call[1], -1)
			if len(arguments) == 0 {
				continue
			}

			referenced := path.Clean(filepath.ToSlash(arguments[len(arguments)-1][1]))
			referencedBy[referenced] = append(referencedBy[referenced], template)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", templatesDir, err)
	}

	return referencedBy
}

// unreferencedExamples are the example files that deliberately have no
// `{{ tffile }}` calling them, with the reason each is acceptable.
//
// It is empty, and the bar for adding to it is high: an example file nothing
// includes is a file nothing reads. "It is only for `terraform validate`" is not a
// reason — every example is validated, and the validated ones are supposed to be
// the ones on the page. Like examplelessTypes, this is a ratchet: entries come out
// as examples get wired up and none goes in without a reason worth reading.
var unreferencedExamples = map[string]string{}

// TestEveryExampleFileIsIncludedByATemplate fails when an example file is valid,
// correctly named, resolvable — and rendered nowhere.
//
// This is the gap the other two guards leave between them. A template that carries
// its own inline ```terraform block instead of calling `{{ tffile }}` satisfies both
// of them trivially: the example directory has the entry file tfplugindocs expects,
// and there is no dangling reference because there is no reference at all. The
// example is simply dead, and the page shows the inline copy instead.
//
// That is worse than a cosmetic duplication, because the two halves are checked by
// different things — or rather, one of them is checked by nothing. `task
// validate-examples` type-checks every directory under examples/ against the
// provider's real schema; HCL fenced inside a Markdown template is invisible to it,
// to `terraform fmt`, and to the compiler. So the page displays the unverified copy
// while the verified one sits unused, which is exactly backwards, and is how four
// examples reached main with attributes that do not exist.
//
// Twenty-one templates were in that state. Their inline copies had drifted in
// precisely the way an unchecked copy drifts: `organization_id` where the examples
// had been swept to `org_id`, `pipeline_id` where triggers now take
// `pipeline_definition_id`, and a `name` on `circleci_trigger`, which has never had
// one.
func TestEveryExampleFileIsIncludedByATemplate(t *testing.T) {
	t.Parallel()

	referencedBy := templateExampleReferences(t)

	var (
		orphaned []string
		settled  []string
		found    int
	)

	for _, kind := range sortedKeys(exampleEntryFile) {
		kindDir := filepath.Join(repoRoot, "examples", kind)

		typeDirs, err := os.ReadDir(kindDir)
		if os.IsNotExist(err) {
			continue
		}

		if err != nil {
			t.Fatalf("reading %s: %v", kindDir, err)
		}

		for _, typeDir := range typeDirs {
			if !typeDir.IsDir() {
				continue
			}

			files, err := os.ReadDir(filepath.Join(kindDir, typeDir.Name()))
			if err != nil {
				t.Fatalf("reading %s: %v", filepath.Join(kindDir, typeDir.Name()), err)
			}

			for _, file := range files {
				if file.IsDir() || !strings.HasSuffix(file.Name(), ".tf") {
					continue
				}

				example := path.Join("examples", kind, typeDir.Name(), file.Name())
				found++

				_, referenced := referencedBy[example]
				_, exempt := unreferencedExamples[example]

				switch {
				case referenced && exempt:
					settled = append(settled, example)
				case !referenced && !exempt:
					orphaned = append(orphaned, example+
						"\n      add {{ tffile \""+example+"\" }} to "+
						documentationTemplateFor(t, kind, typeDir.Name()))
				}
			}
		}
	}

	if found == 0 {
		t.Fatal("found no .tf files under examples/; the walk is broken, not the examples")
	}

	if len(orphaned) > 0 {
		sort.Strings(orphaned)

		t.Errorf(
			"%d example file(s) are not included by any template, so nothing renders them:\n  %s\n\n"+
				"Add the call shown for each. If the named template already shows that Terraform as "+
				"an inline ```terraform block, delete the inline block as well — that duplication is "+
				"the whole defect. `task validate-examples` type-checks the file under examples/ "+
				"against the provider's real schema and cannot see HCL fenced inside a template, so "+
				"an inline copy puts unverified configuration on the page while the verified one goes "+
				"unread. If a file genuinely should be included by nothing, add it to "+
				"unreferencedExamples in this file with a reason.",
			len(orphaned), strings.Join(orphaned, "\n  "),
		)
	}

	// Deliberately not an error, for the same reason examplelessTypes reports rather
	// than fails: a branch that wires up an example should not also have to edit this
	// file to go green.
	if len(settled) > 0 {
		sort.Strings(settled)

		t.Logf(
			"%d entry/entries in unreferencedExamples are now included by a template and can be deleted from it: %s",
			len(settled), strings.Join(settled, ", "),
		)
	}

	t.Logf("checked %d example file(s) under examples/", found)
}

// documentationTemplateFor names the template that documents a type, which is the
// one that should include its examples: templates/<kind>/<type>.md.tmpl, with the
// provider prefix dropped from the directory name. Functions have no prefix to
// drop.
func documentationTemplateFor(t *testing.T, kind, typeDirectory string) string {
	t.Helper()

	template := path.Join("templates", kind,
		strings.TrimPrefix(typeDirectory, "circleci_")+".md.tmpl")

	if _, err := os.Stat(filepath.Join(repoRoot, template)); err != nil {
		return template + " (which does not exist yet, and needs writing)"
	}

	return template
}

// examplelessTypes are the registered type names that have no examples/ directory
// yet, with the reason each is still acceptable.
//
// This is a ratchet, not a permanent exemption: the list is expected to shrink and
// nothing may be added to it without a reason worth reading. A type not listed here
// and with no example fails TestEveryRegisteredTypeHasAnExample below.
var examplelessTypes = map[string]string{
	// Deprecated aliases of circleci_pipeline_definition, kept only so existing
	// configurations keep working. Their documentation pages say "use the other
	// name" and show a `moved` block; an example would be an example of the thing
	// we are asking people to stop writing.
	"resources/circleci_pipeline":    "deprecated alias of circleci_pipeline_definition",
	"data-sources/circleci_pipeline": "deprecated alias of circleci_pipeline_definition",
}

// TestEveryRegisteredTypeHasAnExample fails when a resource, data source, ephemeral
// resource or function is registered but has no examples/ directory.
//
// TestEveryConstructorIsRegistered makes a type reachable from Terraform;
// this makes it reachable from the documentation. A registered type with no example
// still gets a generated page — schema reference and nothing else — which is the
// least useful page in the registry and the easiest kind of gap to not notice,
// because it looks complete.
func TestEveryRegisteredTypeHasAnExample(t *testing.T) {
	t.Parallel()

	p := &CircleCiProvider{version: "test"}
	ctx := t.Context()

	// kind/name -> the entry file that would document it.
	expected := map[string]string{}

	for _, newResource := range p.Resources(ctx) {
		var resp resource.MetadataResponse
		newResource().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "circleci"}, &resp)
		expected["resources/"+resp.TypeName] = "resource.tf"
	}

	for _, newDataSource := range p.DataSources(ctx) {
		var resp datasource.MetadataResponse
		newDataSource().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "circleci"}, &resp)
		expected["data-sources/"+resp.TypeName] = "data-source.tf"
	}

	for _, newEphemeral := range p.EphemeralResources(ctx) {
		var resp ephemeral.MetadataResponse
		newEphemeral().Metadata(ctx, ephemeral.MetadataRequest{ProviderTypeName: "circleci"}, &resp)
		expected["ephemeral-resources/"+resp.TypeName] = "ephemeral-resource.tf"
	}

	for _, newFunction := range p.Functions(ctx) {
		var resp function.MetadataResponse
		newFunction().Metadata(ctx, function.MetadataRequest{}, &resp)
		// Functions are addressed by bare name, with no provider prefix.
		expected["functions/"+resp.Name] = "function.tf"
	}

	if len(expected) == 0 {
		t.Fatal("no types registered; the provider is broken, not the examples")
	}

	var (
		missing []string
		settled []string
	)

	for _, key := range sortedKeys(expected) {
		_, exempt := examplelessTypes[key]
		path := filepath.Join(repoRoot, "examples", key, expected[key])
		_, err := os.Stat(path)

		switch {
		case err == nil && exempt:
			settled = append(settled, key)
		case err != nil && !exempt:
			missing = append(missing, "examples/"+key+"/"+expected[key])
		}
	}

	if len(missing) > 0 {
		t.Errorf(
			"%d registered type(s) have no example:\n  %s\n\n"+
				"Write each file above. A page generated with no Example Usage section shows a "+
				"schema table and nothing that would tell a practitioner how the type is used or "+
				"what it depends on. If a type genuinely should not have one, add it to "+
				"examplelessTypes in this file with a reason.",
			len(missing), strings.Join(missing, "\n  "),
		)
	}

	// Deliberately not an error: examples are being written concurrently with this
	// guard, and a branch that adds one should not have to also edit this file to go
	// green. Reporting it keeps the exemption list from outliving its reasons.
	if len(settled) > 0 {
		t.Logf(
			"%d entry/entries in examplelessTypes now have examples and can be deleted from it: %s",
			len(settled), strings.Join(settled, ", "),
		)
	}
}

// singleTerraformFile reports the only .tf file in a directory, when there is
// exactly one — the likely rename target for a directory missing its entry file.
func singleTerraformFile(t *testing.T, dir string) (name string, single bool) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tf") {
			continue
		}

		if name != "" {
			return "", false
		}

		name = entry.Name()
	}

	return name, name != ""
}

// sortedKeys returns a map's keys in a stable order, so failure output does not
// change between runs.
func sortedKeys[V any](m map[string]V) []string {
	out := keys(m)
	sort.Strings(out)

	return out
}
