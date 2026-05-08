#!/usr/bin/env python3
"""Sling one worker polecat per selected issue.

Reads /tmp/ih-issues-selected.json and calls gt sling mol-issue-hunter-work
for each issue. Must be run by a role that can sling (e.g., witness, mayor).

Usage:
    python3 scripts/issue-hunter-sling-workers.py \
        --rig gastown \
        --upstream-repo gastownhall/gastown \
        --fork-repo esciara/gastown
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rig", default="gastown")
    parser.add_argument("--upstream-repo", default="gastownhall/gastown")
    parser.add_argument("--fork-repo", default="esciara/gastown")
    parser.add_argument(
        "--input", default="/tmp/ih-issues-selected.json",
        help="Path to selected issues JSON",
    )
    args = parser.parse_args()

    input_path = Path(args.input)
    if not input_path.exists():
        print(f"ERROR: {input_path} not found", file=sys.stderr)
        sys.exit(1)

    issues = json.loads(input_path.read_text())
    if not issues:
        print("No issues to sling. Exiting cleanly.")
        sys.exit(0)

    errors = []
    for issue in issues:
        num = issue["number"]
        title = issue["title"][:50].replace('"', '\\"')
        print(f"Slinging worker for issue #{num}: {title}")
        result = subprocess.run(
            [
                "gt", "sling", "mol-issue-hunter-work", args.rig,
                "--var", f"github_issue={num}",
                "--var", f"upstream_repo={args.upstream_repo}",
                "--var", f"fork_repo={args.fork_repo}",
                "--args", f"Contribute a fix for upstream issue #{num}: {title}",
            ],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            msg = f"  ERROR slinging #{num}: {result.stderr.strip()}"
            print(msg)
            errors.append(msg)
        else:
            print(f"  OK: {result.stdout.strip()}")

    if errors:
        print(f"\n{len(errors)} error(s) encountered.", file=sys.stderr)
        sys.exit(1)
    else:
        print(f"\nAll {len(issues)} worker(s) slung successfully.")


if __name__ == "__main__":
    main()
