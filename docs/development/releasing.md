# Releasing

This page is for maintainers, and for anyone running a fork who wants their own binaries and
images.

## What a release publishes

A release is a git tag that starts with `v`, in the form `vX.Y.Z`. A repo admin cuts it from
an up-to-date `main` with:

```bash
just release::upgrade
```

The recipe assembles the release notes, commits, tags and pushes; see
[Cutting a release](changelog.md#cutting-a-release). Do not push a `vX.Y.Z` tag by hand: its
notes would not be assembled, and the next release would list them under the wrong version.
Only pre-release tags such as `v1.3.0-rc.1` may be pushed by hand.

Pushing the tag starts two workflows:

| Workflow | Publishes |
|---|---|
| `.github/workflows/release.yml` | A GitHub Release for the tag, with the version's `CHANGELOG.md` section as its notes and the `dota_lobby` binary attached (Linux amd64 only) |
| `.github/workflows/container.yml` | A multi-arch image (`linux/amd64` and `linux/arm64`) at `ghcr.io/<owner>/dota_lobby` |

The image tags never have a leading `v`. For `v1.2.3` the image gets:

| Tag | Notes |
|---|---|
| `1.2.3` | The exact version. This is the one the docs tell people to pin |
| `1.2` | Moves to the newest `1.2.x` |
| `1` | Moves to the newest `1.x.x`; not published for `v0.x` tags |
| `latest` | Moves to the newest release |
| `sha-<short>` | The short commit hash the tag points at |

A pre-release tag such as `v1.3.0-rc.1` only gets its exact version (`1.3.0-rc.1`) and the
`sha-<short>` tag; it does not move `1.3`, `1` or `latest`.

The image name comes from `ghcr.io/${{ github.repository }}`, so a fork publishes under its
own namespace (`ghcr.io/<your-user>/dota_lobby`) with its own `GITHUB_TOKEN`; no extra
secrets are needed. `compose.yaml` and these docs use the literal
`ghcr.io/kettleofketchup/dota_lobby`, so a fork that wants to use its own image changes the
`image:` line in its `compose.yaml`.

On pull requests that touch the `Dockerfile`, `.dockerignore`, `go.mod`/`go.sum`, `cmd/`,
`pkg/` or the workflow itself, `container.yml` builds both platforms without pushing.

## Docs

The site is published to GitHub Pages by `.github/workflows/docs.yml` on **every push to
`main`**, not on tags. Pull requests only build it (`uv run --locked mkdocs build --strict`),
so a broken link fails the pull request instead of the deploy. The docs therefore describe
`main`, which can be ahead of the newest release.

## Release notes

Each pull request adds its notes as a fragment in `changelog.d/`, and the release recipe
assembles them into `CHANGELOG.md`. `release.yml` uses that version's section, followed by
the commits since the previous release, as the GitHub Release body. See
[Changelog](changelog.md) for how to write a fragment and what CI checks.

## One-time setup

`GITHUB_TOKEN` cannot change these settings, so someone with admin rights on the repository
does them once, in this order.

1. **Before the first push to `main` that includes `docs.yml`:** go to Settings → Pages →
   Build and deployment and set **Source** to **GitHub Actions**. Without it, the first docs
   run on `main` fails at the deploy step.
2. **After the first `v*` tag has been pushed and `container.yml` has finished:** open the
   `dota_lobby` package under the owner's Packages on GitHub, go to Package settings, set its
   visibility to **Public**, and link it to the repository if it is not linked already. New
   GHCR packages start private, so until you do this everyone else gets `unauthorized` when
   they pull the image.

!!! note
    Until the first tag exists and the package is public, there is no image anyone can
    pull. Users who arrive before that should
    [build the image locally](../deployment/docker.md#build-the-image-locally).

## Checklist

1. On `main`: CI is green (tests, lint and the docs build) and `make test` passes locally.
2. Cut the release (repo admin): `just release::upgrade|hotfix|major`.
3. Check that both `release.yml` and `container.yml` succeeded on the Actions tab.
4. If this is the first release, do step 2 of [One-time setup](#one-time-setup).
5. Check the image pulls without logging in:
   `docker pull ghcr.io/kettleofketchup/dota_lobby:X.Y.Z` (no `v`).
