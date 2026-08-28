---
description: Loading, storing, forwarding, or rotating credentials and tokens.
---

# Credentials come directly from the system keyring

The typed owner configuration authorizes profiles, consumers, aliases, and
workspace roots. Generated shell and service integrations obtain values from
the system Secret Service at runtime; secret values never appear in generated
environment files, units, logs, backups, command arguments, or source control.

Clear inherited credentials before loading the authorized profile. Missing or
unauthorized credentials fail closed. Do not add plaintext fallback, duplicate
stores, wrapper-only access, placeholder credentials, or silent degradation.

Non-interactive commands use `env-keyring auto-exec`: the working directory
selects the authorized profile and credentials exist only in the child process.
Callers never name a profile or read a credential file.
