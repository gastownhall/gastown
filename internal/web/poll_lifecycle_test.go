//go:build !windows

package web

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint("deadline=", deadline), func(t *testing.T) { testDashboardMailCancellation(t, deadline) })
	}
}

func testDashboardMailCancellation(t *testing.T, useDeadline bool) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", ".gt-types-configured"), []byte(beads.TypeConfigSentinelValue()), 0644); err != nil {
		t.Fatal(err)
	}
	// A surviving bd writes a marker after the dashboard caller has timed out.
	// Allow helper/test-binary startup under macOS race instrumentation; the
	// completion marker remains strictly later than the command deadline.
	bd := `#!/bin/sh
echo started >> "$GT_TEST_MAIL_DIR/started"
sleep 4
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
	go func() { _, err := h.runGtCommand(ctx, 3*time.Second, []string{"mail", "inbox"}); done <- err }()
	deadline := time.Now().Add(2500 * time.Millisecond)
	for {
		if b, _ := os.ReadFile(filepath.Join(dir, "started")); len(strings.Fields(string(b))) >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real mail fanout did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !useDeadline {
		cancel()
	}
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected cancellation")
		}
	case <-time.After(3500 * time.Millisecond):
		t.Fatal("command cancellation blocked on descendant pipes")
	}
	time.Sleep(4100 * time.Millisecond)
	if b, _ := os.ReadFile(filepath.Join(dir, "survived")); len(b) > 0 {
		t.Fatalf("bd descendants survived dashboard cancellation: %s", b)
	}
}

func TestDashboardMuxPinsCLIExecutable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "candidate-gt")
	log := filepath.Join(dir, "calls")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho called > \"$GT_PIN_LOG\"\necho '[]'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_PIN_LOG", log)
	t.Setenv("PATH", "/usr/bin:/bin") // No installed gt is available to this fixture.
	mux, err := NewDashboardMuxWithOptions(&MockConvoyFetcher{}, nil, DashboardOptions{GTPath: path})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/mail/inbox", nil))
	if w.Code != 200 {
		t.Fatalf("request failed: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(log); err != nil {
		t.Fatal("mux did not execute its explicit candidate CLI")
	}
	live := &LiveConvoyFetcher{}
	if _, err := NewDashboardMuxWithOptions(live, nil, DashboardOptions{GTPath: path}); err != nil {
		t.Fatal(err)
	}
	if live.gtBin != path {
		t.Fatal("canonical convoy reader was not pinned to candidate CLI")
	}
}
