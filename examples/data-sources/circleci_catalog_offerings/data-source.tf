# The execution catalog: which resource classes jobs can run on, and which machine
# images each one offers.
data "circleci_catalog_offerings" "this" {}

variable "resource_class" {
  type        = string
  description = "Resource class the generated CircleCI config will request."
  default     = "medium"
}

# Reject an unavailable resource class at plan time rather than discovering it when
# a job fails to start.
check "resource_class_is_available" {
  assert {
    condition     = contains(data.circleci_catalog_offerings.this.resource_classes, var.resource_class)
    error_message = "${var.resource_class} is not an available CircleCI resource class for this organization. Available: ${join(", ", data.circleci_catalog_offerings.this.resource_classes)}."
  }
}

# Each platform is a map keyed by resource class, so an image list can be indexed
# directly.
output "medium_linux_images" {
  value = lookup(data.circleci_catalog_offerings.this.linux, "medium", [])
}

output "macos_available" {
  value = length(data.circleci_catalog_offerings.this.macos) > 0
}

# Anything listed as deprecated needs migrating: an organization opted into
# resource class brownouts will see jobs on these classes fail during a brownout.
output "deprecated_resource_classes" {
  value = keys(data.circleci_catalog_offerings.this.deprecated)
}
