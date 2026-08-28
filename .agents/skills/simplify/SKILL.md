---
name: simplify
description: simplify, refactor, readability, behavior-preserving, changed-code, cleanup
---

# Simplify

Improve recently changed code for clarity and maintainability without changing
observable behavior. Follow the active project's own instructions, language
conventions, public interfaces, and validation commands.

## Use for

- Behavior-preserving cleanup after a feature, fix, review, or writing change.
- Reducing needless complexity within an explicitly requested changed scope.

## Do not use for

- New behavior, bug diagnosis, architecture/API redesign, performance tuning,
  generated output, or broad opportunistic rewrites.
- Guessing a changed scope when neither the request nor version control proves it.

## Method

1. Establish the changed scope and its affected consumers.
2. Identify generated files and their owner; edit the owner and regenerate.
3. Run the smallest canonical behavioral check that proves the baseline. If the
   check is unavailable or already red, stop with the exact command/error.
4. Remove redundant branches, nesting, indirection, duplication, and comments
   that merely restate the code.
5. Prefer descriptive names, explicit control flow, cohesive functions, and the
   project's established abstractions over dense or clever expressions.
6. Preserve error semantics, side effects, ordering, compatibility requirements,
   public types, outputs, and performance characteristics unless the request
   explicitly changes them.
7. Revalidate affected cross-file consumers with the same canonical surface.
8. Report the simplification and the evidence that behavior was preserved.

## Boundaries

- Work only in the requested or recently changed scope unless broader cleanup is
  necessary for correctness.
- Do not replace a clear abstraction merely to reduce line count.
- Do not hide failures with suppression, fallback behavior, hardcoded results, or
  weakened tests.
- Stop and report the conflict when preserving behavior and following the active
  project contract cannot both be achieved.
- Project conventions and declared owners prevail over subjective cleanup. If
  two active contracts conflict, report both instead of choosing one.
