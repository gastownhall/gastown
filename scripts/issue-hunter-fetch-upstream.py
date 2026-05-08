#!/usr/bin/env python3
"""Fetch open issues from the upstream GitHub repo.

Queries the upstream repo for all open issues that are NOT pull requests and
writes them to /tmp/ih-issues-raw.json for the subsequent pipeline steps.

Note: ``gh issue list`` uses GitHub's search API with ``type:issue``, which
excludes pull requests by design.

Output: /tmp/ih-issues-raw.json  (list of open issues, no PRs)
"""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

UPSTREAM_REPO = "gastownhall/gastown"
ISSUE_LIMIT = 500
ISSUE_FIELDS = "number,title,body,labels,author,url"


def fetch_issues(repo: str, limit: int) -> list[dict]:
    result = subprocess.run(
        [
            "gh", "issue", "list",
            "--repo", repo,
            "--state", "open",
            "--limit", str(limit),
            "--json", ISSUE_FIELDS,
        ],
        capture_output=True,
        text=True,
        check=True,
    )
    return json.loads(result.stdout)


def main() -> None:
    print(f"Fetching open issues from {UPSTREAM_REPO}...")
    try:
        issues = fetch_issues(UPSTREAM_REPO, ISSUE_LIMIT)
    except subprocess.CalledProcessError as e:
        print(f"ERROR: gh issue list failed:\n{e.stderr}", file=sys.stderr)
        sys.exit(1)

    print(f"  Fetched {len(issues)} open issues")

    output_path = Path("/tmp/ih-issues-raw.json")
    output_path.write_text(json.dumps(issues, indent=2))
    print(f"Output: {output_path}")


if __name__ == "__main__":
    main()
