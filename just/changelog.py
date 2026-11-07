#!/usr/bin/env python3
"""Changelog fragments: validate changelog.d/ and assemble it into CHANGELOG.md.

    just/changelog.py check                   # every fragment rule; exit 1 on any problem
    just/changelog.py assemble VERSION DATE   # write ## [VERSION] - DATE, print consumed paths
    just/changelog.py has-label NAME          # exit 0 if the PR carries NAME, 1 if not, 2 on API error
    just/changelog.py slug [BRANCH]           # print the fragment slug for BRANCH (default: current)

PRs add files under changelog.d/<section>/ and never edit CHANGELOG.md, so a branch that
merges after a release cannot drop its notes under the released version. Stdlib only;
tests in test_changelog.py.
"""

import argparse
import difflib
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path

POINTER = "<!-- Do not edit. Unreleased changes live in changelog.d/; `just release::` assembles them here. -->"
FRAGMENT_DIR = "changelog.d"
NO_CHANGES = "- No notable changes."

SLUG = re.compile(r"^[a-z0-9][a-z0-9._-]*$")
VERSION_RE = re.compile(r"\d+\.\d+\.\d+")
DATE_RE = re.compile(r"\d{4}-\d{2}-\d{2}")
RELEASE_HEADING = re.compile(r"^## \[([^\]]+)\]")
SUB_HEADING = re.compile(r"^### (.+?)\s*$")
LINK_DEFINITION = re.compile(r"^\[[^\]]+\]:\s")
FENCE = re.compile(r"^\s*(```|~~~)")
UNRELEASED = re.compile(r"^## \[Unreleased\][^\n]*\n", re.M)
NO_SLUG_BRANCHES = ("main", "master")


class ChangelogError(Exception):
    pass


def sections(root: Path) -> list:
    return json.loads((root / FRAGMENT_DIR / "sections.json").read_text(encoding="utf-8"))


def fragment_paths(root: Path) -> list:
    """Tracked fragment paths: the ones a release consumes and `git rm`s."""
    root = Path(root)
    known = {s["dir"] for s in sections(root)}
    return sorted(p for p in _tracked(root) if _is_fragment(p, known))


def _ls_files(root: Path, *args: str) -> list:
    """git ls-files, not a walk: ignored debris (editor backups) must not count."""
    out = subprocess.run(
        ["git", "-C", str(root), "ls-files", "-z", *args, "--", FRAGMENT_DIR],
        check=True,
        capture_output=True,
    ).stdout
    return [p for p in out.decode().split("\0") if p]


def _tracked(root: Path) -> list:
    return _ls_files(root)


def untracked(root: Path) -> list:
    """Files under changelog.d/ that are neither tracked nor ignored, such as a fragment not
    yet `git add`ed. check() reads them too, so a local run cannot pass on a file CI rejects."""
    return sorted(_ls_files(Path(root), "--others", "--exclude-standard"))


def slugify(branch: str) -> str:
    """Branch name to fragment slug: lowercase, every run of characters outside [a-z0-9._-]
    (including '/') becomes one '-', leading non-alphanumerics and trailing '-'/'.' dropped."""
    slug = re.sub(r"[^a-z0-9._-]+", "-", branch.lower())
    slug = re.sub(r"-{2,}", "-", slug)
    return re.sub(r"^[^a-z0-9]+", "", slug).rstrip("-.")


def _is_fragment(path: str, known: set) -> bool:
    parts = path.split("/")
    return len(parts) == 3 and parts[1] in known and parts[2].endswith(".md") and bool(SLUG.match(parts[2][:-3]))


