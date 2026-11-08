# CI checks (module: ci::). .github/workflows/changelog.yml and release.yml call these, and
# they run locally too. Each recipe works from the repository root.

# Changelog fragments gate. Every run: fragment rules and the checker's tests. A
# pull_request, or a local run on a branch other than main: CHANGELOG.md untouched since the
# merge base (unless labelled changelog:edit), no fragment a release already consumed, and a
# warning when none is added. A push to main (or a local run on main): HEAD must not
# hand-edit CHANGELOG.md. Every gate runs and reports before the recipe fails. Host python3,
# stdlib only.

# Changelog fragments gate (what CI runs)
[group('ci')]
changelog-check:
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{justfile_directory()}}"
    fail=0
    python3 just/changelog.py check || fail=1
    python3 just/test_changelog.py || fail=1
    event="${GITHUB_EVENT_NAME:-}"
    branch="$(git branch --show-current)"
    mode=push
    if [ "$event" = "pull_request" ]; then
        mode=pr
    elif [ -z "$event" ] && [ -n "$branch" ] && [ "$branch" != "main" ]; then
        mode=local
    fi

    if [ "$mode" = "push" ]; then
        # A direct push that hand-edits CHANGELOG.md skips the PR gate. Exempt: release
        # commits, squash merges ("(#N)" subject) and merge commits (two or more parents);
        # the PR behind the last two was already gated. Only HEAD is checked.
        subject="$(git log -1 --format=%s HEAD)"
        parents=$(( $(git rev-list --parents -n1 HEAD | wc -w) - 1 ))
        release_re='^chore\(release\): v'; squash_re='\(#[0-9]+\)$'
        if [[ ! "$subject" =~ $release_re && ! "$subject" =~ $squash_re ]] && [ "$parents" -lt 2 ] \
            && git rev-parse -q --verify HEAD~1 >/dev/null \
            && ! git diff --quiet HEAD~1 HEAD -- CHANGELOG.md; then
            echo "::error file=CHANGELOG.md::$(git rev-parse --short HEAD) edits CHANGELOG.md outside a release commit. Backfill notes go in changelog.d/<section>/pr-<N>.md."
            fail=1
        fi
        echo "event '${event:-none}' on '${branch:-detached HEAD}': fragment checks and the HEAD commit only"
        exit "$fail"
    fi

    if [ "$mode" = "pr" ]; then
        base="$(scripts/ci-merge-base.sh)" || exit 1
    else
        # Local run on a feature branch: the same gates a pull request gets, against
        # origin/main as last fetched. The PR's labels cannot be read from here.
        if ! base="$(git merge-base HEAD origin/main 2>/dev/null)"; then
            echo "::warning::no merge base with origin/main (run git fetch origin); skipping the pull-request gates"
            exit "$fail"
        fi
        if slug="$(python3 just/changelog.py slug "$branch" 2>/dev/null)"; then
            echo "Local run on '${branch}': fragments from this branch are changelog.d/<section>/${slug}.md"
        fi
    fi
    echo "Comparing $(git rev-parse --short HEAD) against merge base $(git rev-parse --short "$base")"

    if ! git diff --quiet "$base" HEAD -- CHANGELOG.md; then
        if [ "$mode" = "local" ]; then
            if [ "${CHANGELOG_EDIT:-}" = "1" ]; then
                echo "::notice file=CHANGELOG.md::This branch edits CHANGELOG.md; CHANGELOG_EDIT=1 says the PR will carry the changelog:edit label, which CI checks."
            else
                echo "::error file=CHANGELOG.md::This branch edits CHANGELOG.md, so CI will fail its PR. Move the new bullets into changelog.d/<section>/<branch-slug>.md (see changelog.d/README.md) and revert the CHANGELOG.md hunk. A deliberate correction to a released section needs the changelog:edit label on the PR; rerun with CHANGELOG_EDIT=1 to check the rest locally."
                fail=1
            fi
        else
            # Read live, not from the frozen event payload, so a re-run sees a label added later.
            rc=0; python3 just/changelog.py has-label changelog:edit || rc=$?
            if [ "$rc" -eq 0 ]; then
                echo "CHANGELOG.md edit allowed by the changelog:edit label"
            elif [ "$rc" -ne 1 ]; then
                echo "::error file=CHANGELOG.md::could not read PR labels (exit $rc); re-run the changelog job."
                fail=1
            else
                echo "::error file=CHANGELOG.md::This PR edits CHANGELOG.md. Move the new bullets into changelog.d/<section>/<branch-slug>.md (see changelog.d/README.md) and revert the CHANGELOG.md hunk. A deliberate correction to a released section: add the changelog:edit label, then re-run the failed changelog job."
                fail=1
            fi
        fi
    fi

    mapfile -d '' -t added < <(git diff -z --name-only --diff-filter=A "$base" HEAD -- 'changelog.d/*/*.md')
    if [ "$mode" = "local" ]; then
        # Not yet committed (staged or untracked) fragments count locally as added too.
        mapfile -d '' -t pending < <({ git diff -z --cached --name-only --diff-filter=A -- 'changelog.d/*/*.md'; git ls-files -z --others --exclude-standard -- 'changelog.d/*/*.md'; })
        added+=(${pending[@]+"${pending[@]}"})
    fi
    for path in ${added[@]+"${added[@]}"}; do
        hit="$(git log -n 1 --diff-filter=D --format='%H %s' --grep='^chore(release): v' "$base" -- "$path")"
        if [ -n "$hit" ]; then
            version="${hit#* chore(release): }"
            echo "::error file=${path}::${path} was released in ${version} (${hit:0:9}) and must not come back; delete it. A genuinely new note needs a new slug."
            fail=1
        fi
    done

    if [ "${#added[@]}" -eq 0 ]; then
        echo "::warning::This PR adds no changelog.d/ fragment. Fine for chore/CI/test work; anything a user or operator would notice needs one."
    fi
    exit "$fail"

