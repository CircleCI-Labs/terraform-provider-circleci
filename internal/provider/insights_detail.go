// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"net/http"

	"terraform-provider-circleci/internal/circleci"
)

// insightsDetail renders an Insights error for a Terraform diagnostic, adding the
// context the bare server message leaves out.
//
// Two Insights failures are routinely misread, and both are worth annotating:
//
// A 429 is the rate limiter, not a transient network fault. Insights is quota'd
// per caller, and a configuration that reads several Insights data sources in one
// plan can exhaust it on its own. Saying so points at reducing the number of reads
// rather than at retrying harder.
//
// A 404 is ambiguous. It means the project or organization does not exist, that
// the token cannot see it, or — most often — that the slug is malformed. The
// client rejects a slug with the wrong number of segments before making a request,
// so a 404 that gets this far is more likely to be a slug whose segments are wrong
// than one whose shape is.
func insightsDetail(err error) string {
	detail := circleci.Detail(err)

	switch {
	case circleci.HasStatus(err, http.StatusTooManyRequests):
		return detail + "\n\nCircleCI rate-limits Insights requests per caller. A configuration that reads " +
			"several Insights data sources in one plan can exhaust that quota by itself; reduce the number of " +
			"Insights reads or spread them across separate runs."
	case circleci.IsNotFound(err):
		return detail + "\n\nThis means the project or organization does not exist, or the configured token " +
			"cannot see it. Check the slug: a project slug is `vcs-slug/org-name/repo-name` (or " +
			"`circleci/<org-uuid>/<project-uuid>` for GitLab, GitHub App and GitHub Server projects), and an " +
			"organization slug is `vcs-slug/org-name` (or `circleci/<org-uuid>`)."
	default:
		return detail
	}
}