def _not_a_fragment(path: str, by_dir: dict) -> str:
    """Say which part of the path is wrong, not just the template."""
    names = ", ".join(by_dir)
    parts = path.split("/")
    if len(parts) != 3:
        stem = parts[-1][:-3] if parts[-1].endswith(".md") else parts[-1]
        hint = f" (did you mean {FRAGMENT_DIR}/{stem}/<slug>.md?)" if len(parts) == 2 and stem in by_dir else ""
        return (
            f"{path}:1: not a fragment: it must sit exactly at {FRAGMENT_DIR}/<section>/<slug>.md{hint}; "
            f"sections: {names}"
        )
    section, name = parts[1], parts[2]
    reasons = []
    if section not in by_dir:
        guess = difflib.get_close_matches(section, list(by_dir), n=1)
        maybe = f" (did you mean '{guess[0]}'?)" if guess else ""
        reasons.append(f"unknown section '{section}'{maybe}; sections: {names}")
    if name.endswith(".md"):
        slug = name[:-3]
    else:
        slug = name.rsplit(".", 1)[0] if "." in name[1:] else name
        reasons.append("the file must end in .md")
    if not SLUG.match(slug):
        suggestion = slugify(slug)
        maybe = f" (try '{suggestion}')" if suggestion else ""
        reasons.append(
            f"slug '{slug}' must use only lowercase letters, digits, '.', '_' and '-', "
            f"starting with a letter or digit{maybe}"
        )
    return f"{path}:1: not a fragment: " + "; ".join(reasons)


def _check_unreleased(text: str) -> list:
    """[Unreleased] holds only the pointer comment. A release refuses to discard anything
    else there, and notes left there would end up under whichever version ships next."""
    match = UNRELEASED.search(text)
    if not match:
        return ["CHANGELOG.md:1: no '## [Unreleased]' heading"]
    first = text.count("\n", 0, match.end()) + 1
    for n, line in enumerate(text[match.end() :].splitlines(), first):
        if line.startswith("## ["):
            break
        if line.strip() and line.strip() != POINTER:
            return [
                f"CHANGELOG.md:{n}: [Unreleased] must contain only the pointer comment; move these lines "
                f"into {FRAGMENT_DIR}/<section>/<slug>.md (see {FRAGMENT_DIR}/README.md). The "
                "changelog:edit label does not cover [Unreleased]."
            ]
    return []


def _drop_fences(lines: list) -> list:
    kept, inside = [], False
    for line in lines:
        if FENCE.match(line):
            inside = not inside
        elif not inside:
            kept.append(line)
    return kept


def _collapse(text: str) -> str:
    return " ".join(text.split())


def _bullets(lines: list) -> list:
    """Top-level bullets, continuation lines joined, whitespace collapsed."""
    found = []
    is_open = False
    for line in _drop_fences(lines):
        if line.startswith("- "):
            found.append(line[2:])
            is_open = True
        elif is_open and line.startswith(" ") and line.strip():
            found[-1] += " " + line
        elif line.strip():
            is_open = False  # an unindented non-bullet line ends the bullet
    return [_collapse(b) for b in found if b.strip()]


def released(text: str) -> list:
    """(version, heading, body lines) for every ### block under a released ## [X.Y.Z]."""
    blocks = []
    version = heading = None
    for line in text.splitlines():
        release = RELEASE_HEADING.match(line)
        if release:
            version = release.group(1) if VERSION_RE.fullmatch(release.group(1)) else None
            heading = None
            continue
        sub = SUB_HEADING.match(line)
        if sub and version:
            heading = sub.group(1)
            blocks.append((version, heading, []))
            continue
        if version and heading:
            blocks[-1][2].append(line)
    return blocks


def _check_fragment(path: str, raw: bytes, section: dict, history: list) -> list:
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError:
        return [f"{path}:1: not valid UTF-8"]
    if not text.strip():
        return [f"{path}:1: empty fragment"]
    lines = text.splitlines()
    problems = []
    first = True
    inside = False
    for n, line in enumerate(lines, 1):
        if not line.strip():
            continue
        if not section["prose"]:
            if first and not line.startswith("- "):
                problems.append(f"{path}:{n}: a {section['heading']} fragment must start with a '- ' bullet")
            elif not first and not line.startswith(("- ", "  ")):
                problems.append(f"{path}:{n}: start a new '- ' bullet or indent the continuation 2 spaces")
        first = False
        if FENCE.match(line):
            inside = not inside
            continue
        if inside:
            continue
        stripped = line.lstrip()
        if stripped.startswith("#"):
            problems.append(f"{path}:{n}: no headings in a fragment; the section comes from its directory")
        elif stripped.startswith("<!--"):
            problems.append(f"{path}:{n}: no HTML comments in a fragment")
        elif LINK_DEFINITION.match(stripped):
            problems.append(f"{path}:{n}: no link-reference definitions; use an inline link")
    if inside:
        problems.append(f"{path}:{len(lines)}: unclosed code fence")
    if problems:
        return problems
    return _check_resurrection(path, lines, section, history)


