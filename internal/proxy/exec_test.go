package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// logEntry captures a single structured log record for test assertions.
type logEntry struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

// logCapture is a slog.Handler that records all log entries in memory.
type logCapture struct {
	mu      sync.Mutex
	entries []logEntry
}

func (lc *logCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (lc *logCapture) Handle(_ context.Context, r slog.Record) error {
	e := logEntry{
		level: r.Level,
		msg:   r.Message,
		attrs: make(map[string]string),
	}
	r.Attrs(func(a slog.Attr) bool {
		e.attrs[a.Key] = a.Value.String()
		return true
	})
	lc.mu.Lock()
	lc.entries = append(lc.entries, e)
	lc.mu.Unlock()
	return nil
}

func (lc *logCapture) WithAttrs(_ []slog.Attr) slog.Handler { return lc }
func (lc *logCapture) WithGroup(_ string) slog.Handler      { return lc }

// findEntry returns the first log entry matching the given level and message.
func (lc *logCapture) findEntry(level slog.Level, msg string) (logEntry, bool) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for _, e := range lc.entries {
		if e.level == level && e.msg == msg {
			return e, true
		}
	}
	return logEntry{}, false
}

// hasLevel reports whether any entry with the given level was logged.
func (lc *logCapture) hasLevel(level slog.Level) bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for _, e := range lc.entries {
		if e.level == level {
			return true
		}
	}
	return false
}

// newExecTestServer creates a Server for exec tests with a valid TownRoot.
// All exec tests pass nil as the CA since they don't exercise TLS handshakes.
func newExecTestServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	cfg.TownRoot = t.TempDir()
	srv, err := New(cfg, nil)
	require.NoError(t, err)
	return srv
}

// makeFakeRequest builds an httptest.Request with a fake TLS peer certificate CN.
func makeFakeRequest(method, path, body, cn string) *http.Request {
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if cn != "" {
		req.TLS = &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{
				{Subject: pkix.Name{CommonName: cn}},
			},
		}
	}
	return req
}

