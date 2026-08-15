# Pi runtime

Gas Town can run Pi for every role, use different Pi models by role, and set
role-specific thinking effort. Pi model profiles are defined once and then
assigned to roles by name.

## Prerequisites

Verify that Pi is installed and that at least one model is ready:

```bash
pi --version
pi --list-models
```

Gas Town does not manage provider credentials. Pi continues to read its normal
authentication and model settings from `~/.pi/agent/`.

## Set Pi as the default runtime

The built-in `pi` profile uses Pi's currently selected model:

```bash
cd ~/gt
gt config default-agent pi
```

The built-in profile installs and explicitly loads the Gas Town lifecycle
extension, records Pi session IDs, and resumes interrupted sessions with
`pi --session`. Gas Town also passes Pi's `--approve` flag so autonomous role
startup does not stop at the project-trust prompt. These lifecycle arguments
remain active when a custom model profile supplies its own arguments.

## Create reusable model profiles

Pin models by creating named agent profiles. The examples below use one remote
model and one local model; any model shown by `pi --list-models` can be used.

```bash
gt config agent set pi-luna \
  "pi --model openai-codex/gpt-5.6-luna" \
  --provider pi

gt config agent set pi-local \
  "pi --model ollama/gemma4:latest" \
  --provider pi
```

Profiles keep model identifiers in one place. Updating a profile changes every
role assigned to that profile when its next session starts.

## Assign models and thinking effort by role

Use `gt config role set <role> <profile> [effort]` for town-wide assignments:

```bash
gt config role set mayor pi-luna high
gt config role set deacon pi-local
gt config role set witness pi-local
gt config role set refinery pi-luna medium
gt config role set polecat pi-luna high
gt config role set crew pi-luna max
```

Pi supports `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, and `max`. Gas
Town passes the selected value to Pi as `--thinking <effort>`. Other managed
runtimes accept `low`, `medium`, `high`, and `max`. Validation follows the
selected profile, and a role setting overrides a `--thinking` value included
in the underlying profile. When a model reports no thinking capability, omit
the effort as shown for `pi-local`; Pi otherwise clamps the request to `off`.

Inspect the effective town-wide map:

```bash
gt config role list
```

Clear a role assignment and return it to the town defaults:

```bash
gt config role unset witness
```

Changes apply to new sessions. Restart or hand off an already-running role to
apply a changed model or effort.

## Override a single rig

Rig settings take precedence over town settings. Set the profile and effort in
one validated command:

```bash
gt rig role set sample polecat pi-local
gt rig role set sample witness pi-luna xhigh
gt rig role list sample
```

Remove the rig override to return to the town assignment:

```bash
gt rig role unset sample polecat
```

The lower-level `gt rig settings set` interface remains available and now
rejects unknown role names, unknown profiles, and effort levels unsupported by
the selected runtime.

## Container setup

The repository `Dockerfile` includes Pi but contains no credentials. Build the
image normally:

```bash
docker build -t gastown-pi .
```

Provide Pi settings at runtime. A writable mount is required because Pi records
session state alongside its settings:

```bash
docker run --rm -it \
  -v "$HOME/.pi/agent:/home/agent/.pi/agent" \
  -v gastown-pi-hq:/gt \
  -e GIT_USER="Container User" \
  -e GIT_EMAIL="container@example.invalid" \
  gastown-pi
```

The mount exposes provider credentials to the container. Use only a locally
built, trusted image, and do not copy `auth.json` into an image layer or commit
it to source control.

Inside the container:

```bash
cd /gt
pi --list-models
gt config agent set pi-luna \
  "pi --model openai-codex/gpt-5.6-luna" \
  --provider pi
gt config role set mayor pi-luna low
gt config role list
gt doctor --fix
gt mayor start
```

To smoke-test provider authentication without starting a Gas Town role:

```bash
pi --model openai-codex/gpt-5.6-luna \
  --thinking low \
  --no-session \
  -p "Reply with exactly: PI_CONTAINER_OK"
```

### Container acceptance checklist

The following sequence verifies configuration persistence, the authenticated
model path, role startup, lifecycle hooks, and inter-agent input. Run it in the
container after configuring `pi-luna` as shown above:

```bash
# Confirm the selected model is authenticated.
pi --model openai-codex/gpt-5.6-luna \
  --thinking low \
  --no-session \
  -p "Reply with exactly: PI_CONTAINER_OK"

# Confirm invalid effort is rejected and the valid setting remains.
gt config role set mayor pi-luna low
gt config role set mayor pi-luna extreme || true
gt config role list

# Start the town and exercise the live Pi session.
gt start
gt status
gt nudge mayor "Reply with exactly: PI_NUDGE_OK" --mode=immediate
gt mayor attach
```

Expected results:

- The direct model call prints `PI_CONTAINER_OK`.
- The invalid effort command reports the accepted values and does not replace
  the existing `low` setting.
- `gt status` shows Mayor running with the configured Pi profile.
- The Mayor session starts without a trust prompt, processes its startup hook,
  and replies `PI_NUDGE_OK` at the configured model and effort.

Restart the container and rerun `gt config role list` to confirm that the HQ
and Pi settings mounts are writable and persistent.

## Troubleshooting

- `agent ... not found`: create the profile with `gt config agent set`, or use
  the built-in `pi` profile.
- `pi: command not found`: install Pi on the host, or rebuild the repository
  image so it includes the configured `PI_VERSION`.
- A role still uses its previous model: restart or hand off the existing
  session; configuration is resolved when a session starts.
- A local Ollama model does not connect from a container: expose the Ollama
  service to the container and configure Pi to use the reachable host address.
