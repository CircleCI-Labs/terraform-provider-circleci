data "circleci_ios_signing_certificate" "distribution" {
  id = "11111111-1111-1111-1111-111111111111"
}

output "distribution_certificate_expires_at" {
  value = data.circleci_ios_signing_certificate.distribution.expires_at
}
