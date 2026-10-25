package opencodeserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestStateRoundTripAndActiveCheck(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		if user != "opencode" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"healthy":true,"version":"1.18.16"}`))
	}))
	defer server.Close()

	townRoot := t.TempDir()
	want := State{
		GasTownSession:  "gt-rig/polecats/worker",
		OpenCodeSession: "ses_test",
		Directory:       "/worktree",
		URL:             server.URL,
		Username:        "opencode",
		Password:        "secret",
		PID:             123,
		Version:         "1.18.16",
		CreatedAt:       time.Now().UTC().Truncate(time.Second),
	}
	if err := SaveState(townRoot, want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	got, err := LoadState(townRoot, want.GasTownSession)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.OpenCodeSession != want.OpenCodeSession || got.Password != want.Password || got.GasTownSession != want.GasTownSession {
		t.Fatalf("LoadState = %#v, want %#v", got, want)
	}
	info, err := os.Stat(StatePath(townRoot, want.GasTownSession))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatalf("state permissions = %o, want no group/other access", info.Mode().Perm())
	}

	release, err := AcquireSessionLock(townRoot, want.GasTownSession)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	active, ok := ActiveState(context.Background(), townRoot, want.GasTownSession)
	if !ok || active.OpenCodeSession != want.OpenCodeSession {
		t.Fatalf("ActiveState = %#v, %v", active, ok)
	}

	if err := RemoveState(townRoot, want.GasTownSession, "different_session"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(townRoot, want.GasTownSession); err != nil {
		t.Fatalf("mismatched RemoveState removed mapping: %v", err)
	}
	if err := RemoveState(townRoot, want.GasTownSession, want.OpenCodeSession); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(townRoot, want.GasTownSession); !os.IsNotExist(err) {
		t.Fatalf("LoadState after remove = %v, want not exists", err)
	}
}

func TestActiveStateRejectsHealthyServerWithoutWorkerLock(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"healthy":true,"version":"1.18.16"}`))
	}))
	defer server.Close()

	townRoot := t.TempDir()
	state := State{
		GasTownSession:  "gt-rig-worker",
		OpenCodeSession: "ses_test",
		Directory:       t.TempDir(),
		URL:             server.URL,
		Username:        "opencode",
		Password:        "secret",
	}
	if err := SaveState(townRoot, state); err != nil {
		t.Fatal(err)
	}
	if _, ok := ActiveState(context.Background(), townRoot, state.GasTownSession); ok {
		t.Fatal("ActiveState reported server without worker ownership as active")
	}
}

func TestActiveStateRejectsStaleServer(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	state := State{
		GasTownSession:  "gt-rig-worker",
		OpenCodeSession: "ses_test",
		Directory:       t.TempDir(),
		URL:             "http://127.0.0.1:1",
		Username:        "opencode",
		Password:        "secret",
	}
	if err := SaveState(townRoot, state); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireSessionLock(townRoot, state.GasTownSession)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, ok := ActiveState(ctx, townRoot, state.GasTownSession); ok {
		t.Fatal("ActiveState reported stale server as active")
	}
}

func TestActiveStateRejectsNonLoopbackURL(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	state := State{
		GasTownSession:  "gt-rig-worker",
		OpenCodeSession: "ses_test",
		Directory:       t.TempDir(),
		URL:             "http://example.com",
		Username:        "opencode",
		Password:        "secret",
	}
	if err := SaveState(townRoot, state); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireSessionLock(townRoot, state.GasTownSession)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, ok := ActiveState(context.Background(), townRoot, state.GasTownSession); ok {
		t.Fatal("ActiveState accepted a non-loopback server URL")
	}
}
