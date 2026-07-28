# Export traces to a collector over OTLP/gRPC. Note that the endpoint is a bare
# host and port: no scheme.
resource "circleci_otel_exporter" "grpc" {
  organization_id = "00000000-0000-0000-0000-000000000000"
  endpoint        = "otel.example.com:4317"
  protocol        = "grpc"

  headers = {
    "x-api-key" = var.collector_api_key
  }
}

# OTLP/HTTP, usually on port 4318.
resource "circleci_otel_exporter" "http" {
  organization_id = "00000000-0000-0000-0000-000000000000"
  endpoint        = "otel-http.example.com:4318"
  protocol        = "http"
}

# A collector reachable only over a private network, where transport security is
# handled elsewhere. Anything sent in `headers` travels in the clear.
resource "circleci_otel_exporter" "internal" {
  organization_id = "00000000-0000-0000-0000-000000000000"
  endpoint        = "collector.internal:4317"
  protocol        = "grpc"
  insecure        = true
}

variable "collector_api_key" {
  type      = string
  sensitive = true
}

# CircleCI reports configuration problems it detects, such as an endpoint that
# stopped resolving.
output "exporter_issues" {
  value = circleci_otel_exporter.grpc.issues
}
