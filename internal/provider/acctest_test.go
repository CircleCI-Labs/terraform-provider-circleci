// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"os"
	"testing"
)

// Acceptance tests in this package talk to a real CircleCI installation, so
// every organization, project, pipeline, trigger, context and webhook they
// reference has to exist there. Those identifiers used to be hardcoded, which
// tied the suite to a single (now deleted) account. They are now read from
// environment variables instead: each helper below skips the calling test when
// its variable is unset, so a developer without a provisioned test account gets
// skips rather than failures. README.md documents the full list.

// testAccEnv returns the value of the named acceptance-test environment
// variable, skipping the calling test when it is empty.
func testAccEnv(t *testing.T, name, description string) string {
	t.Helper()

	value := os.Getenv(name)
	if value == "" {
		t.Skipf("%s must be set for this acceptance test (%s); see the Development section of README.md.", name, description)
	}

	return value
}

// testOrgID returns the UUID of the primary CircleCI-VCS test organization.
func testOrgID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_ORG_ID", "UUID of the primary test organization")
}

// testOrgSlug returns the slug of the primary test organization, for example
// "circleci/<org-identifier>".
func testOrgSlug(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_ORG_SLUG", "slug of the primary test organization")
}

// testOrgName returns the display name of the primary test organization.
func testOrgName(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_ORG_NAME", "name of the primary test organization")
}

// testAltOrgID returns the UUID of a second CircleCI-VCS organization, used by
// the tests that move a project between organizations.
func testAltOrgID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_ALT_ORG_ID", "UUID of a second test organization")
}

// testAltOrgSlug returns the slug of the second CircleCI-VCS organization.
func testAltOrgSlug(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_ALT_ORG_SLUG", "slug of a second test organization")
}

// testGithubOrgID returns the UUID of a GitHub-backed test organization.
func testGithubOrgID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GITHUB_ORG_ID", "UUID of a GitHub-backed test organization")
}

// testGithubOrgSlug returns the slug of a GitHub-backed test organization, for
// example "gh/myorg".
func testGithubOrgSlug(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GITHUB_ORG_SLUG", "slug of a GitHub-backed test organization")
}

// testProjectID returns the UUID of the project that pipelines, triggers,
// webhooks and context restrictions are created against.
func testProjectID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_PROJECT_ID", "UUID of the writable test project")
}

// testProjectSlug returns the slug of the project that environment variables
// are created on, for example "circleci/<org>/<project>".
func testProjectSlug(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_PROJECT_SLUG", "slug of the writable test project")
}

// testStaticProjectID returns the UUID of a pre-existing project that is only
// ever read, never modified.
func testStaticProjectID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_STATIC_PROJECT_ID", "UUID of a pre-existing read-only project")
}

// testStaticProjectSlug returns the slug of the pre-existing read-only project.
func testStaticProjectSlug(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_STATIC_PROJECT_SLUG", "slug of a pre-existing read-only project")
}

// testStaticProjectName returns the name of the pre-existing read-only project.
func testStaticProjectName(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_STATIC_PROJECT_NAME", "name of a pre-existing read-only project")
}

// testPipelineID returns the UUID of a pre-existing pipeline belonging to the
// project identified by CIRCLECI_TEST_PROJECT_ID.
func testPipelineID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_PIPELINE_ID", "UUID of a pre-existing pipeline in the writable test project")
}

// testGithubAppRepoExternalID returns the external (GitHub) ID of the
// repository reachable through the GitHub App integration.
func testGithubAppRepoExternalID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GITHUB_APP_REPO_EXTERNAL_ID", "GitHub App repository external ID")
}

// testGithubAppRepoName returns the full name ("owner/repo") of the repository
// reachable through the GitHub App integration.
func testGithubAppRepoName(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GITHUB_APP_REPO_NAME", "GitHub App repository full name")
}

// testGithubServerProjectID returns the UUID of a project backed by a GitHub
// Server (self-hosted) integration.
func testGithubServerProjectID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GITHUB_SERVER_PROJECT_ID", "UUID of a GitHub Server backed project")
}

// testGithubServerPipelineID returns the UUID of a pipeline in the GitHub
// Server backed project.
func testGithubServerPipelineID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GITHUB_SERVER_PIPELINE_ID", "UUID of a pipeline in the GitHub Server backed project")
}

// testGithubServerRepoExternalID returns the external ID of the repository
// reachable through the GitHub Server integration.
func testGithubServerRepoExternalID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GITHUB_SERVER_REPO_EXTERNAL_ID", "GitHub Server repository external ID")
}

// testContextID returns the UUID of a pre-existing context in the primary test
// organization.
func testContextID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_CONTEXT_ID", "UUID of a pre-existing context")
}

// testContextName returns the name of the pre-existing context.
func testContextName(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_CONTEXT_NAME", "name of a pre-existing context")
}

// testContextEnvVarName returns the name of an environment variable that
// already exists on the pre-existing context.
func testContextEnvVarName(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_CONTEXT_ENV_VAR_NAME", "name of an environment variable on the pre-existing context")
}

// testTriggerID returns the UUID of a pre-existing GitHub App trigger.
func testTriggerID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_TRIGGER_ID", "UUID of a pre-existing GitHub App trigger")
}

// testTriggerProjectID returns the UUID of the project owning the pre-existing
// GitHub App trigger.
func testTriggerProjectID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_TRIGGER_PROJECT_ID", "UUID of the project owning the pre-existing trigger")
}

// testScheduledTriggerID returns the UUID of a pre-existing scheduled trigger
// belonging to the project identified by CIRCLECI_TEST_STATIC_PROJECT_ID.
func testScheduledTriggerID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_SCHEDULED_TRIGGER_ID", "UUID of a pre-existing scheduled trigger")
}

// testWebhookID returns the UUID of a pre-existing webhook scoped to the
// project identified by CIRCLECI_TEST_PROJECT_ID.
func testWebhookID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_WEBHOOK_ID", "UUID of a pre-existing webhook")
}

// testWebhookName returns the name of the pre-existing webhook.
func testWebhookName(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_WEBHOOK_NAME", "name of the pre-existing webhook")
}

// testWebhookURL returns the receiver URL of the pre-existing webhook.
func testWebhookURL(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_WEBHOOK_URL", "URL of the pre-existing webhook")
}

// testRunnerNamespace returns the runner namespace of the primary test
// organization, used to build "<namespace>/<resource-class>" names.
func testRunnerNamespace(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_RUNNER_NAMESPACE", "runner namespace of the primary test organization")
}
