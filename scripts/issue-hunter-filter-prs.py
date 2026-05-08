#!/usr/bin/env python3
"""Filter GitHub issues that already have an open or recently merged PR.

Two detection methods — an issue is "covered" if either fires:
  1. closingIssuesReferences: GitHub's own linked-issue tracking (most reliable)
  2. Keyword regex in PR title/body: closes/fixes/resolves #N (informal references)

Input:  /tmp/ih-issues-raw.json       (from fetch-upstream-issues step)
Output: /tmp/ih-issues-uncovered.json (issues with no covering PR)
"""

from __future__ import annotations

import json
import re
import subprocess
import sys
from pathlib import Path

UPSTREAM_REPO = "gastownhall/gastown"
PR_LIMIT_OPEN = 200
PR_LIMIT_MERGED = 100


def fetch_prs(state: str, limit: int) -> list[dict]:
    result = subprocess.run(
        [
            "gh", "pr", "list",
            "--repo", UPSTREAM_REPO,
            "--state", state,
            "--limit", str(limit),
            "--json", "number,title,body,closingIssuesReferences",
        ],
        capture_output=True,
        text=True,
        check=True,
    )
    return json.loads(result.stdout)


def extract_keyword_refs(text: str) -> set[int]:
    return set(
        int(m)
        for m in re.findall(
            r"(?:closes?|fixes?|resolves?)\s+#(\d+)", text or "", re.I
        )
    )


def main() -> None:
    issues_path = Path("/tmp/ih-issues-raw.json")
    if not issues_path.exists():
        print("ERROR: /tmp/ih-issues-raw.json not found", file=sys.stderr)
        sys.exit(1)

    print("Fetching open PRs...")
    open_prs = fetch_prs("open", PR_LIMIT_OPEN)
    print(f"  Found {len(open_prs)} open PRs")

    print("Fetching recently merged PRs...")
    merged_prs = fetch_prs("merged", PR_LIMIT_MERGED)
    print(f"  Found {len(merged_prs)} merged PRs")

    covered: set[int] = set()
    for pr in open_prs + merged_prs:
        # Method 1: GitHub's own closingIssuesReferences (most reliable)
        for ref in pr.get("closingIssuesReferences") or []:
            covered.add(ref["number"])
        # Method 2: keyword regex fallback (catches informal references)
        covered |= extract_keyword_refs(pr.get("body") or "")
        covered |= extract_keyword_refs(pr.get("title") or "")

    issues = json.loads(issues_path.read_text())
    uncovered = [i for i in issues if i["number"] not in covered]

    output_path = Path("/tmp/ih-issues-uncovered.json")
    output_path.write_text(json.dumps(uncovered, indent=2))
    print(f"\nCovered by a PR: {len(covered)} issue(s)")
    print(f"Uncovered (candidates): {len(uncovered)} of {len(issues)}")
    print(f"Output: {output_path}")


if __name__ == "__main__":
    main()