def _check_resurrection(path: str, lines: list, section: dict, history: list) -> list:
    hint = "a release already consumed this fragment; delete the file (a genuinely new note needs a new slug)"
    if section["prose"]:
        mine = _collapse("\n".join(_drop_fences(lines)))
        if not mine:
            return []
        for version, heading, body in history:
            if heading == section["heading"] and mine in _collapse("\n".join(_drop_fences(body))):
                return [f"{path}:1: released in {version}: {hint}"]
        return []
    shipped = {}
    for version, _heading, body in history:
        for bullet in _bullets(body):
            shipped.setdefault(bullet, version)
    for bullet in _bullets(lines):
        if bullet in shipped:
            return [f"{path}:1: released in {shipped[bullet]}: {hint}"]
    return []


def check(root: Path) -> list:
    root = Path(root)
    try:
        config = sections(root)
    except (OSError, ValueError) as exc:
        return [f"{FRAGMENT_DIR}/sections.json:1: unreadable ({exc})"]
    by_dir = {s["dir"]: s for s in config}
    text = (root / "CHANGELOG.md").read_text(encoding="utf-8")
    history = released(text)
    problems = _check_unreleased(text)
    for path in sorted(set(_tracked(root)) | set(untracked(root))):
        if path in (f"{FRAGMENT_DIR}/sections.json", f"{FRAGMENT_DIR}/README.md"):
            continue
        if not _is_fragment(path, set(by_dir)):
            problems.append(_not_a_fragment(path, by_dir))
            continue
        full = root / path
        if not full.exists():
            problems.append(f"{path}:1: tracked but missing from the working tree")
            continue
        problems += _check_fragment(path, full.read_bytes(), by_dir[path.split("/")[1]], history)
    return problems


def render(root: Path, paths: list) -> str:
    """The release body: sections.json order, fragments by code-point filename order."""
    root = Path(root)
    parts = []
    for section in sections(root):
        prefix = f"{FRAGMENT_DIR}/{section['dir']}/"
        mine = sorted((p for p in paths if p.startswith(prefix)), key=lambda p: p.rsplit("/", 1)[1])
        texts = [(root / p).read_text(encoding="utf-8").strip() for p in mine]
        texts = [t for t in texts if t]
        if texts:
            joined = ("\n\n" if section["prose"] else "\n").join(texts)
            parts.append(f"### {section['heading']}\n\n{joined}")
    return "\n\n".join(parts) or NO_CHANGES


def assemble(root: Path, version: str, date: str) -> list:
    root = Path(root)
    if not VERSION_RE.fullmatch(version):
        raise ChangelogError(f"version '{version}' is not MAJOR.MINOR.PATCH")
    if not DATE_RE.fullmatch(date):
        raise ChangelogError(f"date '{date}' is not YYYY-MM-DD")
    problems = check(root)
    if problems:
        raise ChangelogError("changelog.d/ fails check:\n" + "\n".join(problems))

    path = root / "CHANGELOG.md"
    text = path.read_text(encoding="utf-8")
    if re.search(rf"^## \[{re.escape(version)}\]", text, re.M):
        raise ChangelogError(f"CHANGELOG.md already has a [{version}] section; refusing to add a second.")
    match = re.search(r"^## \[Unreleased\][^\n]*\n", text, re.M)
    if not match:
        raise ChangelogError("CHANGELOG.md has no '## [Unreleased]' heading.")
    rest = text[match.end() :]
    nxt = re.search(r"^## \[", rest, re.M)
    body, tail = (rest[: nxt.start()], rest[nxt.start() :]) if nxt else (rest, "")
    if body.strip() not in ("", POINTER):
        raise ChangelogError(
            "CHANGELOG.md's [Unreleased] has content besides the pointer comment. Move it into a "
            "changelog.d/ fragment first; the release will not discard it."
        )

    paths = fragment_paths(root)
    section = f"## [{version}] - {date}\n\n{render(root, paths)}\n"
    new = text[: match.start()] + f"## [Unreleased]\n\n{POINTER}\n\n{section}"
    new += f"\n{tail}" if tail else ""
    path.write_text(new, encoding="utf-8")
    return paths


