package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func countingGtStub(t *testing.T) (gtPath, logPath string) {
	t.Helper()
	binDir := t.TempDir()
	gtPath = filepath.Join(binDir, "gt")
	logPath = filepath.Join(binDir, "gt-invocations.log")
	script := `#!/usr/bin/env sh
printf '%s %s\n' "$(date +%s.%N)" "$*" >> "$GT_INVOCATION_LOG"
case "$*" in
  "status --json") printf '{"agents":[]}\n' ;;
  "hooks list") printf 'hook-a\n' ;;
  "mail inbox") printf '0 messages\n' ;;
  *) printf 'ok\n' ;;
esac
exit 0
`
	if err := os.WriteFile(gtPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gt: %v", err)
	}
	t.Setenv("GT_INVOCATION_LOG", logPath)
	return gtPath, logPath
}

func countGtSpawns(t *testing.T, logPath string) (int, string) {
	t.Helper()
	body, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ""
		}
		t.Fatalf("read invocation log: %v", err)
	}
	spawns := 0
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			spawns++
		}
	}
	return spawns, strings.TrimSpace(string(body))
}

func newStormAPIHandler(t *testing.T, gtPath string) *APIHandler {
	t.Helper()
	return &APIHandler{
		gtPath:            gtPath,
		workDir:           t.TempDir(),
		defaultRunTimeout: 5 * time.Second,
		maxRunTimeout:     10 * time.Second,
		cmdSem:            make(chan struct{}, maxConcurrentCommands),
	}
}

// TestSSEIdleConnectionDoesNotStormGt is the dashboard process-storm feedback loop.
//
// User symptom (gastownhall/gastown#2618, #3396, originally #1760): leaving the
// dashboard tab open hammers gt/bd/Dolt because /api/events polls by spawning
// subprocesses. An idle EventSource — no user actions, just the live connection
// the browser opens — must not do that.
//
// Budget: at most one hash sample (3 gt invocations) in a 5s window. That is
// the 10s poll that landed in #1805. The reported storm is 3 subprocesses every
// 2s (~6+ invocations in this window).
func TestSSEIdleConnectionDoesNotStormGt(t *testing.T) {
	gtPath, logPath := countingGtStub(t)
	h := newStormAPIHandler(t, gtPath)

	const observe = 5 * time.Second
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	ctx, cancel := context.WithTimeout(req.Context(), observe)
	defer cancel()
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	h.handleSSE(w, req)

	spawns, logBody := countGtSpawns(t, logPath)
	const maxSpawns = 3
	if spawns > maxSpawns {
		t.Errorf("idle SSE connection spawned %d gt subprocesses in %s (log=%q); want <= %d. "+
			"This is the dashboard process storm: /api/events is polling by exec'ing gt.",
			spawns, observe, logBody, maxSpawns)
	}
}

// TestComputeDashboardHashStableForIdenticalState locks the HTMX amplification:
// if the hash of identical gt output changes between polls, every tick emits
// dashboard-update and the browser refetches the full page (14 bd/gt fetchers).
//
// The stub flips completion order between polls so an append-order hash is
// guaranteed to change even though the command output is identical.
func TestComputeDashboardHashStableForIdenticalState(t *testing.T) {
	binDir := t.TempDir()
	gtPath := filepath.Join(binDir, "gt")
	flagPath := filepath.Join(binDir, "second-poll")
	script := `#!/usr/bin/env sh
case "$*" in
  "status --json")
    if [ -f "$GT_HASH_SECOND_POLL" ]; then sleep 0.05; fi
    printf '{"agents":[]}\n'
    ;;
  "hooks list")
    if [ ! -f "$GT_HASH_SECOND_POLL" ]; then sleep 0.05; fi
    printf 'hook-a\n'
    ;;
  "mail inbox")
    printf '0 messages\n'
    ;;
  *) printf 'ok\n' ;;
esac
exit 0
`
	if err := os.WriteFile(gtPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gt: %v", err)
	}
	t.Setenv("GT_HASH_SECOND_POLL", flagPath)

	h := newStormAPIHandler(t, gtPath)
	ctx := context.Background()
	first := h.computeDashboardHash(ctx)
	if first == "" {
		t.Fatal("computeDashboardHash returned empty hash")
	}
	if err := os.WriteFile(flagPath, []byte("1"), 0o644); err != nil {
		t.Fatalf("write second-poll flag: %v", err)
	}
	second := h.computeDashboardHash(ctx)
	if second != first {
		t.Fatalf("computeDashboardHash changed from %q to %q for identical gt output (completion order differed)", first, second)
	}
}
