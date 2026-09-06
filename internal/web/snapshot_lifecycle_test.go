//go:build !windows

package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type liveSnapshotMailFixture struct {
	MockConvoyFetcher
	live *LiveConvoyFetcher
}

func (f *liveSnapshotMailFixture) WithContext(ctx context.Context) ConvoyFetcher {
	return &liveSnapshotMailFixture{live: f.live.WithContext(ctx).(*LiveConvoyFetcher)}
}
func (f *liveSnapshotMailFixture) FetchMail() ([]MailRow, error) { return f.live.FetchMail() }

func TestDashboardSnapshotCancelsLiveReadTree(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "last-client-disconnect"}[disconnect], func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bd")
			// Test-owned process tree only. A surviving shell child leaves a marker.
			script := `#!/bin/sh
(sleep 3 &
 sleeper=$!
 echo start > "$0.started"
 wait "$sleeper"
 echo survived > "$0.survived") &
wait
echo '[]'
`
			if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			f := &liveSnapshotMailFixture{live: &LiveConvoyFetcher{townRoot: dir, bdBin: bin, cmdTimeout: 5 * time.Second}}
			h, api := newSnapshotHarness(t, f)
			h.fetchTimeout = 2 * time.Second
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan string, 1)
			go func() { done <- api.computeDashboardHash(ctx) }()
			until := time.Now().Add(1500 * time.Millisecond)
			for {
				if _, err := os.Stat(bin + ".started"); err == nil {
					break
				}
				if time.Now().After(until) {
					t.Fatal("live mail read did not start")
				}
				time.Sleep(time.Millisecond)
			}
			if disconnect {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(2500 * time.Millisecond):
				t.Fatal("snapshot waiter exceeded bound")
			}
			time.Sleep(3100 * time.Millisecond)
			if _, err := os.Stat(bin + ".survived"); err == nil {
				t.Fatal("live snapshot child survived cancellation")
			}
			h.cacheMu.Lock()
			active := h.refresh != nil
			h.cacheMu.Unlock()
			if active {
				t.Fatal("cancelled snapshot did not drain")
			}
		})
	}
}

func TestDashboardSnapshotLiveReadErrorsAreNotEmptyPanels(t *testing.T) {
	for _, script := range []string{"#!/bin/sh\nexit 1\n", "#!/bin/sh\necho broken-json\n"} {
		dir := t.TempDir()
		bin := filepath.Join(dir, "bd")
		if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
		f := (&LiveConvoyFetcher{townRoot: dir, bdBin: bin, cmdTimeout: time.Second}).WithContext(context.Background()).(*LiveConvoyFetcher)
		if _, err := f.FetchHooks(); err == nil {
			t.Fatal("hook failure hidden")
		}
		if _, err := f.FetchQueues(); err == nil {
			t.Fatal("queue failure hidden")
		}
		if _, err := f.FetchEscalations(); err == nil {
			t.Fatal("escalation failure hidden")
		}
		if _, err := f.FetchIssues(); err == nil {
			t.Fatal("issue failure hidden")
		}
	}
}

type liveSnapshotTmuxFixture struct {
	MockConvoyFetcher
	live *LiveConvoyFetcher
}

func (f *liveSnapshotTmuxFixture) WithContext(ctx context.Context) ConvoyFetcher {
	return &liveSnapshotTmuxFixture{live: f.live.WithContext(ctx).(*LiveConvoyFetcher)}
}
func (f *liveSnapshotTmuxFixture) FetchWorkers() ([]WorkerRow, error) { return f.live.FetchWorkers() }
func (f *liveSnapshotTmuxFixture) FetchSessions() ([]SessionRow, error) {
	return f.live.FetchSessions()
}
func (f *liveSnapshotTmuxFixture) FetchMayor() (*MayorStatus, error) { return f.live.FetchMayor() }

func TestDashboardSnapshotTmuxFailuresRetainLivePanels(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mayor", "rigs.json"), []byte(`{"rigs":{}}`), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bd")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho '[]'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	tmuxBin := filepath.Join(dir, "tmux")
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	for _, test := range []struct {
		name, script string
		timeout      time.Duration
	}{
		{"exec failure", "#!/bin/sh\nexit 1\n", time.Second},
		{"timeout", "#!/bin/sh\nsleep 2\n", 400 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(tmuxBin, []byte(test.script), 0755); err != nil {
				t.Fatal(err)
			}
			live := (&LiveConvoyFetcher{townRoot: dir, bdBin: bin, cmdTimeout: time.Second, tmuxCmdTimeout: test.timeout}).WithContext(context.Background()).(*LiveConvoyFetcher)
			if _, err := live.FetchWorkers(); err == nil {
				t.Fatal("worker tmux failure looked empty")
			}
			if _, err := live.FetchSessions(); err == nil {
				t.Fatal("session tmux failure looked empty")
			}
			if _, err := live.FetchMayor(); err == nil {
				t.Fatal("mayor tmux failure looked detached")
			}
			h, _ := newSnapshotHarness(t, &liveSnapshotTmuxFixture{live: live})
			previous := &ConvoyData{Workers: []WorkerRow{{Name: "retained-worker"}}, Sessions: []SessionRow{{Name: "retained-session"}}, Mayor: &MayorStatus{IsAttached: true}, panelSuccess: map[string]bool{"Workers": true, "Sessions": true, "Mayor": true}}
			snapshot, drained := h.fetchSnapshot(context.Background(), previous)
			<-drained
			if len(snapshot.PanelErrors) != 3 || snapshot.Workers[0].Name != "retained-worker" || snapshot.Sessions[0].Name != "retained-session" || !snapshot.Mayor.IsAttached {
				t.Fatal("live tmux failure erased previous panels or omitted notices")
			}

		})
	}
	// Successful empty output is the confirmed-empty case.
	if err := os.WriteFile(tmuxBin, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	live := (&LiveConvoyFetcher{townRoot: dir, bdBin: bin, cmdTimeout: time.Second, tmuxCmdTimeout: time.Second}).WithContext(context.Background()).(*LiveConvoyFetcher)
	if rows, err := live.FetchWorkers(); err != nil || len(rows) != 0 {
		t.Fatalf("confirmed empty workers: %v", err)
	}
	if rows, err := live.FetchSessions(); err != nil || len(rows) != 0 {
		t.Fatalf("confirmed empty sessions: %v", err)
	}
	if mayor, err := live.FetchMayor(); err != nil || mayor == nil || mayor.IsAttached {
		t.Fatalf("confirmed absent mayor: %v", err)
	}
}
