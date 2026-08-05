// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This file guards a property of a *public* repository: nothing here should cite
// CircleCI's internal codebase.
//
// The provider is built against CircleCI's API, and much of what it knows about that
// API's real behaviour is recorded in comments — deliberately, because a maintainer
// who does not know why a fixture has a particular shape will eventually "tidy" it and
// break a test. What must not appear alongside those claims is the *provenance*: a
// path into an internal repository, an internal service or component name, or an
// identifier from a codebase nobody outside CircleCI can read. The behavioural claim
// is useful to every reader; the citation is useful to nobody and discloses CircleCI's
// internal layout.
//
// ## Why this test matches shapes and keeps an allow list, rather than a deny list
//
// The obvious implementation is a list of the internal names to reject. That list
// cannot live in this file, because publishing an enumeration of CircleCI's internal
// service names would leak exactly what the test exists to prevent — the deny list is
// itself the sensitive artefact.
//
// So the checks here need no secret at all:
//
//  1. *Shapes* that are never legitimate here regardless of what they name — a
//     Clojure namespace, a Go source path in a layout this repository does not use, an
//     internal hostname, a deployment variable.
//  2. Repository references, against an allow list of the repositories that are
//     public. An allow list is safe to publish, and it fails closed: a new
//     `circleci/<something>` citation is rejected until someone confirms it is public
//     and adds it here.
//  3. Cited `*.go` filenames, against the files this repository actually contains —
//     see TestNoGoFileCitationIsForeign. This is the check that has caught the most,
//     because "that file does not exist here" is a property of the repository and needs
//     no list of forbidden names to be complete.
//
// That leaves a gap these tests cannot close: a bare internal service or function name,
// cited with neither a path nor a filename — `contextsservice.ErrNotFound` was exactly
// that, and only a deny list found it. That fuller check is kept outside the repository.
// Run it before a release or before widening access; these tests are the always-on floor,
// not the whole audit.
//
// ## If this test fails
//
// Do not delete the comment. Rewrite it: keep the claim about what the API does, drop
// the citation that proves it. "The create route rejects an empty list" is the useful
// half; naming the handler that rejects it is the half that has to go.

// scannedExtensions are the file types that carry prose or code comments. Binary and
// generated artefacts are skipped because a match in them would be noise, not a leak.
var scannedExtensions = map[string]bool{
	".go": true, ".md": true, ".tmpl": true, ".tf": true,
	".yml": true, ".yaml": true, ".sh": true, ".json": true,
	// Rego policy examples and .hcl configuration carry comments too. They are
	// currently clean; they are listed so that a future comment in one of them is
	// covered rather than silently exempt.
	".rego": true, ".hcl": true,
}

// publicRepositories are the `circleci/…`-style GitHub repositories this repository is
// allowed to cite, because each one is public and a reader can actually follow the
// reference.
//
// `CircleCI-Public/circleci-cli` is not merely allowed but *required*: this provider's
// HTTP layer is derived from it under the MIT licence, and the attribution has to stay.
// `CircleCI-Public/chunk-cli` is where `internal/httpcl` was copied from, also under
// MIT — a different repository from `circleci-cli`, which supplied the *patterns* the
// entity layer follows. Both are public and both attributions are load-bearing.
//
// Note what is deliberately *not* here: a repository slug appearing in test fixture
// data. Fixtures use `example-org/example-repo` precisely so that no fixture looks like
// a citation of a real repository — to this test or to a human reader.
var publicRepositories = map[string]bool{
	"CircleCI-Public/circleci-cli":                true,
	"CircleCI-Public/chunk-cli":                   true,
	"CircleCI-Public/circleci-sdk-go":             true,
	"CircleCI-Public/terraform-provider-circleci": true,
	"CircleCI-Public/api-preview-docs":            true,
	"circleci/server":                             true, // the public Helm chart
	"AwesomeCICD/circleci-org-migration-cli":      true, // third-party, MIT, credited
}