def has_label(name: str, env=os.environ) -> bool:
    """Read the PR's labels live, so a re-run after labelling sees the new label."""
    event_path = env.get("GITHUB_EVENT_PATH")
    event = json.loads(Path(event_path).read_text(encoding="utf-8")) if event_path else {}
    pr = event.get("pull_request")
    if not pr:
        print("has-label: not in a pull_request event; assuming no label", file=sys.stderr)
        return False
    token = env.get("GH_TOKEN") or env.get("GITHUB_TOKEN")
    if not token:
        print("has-label: no GH_TOKEN; using the event payload's labels (frozen at trigger time)", file=sys.stderr)
        return any(label["name"] == name for label in pr.get("labels", []))
    api = env.get("GITHUB_API_URL", "https://api.github.com").rstrip("/")
    url = f"{api}/repos/{env['GITHUB_REPOSITORY']}/issues/{pr['number']}/labels?per_page=100"
    if not url.startswith(("https://", "http://")):
        raise ChangelogError(f"refusing non-HTTP API URL {url!r}")
    request = urllib.request.Request(
        url, headers={"Authorization": f"Bearer {token}", "Accept": "application/vnd.github+json"}
    )
    with urllib.request.urlopen(request, timeout=30) as response:
        labels = json.load(response)
    return any(label["name"] == name for label in labels)


def _print_slug(root: Path, branch) -> int:
    if branch is None:
        branch = subprocess.run(
            ["git", "-C", str(root), "branch", "--show-current"], check=True, capture_output=True, text=True
        ).stdout.strip()
    slug = slugify(branch)
    if not slug or slug in NO_SLUG_BRANCHES:
        print(
            f"slug: branch '{branch or '(detached HEAD)'}' has no fragment slug; name the file "
            "pr-<N>.md after the pull request number instead",
            file=sys.stderr,
        )
        return 1
    print(slug)
    return 0


def main(argv: list) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    sub = parser.add_subparsers(dest="cmd", required=True)
    sub.add_parser("check")
    asm = sub.add_parser("assemble")
    asm.add_argument("version")
    asm.add_argument("date")
    lab = sub.add_parser("has-label")
    lab.add_argument("name")
    slg = sub.add_parser("slug")
    slg.add_argument("branch", nargs="?")
    args = parser.parse_args(argv)

    try:
        if args.cmd == "check":
            problems = check(args.root)
            for problem in problems:
                print(problem)
            if problems:
                print(f"{len(problems)} changelog fragment problem(s); see {FRAGMENT_DIR}/README.md", file=sys.stderr)
            loose = untracked(args.root)
            if loose:
                print(
                    f"note: checked {len(loose)} untracked file(s) ({', '.join(loose)}); "
                    "`git add` them or CI will not see them",
                    file=sys.stderr,
                )
            return 1 if problems else 0
        if args.cmd == "slug":
            return _print_slug(args.root, args.branch)
        if args.cmd == "assemble":
            for path in assemble(args.root, args.version, args.date):
                print(path)
            return 0
        return 0 if has_label(args.name) else 1
    except urllib.error.HTTPError as exc:
        print(f"has-label: API error {exc.code}: {exc.reason}", file=sys.stderr)
        return 2
    except (urllib.error.URLError, TimeoutError) as exc:
        print(f"has-label: API error: {getattr(exc, 'reason', exc)}", file=sys.stderr)
        return 2
    except ChangelogError as exc:
        print(f"changelog.py: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
