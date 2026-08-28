---
description: Running security scanners or closing findings from security reports.
---

# Every reproducible security finding blocks closure

Project-owned `docs/security/*-triage.md` files are the finding ledger. Each
finding requires the active canonical tracker, a decision, owner-source correction or a
technically proven false positive, reproducible evidence, and a clean scanner
rerun. This applies to every severity.

Never close via risk acceptance, generic ignore files, `nosemgrep`, `|| true`,
exit-code suppression, vulnerable old/new coexistence, or an unverified base
image change. Validate declared language compatibility before classifying a
compatibility finding.
