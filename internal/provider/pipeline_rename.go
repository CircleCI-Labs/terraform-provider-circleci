// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"strings"

	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

// One data source is renamed, and is registered under both its old and its new type
// name while the old one is retired:
//
//	circleci_pipeline -> circleci_pipeline_definition
//
// CircleCI has two things a UUID can identify: a pipeline *definition*, which holds
// the checkout source, the config source and the config file path, and a pipeline
// *run*, one execution of a definition that spawns workflows and jobs. In CircleCI's
// own vocabulary "pipeline" colloquially means the run, so naming the definition
// `circleci_pipeline` invited passing a definition id where a run id was wanted. The
// new name says "definition".
//
// The other pipeline data sources renamed at the same time — the definition listing
// and the three run-scoped reads — never shipped under their old names, so they are
// registered under their new names only and have nothing to deprecate.
//
// This is the data source half. pipeline_resource_rename.go is the resource half,
// which additionally needs a state mover; a data source holds no state, so renaming
// one is only ever registration plus a warning.
//
// One file, like org_id_deprecation.go: ending the deprecation should be deleting
// this file, dropping the NewDeprecated* constructor from provider.go, and following
// the compiler.

// pipelineRenameNotice is the tail the messages here share. It says the old name
// still works, because the first question on seeing a rename warning is whether
// something has already broken.
const pipelineRenameNotice = " Both names read exactly the same data; the old one " +
	"keeps working until the next major release."

// renamedTypeName picks between the old and the new name of a renamed type. The
// pipeline definition resource uses it too, so both halves of the rename report names
// the same way.
//
// It serves both whole type names, as the Cloud-only diagnostic needs them, and type
// name suffixes, as Metadata needs them to append to req.ProviderTypeName.
func renamedTypeName(deprecated bool, old, current string) string {
	if deprecated {
		return old
	}

	return current
}

// deprecateRenamedDataSource marks a schema as the copy registered under the old
// type name: a warning on every plan that still uses it, and a matching note at the
// top of its documentation page.
//
// distinction is what the new name makes explicit, phrased to follow "This data
// source" and without terminating punctuation — for example "reads a pipeline
// definition, not a pipeline run".
func deprecateRenamedDataSource(schema *dsschema.Schema, oldName, newName, distinction string) {
	schema.DeprecationMessage = "Use " + newName + " instead. This data source " +
		distinction + "." + pipelineRenameNotice

	schema.MarkdownDescription = "~> **Deprecated in favour of [`" + newName + "`](" +
		strings.TrimPrefix(newName, "circleci_") + ").** This data source " + distinction +
		", which the name `" + oldName + "` obscured." + pipelineRenameNotice + "\n\n" +
		schema.MarkdownDescription
}
