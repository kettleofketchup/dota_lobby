# Release management (module: release::). Cuts vX.Y.Z from an up-to-date main.
#
# A bump assembles the changelog.d/ fragments into a new ## [X.Y.Z] section of CHANGELOG.md,
# deletes them, commits "chore(release): vX.Y.Z" to main, tags that commit (annotated) and
# pushes main, then the tag. The tag starts release.yml (GitHub Release, body from that
# CHANGELOG.md section) and container.yml (GHCR image).
#
# Must be run by a repo admin: the protect-main ruleset lets only its bypass actor (the
# Admin role) push to main directly.
#
#   just release::current   # print the current version
#   just release::hotfix    # patch: x.y.(Z+1)
#   just release::upgrade   # minor: x.(Y+1).0
#   just release::major     # major: (X+1).0.0

# Print the current version: the newest vX.Y.Z tag, pre-release tags ignored (0.0.0 if none)
[group('release')]
current:
    @cd "{{justfile_directory()}}" && just release::_current

# Bump patch (x.y.Z+1), assemble the changelog, tag and push: bug fix
[group('release')]
hotfix: (_bump "patch")

# Bump minor (x.Y+1.0), assemble the changelog, tag and push: feature release
[group('release')]
upgrade: (_bump "minor")

# Bump major (X+1.0.0), assemble the changelog, tag and push: breaking change
[group('release')]
major: (_bump "major")

[private]
_current:
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{justfile_directory()}}"
    latest=$(git tag -l 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n1 || true)
    latest="${latest:-v0.0.0}"
    echo "${latest#v}"

[private]
_bump LEVEL:
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{justfile_directory()}}"

    # 1) Preconditions: on main, clean tree, in sync with origin/main.
    branch=$(git rev-parse --abbrev-ref HEAD)
    if [ "${branch}" != "main" ]; then
        echo "Releases must be cut from main. Run: git checkout main && git pull --ff-only"; exit 1
    fi
    if [ -n "$(git status --porcelain)" ]; then
        echo "Working tree is not clean. Commit or stash changes first."; exit 1
    fi
    echo "Fetching origin..."
    git fetch --tags --quiet origin
    if [ "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]; then
        echo "Local main is not in sync with origin/main. Run: git pull --ff-only"; exit 1
    fi

    # 2) Next version.
    cur=$(just release::_current)
    if ! [[ "${cur}" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
        echo "Cannot parse current version '${cur}' as MAJOR.MINOR.PATCH"; exit 1
    fi
    major=${BASH_REMATCH[1]}; minor=${BASH_REMATCH[2]}; patch=${BASH_REMATCH[3]}
    case "{{LEVEL}}" in
        major) major=$((major + 1)); minor=0; patch=0 ;;
        minor) minor=$((minor + 1)); patch=0 ;;
        patch) patch=$((patch + 1)) ;;
        *) echo "Unknown bump level '{{LEVEL}}'"; exit 1 ;;
    esac
    next="${major}.${minor}.${patch}"
    tag="v${next}"
    if git rev-parse -q --verify "refs/tags/${tag}" >/dev/null; then
        echo "Tag ${tag} already exists; nothing to do."; exit 1
    fi
    echo ""
    echo "  current:  ${cur}"
    echo "  next:     ${next}"
    echo "  effect:   assemble changelog.d/ into CHANGELOG.md [${next}], commit"
    echo "            'chore(release): ${tag}' to main, tag ${tag}, push main and the tag"
    echo "            (release.yml publishes the GitHub Release, container.yml the image)"
    echo ""
    read -p "Release ${tag}? [y/N] " -n 1 -r; echo
    [[ $REPLY =~ ^[Yy]$ ]] || { echo "Cancelled."; exit 1; }

    # 3) Assemble. From here on a failure restores CHANGELOG.md and changelog.d/ from HEAD
    #    (working tree and index), so nothing is left half-done.
    today=$(date +%Y-%m-%d)
    rollback() {
        git checkout HEAD -- CHANGELOG.md changelog.d
        exit 1
    }
    # git ls-files, not a walk: ignored debris must not count as a fragment.
    fragments=$(git ls-files -- 'changelog.d/*/*.md' | wc -l)
    if [ "${fragments}" -eq 0 ]; then
        echo ""
        echo "changelog.d/ has no fragments: ${tag} would ship with no release notes."
        read -p "Continue anyway? [y/N] " -n 1 -r; echo
        [[ $REPLY =~ ^[Yy]$ ]] || { echo "Cancelled."; exit 1; }
    fi
    # Captured first: `mapfile < <(...)` would hide the script's exit status.
    if ! consumed="$(just/changelog.py assemble "${next}" "${today}")"; then
        echo "Failed to assemble changelog.d/ into CHANGELOG.md [${next}]."; rollback
    fi
    paths=()
    while IFS= read -r p; do if [ -n "${p}" ]; then paths+=("${p}"); fi; done <<<"${consumed}"
    if ! grep -qE "^## \[${next}\] - ${today}\$" CHANGELOG.md; then
        echo "CHANGELOG.md has no '## [${next}] - ${today}' section after assembling."; rollback
    fi
    # Zero fragments is allowed above, and `git rm` with no pathspec exits 128.
    if [ ${#paths[@]} -gt 0 ] && ! git rm -q -- "${paths[@]}"; then
        echo "Failed to remove the consumed fragments."; rollback
    fi

    # 4) Commit, then tag that commit. A hook, signing or a missing identity must not leave
    #    CHANGELOG.md modified and the fragments staged for removal.
    git add CHANGELOG.md
    if ! git commit -m "chore(release): ${tag}"; then
        echo "git commit failed. Restoring CHANGELOG.md and changelog.d/."; rollback
    fi
    if ! git tag -a "${tag}" -m "Release ${tag}"; then
        echo "git tag failed. Removing the release commit."
        git reset --hard HEAD~1; exit 1
    fi

    # 5) Push main first; undo locally if it is rejected.
    if ! git push origin main; then
        echo "Push to main was rejected. Only a bypass actor of the protect-main ruleset (a repo admin) may push to main directly; otherwise main moved since the fetch. Removing the local tag and release commit."
        git tag -d "${tag}"; git reset --hard HEAD~1
        exit 1
    fi
    if ! git push origin "${tag}"; then
        echo "main is pushed but the tag push failed. Retry with: git push origin ${tag}"; exit 1
    fi
    echo "Released ${tag}: main and the tag are pushed; release.yml and container.yml are running."
