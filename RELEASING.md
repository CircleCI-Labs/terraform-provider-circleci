# Releasing

What a release needs, what is already in place, and what only a maintainer can do.

Written because the release path is moving from GitHub Actions into CircleCI, and that
move needs one credential GitHub used to supply for free.

## What is already in place

| | |
|---|---|
| `.goreleaser.yml` | Builds every platform, produces the checksum file, GPG-signs it, and attaches `terraform-registry-manifest.json`. Registry-shaped already — do not change the artefact layout. |
| `terraform-registry-manifest.json` | Declares protocol version 6.0, which matches the provider's use of terraform-plugin-framework. |
| `.circleci/config.yml` | Lint, tests, doc drift, example validation and a vulnerability scan. Until now it had **no tag handling at all**, which is the real reason releases were never in CircleCI. |

## Tag handling, and why no exclusion condition is needed

**CircleCI does not run a workflow for a tag unless that workflow explicitly opts in.**
So a `release` workflow filtered to tags is the whole change:

```yaml
workflows:
  release:
    jobs:
      - goreleaser:
          filters:
            tags:
              only: /^v.*/
            branches:
              ignore: /.*/     # tags only, never a branch push
```

`build-and-test` needs no exclusion. It declares no tag filter, so a tag push skips it
without being told to. Confirmed by inspection: before this change the config contained no
`filters:` block anywhere, meaning a `v0.5.0` push triggered **nothing**.

## What a maintainer must supply

GoReleaser needs four things. GitHub Actions supplied one of them implicitly, which is the
part most likely to be missed.

| Variable | What it is | Notes |
|---|---|---|
| `GPG_PRIVATE_KEY` | The ASCII-armoured private signing key | Imported into the keyring before GoReleaser runs |
| `GPG_PASSPHRASE` | Its passphrase | The GitHub workflow calls this `PASSPHRASE` |
| `GPG_FINGERPRINT` | Fingerprint of that key | `.goreleaser.yml` reads it directly as `{{ .Env.GPG_FINGERPRINT }}`. In the GitHub workflow it came from the import step's output; in CircleCI it must be supplied |
| `GITHUB_TOKEN` | A GitHub personal access token with **contents: write** on this repository | **This is the new requirement.** GitHub Actions injects `GITHUB_TOKEN` automatically; CircleCI has no equivalent, so a PAT is required for GoReleaser to create the release and upload assets |

Put all four in a CircleCI context and grant it to this project. The release job's comments
name the context it expects.

> [!IMPORTANT]
> The GPG key must be the same one whose **public** half is registered with the Terraform
> Registry. The registry verifies the signature on the checksum file against that key; a
> mismatch fails publication with an error that does not obviously point at the key.

## Preconditions for publishing, which are not code

1. **The repository must be public.** The Terraform Registry only publishes from a public
   GitHub repository. This one is currently private, so publication is blocked regardless of
   tooling.
2. **The GPG public key must be uploaded** to the registry under the publishing namespace.
3. **The namespace must own the provider name.** This branch publishes under
   `CircleCI-Labs`, so the address is `source = "CircleCI-Labs/circleci"`. The namespace is
   the GitHub organization that published it; the type is the repository name with the
   mandatory `terraform-provider-` prefix stripped. `main` deliberately still says
   `CircleCI-Public/circleci`, because that is what an upstream pull request should contain.

## Cutting a release

```sh
task lint && task test && task validate-examples && task ci:diff
git tag v0.5.0
git push origin v0.5.0
```

The tag triggers the release workflow. Nothing else does.

Version numbers carry no compatibility promise yet — nothing has been released from this
work, and the provider has no users outside this repository, so the number is free to pick.

## Deliberately not done

**The GitHub Actions workflow has been removed.** It duplicated the CircleCI `release`
job and fired on the same tag. If a CircleCI release ever proves unworkable, the file is
recoverable from history — it was HashiCorp's scaffold, unmodified.

> [!NOTE]
> **There is only one release path now.** `.github/workflows/release.yml` has been
> removed, so a `v*` tag starts the CircleCI `release` workflow and nothing else. That
> file previously fired on the same tag, and both would have tried to create the same
> GitHub release — one winning, the other failing on a conflict, or the two interleaving
> asset uploads. Releasing from CircleCI is the point of this provider, so CircleCI is
> the path that was kept.

## The alternative worth taking if this repository moves to a GitHub App organization

Today this project is GitHub OAuth (`gh/…`), which gets exactly one **synthetic** pipeline
definition — derived from the project, readable but not creatable. So release configuration
has to live in the same file as CI, separated by tag filters.

In a GitHub App organization, pipeline definitions are real objects and a second one can
point at its own configuration file, triggered only by tags:

```terraform
resource "circleci_pipeline_definition" "release" {
  project_id  = data.circleci_project.provider.id
  name        = "release"
  description = "Tag-triggered release"

  config_source_provider         = "github_app"
  config_source_file_path        = ".circleci/release.yml"
  config_source_repo_external_id = data.circleci_github_app_repository.provider.external_id

  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = data.circleci_github_app_repository.provider.external_id
}

resource "circleci_trigger" "release_on_tag" {
  project_id             = data.circleci_project.provider.id
  pipeline_definition_id = circleci_pipeline_definition.release.id

  event_source_provider         = "github_app"
  event_source_repo_external_id = data.circleci_github_app_repository.provider.external_id
  event_preset                  = "only-tags"
}
```

That is better than filter syntax on two counts: the release configuration lives in its own
file, and the separation is declared rather than implied. It also means this provider
configures its own release using its own resources, which is worth being able to point at.

`only-tags` is a real event preset — it is in the set the trigger validator enforces.
