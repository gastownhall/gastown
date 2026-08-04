#!/usr/bin/env python3
"""USE-vs-MENTION census for `bd mol wisp gc` across gt formula files.

si-sj0j. The previous instrument (`grep -cE '^bd mol wisp gc'`) under-reported a hazard, which is
the direction nobody re-checks. Two stacked failures:

  ANCHOR    mol-witness-patrol stores its step description as a single-line TOML string with \\n
            escapes, so live commands sit MID-LINE and `^` can never fire -> whole file missed.
  grep -c   counts LINES; two commands sharing one physical line report as 1.

WHY THIS APPROACH IS DIFFERENT IN KIND, not just a better regex:

The anchor tried to infer *use vs mention* from PHYSICAL LAYOUT (does the command start a line?).
Layout is exactly what the two TOML string forms disagree about, so any layout-based rule is
defeated by a serialization choice. This parses the TOML instead — which RESOLVES both forms to the
same string — and then classifies on MARKDOWN FENCE CONTEXT, which is the structure that actually
encodes intent:

    inside a ```bash fence   -> the step is ORDERING the command      -> USE
    outside any fence        -> prose talking about it (backticked)   -> MENTION

That is the real distinction. The si-e90 warning says the step "used to run `bd mol wisp gc ...`" in
inline backticks, in prose; the hazardous formulas put it in a fenced bash block under "First, clean
up wisps". Fence membership separates them and is invariant under both TOML string forms.

Scope honesty: this classifies DECLARED steps in the file it is given. It does not resolve
`extends` inheritance (3 formulas inherit steps and declare none), and it cannot see a command
constructed at runtime. Both are stated in the output rather than left implicit.
"""
import glob
import os
import re
import sys

import tomli

CMD = re.compile(r"bd mol wisp gc[^\n`\"']*")
FENCE = re.compile(r"```[a-zA-Z0-9]*\n(.*?)```", re.S)


def strings_of(obj):
    """Every string value anywhere in the parsed document.

    Deliberately structure-agnostic: an earlier census keyed on `^id =` and got three different
    defensible denominators (47 files / 44 with ids / 37 with [[steps]]) because `id` is overloaded
    across steps, legs, template and advice tables. Walking all strings sidesteps that choice
    entirely — there is no field to pick wrong.
    """
    if isinstance(obj, str):
        yield obj
    elif isinstance(obj, dict):
        for v in obj.values():
            yield from strings_of(v)
    elif isinstance(obj, list):
        for v in obj:
            yield from strings_of(v)


def classify(path):
    """Return (uses, mentions) counts of the command in one formula file."""
    with open(path, "rb") as fh:
        try:
            doc = tomli.load(fh)
        except Exception as exc:  # a file we cannot parse must not read as clean
            return None, str(exc)

    uses = mentions = 0
    for s in strings_of(doc):
        if "bd mol wisp gc" not in s:
            continue
        fenced = "".join(FENCE.findall(s))
        total = len(CMD.findall(s))
        in_fence = len(CMD.findall(fenced))
        uses += in_fence
        mentions += total - in_fence
    return uses, mentions


def main(roots):
    files = []
    for r in roots:
        files += sorted(glob.glob(os.path.join(r, "*.formula.toml")))
    if not files:
        print("no formula files found under: %s" % ", ".join(roots))
        return 1

    hits, unparsed, inherits = [], [], []
    for f in files:
        u, m = classify(f)
        if u is None:
            unparsed.append((f, m))
            continue
        with open(f, "rb") as fh:
            raw = fh.read()
        if b"\nextends = " in raw and b"[[steps]]" not in raw:
            inherits.append(f)
        if u or m:
            hits.append((f, u, m))

    print("scanned %d formula files" % len(files))
    print()
    # Full paths, never basenames: scanning several roots produces duplicate basenames, and two
    # rows reading "mol-refinery-patrol.formula.toml" is an ambiguous census — the reader cannot
    # tell which copy carries the hazard, which is the whole question.
    width = max([len(f) for f, _, _ in hits] + [4])
    print("%-*s %5s %8s" % (width, "file", "USE", "MENTION"))
    for f, u, m in sorted(hits, key=lambda x: (-x[1], x[0])):
        print("%-*s %5d %8d" % (width, f, u, m))

    tu = sum(u for _, u, _ in hits)
    tm = sum(m for _, _, m in hits)
    carriers = [f for f, u, _ in hits if u]
    print()
    print("HAZARD: %d occurrence(s) ORDERED across %d formula(s)" % (tu, len(carriers)))
    print("        %d occurrence(s) are MENTIONS in prose (already-fixed copies)" % tm)

    if unparsed:
        print()
        print("UNPARSED — these were NOT checked, do not read as clean:")
        for f, e in unparsed:
            print("   %s: %s" % (os.path.basename(f), e))
    if inherits:
        print()
        print("INHERITS STEPS (declares none) — a content scan CANNOT see their steps;")
        print("resolve the parent before calling these clean:")
        for f in inherits:
            print("   %s" % f)

    # Exit non-zero when the hazard is ORDERED anywhere, so this can gate a build or a doctor
    # check rather than only inform a reader. Mentions never fail the gate — the already-fixed
    # copies quote these commands inside the warning that removes them, and failing on those
    # would send someone to "fix" files that already carry the fix, deleting si-e90's own evidence.
    #
    # An UNPARSED file also fails: a file we could not read must not be reported as clean, which
    # is the same under-report direction this script exists to correct.
    if unparsed:
        return 2
    return 1 if tu else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:] or ["."]))
