# Technology Stack

Gas Town is a Go CLI application. The module is
`github.com/steveyegge/gastown`, with command entry points under `cmd/` and most
implementation under `internal/`.

Core technologies:

- Go 1.26.x as declared in `go.mod`
- Cobra for CLI command wiring
- tmux for agent session orchestration
- Beads (`bd`) for git-native issue and work tracking
- Dolt for durable shared Beads storage
- Docker and Docker Compose for sandboxed local execution
- Embedded templates for runtime hooks, slash commands, formulas, and setup
  files

Common local commands:

```bash
make build
go test ./...
go test ./internal/config/... ./internal/crew/... ./internal/hooks/... -count=1
```

Use the repository's container or devcontainer for dependency installation,
builds, tests, and long-running commands when available. Avoid host-level
package changes during development.

Runtime preset work usually starts in `internal/config/agents.go`, with tests in
`internal/config/agents_test.go`. Hook-capable runtimes may also need templates
under `internal/hooks/templates/` plus installer or sync test coverage.

