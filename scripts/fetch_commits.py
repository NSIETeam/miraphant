#!/usr/bin/env python3
"""Fetch commits from all Miraphant repos and write commits.json"""
import json
import subprocess
import sys
import os
from datetime import datetime

REPOS = [
    ("Felix201209", "otto", "otto"),
    ("NSIETeam", "circle", "circle"),
    ("NSIETeam", "OliveWolf", "OliveWolf"),
    ("NSIETeam", "miraphant", "miraphant"),
    ("NSIETeam", "EasyHermes", "EasyHermes"),
]

def fetch_commits(owner, repo, label):
    """Fetch all commits from a repo via gh CLI"""
    try:
        result = subprocess.run(
            ["gh", "api", f"repos/{owner}/{repo}/commits", "--paginate", "-q",
             '.[] | "\\(.commit.author.name)|\\(.commit.author.date)|\\(.commit.message | split("\\n")[0])"'],
            capture_output=True, text=True, timeout=120
        )
        if result.returncode != 0:
            print(f"WARN: Failed to fetch {owner}/{repo}: {result.stderr[:200]}", file=sys.stderr)
            return []

        commits = []
        for line in result.stdout.strip().split('\n'):
            line = line.strip()
            if not line:
                continue
            parts = line.split('|')
            if len(parts) < 3:
                continue
            author = parts[0]
            date = parts[1]
            msg = '|'.join(parts[2:])
            commits.append({
                "d": date,
                "a": author,
                "m": msg,
                "r": label
            })
        print(f"  {owner}/{repo}: {len(commits)} commits", file=sys.stderr)
        return commits
    except Exception as e:
        print(f"WARN: Error fetching {owner}/{repo}: {e}", file=sys.stderr)
        return []

def main():
    print("Fetching commits from all repos...", file=sys.stderr)
    all_commits = []
    for owner, repo, label in REPOS:
        commits = fetch_commits(owner, repo, label)
        all_commits.extend(commits)

    # Sort by date descending
    all_commits.sort(key=lambda c: c["d"], reverse=True)

    output = {
        "generated_at": datetime.utcnow().strftime("%Y-%m-%dT%H:%M:%SZ"),
        "total": len(all_commits),
        "commits": all_commits
    }

    # Write to commits.json
    output_path = os.path.join(os.path.dirname(__file__), "..", "commits.json")
    with open(output_path, 'w', encoding='utf-8') as f:
        json.dump(output, f, ensure_ascii=False, indent=None)

    print(f"\nDone: {len(all_commits)} commits written to commits.json", file=sys.stderr)
    print(f"Generated at: {output['generated_at']}", file=sys.stderr)

if __name__ == "__main__":
    main()
