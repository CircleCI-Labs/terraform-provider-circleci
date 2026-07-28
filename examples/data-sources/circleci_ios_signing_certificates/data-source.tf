data "circleci_ios_signing_certificates" "all" {
  organization_id = "00000000-0000-0000-0000-000000000000"
}

# Find the distribution certificate expiring soonest, to build an alert on.
output "distribution_certificate_ids" {
  value = [
    for cert in data.circleci_ios_signing_certificates.all.certificates :
    cert.id if cert.cert_type == "distribution"
  ]
}
