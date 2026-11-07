#!/usr/bin/env bash
# Print the merge base of HEAD with a pull_request's base branch. Execute, never source:
#   base="$(scripts/ci-merge-base.sh)" || exit 1
# Exit 2: not a pull_request (no base ref). Exit 1: no merge base (shallow checkout).
set -euo pipefail

base_ref="${GITHUB_BASE_REF:-}"
if [ -z "$base_ref" ]; then
    echo "ci-merge-base.sh: no base ref (not a pull_request event)" >&2
    exit 2
fi

# Explicit refspec: actions/checkout fetches only the PR ref, so a plain `git fetch origin
# "$base_ref"` never creates refs/remotes/origin/<base> for merge-base below. A dev clone's
# wildcard refspec hides this.
git fetch --quiet origin "+refs/heads/${base_ref}:refs/remotes/origin/${base_ref}" >&2
if ! git merge-base HEAD "refs/remotes/origin/${base_ref}"; then
    echo "Could not find a merge base with ${base_ref}." >&2
    echo "The checkout is probably shallow; the job needs fetch-depth: 0." >&2
    exit 1
fi
