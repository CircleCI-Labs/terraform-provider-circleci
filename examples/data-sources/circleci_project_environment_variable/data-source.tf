# The API always returns `value` masked (e.g. "xxxx1234"); it never discloses the
# configured value on any route. This data source is useful for confirming that a
# variable with a given name exists, not for reading its value back out.
data "circleci_project_environment_variable" "example" {
  project_slug = "github/my-org/my-repo"
  name         = "MY_SECRET"
}

output "circleci_my_secret_masked_value" {
  value = data.circleci_project_environment_variable.example.value
}
