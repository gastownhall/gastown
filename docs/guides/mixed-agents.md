# Mixed agent types

Gas Town can run more than one agent runtime in the same town. A common
mix is Codex for workers and Pi for Mayor. Session start, hooks, mail,
and nudge resolve the agent for that role or crew worker. There is no
town-wide lock to a single provider.

## Custom Codex aliases

`gt config agent set` can encode a model or thinking flag on a Codex
alias. Gas Town still injects the built-in non-interactive flags
(`--dangerously-bypass-approvals-and-sandbox` and
`-c check_for_update_on_startup=false`) so `gt up` does not block on the
workspace trust prompt. Set `--sandbox` or `--ask-for-approval` yourself
to opt out of the bypass.

```bash
gt config agent set codex-cheap "codex -m gpt-5.3-codex-spark" --provider codex
gt config mix default=codex-cheap mayor=pi:high
```

## One-command mix

```bash
gt config mix default=codex mayor=pi:high crew=codex polecat=codex
gt config mix
```

Each argument is `target=agent`. Role targets may add `:effort`. Named
crew workers use the `crew:name` form:

```
default=codex
mayor=pi
mayor=pi:high
crew=codex
crew:alice=pi
```

`gt config mix` with no arguments prints the effective table: role,
agent, provider, effort, source, crew overrides, and whether each
binary is on PATH.

## What the mix writes

The command updates town settings in one save:

- `default_agent` from `default=…`
- `role_agents` and optional `role_effort` from role assignments
- `crew_agents` from `crew:name=…`

A later `gt config cost-tier` preset overwrites `role_agents`. Use
`gt config mix` or `gt config role set` after a tier change if you want
a custom mix.

If you switch a role to a new agent without `:effort`, and the stored
effort is not valid for that agent, the mix command clears the effort
instead of rejecting the assignment.

## Apply the mix to running sessions

Configuration is read when a session starts. Restart or hand off an
already-running role:

```bash
gt mayor restart
gt deacon restart
```

Or override one spawn without changing the town mix:

```bash
gt sling gt-abc myproject --agent pi
gt start crew alice --agent codex
```

## Per-role and per-rig commands

The mix command is the comfortable path. These still work:

```bash
gt config role set mayor pi high
gt config role list
gt rig role set myrig polecat codex
gt config mix crew:alice=pi
```

See [Pi runtime](../PI.md) for Pi model profiles and thinking effort.