# Write release notes for a v* tag: the CHANGELOG.md section for that version, then the
# commits since the previous release tag (pre-release tags are skipped). Needs full history
# (fetch-depth: 0). A tag with no section gets the commit list only; a hand-pushed vX.Y.Z
# with pending fragments also gets a warning, because the next release would list those
# fragments under the wrong version.

# Write GitHub Release notes for a v* tag
[group('ci')]
release-notes tag out="release-notes.md":
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{justfile_directory()}}"
    tag="{{ tag }}"
    version="${tag#v}"
    prev=$(git describe --tags --abbrev=0 --match 'v*' --exclude '*-*' "${tag}^" 2>/dev/null || true)
    section=""
    if [ -f CHANGELOG.md ]; then
        section=$(awk -v ver="$version" '
            $0 ~ "^## \\[" ver "\\]" {flag=1; next}
            flag && /^## \[/        {flag=0}
            flag                    {print}
        ' CHANGELOG.md | sed '/./,$!d')
    fi
    pending=""
    if [ -z "$section" ] && [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        pending=$(git ls-tree -r --name-only "$tag" -- changelog.d | grep -E '^changelog\.d/[^/]+/[^/]+\.md$' || true)
    fi
    {
        if [ -n "$section" ]; then printf '%s\n\n' "$section"; fi
        echo "### Commits"
        echo
        if [ -n "$prev" ]; then
            git log --no-merges --pretty='- %s (%h)' "${prev}..${tag}"
            echo
            echo "**Full changelog:** \`${prev}...${tag}\`"
        else
            git log --no-merges --pretty='- %s (%h)' "${tag}"
        fi
    } > "{{ out }}"
    if [ -n "$pending" ]; then
        echo "::warning::${tag} has no [${version}] section in CHANGELOG.md but has pending fragments; it was probably tagged by hand, and the next release will list them under the wrong version."
    fi
    echo "----- {{ out }} -----"
    cat "{{ out }}"
