# Gas Town

A Town of Rigs in which named Roles execute Beads from their Hooks.

## Language

### Places

**Town**:
The outermost workspace that contains every Rig.
_Avoid_: HQ (keep `hq-` only as the town-level Bead prefix), workspace, installation

**Rig**:
A project under Town management: one git repository and its Roles.
_Avoid_: repo, project, workspace

### Roles

**Overseer**:
The human operator of a Town.
_Avoid_: user, owner, admin

**Mayor**:
The Town-level chief-of-staff Role.

**Deacon**:
The Town-level watchdog Role.
_Avoid_: daemon (the Daemon is a different thing)

**Daemon**:
The non-agent Town process.
_Avoid_: Deacon, watchdog

**Dog**:
A Deacon helper Role for Town infrastructure, not project work.
_Avoid_: worker, crew

**Boot**:
The Dog that watches the Deacon.

**Witness**:
The per-Rig patrol Role that oversees Polecats and the Refinery.

**Refinery**:
The per-Rig merge-queue Role.

**Polecat**:
A Rig worker Role with persistent identity and ephemeral Sessions.
_Avoid_: worker (unqualified), ephemeral agent

**Crew**:
A persistent, named, Overseer-managed Rig workspace.
_Avoid_: developer, user workspace, persistent Polecat

**Agent**:
A named Role instance with durable identity.
_Avoid_: session, process, bot

**Session**:
One live runtime of an Agent.
_Avoid_: agent, run (except as the spawn-to-stop interval)

### Work

**Bead**:
The atomic tracked work record.
_Avoid_: issue, ticket, task (as names for the record itself)

**Hook**:
The Bead currently pinned to a Role as its assignment.
_Avoid_: queue, inbox, worktree, Claude Code hook

**Sling**:
The assignment of a Bead onto a Role's Hook.
_Avoid_: assign, dispatch, enqueue

**Convoy**:
A persistent Town-level tracker for a batch of related Beads.
_Avoid_: batch, swarm (as the tracker)

**Swarm**:
The ephemeral set of Agents currently on a Convoy's Beads.

**Formula**:
A reusable workflow template.
_Avoid_: playbook, runbook, protomolecule

**Molecule**:
An instantiated Formula: a chained workflow of Beads.

**Wisp**:
An ephemeral Bead, discarded after the run.
_Avoid_: temp issue, vapor

### Communication

**Mail**:
A persistent Role-to-Role message.
_Avoid_: email, inbox (unqualified)

**Nudge**:
A live-Session prompt.
_Avoid_: ping, poke, message (unqualified)

**Handoff**:
A Session end that continues the same Hook in a fresh Session.

**Seance**:
A query of predecessor Sessions.

### Propulsion

**GUPP**:
The rule that a Role with work on its Hook runs that work.
_Avoid_: propulsion principle (as a second name for the same rule)

**Patrol**:
A repeating health-and-recovery cycle for Deacon, Witness, or Refinery.

**Merge queue**:
The Refinery's ordered pipeline of Polecat merge requests.
_Avoid_: CI, merge train
