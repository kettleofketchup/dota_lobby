- `CHANGELOG.md`, assembled at release time from per-PR fragments in `changelog.d/`.
- A `changelog` CI check that rejects hand edits to `CHANGELOG.md`, malformed fragments and
  fragments a release already consumed, and warns when a pull request adds none.
- `just release::hotfix`, `just release::upgrade` and `just release::major` cut a release:
  they assemble the notes, commit, tag `vX.Y.Z` and push.
- GitHub Releases carry the version's `CHANGELOG.md` section and a commit list as their body.