// internalReferenceShapes are patterns that cannot legitimately appear in this
// repository, whatever they happen to name.
var internalReferenceShapes = []struct {
	name    string
	pattern *regexp.Regexp
	// why explains, in the failure message, what a maintainer should do instead.
	why string
}{
	{
		name:    "Clojure namespace or source file",
		pattern: regexp.MustCompile(`[A-Za-z0-9_./-]+\.clj\b|\bsrc/circle/`),
		why:     "this provider is Go; a .clj path can only be a citation of an internal codebase",
	},
	{
		name: "internal Go source path",
		// Deliberately anchored on layouts this repository does not use. Our own tree
		// is internal/circleci and internal/provider, which these cannot match.
		pattern: regexp.MustCompile(
			`(^|[^\w/-])(api/(external|public|publicapi|internalapi|org|project|context|runner)/` +
				`|server/handlers/|server/server\.go|bff/|cmd/external/)`),
		why: "a source path in a layout this repository does not have is a citation of another codebase",
	},
	{
		name:    "internal hostname",
		pattern: regexp.MustCompile(`internal\.circleci|circleci\.internal`),
		why:     "internal hostnames must never be published, even as an example",
	},
	{
		name:    "deployment or chart variable",
		pattern: regexp.MustCompile(`\b[A-Z][A-Z0-9]*_BASE_URL\b|\bimages\.yaml\b`),
		why:     "describing how CircleCI is deployed is disclosure even from a public chart",
	},
	{
		name:    "internal architecture naming",
		pattern: regexp.MustCompile(`\b[Mm]onolith`),
		why: "naming a component of CircleCI's internal architecture discloses its layout; " +
			"attribute the behaviour to the route or to \"the v2 API\" instead",
	},
	{
		name: "internal-codebase provenance",
		pattern: regexp.MustCompile(
			`owning (?:service|handler|backend|component|process)` +
				`|handler source|internally documented|documented internally`),
		why: "these phrases say \"we read CircleCI's internal source\" without naming anything; " +
			"say \"the API\" and state the behaviour as an observation",
	},
}

// repositoryReference finds a GitHub repository citation.
//
// The `github.com/` prefix is **required**, and that is a deliberate limitation. A bare
// `circleci/<something>` is hopelessly ambiguous in this repository: `.circleci/config.yml`
// is a real path every CircleCI user has, `circleci/ios-signing` is a live API path
// segment, and `CircleCI-Public/circleci` is the provider's own registry source string.
// Matching the bare form produced a false positive on every one of those. Requiring the
// host means this check only fires on something that is unambiguously a repository
// citation — the form every actual leak took.
//
// The bare form is covered by the fuller deny-list check kept outside the repository.
var repositoryReference = regexp.MustCompile(
	`github\.com/(CircleCI-Public|circleci|AwesomeCICD)/([A-Za-z0-9_.-]+)`)

// goFileCitation matches a Go filename mentioned in prose or a comment.
var goFileCitation = regexp.MustCompile(`\b[a-z][a-z0-9_]*\.go\b`)

// citableForeignGoFiles are Go filenames that do not exist in this repository but are
// legitimate to cite, because they belong to a public dependency a reader can go and
// read. Everything here is from terraform-plugin-framework or terraform-plugin-docs.
var citableForeignGoFiles = map[string]bool{
	"attribute_validation.go":                   true,
	"list_nested_attribute.go":                  true,
	"requires_replace_if.go":                    true,
	"write_only_nested_attribute_validation.go": true,
	"generate.go":                               true,
}

// TestNoGoFileCitationIsForeign fails if a tracked file cites a `*.go` filename that is
// neither a file in this repository nor a known public dependency.
//
// This is the most useful check in the file, and the only one that found anything the
// deny list did not. A citation of `handler_test.go` or `context_post.go` names a file in
// a codebase nobody outside CircleCI can read, and no deny list would ever have contained
// those names — but "this filename does not exist here" needs no deny list at all. It is a
// property of the repository, checkable from the repository.
//
// It also catches a second class of defect for free: a citation of one of *our* files that
// has since been renamed or deleted. That is not a leak, but it is a broken reference, and
// a reader who goes looking for the file wastes their time.
func TestNoGoFileCitationIsForeign(t *testing.T) {
	t.Parallel()

	ours := map[string]bool{}
	for _, file := range trackedFiles(t) {
		if strings.HasSuffix(file, ".go") {
			ours[filepath.Base(file)] = true
		}
	}

	for _, file := range trackedScannableFiles(t) {
		if filepath.Base(file) == "no_internal_references_test.go" {
			continue
		}

		body, err := os.ReadFile(filepath.Join(repoRoot, file))
		if err != nil {
			t.Errorf("reading %s: %v", file, err)
			continue
		}

		for lineNumber, line := range strings.Split(string(body), "\n") {
			for _, cited := range goFileCitation.FindAllString(line, -1) {
				if ours[cited] || citableForeignGoFiles[cited] {
					continue
				}
				t.Errorf(
					"%s:%d cites %q, which is not a file in this repository\n"+
						"\tIf it is a file in a codebase readers cannot see, remove the citation and "+
						"keep the claim it supported.\n"+
						"\tIf it is one of ours that was renamed, correct it.\n"+
						"\tIf it belongs to a public dependency, add it to citableForeignGoFiles.",
					file, lineNumber+1, cited)
			}
		}
	}
}

