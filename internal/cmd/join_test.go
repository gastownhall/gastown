package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/agentlink"
	"github.com/steveyegge/gastown/internal/server"
)

// tokenCapture wraps the real enroll handler and records every token it
// hands out, so tests can assert the token never reaches the user's terminal.
type tokenCapture struct {
	h      http.Handler
	mu     sync.Mutex
	tokens []string
}

func (c *tokenCapture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, r)
	var resp struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Token != "" {
		c.mu.Lock()
		c.tokens = append(c.tokens, resp.Token)
		c.mu.Unlock()
	}
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

type joinEnv struct {
	store    *server.Store
	srv      *httptest.Server
	capture  *tokenCapture
	townRoot string
}

func newJoinEnv(t *testing.T) *joinEnv {
	t.Helper()
	store, err := server.OpenStore(filepath.Join(t.TempDir(), "gtb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	capture := &tokenCapture{h: store.EnrollHandler(time.Now)}
	mux := http.NewServeMux()
	mux.Handle("POST /agent/v1/enroll", capture)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(town, "mayor", "town.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(town, "some", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	return &joinEnv{store: store, srv: srv, capture: capture, townRoot: town}
}

func (e *joinEnv) code(t *testing.T) string {
	t.Helper()
	c, err := e.store.CreateEnrollCode(0)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// runJoinCapture runs `gtb join` and returns everything written to the
// command's out/err, the process stdout/stderr, and the error text.
func runJoinCapture(t *testing.T, args []string, code string, force, jsonOut bool) (string, error) {
	t.Helper()
	oldCode, oldForce, oldJSON := joinCode, joinForce, joinJSON
	joinCode, joinForce, joinJSON = code, force, jsonOut
	t.Cleanup(func() { joinCode, joinForce, joinJSON = oldCode, oldForce, oldJSON })

	oldStdout, oldStderr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = w, w
	done := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); done <- b }()

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&buf)
	c.SetErr(&buf)
	runErr := runJoin(c, args)

	w.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	proc := <-done

	all := buf.String() + string(proc)
	if runErr != nil {
		all += "\nERR: " + runErr.Error()
	}
	return all, runErr
}

func assertNoToken(t *testing.T, e *joinEnv, out string) {
	t.Helper()
	e.capture.mu.Lock()
	defer e.capture.mu.Unlock()
	for _, tok := range e.capture.tokens {
		if strings.Contains(out, tok) {
			t.Fatalf("token leaked to output: %q", out)
		}
	}
	if b, err := os.ReadFile(agentlink.TokenPath(e.townRoot)); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" && strings.Contains(out, tok) {
			t.Fatalf("saved token leaked to output: %q", out)
		}
	}
}

func TestJoin_Success(t *testing.T) {
	e := newJoinEnv(t)
	out, err := runJoinCapture(t, []string{e.srv.URL + "/"}, e.code(t), false, false)
	if err != nil {
		t.Fatalf("join: %v\n%s", err, out)
	}
	u, id, tok, err := agentlink.LoadToken(e.townRoot)
	if err != nil {
		t.Fatal(err)
	}
	if u != e.srv.URL {
		t.Fatalf("server_url %q, want %q", u, e.srv.URL)
	}
	want := "joined " + e.srv.URL + " as machine " + id
	if !strings.Contains(out, want) {
		t.Fatalf("output %q, want %q", out, want)
	}
	m, err := e.store.Machine(id)
	if err != nil {
		t.Fatal(err)
	}
	host, _ := os.Hostname()
	if m.Hostname != host || m.Protocol != agentlink.ProtocolVersion || m.GtbVersion != Version {
		t.Fatalf("machine %+v", m)
	}
	if len(e.capture.tokens) != 1 || e.capture.tokens[0] != tok {
		t.Fatalf("saved token is not the issued one")
	}
	assertNoToken(t, e, out)
}

