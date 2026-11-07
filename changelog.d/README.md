# Changelog fragments

Never edit `CHANGELOG.md` in a PR. Add one file per section instead:

    changelog.d/<section>/<slug>.md

- `<section>` is a `dir` from `sections.json`: `upgrading`, `security`, `added`, `changed`,
  `deprecated`, `removed`, `fixed`, `documentation`.
- `<slug>` is your branch name with `/` turned into `-`: branch `changelog-fragments` writes
  `added/changelog-fragments.md`, branch `fix/lobby-name` writes `fixed/fix-lobby-name.md`.
  It must match `^[a-z0-9][a-z0-9._-]*$`. Backfilling an already-merged PR on `main`:
  `pr-<N>.md`. Never `main.md`.
- Bullet sections hold `- ` bullets (continuations indented 2 spaces). `upgrading` is prose
  for operators (renamed config keys, breaking API changes, steps before or after
  upgrading) and also takes paragraphs and fenced code.
- No `#` headings, HTML comments or link-reference definitions; inline links are fine.

`just release::` assembles every fragment into the new `## [X.Y.Z]` section and deletes them.
`just ci::changelog-check` runs the same checks as CI (`just/changelog.py check` and its tests).