// TestNoInternalReferences fails if any tracked file cites CircleCI's internal
// codebase. See this file's header for why it matches shapes rather than names.
func TestNoInternalReferences(t *testing.T) {
	t.Parallel()

	for _, file := range trackedScannableFiles(t) {
		// This file necessarily contains the patterns it searches for.
		if filepath.Base(file) == "no_internal_references_test.go" {
			continue
		}

		body, err := os.ReadFile(filepath.Join(repoRoot, file))
		if err != nil {
			t.Errorf("reading %s: %v", file, err)
			continue
		}

		for lineNumber, line := range strings.Split(string(body), "\n") {
			for _, shape := range internalReferenceShapes {
				if match := shape.pattern.FindString(line); match != "" {
					t.Errorf(
						"%s:%d cites %s (%q)\n\t%s\n\tKeep the behavioural claim, drop the citation.",
						file, lineNumber+1, shape.name, match, shape.why)
				}
			}

			for _, match := range repositoryReference.FindAllStringSubmatch(line, -1) {
				repository := match[1] + "/" + match[2]
				if publicRepositories[repository] {
					continue
				}
				t.Errorf(
					"%s:%d cites repository %q, which is not on the public allow list\n"+
						"\tIf it is public, add it to publicRepositories. If it is internal, "+
						"remove the citation and keep the claim it was supporting.",
					file, lineNumber+1, repository)
			}
		}
	}
}

// trackedFiles lists every git-tracked file, used to decide whether a cited filename
// belongs to this repository.
func trackedFiles(t *testing.T) []string {
	t.Helper()

	output, err := exec.Command("git", "-C", repoRoot, "ls-files").Output()
	if err != nil {
		t.Skipf("git ls-files is unavailable, so this guard cannot run: %v", err)
	}

	return strings.Split(strings.TrimSpace(string(output)), "\n")
}

// trackedScannableFiles lists the git-tracked files worth scanning. It asks git rather
// than walking the tree so that untracked scratch files — including any local copy of
// the fuller deny-list check — cannot fail the build.
func trackedScannableFiles(t *testing.T) []string {
	t.Helper()

	output, err := exec.Command("git", "-C", repoRoot, "ls-files").Output()
	if err != nil {
		t.Skipf("git ls-files is unavailable, so this guard cannot run: %v", err)
	}

	var files []string
	for _, file := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if scannedExtensions[filepath.Ext(file)] {
			files = append(files, file)
		}
	}

	// A silent zero-file pass would make this test vacuous, and it has no natural
	// lower bound to assert other than "clearly more than a handful".
	if len(files) < 100 {
		t.Fatalf("only %d scannable files found; the guard is not actually scanning the repository", len(files))
	}

	return files
}

// registryCallout matches Terraform Registry admonition syntax at the start of a line.
var registryCallout = regexp.MustCompile(`(?m)^\s*[!~]>`)

// TestRegistryCalloutsOnlyAppearWhereTheRegistryRendersThem fails if `!>` or `~>` is used
// in a Markdown file that GitHub renders.
//
// The two syntaxes are trivially easy to confuse, and the failure is silent — nothing
// errors, the page just looks broken:
//
//   - `~>` and `!>` are *Terraform Registry* syntax. The registry turns them into
//     coloured admonitions. They are correct under docs/ and templates/, which is what
//     the registry publishes, and there are well over a hundred legitimate uses there.
//   - GitHub knows nothing about them and renders them as literal text, so a warning in
//     README.md comes out as a stray "!>" in the middle of a paragraph.
//
// GitHub's own syntax is `> [!WARNING]` / `> [!NOTE]` / `> [!IMPORTANT]`, and every line
// of the block has to stay quoted — an unquoted continuation line silently ends the
// admonition and leaves the rest as ordinary text, which is how the first fix here was
// wrong.
func TestRegistryCalloutsOnlyAppearWhereTheRegistryRendersThem(t *testing.T) {
	t.Parallel()

	for _, file := range trackedFiles(t) {
		if filepath.Ext(file) != ".md" {
			continue
		}
		// docs/ is what the registry publishes; templates/ generates it.
		if strings.HasPrefix(file, "docs/") || strings.HasPrefix(file, "templates/") {
			continue
		}

		body, err := os.ReadFile(filepath.Join(repoRoot, file))
		if err != nil {
			t.Errorf("reading %s: %v", file, err)
			continue
		}

		for lineNumber, line := range strings.Split(string(body), "\n") {
			if registryCallout.MatchString(line) {
				t.Errorf(
					"%s:%d uses Terraform Registry callout syntax (%q), which GitHub renders as "+
						"literal text\n\tUse a GitHub alert instead: \"> [!WARNING]\", \"> [!NOTE]\" or "+
						"\"> [!IMPORTANT]\", and keep every continuation line prefixed with \"> \".",
					file, lineNumber+1, strings.TrimSpace(line)[:2])
			}
		}
	}
}