func TestJoin_DoesNotPrintToken(t *testing.T) {
	e := newJoinEnv(t)

	out, err := runJoinCapture(t, []string{e.srv.URL}, e.code(t), false, false)
	if err != nil {
		t.Fatalf("human join: %v", err)
	}
	assertNoToken(t, e, out)

	out, err = runJoinCapture(t, []string{e.srv.URL}, e.code(t), true, true)
	if err != nil {
		t.Fatalf("json join: %v", err)
	}
	assertNoToken(t, e, out)
	var js map[string]any
	jsonPart := out[strings.Index(out, "{"):]
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonPart)), &js); err != nil {
		t.Fatalf("json output %q: %v", out, err)
	}
	if js["machine_id"] == "" || js["server_url"] != e.srv.URL {
		t.Fatalf("json %v", js)
	}
	if _, ok := js["token"]; ok {
		t.Fatal("json output has a token field")
	}

	// Failure after a token exists: refused, and the token is not printed.
	out, err = runJoinCapture(t, []string{e.srv.URL}, e.code(t), false, true)
	if err == nil {
		t.Fatal("expected refusal")
	}
	assertNoToken(t, e, out)

	// Failure from the server (used code), with --force.
	used := e.code(t)
	if _, err := runJoinCapture(t, []string{e.srv.URL}, used, true, false); err != nil {
		t.Fatal(err)
	}
	out, err = runJoinCapture(t, []string{e.srv.URL}, used, true, true)
	if err == nil {
		t.Fatal("expected failure on reused code")
	}
	assertNoToken(t, e, out)
}

func TestJoin_RefusesExistingTokenWithoutForce(t *testing.T) {
	e := newJoinEnv(t)
	if _, err := runJoinCapture(t, []string{e.srv.URL}, e.code(t), false, false); err != nil {
		t.Fatal(err)
	}
	_, firstID, firstTok, _ := agentlink.LoadToken(e.townRoot)

	code := e.code(t)
	out, err := runJoinCapture(t, []string{e.srv.URL}, code, false, false)
	if err == nil || !strings.Contains(out, "--force") {
		t.Fatalf("expected refusal mentioning --force, got %v\n%s", err, out)
	}
	if _, id, tok, _ := agentlink.LoadToken(e.townRoot); id != firstID || tok != firstTok {
		t.Fatal("existing token was replaced without --force")
	}
	// The refused join did not burn the code.
	if _, err := runJoinCapture(t, []string{e.srv.URL}, code, true, false); err != nil {
		t.Fatalf("--force join: %v", err)
	}
	if _, id, _, _ := agentlink.LoadToken(e.townRoot); id == firstID {
		t.Fatal("--force did not replace the token")
	}
}

func TestJoin_InvalidCode(t *testing.T) {
	e := newJoinEnv(t)
	out, err := runJoinCapture(t, []string{e.srv.URL}, "BOGUS", false, false)
	if err == nil || !strings.Contains(out, "enrollment code is invalid, used or expired") {
		t.Fatalf("got %v\n%s", err, out)
	}
	if _, err := os.Stat(agentlink.TokenPath(e.townRoot)); !os.IsNotExist(err) {
		t.Fatal("token file written on failure")
	}
}

func TestJoin_TooManyAttempts(t *testing.T) {
	e := newJoinEnv(t)
	for i := 0; i < 10; i++ {
		_, _ = runJoinCapture(t, []string{e.srv.URL}, "BOGUS", false, false)
	}
	out, err := runJoinCapture(t, []string{e.srv.URL}, e.code(t), false, false)
	if err == nil || !strings.Contains(out, "too many attempts; wait and retry") {
		t.Fatalf("got %v\n%s", err, out)
	}
}

func TestJoin_RejectsInsecureURL(t *testing.T) {
	e := newJoinEnv(t)
	_, err := runJoinCapture(t, []string{"http://10.0.0.1"}, e.code(t), false, false)
	if err == nil {
		t.Fatal("http to a non-loopback host accepted")
	}
}

func TestJoin_RequiresCode(t *testing.T) {
	e := newJoinEnv(t)
	if _, err := runJoinCapture(t, []string{e.srv.URL}, "", false, false); err == nil {
		t.Fatal("missing --code accepted")
	}
}

func TestJoin_NotInTown(t *testing.T) {
	e := newJoinEnv(t)
	t.Chdir(t.TempDir())
	if _, err := runJoinCapture(t, []string{e.srv.URL}, e.code(t), false, false); err == nil || !strings.Contains(err.Error(), "town") {
		t.Fatalf("expected not-in-town error, got %v", err)
	}
}

func TestJoin_DoesNotFollowRedirects(t *testing.T) {
	e := newJoinEnv(t)
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/agent/v1/enroll", http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	if _, err := runJoinCapture(t, []string{redir.URL}, e.code(t), false, false); err == nil {
		t.Fatal("redirect accepted")
	}
	if hits != 0 {
		t.Fatal("redirect followed; code sent to another host")
	}
}
