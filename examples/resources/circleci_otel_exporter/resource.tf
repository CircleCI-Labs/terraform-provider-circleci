# Export traces to a collector over OTLP/gRPC. Note that the endpoint is a bare
# host and port: no scheme.

# --- the write-only path, which is the one to prefer -------------------------
#
# headers_wo is a write-only argument (Terraform 1.11 or later): the provider sends
# the headers to CircleCI and then discards them, so they land in neither Terraform
# state nor the plan file. Headers usually carry the collector's credentials, which
# is what makes this worth doing.
#
# Declaring the variable `ephemeral` means Terraform will not persist it either,
# which closes the last gap. In a real configuration this would usually come from a
# secret manager's ephemeral resource instead of a variable -- for example
# `ephemeral.vault_kv_secret_v2.collector.data["api_key"]`. An ephemeral value may
# only be referenced from a write-only argument.
variable "collector_api_key_wo" {
  description = "Collector credential, read from a secret manager."
  type        = string
  sensitive   = true
  ephemeral   = true
}

# Because nothing derived from the headers is stored, Terraform cannot tell that
# they changed. headers_wo_version is the rotation counter: increment it whenever
# any header changes, or the new headers are never sent. One counter covers the
# whole map.
#
# Incrementing it forces a new resource with a new id, exactly as editing `headers`
# does. That is not a choice: the API has no update route, so traces are not
# exported during the gap.
resource "circleci_otel_exporter" "grpc" {
  org_id   = "00000000-0000-0000-0000-000000000000"
  endpoint = "otel.example.com:4317"
  protocol = "grpc"

  headers_wo = {
    "x-api-key" = var.collector_api_key_wo
  }
  headers_wo_version = 1 # bump to 2 to send rotated credentials
}

# --- the state-backed path, still supported ----------------------------------
#
# headers is the original spelling. It works identically from CircleCI's point of
# view, and it is recorded in Terraform state in cleartext. It does keep one thing
# headers_wo cannot: because the API returns header *names* in full, the provider
# compares the names in state against the names CircleCI reports, and so notices a
# header added or removed outside Terraform. headers_wo stores no names, so it
# notices neither that nor a changed value.
#
# Set at most one of headers and headers_wo.
variable "collector_api_key" {
  type      = string
  sensitive = true
}

resource "circleci_otel_exporter" "grpc_stateful" {
  org_id   = "00000000-0000-0000-0000-000000000000"
  endpoint = "otel-legacy.example.com:4317"
  protocol = "grpc"

  headers = {
    "x-api-key" = var.collector_api_key
  }
}

# --- no headers at all --------------------------------------------------------
#
# Neither attribute is required: an exporter that needs no credentials sets
# neither. That is why the two conflict rather than being exactly-one-of.

# OTLP/HTTP, usually on port 4318.
resource "circleci_otel_exporter" "http" {
  org_id   = "00000000-0000-0000-0000-000000000000"
  endpoint = "otel-http.example.com:4318"
  protocol = "http"
}

# A collector reachable only over a private network, where transport security is
# handled elsewhere. Anything sent in headers or headers_wo travels in the clear.
resource "circleci_otel_exporter" "internal" {
  org_id   = "00000000-0000-0000-0000-000000000000"
  endpoint = "collector.internal:4317"
  protocol = "grpc"
  insecure = true
}

# CircleCI reports configuration problems it detects, such as an endpoint that
# stopped resolving. This is unaffected by which header spelling was used.
output "exporter_issues" {
  value = circleci_otel_exporter.grpc.issues
}
