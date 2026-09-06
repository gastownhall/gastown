//go:build !windows

package cmd

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/web"
)

func TestDashboardReadyCLIHelper(t *testing.T) {
	if os.Getenv("GT_TEST_READY_HELPER") != "1" {
		return
	}
	if err := os.Chdir(os.Getenv("GT_TEST_READY_DIR")); err != nil {
		panic(err)
	}
	readyJSON = true
	readyRig = ""
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	if err := runReady(cmd, nil); err != nil {
		panic(err)
	}
	os.Exit(0)
}

// Exercise /api/ready -> CLI runReady -> parallel town/rig Beads.Ready.
// PATH contains only a fake bd and system shell tools; no database is used.
func TestDashboardReadyTimeoutStopsActualFanout(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"mayor", ".beads", "fixture/.beads"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"mayor/town.json": `{"name":"fixture"}`, "mayor/rigs.json": `{"rigs":{"fixture":{"git_url":"https://example.invalid/fixture.git"}}}`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{".beads", "fixture/.beads"} {
		if err := os.WriteFile(filepath.Join(dir, d, ".gt-types-configured"), []byte(beads.TypeConfigSentinelValue()), 0644); err != nil {
			t.Fatal(err)
		}
	}
	bd := `#!/bin/sh
echo "$*" >> "$GT_TEST_READY_DIR/requests"
case "$*" in
 *version*) echo 'bd version 0.60.0'; exit 0;;
 *ready*) echo started >> "$GT_TEST_READY_DIR/started"; sleep 3; echo survived >> "$GT_TEST_READY_DIR/survived";;
esac
echo '[]'
`
	if err := os.WriteFile(filepath.Join(dir, "bd"), []byte(bd), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "gt")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec \"$GT_TEST_READY_BINARY\" -test.run '^TestDashboardReadyCLIHelper$'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	t.Setenv("GT_TEST_READY_HELPER", "1")
	t.Setenv("GT_TEST_READY_DIR", dir)
	t.Setenv("GT_TEST_READY_BINARY", os.Args[0])
	t.Setenv("BEADS_DIR", filepath.Join(dir, ".beads"))
	mux, err := web.NewDashboardMuxWithOptions(nil, nil, web.DashboardOptions{GTPath: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/ready", nil).WithContext(ctx))
	}()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("ready request exceeded deadline plus pipe bound")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "started"))
	if len(strings.Fields(string(b))) != 2 {
		requests, _ := os.ReadFile(filepath.Join(dir, "requests"))
		t.Fatalf("expected actual town+rig ready fanout, got %q; requests %s", b, requests)
	}
	time.Sleep(3100 * time.Millisecond)
	if b, _ := os.ReadFile(filepath.Join(dir, "survived")); len(b) > 0 {
		t.Fatalf("actual ready descendants survived timeout: %s", b)
	}
}
