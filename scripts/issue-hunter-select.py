#!/usr/bin/env python3
"""Select and rank up to max_issues issues by priority.

Selection rules:
  1. Issues authored by PREFER_AUTHOR come first.
  2. Within each author group, sort by priority label: p0 > p1 > p2 > p3 > none.
  3. Take the top IH_MAX_ISSUES (default 5, override via env var).

Input:  /tmp/ih-issues-available.json  (from filter-claimed step)
Output: /tmp/ih-issues-selected.json   (ranked selection ready for slinging)
"""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path

PREFER_AUTHOR = "esciara"
PRIORITY_ORDER = {"priority/p0": 0, "priority/p1": 1, "priority/p2": 2, "priority/p3": 3}
DEFAULT_MAX = 5


def priority_key(issue: dict) -> tuple[int, int, int]:
    labels = [lbl["name"] for lbl in issue.get("labels", [])]
    p = min((PRIORITY_ORDER[l] for l in labels if l in PRIORITY_ORDER), default=99)
    authored = 0 if issue.get("author", {}).get("login") == PREFER_AUTHOR else 1
    return (authored, p, issue["number"])


def main() -> None:
    input_path = Path("/tmp/ih-issues-available.json")
    if not input_path.exists():
        print("ERROR: /tmp/ih-issues-available.json not found", file=sys.stderr)
        sys.exit(1)

    try:
        max_issues = int(os.environ.get("IH_MAX_ISSUES", DEFAULT_MAX))
    except ValueError:
        print("ERROR: IH_MAX_ISSUES must be an integer", file=sys.stderr)
        sys.exit(1)

    issues = json.loads(input_path.read_text())
    ranked = sorted(issues, key=priority_key)
    selected = ranked[:max_issues]

    output_path = Path("/tmp/ih-issues-selected.json")
    output_path.write_text(json.dumps(selected, indent=2))

    if not selected:
        print("No issues available — nothing to sling.")
    else:
        print(f"Selected {len(selected)} issue(s) (max={max_issues}):")
        for issue in selected:
            labels = [lbl["name"] for lbl in issue.get("labels", [])]
            print(
                f"  #{issue['number']} [{', '.join(labels) or 'no labels'}]"
                f" {issue['title'][:60]}"
            )
    print(f"Output: {output_path}")


if __name__ == "__main__":
    main()
