+++
name = "dolt-backup"
description = "Smart Dolt database backup with change detection"
version = 2

[gate]
type = "cooldown"
duration = "15m"

[tracking]
labels = ["plugin:dolt-backup", "category:data-safety"]
digest = true

[execution]
timeout = "5m"
notify_on_failure = true
severity = "high"
+++

# Dolt Backup

Syncs production Dolt databases to filesystem backups via `dolt backup sync`.
Executed via `run.sh` — no AI interpretation.

## What it does

1. Discover production DBs from the rig registry (routes.jsonl + per-rig
   metadata.json `dolt_database`), skipping any `.dolt` dir not registered
2. For each registered DB: compare HEAD hash against last backup
3. Skip unchanged databases
4. Run `dolt backup sync` for changed databases
5. Only escalate when actual backup operations fail (FAILED > 0)

## Usage

```bash
./run.sh                          # Normal execution
./run.sh --dry-run                # Report without syncing
./run.sh --databases hq,beads    # Specific databases only
```
