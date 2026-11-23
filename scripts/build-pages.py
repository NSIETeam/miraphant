#!/usr/bin/env python3
"""Build a clean, allowlisted static website tree for inspection or Pages CI."""

from __future__ import annotations

import argparse
import shutil
import subprocess
from pathlib import Path, PurePosixPath


ROOT = Path(__file__).resolve().parents[1]
ALLOWED_ROOT_FILES = {"badge-king.svg", "commits.json", "favicon.svg", "logo.png"}
ALLOWED_ROOT_DIRS = {
    "about",
    "ai-club",
    "changelog",
    "circle",
    "css",
    "downloads",
    "flexhmi",
    "followup",
    "images",
    "js",
    "manifesto",
    "oa",
    "olivewolf",
    "otto",
    "portal",
    "products",
}
DENIED_PARTS = {".git", "docs", "services", "work", "node_modules", "build", "data", "logs", "cache"}
DENIED_SUFFIXES = {".db", ".sqlite", ".pem", ".key", ".crt", ".log"}
OWNER_MARKER_NAME = ".miraphant-pages-staging-owner"
OWNER_MARKER_CONTENT = "miraphant-pages-staging-v1"


def tracked_paths() -> list[Path]:
    result = subprocess.run(
        ["git", "-C", str(ROOT), "ls-files", "-z"],
        check=True,
        stdout=subprocess.PIPE,
    )
    return [Path(path.decode("utf-8")) for path in result.stdout.split(b"\0") if path]


def is_allowed(path: Path) -> bool:
    posix = PurePosixPath(path.as_posix())
    if any(part in DENIED_PARTS for part in posix.parts):
        return False
    if posix.suffix.lower() in DENIED_SUFFIXES or any(part.startswith(".env") for part in posix.parts):
        return False
    if len(posix.parts) == 1:
        return posix.suffix.lower() == ".html" or posix.name in ALLOWED_ROOT_FILES
    return posix.parts[0] in ALLOWED_ROOT_DIRS


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--output",
        type=Path,
        default=ROOT.parent / "pages-staging",
        help="clean output webroot (default: ../pages-staging)",
    )
    args = parser.parse_args()
    output_arg = args.output.expanduser()
    if output_arg.is_symlink():
        raise SystemExit("Refusing a symlink output directory")
    output = output_arg.resolve()
    if output == ROOT or ROOT in output.parents or output in ROOT.parents:
        raise SystemExit("Refusing an output directory that contains or is inside the source repository")

    selected = sorted(path for path in tracked_paths() if is_allowed(path))
    required = {Path("index.html"), Path("404.html"), Path("commits.json"), Path("logo.png")}
    selected_set = set(selected)
    missing = required - selected_set
    if missing:
        raise SystemExit(f"Required public website files are not tracked: {', '.join(map(str, sorted(missing)))}")
    if not any(path.parts[0] == "downloads" for path in selected if path.parts):
        raise SystemExit("The allowlist omitted the public downloads")

    # Validate every input before considering removal of any previous output.
    for relative in selected:
        source = ROOT / relative
        ancestors = [ROOT]
        current = ROOT
        for part in relative.parts[:-1]:
            current = current / part
            ancestors.append(current)
        ancestors.append(source)
        if any(path.is_symlink() for path in ancestors):
            raise SystemExit(f"Refusing a website source below a symlink: {relative}")
        resolved_source = source.resolve()
        if ROOT not in resolved_source.parents or not resolved_source.is_file():
            raise SystemExit(f"Refusing non-regular website source: {relative}")

    marker = output.parent / f".{output.name}{OWNER_MARKER_NAME}"
    marker_content = f"{OWNER_MARKER_CONTENT}\n{output}\n"
    if marker.is_symlink():
        raise SystemExit("Refusing a symlink ownership marker")
    if marker.exists():
        if not marker.is_file() or marker.read_text(encoding="utf-8") != marker_content:
            raise SystemExit(f"Refusing to use an output directory without this script's ownership marker: {output}")
    elif output.exists():
        raise SystemExit(f"Refusing to remove an existing directory without an ownership marker: {output}")

    output.parent.mkdir(parents=True, exist_ok=True)
    if not marker.exists():
        with marker.open("x", encoding="utf-8") as owner_file:
            owner_file.write(marker_content)

    if output.exists():
        shutil.rmtree(output)
    output.mkdir(parents=True)

    for relative in selected:
        source = ROOT / relative
        destination = output / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, destination)

    staged = sorted(path.relative_to(output) for path in output.rglob("*") if path.is_file())
    if staged != selected:
        raise SystemExit("Staged files do not match the tracked allowlist")
    if any(any(part in DENIED_PARTS for part in PurePosixPath(p.as_posix()).parts) for p in staged):
        raise SystemExit("The staged site contains a denied directory")
    if any(p.suffix.lower() in DENIED_SUFFIXES or any(part.startswith(".env") for part in p.parts) for p in staged):
        raise SystemExit("The staged site contains a denied secret or runtime file")

    print(f"Built static staging tree: {output}")
    print(f"Included {len(staged)} tracked allowlisted files")
    print("Excluded service source, docs, Git metadata, environment files, databases, logs, dependencies, and build caches")


if __name__ == "__main__":
    main()
