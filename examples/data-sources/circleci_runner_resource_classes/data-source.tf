# Every resource class an organization owns.
data "circleci_runner_resource_classes" "example" {
  organization_id = "00000000-0000-0000-0000-000000000000"
}

output "resource_class_names" {
  value = [
    for class in data.circleci_runner_resource_classes.example.resource_classes :
    class.resource_class
  ]
}

# Narrowed to a single runner namespace.
data "circleci_runner_resource_classes" "namespaced" {
  namespace = "my-namespace"
}

# Resource class IDs by name, for modules that take an ID.
output "resource_class_ids" {
  value = {
    for class in data.circleci_runner_resource_classes.namespaced.resource_classes :
    class.resource_class => class.id
  }
}
