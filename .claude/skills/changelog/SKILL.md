---
name: changelog
description: "dota_lobby changelog and release workflow: add a changelog.d/<section>/<slug>.md fragment in every PR instead of editing CHANGELOG.md, what the `changelog` CI check enforces, the changelog:edit label, and how `just release::hotfix|upgrade|major` assembles fragments and cuts a vX.Y.Z release. Use when opening or finishing a PR, writing release notes, touching CHANGELOG.md, fixing a failed changelog check, tagging, or releasing."
---

# Changelog fragments

Nobody edits `CHANGELOG.md` in a PR. Each PR adds its notes as small files in `changelog.d/`;
the release recipe assembles them. Why: when every PR edits `[Unreleased]`, parallel PRs
conflict, and a branch that edited `[Unreleased]` and merged after a release had its notes
land under the released version with no conflict to warn anyone. A new file with a unique
name cannot.

## In every PR

1. Pick the section(s), one file each. Sections (`changelog.d/sections.json`, in release
   order): `upgrading` (prose for operators: renamed config keys, breaking API changes, steps
   before or after upgrading), `security`, `added`, `changed`, `deprecated`, `removed`,
   `fixed`, `documentation`.
2. Pick the slug: run `python3 just/changelog.py slug` (current branch) or apply the rule:
   lowercase, every run of characters outside `a-z0-9._-` (including `/`) becomes one `-`,
   leading non-alphanumerics and trailing `-`/`.` dropped (`Fix/Lobby_Name` gives
   `fix-lobby_name`). `main` has no slug and `main.md` is never valid: a PR from a fork's
   `main`, or a backfill pushed straight to `main` for a merged PR, is `pr-<N>.md`.
3. Write `changelog.d/<section>/<slug>.md`. Bullet sections: `- ` bullets, continuation lines
   indented 2 spaces. `upgrading`: paragraphs and fenced code. Never `#` headings, HTML
   comments or link-reference definitions; inline links are fine.
4. `git add` the fragment, then run `just ci::changelog-check`. It checks untracked files too
   (and names them in a note), runs the checker's tests, and on a feature branch runs the PR
   gates against `origin/main`. It cannot read labels: if the PR will carry
   `changelog:edit`, run it with `CHANGELOG_EDIT=1`.

Skip the fragment only for work nobody running the service would notice (CI, tests, chores).

## What the `changelog` check does

`.github/workflows/changelog.yml` runs `just ci::changelog-check` on pull requests and on
pushes to `main`. Every run: fragment format, `[Unreleased]` holding only the pointer
comment, and no fragment that repeats a released bullet or upgrading note. On a PR it also fails when `CHANGELOG.md` changed without the
`changelog:edit` label, or when the PR adds a fragment path a release already deleted (a
stale branch: delete the file; a genuinely new note needs a new slug). It warns when no
fragment is added. On a push it fails when the pushed head commit edits `CHANGELOG.md`,
unless it is a `chore(release): v` commit, a merge commit, or a squash merge (`(#N)`).

**`changelog:edit`** is for deliberate corrections to a released section. The label is read
live from the API, but adding it does not start a run: re-run the failed job
(`gh run rerun <run-id> --failed`) or push again.

## Releasing

A repo admin runs, on a clean `main` in sync with `origin/main`:

```bash
just release::current   # print the current version
just release::hotfix    # x.y.(Z+1)
just release::upgrade   # x.(Y+1).0
just release::major     # (X+1).0.0
```

`_bump` takes the newest `vX.Y.Z` tag (pre-release tags ignored, `0.0.0` if none), asks
`Release vX.Y.Z? [y/N]`, runs `just/changelog.py assemble` to write `## [X.Y.Z] - DATE`,
`git rm`s the fragments, commits `chore(release): vX.Y.Z`, makes an annotated tag, pushes
`main`, then the tag. A failed assembly or commit restores `CHANGELOG.md` and `changelog.d/`;
a rejected push of `main` removes the local tag and commit. Only an admin can do this: the
`protect-main` ruleset lets only its bypass actor (the Admin role) push to `main` directly.
Never push a `vX.Y.Z` tag by hand (its notes would not be assembled); pre-release tags such
as `v1.3.0-rc.1` are fine. The tag starts `release.yml`, whose Release body is the version's
`CHANGELOG.md` section plus a commit list (`just ci::release-notes`), and `container.yml`.

## One-time admin setup

1. Create the label before the PR that introduces this scheme is opened, and apply it to that
   PR (it creates `CHANGELOG.md`):
   ```bash
   gh label create changelog:edit --color D93F0B --description "PR deliberately edits CHANGELOG.md (corrections to released sections)"
   ```
2. Edit the existing `protect-main` ruleset (do not create a second one): add "Require status
   checks to pass" with the `changelog` check, and keep the Admin bypass in mode `always` so
   the release recipe can push. Until then violations are detected, not prevented.
3. Recommended: disable rebase merging in the repo settings and remove `rebase` from
   `protect-main`'s allowed merge methods (the ruleset is enforced separately). The push check
   reads only the head commit, so a rebase merge can slip an earlier `CHANGELOG.md` edit past
   it, and fails a labelled edit in its last commit.

## Files

`changelog.d/README.md` (rules), `changelog.d/sections.json`, `just/changelog.py`
(`check`, `assemble`, `has-label`), `just/test_changelog.py`, `just/ci.just`,
`just/release.just`, `scripts/ci-merge-base.sh`, `docs/development/changelog.md`.
