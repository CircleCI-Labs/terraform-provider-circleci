data "circleci_otel_exporters" "all" {
  org_id = "00000000-0000-0000-0000-000000000000"
}

# Every configured endpoint, including exporters created outside Terraform.
output "endpoints" {
  value = [for exporter in data.circleci_otel_exporters.all.exporters : exporter.endpoint]
}

# Exporters CircleCI has flagged a problem with.
output "unhealthy_exporters" {
  value = [
    for exporter in data.circleci_otel_exporters.all.exporters : {
      id     = exporter.id
      issues = exporter.issues
    }
    if length(exporter.issues) > 0
  ]
}

# How much of the per-organization limit is left.
output "remaining_exporter_slots" {
  value = 5 - length(data.circleci_otel_exporters.all.exporters)
}
