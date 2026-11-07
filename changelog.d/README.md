# Changelog fragments

Never edit `CHANGELOG.md` in a PR. Add one file per section instead:

    changelog.d/<section>/<slug>.md

- `<section>` is a `dir` from `sections.json`: `upgrading`, `security`, `added`, `changed`,
  `deprecated`, `removed`, `fixed`, `documentation`.
- `<slug>` comes from your branch name: lowercase it, turn every run of characters other
  than `a-z`, `0-9`, `.`, `_` and `-` (including `/`) into one `-`, and drop leading
  non-alphanumerics and trailing `-` or `.`. Branch `changelog-fragments` writes
  `added/changelog-fragments.md`; `Fix/Lobby_Name` writes `fixed/fix-lobby_name.md`;
  `feature/#12-timeout` writes `feature-12-timeout.md`. `python3 just/changelog.py slug`
  prints the slug for the current branch. The result must match `^[a-z0-9][a-z0-9._-]*$`.
- No slug from `main` (never `main.md`): a PR opened from a fork's `main`, or a backfill for
  an already-merged PR, uses `pr-<N>.md` with the PR number.
- Bullet sections hold `- ` bullets (continuations indented 2 spaces). `upgrading` is prose
  for operators (renamed config keys, breaking API changes, steps before or after
  upgrading) and also takes paragraphs and fenced code.
- No `#` headings, HTML comments or link-reference definitions; inline links are fine.

Then `git add` the fragment and run `just ci::changelog-check`. It checks untracked files too
(and says so), but CI only sees what you commit. On a feature branch it also runs the PR gates
against `origin/main` as last fetched: no `CHANGELOG.md` edit, no resurrected fragment, a
warning when you add none. It cannot read PR labels; set `CHANGELOG_EDIT=1` if the PR will
carry `changelog:edit`. CI has the final say.

`just release::` assembles every fragment into the new `## [X.Y.Z]` section and deletes them.
