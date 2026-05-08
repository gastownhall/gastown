#!/usr/bin/env python3
"""Filter GitHub issues that are already claimed by an existing branch on origin.

An issue is "claimed" if a branch matching ``origin/issue-hunter/<number>-*``
already exists on the fork.  This prevents double-slinging on re-runs before
the previous polecat finishes.

Input:  /tmp/ih-issues-uncovered.json  (from filter-prs step)
Output: /tmp/ih-issues-available.json  (issues with no existing branch claim)
"""

from __future__ import annotations

import json
import re
import subprocess
import sys
from pathlib import Path

# Repo root is one level above this script's directory.
REPO_ROOT = Path(__file__).parent.parent


def fetch_remote_branches() -> list[str]:
    """Fetch origin and return all remote branch names."""
    subprocess.run(
        ["git", "-C", str(REPO_ROOT), "fetch", "origin", "--prune"],
        capture_output=True,
    )
    result = subprocess.run(
        ["git", "-C", str(REPO_ROOT), "branch", "-r"],
        capture_output=True,
        text=True,
        check=True,
    )
    return [line.strip() for line in result.stdout.splitlines() if line.strip()]


def extract_claimed_numbers(remote_branches: list[str]) -> set[int]:
    """Return issue numbers claimed by existing issue-hunter branches."""
    claimed: set[int] = set()
    for branch in remote_branches:
        # Match: origin/issue-hunter/<number>[-...]
        m = re.match(r"origin/issue-hunter/(\d+)", branch)
        if m:
            claimed.add(int(m.group(1)))
    return claimed


def main() -> None:
    input_path = Path("/tmp/ih-issues-uncovered.json")
    if not input_path.exists():
        print("ERROR: /tmp/ih-issues-uncovered.json not found", file=sys.stderr)
        sys.exit(1)

    print("Fetching remote branches...")
    remote_branches = fetch_remote_branches()
    print(f"  Found {len(remote_branches)} remote branches")

    claimed = extract_claimed_numbers(remote_branches)
    print(f"  Claimed issue numbers: {sorted(claimed) or 'none'}")

    issues = json.loads(input_path.read_text())
    available = [i for i in issues if i["number"] not in claimed]

    output_path = Path("/tmp/ih-issues-available.json")
    output_path.write_text(json.dumps(available, indent=2))
    print(f"\nAvailable (not claimed): {len(available)} of {len(issues)}")
    print(f"Output: {output_path}")


if __name__ == "__main__":
    main()
