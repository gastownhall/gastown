---
name: security-review
description: security, review, scanner, triage, secrets, containers, dependencies, input, closure
---

# Security review

Activate for credentials, authentication, authorization, external input,
dependencies, containers, persistence, network boundaries, and scanner output.

## Procedure

1. Read project governance and discover `docs/security/*-triage.md`.
2. Run `agentsctl security-triage <repository-root>` to expose incomplete
   tracking before implementation.
3. Reproduce every finding with the project's canonical Semgrep, Snyk,
   secret-scanning, dependency, and language gates.
4. Trace the finding to its owner configuration or primitive. Correct it there,
   regenerate projections, and remove the obsolete implementation completely.
5. Record the tracker, decision, and exact command evidence in the source report, then
   rerun the scanner and runtime path.

All severities block closure. Only a technically demonstrated false positive is
a valid alternative to correction. Risk acceptance, ignores, wrappers,
fallbacks, hardcoded values, stubs, `|| true`, and success without fresh command
output are forbidden.

Credentials must flow automatically and directly from the authorized system
keyring integration. Containers must run non-root; dependency automation uses a
rolling seven-day cooldown; interpreter-bound input uses the owner's validation
and escaping primitive.
