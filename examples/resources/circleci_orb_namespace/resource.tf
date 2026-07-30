# A namespace is the globally unique prefix that owns an organization's orbs,
# as in "<namespace>/<orb>". CircleCI Cloud only.
resource "circleci_orb_namespace" "example" {
  name   = "acme"
  org_id = "00000000-0000-0000-0000-000000000000"
}

# The same namespace also names self-hosted runner resource classes, which are
# always "<namespace>/<class>". Create the namespace before the resource class.
resource "circleci_runner_resource_class" "builders" {
  org_id         = "00000000-0000-0000-0000-000000000000"
  resource_class = "${circleci_orb_namespace.example.name}/builders"
  description    = "Self-hosted build runners"
}
