# Project Structure

Important top-level paths:

- `cmd/` - Go entry points for binaries such as `gt`
- `internal/cmd/` - Cobra command implementations
- `internal/config/` - settings, runtime presets, environment construction, and
  startup command generation
- `internal/hooks/` - hook configuration parsing, merge logic, installers, and
  embedded templates for supported agent runtimes
- `internal/crew/`, `internal/polecat/`, `internal/witness/`,
  `internal/refinery/` - role-specific lifecycle and orchestration code
- `internal/templates/` - embedded command and town-root templates
- `docs/` - design notes, operational guides, and provider integration docs
- `.beads/` - git-backed issue data used by Beads
- `.cursor/` and `.kiro/` - runtime-specific onboarding and steering files

Runtime integration pattern:

1. Add or update the built-in preset in `internal/config/agents.go`.
2. Ensure default command, args, process names, resume style, prompt mode, and
   readiness behavior are covered in `internal/config/agents_test.go`.
3. If the runtime has executable hooks, add templates under
   `internal/hooks/templates/<provider>/` and cover install/sync behavior.
4. Update README or docs when users need new setup commands or prerequisites.

Keep changes narrow. Prefer extending the preset registry over adding new
provider-specific switch statements.

