package daemon

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"
)

func agentlinkTestDaemon(t *testing.T, town string) (*Daemon, *bytes.Buffer, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	buf := &bytes.Buffer{}
	return &Daemon{config: &Config{TownRoot: town}, logger: log.New(&safeWriter{w: buf}, "", 0), ctx: ctx, cancel: cancel}, buf, cancel
}

type safeWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *safeWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func TestStartAgentLink_NoTokenDoesNothing(t *testing.T) {
	d, buf, cancel := agentlinkTestDaemon(t, t.TempDir())
	defer cancel()
	called := false
	if d.startAgentLink(func(context.Context, agentlink.LoopConfig) error { called = true; return nil }) {
		t.Fatal("started without a token")
	}
	if called || strings.Contains(buf.String(), "agentlink") {
		t.Fatalf("called=%v log=%q", called, buf.String())
	}
}

func TestStartAgentLink_DisabledByEnv(t *testing.T) {
	town := t.TempDir()
	if err := agentlink.SaveToken(town, "https://gtb.example.com", "m1", "secret-token"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GTB_AGENTLINK", "0")
	d, _, cancel := agentlinkTestDaemon(t, town)
	defer cancel()
	if d.startAgentLink(func(context.Context, agentlink.LoopConfig) error { return nil }) {
		t.Fatal("started with GTB_AGENTLINK=0")
	}
}

func TestStartAgentLink_StartsAndStopsWithDaemon(t *testing.T) {
	town := t.TempDir()
	if err := agentlink.SaveToken(town, "https://gtb.example.com", "m1", "secret-token"); err != nil {
		t.Fatal(err)
	}
	d, buf, cancel := agentlinkTestDaemon(t, town)
	got := make(chan agentlink.LoopConfig, 1)
	stopped := make(chan struct{})
	ok := d.startAgentLink(func(ctx context.Context, cfg agentlink.LoopConfig) error {
		got <- cfg
		<-ctx.Done()
		close(stopped)
		return nil
	})
	if !ok {
		t.Fatal("not started")
	}
	cfg := <-got
	if cfg.ServerURL != "https://gtb.example.com" || cfg.MachineID != "m1" || cfg.Token != "secret-token" || cfg.TownRoot != town {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.Collect == nil || cfg.Apply == nil || cfg.Scan == nil {
		t.Fatal("cfg missing Collect/Apply/Scan")
	}
	cancel() // daemon shutdown
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("loop context not cancelled on shutdown")
	}
	d.agentlinkWG.Wait()
	if !strings.Contains(buf.String(), "agentlink: starting link to https://gtb.example.com") || strings.Contains(buf.String(), "secret-token") {
		t.Fatalf("log = %q", buf.String())
	}
}
