---
name: security-triage
description: Validate and close scanner findings from project-owned security triage reports.
argument-hint: "[repository roots]"
---

# Security triage

Use each repository's `docs/security/*-triage.md` as the finding ledger. Run
`agentsctl security-triage $ARGUMENTS` before changing code, then use the
project's canonical scanner commands to reproduce every finding.

For each finding, fix the owner source, regenerate derived files, record the
tracker, decision, and reproducible evidence in the same report, and rerun the scanner.
Every severity blocks closure. A false positive requires a precise technical
explanation and a clean reproduction; risk acceptance, generic ignores,
fallbacks, `|| true`, and unevidenced suppressions do not close findings.