func TestPolecatName(t *testing.T) {
	cases := []struct {
		cn   string
		want string
	}{
		{"gt-gastown-furiosa", "furiosa"},
		{"gt-gas-town-furiosa", "furiosa"}, // hyphenated rig
		{"gt-gastown-", ""},                // empty name
		{"gt--furiosa", ""},                // empty rig; rejected
		{"noprefix-rig-name", ""},          // missing gt- prefix
		{"gt-nodashinrest", ""},            // only one component after stripping gt-
		{"", ""},                           // empty string
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.cn, func(t *testing.T) {
			got := polecatName(tc.cn)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCnToIdentity(t *testing.T) {
	cases := []struct {
		cn   string
		want string
	}{
		{"gt-gastown-furiosa", "gastown/furiosa"},
		{"gt-gas-town-furiosa", "gas-town/furiosa"}, // hyphenated rig
		{"gt-gastown-", ""},                         // empty name
		{"gt--furiosa", ""},                         // empty rig (two consecutive dashes after gt-)
		{"noprefix-rig-name", ""},                   // missing gt- prefix
		{"gt-nodashinrest", ""},                     // only one component after stripping gt-
		{"", ""},                                    // empty string
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.cn, func(t *testing.T) {
			got := cnToIdentity(tc.cn)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExtractIdentity(t *testing.T) {
	t.Run("nil TLS returns empty string", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/exec", nil)
		// req.TLS is nil by default from httptest
		assert.Equal(t, "", extractIdentity(req))
	})

	t.Run("empty PeerCertificates returns empty string", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/exec", nil)
		req.TLS = &tls.ConnectionState{PeerCertificates: nil}
		assert.Equal(t, "", extractIdentity(req))
	})

	t.Run("valid CN parses to identity", func(t *testing.T) {
		req := makeFakeRequest("POST", "/v1/exec", "", "gt-gastown-rust")
		assert.Equal(t, "gastown/rust", extractIdentity(req))
	})
}

func TestHandleExec(t *testing.T) {
	srv := newExecTestServer(t, Config{AllowedCommands: []string{"echo", "sh", "sleep"}})

	t.Run("GET returns 405", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/exec", nil)
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("body over 1 MiB returns 400", func(t *testing.T) {
		// Valid JSON prefix with a huge payload that exceeds the 1 MiB limit.
		bigStr := strings.Repeat("x", 1<<20)
		body := `{"argv":["echo","` + bigStr + `"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("malformed JSON returns 400", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader("{not json}"))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("empty argv returns 400", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(`{"argv":[]}`))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("argv[0] not in allowlist returns 403", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(`{"argv":["curl","http://evil.com"]}`))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("allowed command succeeds with correct output", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(`{"argv":["echo","hello"]}`))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp execResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, 0, resp.ExitCode)
		assert.Contains(t, resp.Stdout, "hello")
	})

	t.Run("GT_PROXY_IDENTITY env var is set when CN is present", func(t *testing.T) {
		// Write a tiny script that prints the GT_PROXY_IDENTITY env var.
		// The script is placed in a temp dir added to PATH so AllowedCommands
		// can reference it by plain name (no path separator — issue 12).
		scriptDir := t.TempDir()
		commandName := "printenv.sh"
		if runtime.GOOS == "windows" {
			commandName = "printenv"
			scriptPath := filepath.Join(scriptDir, commandName+".cmd")
			require.NoError(t, os.WriteFile(scriptPath, []byte("@echo off\r\n<nul set /p =%GT_PROXY_IDENTITY%\r\n"), 0644))
		} else {
			scriptPath := filepath.Join(scriptDir, commandName)
			require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\nprintf '%s' \"$GT_PROXY_IDENTITY\"\n"), 0755))
		}
		t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		srv2 := newExecTestServer(t, Config{AllowedCommands: []string{commandName}})
		body := `{"argv":["` + commandName + `"]}`
		req := makeFakeRequest("POST", "/v1/exec", body, "gt-gastown-rust")
		rec := httptest.NewRecorder()
		srv2.handleExec(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp execResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, "gastown/rust", resp.Stdout)
	})

	t.Run("non-zero exit code is returned", func(t *testing.T) {
		body := `{"argv":["sh","-c","exit 42"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp execResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, 42, resp.ExitCode)
	})

	t.Run("context cancellation kills command and handler returns", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		body := `{"argv":["sleep","10"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		done := make(chan struct{})
		go func() {
			srv.handleExec(rec, req)
			close(done)
		}()

		// Give the command time to start, then cancel.
		time.Sleep(100 * time.Millisecond)
		cancel()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("handleExec did not return after context cancellation")
		}
		// Response should be written with non-zero exit code.
		var resp execResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.NotZero(t, resp.ExitCode)
	})
}

func TestRunCommand(t *testing.T) {
	t.Run("echo world produces expected stdout", func(t *testing.T) {
		stdout, stderr, code := runCommand(context.Background(), []string{"echo", "world"}, "")
		assert.Equal(t, "world\n", stdout)
		assert.Equal(t, "", stderr)
		assert.Equal(t, 0, code)
	})

	t.Run("sh exit 42 returns exitCode 42", func(t *testing.T) {
		_, _, code := runCommand(context.Background(), []string{"sh", "-c", "exit 42"}, "")
		assert.Equal(t, 42, code)
	})

	t.Run("stderr is captured separately", func(t *testing.T) {
		stdout, stderr, code := runCommand(context.Background(), []string{"sh", "-c", "echo err >&2"}, "")
		assert.Equal(t, "", stdout)
		assert.Equal(t, "err\n", stderr)
		assert.Equal(t, 0, code)
	})

	t.Run("non-existent binary returns exitCode 1", func(t *testing.T) {
		_, _, code := runCommand(context.Background(), []string{"/no/such/binary/xyzzy"}, "")
		assert.Equal(t, 1, code)
	})

	t.Run("environment is restricted", func(t *testing.T) {
		// Set a sentinel in the test process env; the subprocess must not see it.
		t.Setenv("PROXY_TEST_SENTINEL", "super_secret_sentinel_12345")

		stdout, _, code := runCommand(context.Background(), []string{"sh", "-c", "echo ${PROXY_TEST_SENTINEL:-NOT_SET}"}, "")
		assert.Equal(t, 0, code)
		assert.NotContains(t, stdout, "super_secret_sentinel_12345",
			"subprocess should not inherit test env vars")
	})
}

func TestHandleExec_BDCreateRepoAliasPinsCanonicalBeadsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake bd uses POSIX sh")
	}

	townRoot := t.TempDir()
	townBeads := filepath.Join(townRoot, ".beads")
	rigRoot := filepath.Join(townRoot, "gastown")
	canonicalRig := filepath.Join(rigRoot, "mayor", "rig")
	canonicalBeads := filepath.Join(canonicalRig, ".beads")
	decoyBeads := filepath.Join(rigRoot, ".beads")
	require.NoError(t, os.MkdirAll(canonicalBeads, 0755))
	require.NoError(t, os.MkdirAll(decoyBeads, 0755))
	require.NoError(t, os.MkdirAll(townBeads, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(
		`{"prefix":"hq-","path":"."}`+"\n"+
			`{"prefix":"gt-","path":"gastown/mayor/rig"}`+"\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(townBeads, "metadata.json"), []byte(`{"dolt_database":"hq"}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(canonicalBeads, "metadata.json"), []byte(`{"dolt_database":"gastown"}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(decoyBeads, "metadata.json"), []byte(`{"dolt_database":"hq"}`), 0644))

	scriptDir := t.TempDir()
	scriptPath := filepath.Join(scriptDir, "bd")
	require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\nfor arg in \"$@\"; do printf '[%s]' \"$arg\"; done\nprintf '\\nBEADS_DIR=%s\\nBEADS_DOLT_SERVER_DATABASE=%s\\nBEADS_DOLT_DATA_DIR=%s\\nBD_DOLT_AUTO_COMMIT=%s\\nBD_NO_GIT_OPS=%s\\n' \"${BEADS_DIR:-}\" \"${BEADS_DOLT_SERVER_DATABASE:-}\" \"${BEADS_DOLT_DATA_DIR:-}\" \"${BD_DOLT_AUTO_COMMIT:-}\" \"${BD_NO_GIT_OPS:-}\"\n"), 0755))
	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "stale")
	t.Setenv("BEADS_DIR", "/wrong")

	srv, err := New(Config{
		TownRoot:        townRoot,
		AllowedCommands: []string{"bd"},
		AllowedSubcommands: map[string][]string{
			"bd": {"create"},
		},
		Logger: discardLogger(),
	}, nil)
	require.NoError(t, err)

	run := func(body string) execResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var resp execResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		require.Equal(t, 0, resp.ExitCode, resp.Stderr)
		return resp
	}
	runStatus := func(body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		return rec.Code, rec.Body.String()
	}

	t.Run("space form rig alias", func(t *testing.T) {
		resp := run(`{"argv":["bd","create","--repo","gastown","--title","x"]}`)
		assert.Contains(t, resp.Stdout, "[create][--title][x]")
		assert.NotContains(t, resp.Stdout, "[--repo]")
		assert.NotContains(t, resp.Stdout, "[gastown]")
		assert.Contains(t, resp.Stdout, "BEADS_DIR="+canonicalBeads)
		assert.Contains(t, resp.Stdout, "BEADS_DOLT_SERVER_DATABASE=gastown\n")
		assert.Contains(t, resp.Stdout, "BD_DOLT_AUTO_COMMIT=on")
		assert.Contains(t, resp.Stdout, "BD_NO_GIT_OPS=true")
		assert.NotContains(t, resp.Stdout, "BEADS_DIR="+decoyBeads)
	})

	t.Run("bd global flag before create", func(t *testing.T) {
		resp := run(`{"argv":["bd","--allow-stale","create","--repo","gastown","--title","x"]}`)
		assert.Contains(t, resp.Stdout, "[--allow-stale][create][--title][x]")
		assert.NotContains(t, resp.Stdout, "[--repo]")
		assert.Contains(t, resp.Stdout, "BEADS_DIR="+canonicalBeads)
	})

	t.Run("equals form rig alias", func(t *testing.T) {
		resp := run(`{"argv":["bd","create","--repo=gastown","--title","x"]}`)
		assert.Contains(t, resp.Stdout, "[create][--title][x]")
		assert.NotContains(t, resp.Stdout, "--repo=gastown")
		assert.NotContains(t, resp.Stdout, "[gastown]")
		assert.Contains(t, resp.Stdout, "BEADS_DIR="+canonicalBeads)
		assert.Contains(t, resp.Stdout, "BEADS_DOLT_SERVER_DATABASE=gastown\n")
		assert.Contains(t, resp.Stdout, "BD_DOLT_AUTO_COMMIT=on")
	})

	t.Run("hq alias", func(t *testing.T) {
		resp := run(`{"argv":["bd","create","--repo","hq","--title","x"]}`)
		assert.Contains(t, resp.Stdout, "[create][--title][x]")
		assert.NotContains(t, resp.Stdout, "[--repo]")
		assert.NotContains(t, resp.Stdout, "[hq]")
		assert.Contains(t, resp.Stdout, "BEADS_DIR="+townBeads)
		assert.Contains(t, resp.Stdout, "BEADS_DOLT_SERVER_DATABASE=hq\n")
	})

	t.Run("town alias", func(t *testing.T) {
		resp := run(`{"argv":["bd","create","--repo","town","--title","x"]}`)
		assert.Contains(t, resp.Stdout, "[create][--title][x]")
		assert.NotContains(t, resp.Stdout, "[--repo]")
		assert.NotContains(t, resp.Stdout, "[town]")
		assert.Contains(t, resp.Stdout, "BEADS_DIR="+townBeads)
		assert.Contains(t, resp.Stdout, "BEADS_DOLT_SERVER_DATABASE=hq\n")
	})

	t.Run("path-like repo remains explicit", func(t *testing.T) {
		resp := run(`{"argv":["bd","create","--repo","gastown/mayor/rig","--title","x"]}`)
		assert.Contains(t, resp.Stdout, "[create][--repo][gastown/mayor/rig][--title][x]")
		assert.Contains(t, resp.Stdout, "BEADS_DIR=\n")
		assert.Contains(t, resp.Stdout, "BEADS_DOLT_SERVER_DATABASE=\n")
		assert.Contains(t, resp.Stdout, "BEADS_DOLT_DATA_DIR=\n")
		assert.Contains(t, resp.Stdout, "BD_DOLT_AUTO_COMMIT=on")
		assert.Contains(t, resp.Stdout, "BD_NO_GIT_OPS=true")
	})

	t.Run("unknown bare repo remains explicit", func(t *testing.T) {
		resp := run(`{"argv":["bd","create","--repo","unknown","--title","x"]}`)
		assert.Contains(t, resp.Stdout, "[create][--repo][unknown][--title][x]")
		assert.Contains(t, resp.Stdout, "BEADS_DIR=\n")
		assert.Contains(t, resp.Stdout, "BD_DOLT_AUTO_COMMIT=on")
	})

	t.Run("repo after sentinel remains positional", func(t *testing.T) {
		resp := run(`{"argv":["bd","create","--","--repo","gastown"]}`)
		assert.Contains(t, resp.Stdout, "[create][--][--repo][gastown]")
		assert.Contains(t, resp.Stdout, "BEADS_DIR=\n")
		assert.Contains(t, resp.Stdout, "BEADS_DOLT_SERVER_DATABASE=\n")
	})

	t.Run("duplicate repo flags remain explicit", func(t *testing.T) {
		resp := run(`{"argv":["bd","create","--repo","gastown","--repo","/tmp/other","--title","x"]}`)
		assert.Contains(t, resp.Stdout, "[create][--repo][gastown][--repo][/tmp/other][--title][x]")
		assert.Contains(t, resp.Stdout, "BEADS_DIR=\n")
	})

	t.Run("global flag before forbidden bd subcommand returns 403", func(t *testing.T) {
		code, body := runStatus(`{"argv":["bd","--allow-stale","sql","select 1"]}`)
		assert.Equal(t, http.StatusForbidden, code)
		assert.Contains(t, body, "subcommand not allowed")
	})

	t.Run("unknown leading bd flag returns 403", func(t *testing.T) {
		code, body := runStatus(`{"argv":["bd","--not-a-global","create","--repo","gastown"]}`)
		assert.Equal(t, http.StatusForbidden, code)
		assert.Contains(t, body, "subcommand required")
	})

	t.Run("target-selector bd global returns 403", func(t *testing.T) {
		code, body := runStatus(`{"argv":["bd","--db","/tmp/other","create","--repo","gastown"]}`)
		assert.Equal(t, http.StatusForbidden, code)
		assert.Contains(t, body, "subcommand required")
	})

	t.Run("target-selector flag after subcommand returns 403", func(t *testing.T) {
		code, body := runStatus(`{"argv":["bd","create","--db","/tmp/other","--repo","gastown"]}`)
		assert.Equal(t, http.StatusForbidden, code)
		assert.Contains(t, body, "target-selector")
	})
}

// TestIsAllowed tests the Server.isAllowed helper.
func TestIsAllowed(t *testing.T) {
	srv := newExecTestServer(t, Config{AllowedCommands: []string{"echo", "sh"}})
	assert.True(t, srv.isAllowed("echo"))
	assert.True(t, srv.isAllowed("sh"))
	assert.False(t, srv.isAllowed("curl"))
	assert.False(t, srv.isAllowed(""))

	// Empty allowlist — no commands allowed.
	empty := newExecTestServer(t, Config{})
	assert.False(t, empty.isAllowed("echo"))
	assert.False(t, empty.isAllowed("sh"))
}

// TestHandleExecBodyBytes tests that bodies close to the limit are handled correctly.
func TestHandleExecBodyBytes(t *testing.T) {
	srv := newExecTestServer(t, Config{AllowedCommands: []string{"echo"}})

	t.Run("body exactly at limit succeeds if valid JSON", func(t *testing.T) {
		// Small valid body should succeed.
		req := httptest.NewRequest("POST", "/v1/exec",
			bytes.NewReader([]byte(`{"argv":["echo","ok"]}`)))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	const maxBody = 1 << 20 // 1 MiB — must match exec.go
	prefix := `{"argv":["echo","`
	suffix := `"]}`

	t.Run("body of exactly 1 MiB-1 byte succeeds", func(t *testing.T) {
		fill := strings.Repeat("x", maxBody-1-len(prefix)-len(suffix))
		body := prefix + fill + suffix
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("body of exactly 1 MiB+1 byte returns 400", func(t *testing.T) {
		fill := strings.Repeat("x", maxBody+1-len(prefix)-len(suffix))
		body := prefix + fill + suffix
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

// TestSubcommandValidation tests the subcommand allowlist enforcement.
func TestSubcommandValidation(t *testing.T) {
	srv := newExecTestServer(t, Config{
		AllowedCommands: []string{"echo", "sh"},
		AllowedSubcommands: map[string][]string{
			"echo": {"hello", "world"},
		},
	})

	t.Run("subcommand not in allowlist returns 403", func(t *testing.T) {
		body := `{"argv":["echo","forbidden"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Contains(t, rec.Body.String(), "subcommand not allowed")
	})

	t.Run("subcommand in allowlist returns 200", func(t *testing.T) {
		body := `{"argv":["echo","hello"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("command with subcommand allowlist but no argv[1] returns 403", func(t *testing.T) {
		body := `{"argv":["echo"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Contains(t, rec.Body.String(), "subcommand required")
	})

	t.Run("command with no subcommand allowlist entry passes subcommand check", func(t *testing.T) {
		// "sh" has no entry in AllowedSubcommands, so any subcommand is allowed.
		body := `{"argv":["sh","-c","exit 0"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

// TestHandleExecAuditLog verifies that exec calls produce structured audit log records.
func TestHandleExecAuditLog(t *testing.T) {
	t.Run("success emits INFO record with identity and cmd fields", func(t *testing.T) {
		lc := &logCapture{}
		logger := slog.New(lc)
		srv := newExecTestServer(t, Config{AllowedCommands: []string{"echo"}, Logger: logger})

		req := makeFakeRequest("POST", "/v1/exec", `{"argv":["echo","hi"]}`, "gt-gastown-shiny")
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		e, ok := lc.findEntry(slog.LevelInfo, "exec")
		require.True(t, ok, "expected INFO 'exec' log record")
		assert.Equal(t, "gastown/shiny", e.attrs["identity"])
		assert.Equal(t, "echo", e.attrs["cmd"])
	})

	t.Run("non-zero exit emits WARN record", func(t *testing.T) {
		lc := &logCapture{}
		logger := slog.New(lc)
		srv := newExecTestServer(t, Config{AllowedCommands: []string{"sh"}, Logger: logger})

		body := `{"argv":["sh","-c","exit 7"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		e, ok := lc.findEntry(slog.LevelWarn, "exec failed")
		require.True(t, ok, "expected WARN 'exec failed' log record")
		assert.Equal(t, "sh", e.attrs["cmd"])
	})
}

// TestExecRateLimit verifies that per-client rate limiting returns 429 when exceeded.
func TestExecRateLimit(t *testing.T) {
	// Burst of 1, rate of 0 (never refills) → second request is always rejected.
	srv := newExecTestServer(t, Config{
		AllowedCommands: []string{"echo"},
		ExecRateLimit:   0.001, // near-zero refill; burst covers the first request
		ExecRateBurst:   1,
	})

	body := `{"argv":["echo","hi"]}`

	// First request should succeed (consumes the single burst token).
	req1 := makeFakeRequest("POST", "/v1/exec", body, "gt-gastown-ratelimitclient")
	rec1 := httptest.NewRecorder()
	srv.handleExec(rec1, req1)
	assert.Equal(t, http.StatusOK, rec1.Code)

	// Second immediate request from the same client should be rate-limited.
	req2 := makeFakeRequest("POST", "/v1/exec", body, "gt-gastown-ratelimitclient")
	rec2 := httptest.NewRecorder()
	srv.handleExec(rec2, req2)
	assert.Equal(t, http.StatusTooManyRequests, rec2.Code)
	assert.Contains(t, rec2.Body.String(), "rate limit exceeded")

	// A different client should still succeed (rate limits are per-client).
	req3 := makeFakeRequest("POST", "/v1/exec", body, "gt-gastown-otherclient")
	rec3 := httptest.NewRecorder()
	srv.handleExec(rec3, req3)
	assert.Equal(t, http.StatusOK, rec3.Code)
}

// TestExecRateLimitLogsWarn verifies that rate limit rejections are logged at WARN.
func TestExecRateLimitLogsWarn(t *testing.T) {
	lc := &logCapture{}
	srv := newExecTestServer(t, Config{
		AllowedCommands: []string{"echo"},
		ExecRateLimit:   0.001,
		ExecRateBurst:   1,
		Logger:          slog.New(lc),
	})

	body := `{"argv":["echo","hi"]}`
	// Drain the burst token.
	srv.handleExec(httptest.NewRecorder(),
		makeFakeRequest("POST", "/v1/exec", body, "gt-gastown-warntest"))
	// This one should be rejected and logged.
	srv.handleExec(httptest.NewRecorder(),
		makeFakeRequest("POST", "/v1/exec", body, "gt-gastown-warntest"))

	_, ok := lc.findEntry(slog.LevelWarn, "exec rate limit exceeded")
	assert.True(t, ok, "expected WARN 'exec rate limit exceeded' log entry")
}

// TestExecConcurrencyLimit verifies that the global concurrency cap returns 503.
func TestExecConcurrencyLimit(t *testing.T) {
	// Cap at 1 concurrent exec; burst large enough that rate limiting doesn't interfere.
	srv := newExecTestServer(t, Config{
		AllowedCommands:   []string{"sh"},
		MaxConcurrentExec: 1,
		ExecRateBurst:     100,
	})

	// Block the single semaphore slot with a long-running command.
	started := make(chan struct{})
	unblock := make(chan struct{})

	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		body := `{"argv":["sh","-c","sleep 10"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		req = req.WithContext(ctx)

		// Signal that the goroutine has acquired the semaphore slot by starting
		// the handler, then wait for the test to send a competing request.
		go func() {
			time.Sleep(20 * time.Millisecond)
			close(started)
		}()
		srv.handleExec(httptest.NewRecorder(), req)
	}()

	// Wait until the long-running command has (likely) acquired the semaphore.
	<-started
	_ = unblock // not used — the goroutine's command will time out on its own

	// A second request should be rejected with 503.
	req2 := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(`{"argv":["sh","-c","exit 0"]}`))
	rec2 := httptest.NewRecorder()
	srv.handleExec(rec2, req2)
	assert.Equal(t, http.StatusServiceUnavailable, rec2.Code)
	assert.Contains(t, rec2.Body.String(), "server busy")
}

// TestExecDefaultLimits verifies that default limits are applied when config values are zero.
func TestExecDefaultLimits(t *testing.T) {
	srv := newExecTestServer(t, Config{AllowedCommands: []string{"echo"}})
	assert.Equal(t, 32, cap(srv.execSem), "default MaxConcurrentExec should be 32")
	assert.Equal(t, 60*time.Second, srv.execTimeout, "default ExecTimeout should be 60s")
}

// TestExecTimeout verifies that a per-command timeout kills a slow subprocess.
func TestExecTimeout(t *testing.T) {
	srv := newExecTestServer(t, Config{
		AllowedCommands: []string{"sleep"},
		ExecTimeout:     100 * time.Millisecond,
	})

	body := `{"argv":["sleep","10"]}`
	req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		srv.handleExec(rec, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleExec did not return after ExecTimeout elapsed")
	}
	var resp execResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.NotZero(t, resp.ExitCode, "timed-out command should have non-zero exit code")
}

// TestExecTimeoutNegativeDisables verifies that a negative ExecTimeout passes
// the context through unchanged (no deadline wrapping).
func TestExecTimeoutNegativeDisables(t *testing.T) {
	srv := newExecTestServer(t, Config{
		AllowedCommands: []string{"echo"},
		ExecTimeout:     -1,
	})
	assert.Equal(t, time.Duration(-1), srv.execTimeout)

	// A quick command should still succeed when timeout is disabled.
	body := `{"argv":["echo","ok"]}`
	req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleExec(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp execResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, 0, resp.ExitCode)
}

// TestBinaryResolution verifies that commands not found in PATH are removed from the allowlist.
func TestBinaryResolution(t *testing.T) {
	lc := &logCapture{}
	logger := slog.New(lc)

	srv := newExecTestServer(t, Config{
		AllowedCommands: []string{"echo", "this-binary-does-not-exist-xyzzy-12345"},
		Logger:          logger,
	})

	assert.True(t, srv.isAllowed("echo"), "echo should remain in allowlist")
	assert.False(t, srv.isAllowed("this-binary-does-not-exist-xyzzy-12345"),
		"non-existent binary should be removed from allowlist")
	assert.True(t, lc.hasLevel(slog.LevelError), "expected error log for missing binary")
}
