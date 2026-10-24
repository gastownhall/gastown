package cmd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/server"
)

func setServerFlags(t *testing.T, data, publicURL string, rotate bool) {
	t.Helper()
	oldData, oldURL, oldRotate := serverData, serverPublicURL, serverRotate
	serverData, serverPublicURL, serverRotate = data, publicURL, rotate
	t.Cleanup(func() { serverData, serverPublicURL, serverRotate = oldData, oldURL, oldRotate })
}

func runAdminToken(t *testing.T) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&buf)
	c.SetErr(&buf)
	err := runServerAdminToken(c, nil)
	return buf.String(), err
}

func TestServerCmd_Registered(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"server", "admin-token"})
	if err != nil || c.Name() != "admin-token" {
		t.Fatalf("server admin-token not registered: %v", err)
	}
	if serverCmd.GroupID != GroupServices {
		t.Fatalf("server group = %q", serverCmd.GroupID)
	}
	for _, f := range []string{"listen", "data", "public-url"} {
		if serverCmd.Flags().Lookup(f) == nil && serverCmd.PersistentFlags().Lookup(f) == nil {
			t.Fatalf("flag --%s missing", f)
		}
	}
	if !beadsExemptCommands["server"] || !branchCheckExemptCommands["server"] {
		t.Fatal("server must be exempt from beads and branch checks")
	}
}

func TestServerAdminToken_StatusAndRotate(t *testing.T) {
	data := t.TempDir()
	setServerFlags(t, data, "https://gtb.example.com", false)

	out, err := runAdminToken(t)
	if err != nil || !strings.Contains(out, "no admin token is set") {
		t.Fatalf("no token: %q, %v", out, err)
	}

	st, err := server.OpenStore(filepath.Join(data, "gtb.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.EnsureAdminToken(st, strings.Repeat("e", 40), "", &bytes.Buffer{}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	app := server.NewApp(server.Config{Store: st})
	login := func(tok string) int {
		req, _ := http.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"token":"`+tok+`"}`))
		req.RemoteAddr = "192.0.2.1:1"
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := login(strings.Repeat("e", 40)); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	st.Close()

	out, err = runAdminToken(t)
	if err != nil || !strings.Contains(out, "an admin token is set (created ") || !strings.Contains(out, "use --rotate to replace it") {
		t.Fatalf("status: %q, %v", out, err)
	}
	if strings.Contains(out, strings.Repeat("e", 40)) {
		t.Fatal("status printed the token")
	}

	setServerFlags(t, data, "https://gtb.example.com", true)
	out, err = runAdminToken(t)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 5 || lines[0] != strings.Repeat("=", 64) || lines[3] != "login at https://gtb.example.com/login" {
		t.Fatalf("rotate output:\n%s", out)
	}
	newTok := lines[2]

	st, err = server.OpenStore(filepath.Join(data, "gtb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	app = server.NewApp(server.Config{Store: st})
	if code := login(strings.Repeat("e", 40)); code != http.StatusUnauthorized {
		t.Fatalf("old token after rotate: %d", code)
	}
	if code := login(newTok); code != http.StatusOK {
		t.Fatalf("new token: %d", code)
	}
}

func TestServerRun_ShortEnvTokenRefused(t *testing.T) {
	setServerFlags(t, t.TempDir(), "http://127.0.0.1:0", false)
	t.Setenv("GTB_ADMIN_TOKEN", "short")
	oldListen := serverListen
	serverListen = "127.0.0.1:0"
	t.Cleanup(func() { serverListen = oldListen })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var buf bytes.Buffer
	err := runServerCtx(ctx, &buf, &buf)
	if err == nil || !strings.Contains(err.Error(), "GTB_ADMIN_TOKEN") {
		t.Fatalf("err = %v", err)
	}
}

func TestServerRun_InvalidTrustedProxyRefused(t *testing.T) {
	setServerFlags(t, t.TempDir(), "http://127.0.0.1", false)
	t.Setenv("GTB_ADMIN_TOKEN", "")
	oldListen, oldTrusted := serverListen, serverTrusted
	serverListen = "127.0.0.1:0"
	t.Cleanup(func() { serverListen, serverTrusted = oldListen, oldTrusted })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverTrusted = []string{"10.0.0.0/8", "10.0.0.0/99"}
	t.Setenv("GTB_TRUSTED_PROXIES", "")
	var buf bytes.Buffer
	if err := runServerCtx(ctx, &buf, &buf); err == nil || !strings.Contains(err.Error(), "10.0.0.0/99") {
		t.Fatalf("flag: err = %v", err)
	}
	serverTrusted = nil
	t.Setenv("GTB_TRUSTED_PROXIES", "10.0.0.0/8, bogus")
	if err := runServerCtx(ctx, &buf, &buf); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("env: err = %v", err)
	}
	if strings.Contains(buf.String(), "admin token") {
		t.Fatal("token printed before refusing")
	}
}

func TestServerTrustedProxies_FlagAndEnv(t *testing.T) {
	old := serverTrusted
	t.Cleanup(func() { serverTrusted = old })
	serverTrusted = []string{"10.0.0.0/8"}
	t.Setenv("GTB_TRUSTED_PROXIES", "192.168.0.0/16,fd00::/8")
	ps, err := trustedProxies()
	if err != nil || len(ps) != 3 {
		t.Fatalf("ps = %v, %v", ps, err)
	}
	serverTrusted = nil
	t.Setenv("GTB_TRUSTED_PROXIES", "")
	if ps, err := trustedProxies(); err != nil || len(ps) != 0 {
		t.Fatalf("default: %v, %v", ps, err)
	}
}

func TestServerRun_ServesHealthAndStops(t *testing.T) {
	setServerFlags(t, t.TempDir(), "http://127.0.0.1", false)
	t.Setenv("GTB_ADMIN_TOKEN", "")
	oldListen := serverListen
	serverListen = "127.0.0.1:0"
	t.Cleanup(func() { serverListen = oldListen })

	ready := make(chan string, 1)
	serverReady = func(addr string) { ready <- addr }
	t.Cleanup(func() { serverReady = nil })

	ctx, cancel := context.WithCancel(context.Background())
	var out, logs syncBuffer
	errc := make(chan error, 1)
	go func() { errc <- runServerCtx(ctx, &out, &logs) }()
	var addr string
	select {
	case addr = <-ready:
	case err := <-errc:
		t.Fatalf("server exited: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server not ready")
	}
	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: %d", resp.StatusCode)
	}
	if strings.Contains(logs.String(), "not https") {
		t.Fatalf("loopback listen warned: %q", logs.String())
	}
	if !strings.Contains(out.String(), "gtb dashboard admin token (shown once; store it safely):") {
		t.Fatalf("frame not printed: %q", out.String())
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("server: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop")
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
