# Changelog

Release notes are written in the pull request that makes the change. Each pull request adds
small files called **fragments** under `changelog.d/`, and the release recipe gathers them
into `CHANGELOG.md`. Nobody edits `CHANGELOG.md` in a pull request.

## Why fragments

When every pull request edits the `[Unreleased]` section of one file, parallel pull requests
conflict with each other. Worse, a branch that edited `[Unreleased]` and merged after a
release had its notes land under the released version, with no conflict to warn anyone. A
new file with a unique name cannot do either.

## Writing a fragment

Path: `changelog.d/<section>/<slug>.md`, one file per section.

- `<slug>` comes from your branch name: lowercase it, turn every run of characters other
  than `a-z`, `0-9`, `.`, `_` and `-` (including `/`) into one `-`, and drop leading
  non-alphanumerics and trailing `-` or `.`. Branch `fix/lobby-name` writes
  `changelog.d/fixed/fix-lobby-name.md`; `Fix/Lobby_Name` gives `fix-lobby_name`;
  `feature/#12-timeout` gives `feature-12-timeout`. The result must match
  `^[a-z0-9][a-z0-9._-]*$`. To print the slug for the current branch:

    ```bash
    python3 just/changelog.py slug
    ```

- `main` has no slug, and `main.md` is never valid. A pull request opened from a fork's
  `main`, or a note added on `main` for an already-merged pull request, uses `pr-<N>.md`
  with the pull request number.

| Section | Heading in the release | What goes in it |
|---|---|---|
| `upgrading` | Upgrading | Prose for operators: renamed config keys, breaking API changes, steps to take before or after upgrading |
| `security` | Security | Fixes and hardening that affect security |
| `added` | Added | New features, endpoints, config keys |
| `changed` | Changed | Changes to existing behaviour |
| `deprecated` | Deprecated | Features that will be removed |
| `removed` | Removed | Features that are gone |
| `fixed` | Fixed | Bug fixes |
| `documentation` | Documentation | Notable docs changes |

Every section except `upgrading` holds `- ` bullets; continuation lines are indented two
spaces. `upgrading` takes paragraphs and fenced code. No fragment may contain `#` headings
(the heading comes from the directory), HTML comments or link-reference definitions
(`[x]: https://...`); inline links are fine.

````markdown
- **Lobby timeouts.** `POST /lobby/create` gives up after `lobby.create_timeout` seconds
  instead of waiting forever.
````

````markdown
Add the new timeout to `config.yaml` before upgrading:

```yaml
lobby:
  create_timeout: 30
```
````

`git add` the fragment, then run the command CI runs:

```bash
git add changelog.d/
just ci::changelog-check
```

Locally it runs the fragment checks and the checker's tests. It also reads untracked files
under `changelog.d/` and prints a note naming them, because CI only sees committed files. On
a feature branch it then runs the pull request gates against `origin/main` as last fetched
(run `git fetch origin` first): no `CHANGELOG.md` edit, no fragment a release already
consumed, and a warning when the branch adds none. It cannot read pull request labels, so a
`CHANGELOG.md` edit fails locally; if the pull request will carry `changelog:edit`, run it
with `CHANGELOG_EDIT=1`. The run in CI has the final say.

Only changes someone running the service would notice need a fragment. CI, test and chore
work can skip it; the check then only warns.

## What CI checks

The `changelog` workflow (`.github/workflows/changelog.yml`) runs `just ci::changelog-check`
on every pull request and every push to `main`. Every run checks the fragment format and that
`[Unreleased]` in `CHANGELOG.md` holds only the pointer comment, fails
on a fragment that repeats a bullet or upgrading note that was already released, and runs the
checker's own tests.

On a pull request it also:

- fails when the pull request changes `CHANGELOG.md` and does not carry the `changelog:edit`
  label;
- fails when the pull request adds a fragment path that a release already consumed, which
  happens on a stale branch. Delete the file; a genuinely new note needs a new slug;
- warns when the pull request adds no fragment.

On a push to `main` it fails when the pushed commit edits `CHANGELOG.md`, unless it is a
release commit (`chore(release): vX.Y.Z`), a merge commit, or a squash merge (subject ending
in `(#N)`). Only the pushed head commit is checked.

## Correcting a released section

To fix a mistake in an already released section, edit `CHANGELOG.md` directly in a pull
request and add the `changelog:edit` label. The label is read from the GitHub API when the
job runs, but adding it does not start a new run: re-run the failed `changelog` job
(`gh run rerun <run-id> --failed`) or push again.

## Cutting a release

A repo admin cuts releases from a clean `main` that matches `origin/main`:

```bash
just release::current   # print the current version
just release::hotfix    # x.y.Z+1, bug fixes
just release::upgrade   # x.Y+1.0, features
just release::major     # X+1.0.0, breaking changes
```

The recipe:

1. Refuses unless you are on `main`, the working tree is clean, and `main` matches
   `origin/main` after a fetch.
2. Computes the next version from the newest `vX.Y.Z` tag. Pre-release tags such as
   `v0.0.0-test.1` are ignored; with no release tag the current version is `0.0.0`.
3. Asks `Release vX.Y.Z? [y/N]`.
4. Assembles the fragments into a new `## [X.Y.Z] - YYYY-MM-DD` section of `CHANGELOG.md`,
   deletes them, and commits `chore(release): vX.Y.Z`.
5. Creates an annotated tag `vX.Y.Z`, pushes `main`, then pushes the tag.

If assembling or committing fails, it restores `CHANGELOG.md` and `changelog.d/` and stops. If
the push to `main` is rejected, it deletes the local tag and release commit. If only the tag
push fails, it prints the command to retry.

Only an admin can run it: the `protect-main` ruleset lets only its bypass actor (the Admin
role) push to `main` directly.

The tag starts `release.yml`, which publishes the GitHub Release with the version's
`CHANGELOG.md` section and the commits since the previous release as its body, and
`container.yml`, which publishes the image. See [Releasing](releasing.md).

Do not push a `vX.Y.Z` tag by hand: its notes would not be assembled, and the next release
would list them under the wrong version. Pre-release tags may be pushed by hand.

## One-time setup

A repository admin does this once:

1. Create the label, before the pull request that introduces this scheme is opened, and
   apply it to that pull request (it creates `CHANGELOG.md`):

    ```bash
    gh label create changelog:edit --color D93F0B --description "PR deliberately edits CHANGELOG.md (corrections to released sections)"
    ```

2. Edit the existing `protect-main` ruleset (Settings → Rules → Rulesets; do not create a
   second one): turn on **Require status checks to pass** and add the `changelog` check.
   Keep the Admin bypass in mode **Always** so the release recipe can push. Until this is
   done, violations are reported but a red pull request can still merge.
3. Recommended: turn off rebase merging in Settings → General → Pull Requests, and remove
   `rebase` from the ruleset's allowed merge methods (the two settings are enforced
   separately). The push check reads only the head commit, so a rebase merge could slip an
   earlier `CHANGELOG.md` edit past it, and would fail a labelled edit in its last commit.
