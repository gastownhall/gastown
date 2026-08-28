---
description: Dependency automation, lockfiles, containers, and external-input interpreters.
---

# Supply-chain changes remain intentionally delayed and least privileged

Configure a rolling dependency cooldown of at least seven days in the owning
package-manager and Dependabot configuration. Do not replace it with a fixed
date that silently expires.

Final container stages run as a dedicated non-root user and use a compatible
base with no reproducible scanner findings. Inputs entering LDAP, SQL, shell,
templates, or other interpreters use the owner's typed validation and escaping
primitive; string concatenation is forbidden.
