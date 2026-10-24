// Command devseed fills a gtb server store with sample machines, items,
// artifacts and events so the dashboard can be checked by hand. Dev only:
//
//	go run ./internal/server/devseed -data /tmp/gtb-dev
//	./gtb server --listen 127.0.0.1:8080 --data /tmp/gtb-dev --public-url http://127.0.0.1:8080
//
// It never touches a store that already has machines.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"
	"github.com/steveyegge/gastown/internal/server"
)

func main() {
	data := flag.String("data", "", "server data directory (the store is <data>/gtb.db)")
	flag.Parse()
	if *data == "" {
		log.Fatal("devseed: -data is required")
	}
	st, err := server.OpenStore(filepath.Join(*data, "gtb.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	if ms, err := st.Machines(); err != nil {
		log.Fatal(err)
	} else if len(ms) > 0 {
		log.Fatalf("devseed: %s already has %d machines; use an empty directory", *data, len(ms))
	}
	if err := seed(st); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "devseed: seeded %s\n", *data)
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func seed(st *server.Store) error {
	now := time.Now().UTC()
	machines := []struct {
		id string
		h  agentlink.Hello
	}{
		{"m-laptop", agentlink.Hello{Hostname: "laptop", OS: "darwin", Arch: "arm64", GtbVersion: "0.1.0-dev", Protocol: "1"}},
		{"m-build", agentlink.Hello{Hostname: "build-box", OS: "linux", Arch: "amd64", GtbVersion: "0.1.0-dev", Protocol: "1"}},
	}
	for _, m := range machines {
		if err := st.UpsertMachine(m.id, m.h); err != nil {
			return err
		}
	}
	spec := "# Spec: widget export\n\n## Goal\nExport widgets as CSV.\n\n## Acceptance\n- `gtb widgets export` writes CSV\n- Columns: id, name, size\n\n```go\nfunc Export(w io.Writer) error\n```\n\n<script>alert('not run')</script>\n"
	plan := "# Plan\n\n1. Add the encoder\n2. Wire the command\n3. Tests\n"
	items := []agentlink.ItemView{
		{ID: "gt-101", Rig: "core", Kind: "task", Step: "spec", Title: "Export widgets as CSV", Branch: "feat/widget-export", WaitingForHuman: "approve spec", StepEnteredAt: now.Add(-20 * time.Minute),
			Artifacts: []agentlink.ArtifactView{{Name: "spec", SHA: sha(spec), Content: spec}}},
		{ID: "gt-102", Rig: "core", Kind: "task", Step: "execute", Title: "Speed up the board query", Branch: "feat/board-speed", StepEnteredAt: now.Add(-2 * time.Hour),
			Artifacts: []agentlink.ArtifactView{{Name: "plan", SHA: sha(plan), Content: plan}},
			Diffstat:  []agentlink.FileStat{{Path: "internal/server/store.go", Added: 42, Deleted: 7}, {Path: "internal/server/store_test.go", Added: 88, Deleted: 0}},
			Usage:     &agentlink.Usage{InputTokens: 182000, OutputTokens: 21400, CostUSD: 1.37}},
		{ID: "gt-110", Rig: "core", Kind: "epic", Step: "tasks", Title: "Dashboard v1", StepEnteredAt: now.Add(-24 * time.Hour)},
		{ID: "gt-111", Rig: "core", Kind: "task", Step: "review", Title: "Board page", Parent: "gt-110", Branch: "feat/board", StepEnteredAt: now.Add(-3 * time.Hour)},
		{ID: "gt-112", Rig: "core", Kind: "task", Step: "done", Title: "Login page", Parent: "gt-110", StepEnteredAt: now.Add(-30 * time.Hour)},
	}
	if err := st.ApplySnapshot("m-laptop", agentlink.Snapshot{Rigs: []agentlink.Rig{{Name: "core", Remote: "git@example.com:org/core.git", Gated: true}}, Items: items}); err != nil {
		return err
	}
	evs := []agentlink.Event{
		{ID: "0000000001", At: now.Add(-3 * time.Hour), Type: agentlink.EventItemUpsert, ItemID: "gt-102", Data: raw(agentlink.ItemView{ID: "gt-102", Rig: "core", Kind: "task", Step: "plan", Title: "Speed up the board query", StepEnteredAt: now.Add(-3 * time.Hour)})},
		{ID: "0000000002", At: now.Add(-2 * time.Hour), Type: agentlink.EventItemUpsert, ItemID: "gt-102", Data: raw(items[1])},
		{ID: "0000000003", At: now.Add(-90 * time.Minute), Type: agentlink.EventEvidence, ItemID: "gt-102", Data: raw(map[string]any{"kind": "tdd", "plan_step": 1, "phase": "red", "cmd": "go test ./internal/server", "exit": 1, "tail": "--- FAIL: TestBoardQuery (0.01s)\n    store_test.go:40: 3 queries, want 1", "at": now.Add(-90 * time.Minute)})},
		{ID: "0000000004", At: now.Add(-80 * time.Minute), Type: agentlink.EventEvidence, ItemID: "gt-102", Data: raw(map[string]any{"kind": "tdd", "plan_step": 1, "phase": "green", "cmd": "go test ./internal/server", "exit": 0, "tail": "ok  \tgithub.com/steveyegge/gastown/internal/server\t2.1s", "at": now.Add(-80 * time.Minute)})},
		{ID: "0000000005", At: now.Add(-70 * time.Minute), Type: agentlink.EventEvidence, ItemID: "gt-102", Data: raw(map[string]any{"kind": "check", "passed": true, "tail": "make check\nall checks passed", "at": now.Add(-70 * time.Minute)})},
	}
	if _, err := st.AppendEvents("m-laptop", evs); err != nil {
		return err
	}
	// The build box never reports again, so its item turns stale after the
	// server's freshness threshold (2 minutes).
	build := []agentlink.ItemView{{ID: "fx-7", Rig: "infra", Kind: "fix", Step: "rca", Title: "Flaky deploy step", StepEnteredAt: now.Add(-5 * time.Hour)}}
	return st.ApplySnapshot("m-build", agentlink.Snapshot{Rigs: []agentlink.Rig{{Name: "infra", Gated: false}}, Items: build})
}
