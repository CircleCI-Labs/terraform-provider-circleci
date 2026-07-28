# The import ID is "<organization_id>/<namespace_name>". The organization is part
# of it because the CircleCI API does not report which organization owns a
# namespace, so it cannot be discovered during the read that follows the import.
terraform import circleci_orb_namespace.example "00000000-0000-0000-0000-000000000000/acme"

# The namespace UUID works in place of the name.
terraform import circleci_orb_namespace.example "00000000-0000-0000-0000-000000000000/11111111-1111-1111-1111-111111111111"
