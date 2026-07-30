# Examples

This directory contains examples that are mostly used for documentation, but can also be run/tested manually via the Terraform CLI.

The document generation tool looks for files in the following locations by default. All other *.tf files besides the ones mentioned below are ignored by the documentation tool. This is useful for creating examples that can run and/or are testable even if some parts are not relevant for the documentation.

* **provider/provider.tf** example file for the provider index page
* **data-sources/`full data source name`/data-source.tf** example file for the named data source page
* **resources/`full resource name`/resource.tf** example file for the named resource page
* **ephemeral-resources/`full ephemeral resource name`/ephemeral-resource.tf** example file for the named ephemeral resource page — note the different filename, and that there is no `import.sh` for an ephemeral resource
* **functions/`function name`/function.tf** example file for the named function page

A directory named after a type but holding its Terraform under any other filename produces a page with no example at all, and nothing complains: the file is valid HCL, `terraform fmt` checks it happily, and generation succeeds. `TestEveryExampleDirectoryHasTheEntryFileTfplugindocsLooksFor` in `internal/provider/examples_test.go` is what catches that, along with a `{{ tffile }}` reference to a path that does not exist.

A page can show more than one example: put the extra configurations in additional files in the same directory and add a `{{ tffile "..." }}` call per file to the template. `examples/resources/circleci_trigger` does this, with one file per event source. Every file in the directory is part of one Terraform module, so resource names must not collide between them.
