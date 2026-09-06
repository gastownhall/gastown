//go:build !windows

package web

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/mail"
)

// This child runs the real mail fanout; every bd executable is a local fixture.
func TestDashboardMailHelper(t *testing.T) {
	if os.Getenv("GT_TEST_MAIL_HELPER") != "1" {
		return
	}
	dir := os.Getenv("GT_TEST_MAIL_DIR")
	_, err := mail.NewMailboxWithBeadsDir("mayor/", dir, filepath.Join(dir, ".beads")).List()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestDashboardCancellationStopsMailFanout(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", ".gt-types-configured"), []byte(beads.TypeConfigSentinelValue()), 0644); err != nil {
		t.Fatal(err)
	}
	// A surviving bd writes a marker after the dashboard caller has timed out.
	bd := `#!/bin/sh
echo started >> "$GT_TEST_MAIL_DIR/started"
sleep 0.6
echo survived >> "$GT_TEST_MAIL_DIR/survived"
echo '[]'
`
	if err := os.WriteFile(filepath.Join(dir, "bd"), []byte(bd), 0755); err != nil {
		t.Fatal(err)
	}
	gt := `#!/bin/sh
exec "$GT_TEST_MAIL_BINARY" -test.run '^TestDashboardMailHelper$'
`
	path := filepath.Join(dir, "gt")
	if err := os.WriteFile(path, []byte(gt), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	t.Setenv("GT_TEST_MAIL_HELPER", "1")
	t.Setenv("GT_TEST_MAIL_DIR", dir)
	t.Setenv("GT_TEST_MAIL_BINARY", os.Args[0])
	h := NewAPIHandler(time.Second, time.Second, "test-token")
	h.gtPath = path
	h.workDir = dir
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := h.runGtCommand(ctx, 2*time.Second, []string{"mail", "inbox"}); done <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		if b, _ := os.ReadFile(filepath.Join(dir, "started")); len(strings.Fields(string(b))) >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real mail fanout did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected cancellation")
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("command cancellation blocked on descendant pipes")
	}
	time.Sleep(700 * time.Millisecond)
	if b, _ := os.ReadFile(filepath.Join(dir, "survived")); len(b) > 0 {
		t.Fatalf("bd descendants survived dashboard cancellation: %s", b)
	}
}

func TestDashboardFailedPollIsCoalesced(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	path := filepath.Join(dir, "gt")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho call >> \"$GT_POLL_LOG\"\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_POLL_LOG", log)
	h := NewAPIHandler(time.Second, time.Second, "test-token")
	h.gtPath = path
	h.workDir = dir
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() { defer wg.Done(); h.computeDashboardHash(context.Background()) }()
	}
	wg.Wait()
	b, _ := os.ReadFile(log)
	if n := len(strings.Fields(string(b))); n != 3 {
		t.Fatalf("failed poll amplified into %d commands, want 3", n)
	}
}
