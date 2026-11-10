//go:build e2e

// Package e2e drives the real gtb binaries end to end: a gtb server, a
// machine joined to it, the daemon's agent loop, and the dashboard API.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// adminToken is the test's own admin token. It is only ever sent to the
// test's local server; it is not a secret.
const adminToken = "e2e-admin-token-0123456789abcdefghijklmnop"

const (
	// approveWait bounds how long the flow waits for the machine to apply
	// an approve. The brief allows 10 s; raise it only with a reason.
	approveWait = 10 * time.Second
	pollEvery   = 200 * time.Millisecond
)

// gtbBinary builds the gtb binary once for this test run and returns its path.
func gtbBinary(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "gtb")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/gtb")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build gtb: %v\n%s", err, out)
	}
	return bin
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", wd)
		}
		dir = parent
	}
}

// isolatedEnv returns a clean environment with HOME pointed at home, so git
// and dolt never touch the developer's own config.
func isolatedEnv(home string, port string) []string {
	var env []string
	for _, kv := range os.Environ() {
		key := strings.SplitN(kv, "=", 2)[0]
		if strings.HasPrefix(key, "GT_") || strings.HasPrefix(key, "BEADS_") || key == "GTB_AGENTLINK" || key == "HOME" {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+home,
		"GT_DOLT_PORT="+port,
		"BEADS_DOLT_PORT="+port,
		"BEADS_DOLT_AUTO_START=0",
		"GIT_AUTHOR_NAME=Test User", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test User", "GIT_COMMITTER_EMAIL=test@test.com",
	)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func run(t *testing.T, env []string, dir string, bin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// exitCode returns the process exit code of err, or -1 when it is not an exit error.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}

func gitRun(t *testing.T, env []string, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// bareRepoWithCommit creates a bare repo with one commit and returns its file:// URL.
func bareRepoWithCommit(t *testing.T, env []string, base string) string {
	t.Helper()
	bare := filepath.Join(base, "origin.git")
	work := filepath.Join(base, "seed")
	gitRun(t, env, base, "init", "--bare", "-b", "main", bare)
	gitRun(t, env, base, "init", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, env, work, "add", ".")
	gitRun(t, env, work, "commit", "-m", "init")
	gitRun(t, env, work, "remote", "add", "origin", bare)
	gitRun(t, env, work, "push", "origin", "main")
	return "file://" + bare
}

// enableGate merges workflow.gated.enabled=true into the rig's settings
// config. It creates the file when missing and keeps any other keys it finds.
func enableGate(t *testing.T, town, rig string) {
	t.Helper()
	dir := filepath.Join(town, rig, "settings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	cfg := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
	}
	if _, ok := cfg["type"]; !ok {
		cfg["type"] = "rig-settings"
	}
	if _, ok := cfg["version"]; !ok {
		cfg["version"] = 1
	}
	wf, _ := cfg["workflow"].(map[string]any)
	if wf == nil {
		wf = map[string]any{}
	}
	gated, _ := wf["gated"].(map[string]any)
	if gated == nil {
		gated = map[string]any{}
	}
	gated["enabled"] = true
	wf["gated"] = gated
	cfg["workflow"] = wf
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// dashboard is a logged-in browser-style client for the gtb server API.
type dashboard struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

func newDashboard(t *testing.T, base string) *dashboard {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	d := &dashboard{t: t, base: base, http: &http.Client{Jar: jar, Timeout: 15 * time.Second}}
	body, _ := json.Marshal(map[string]string{"token": adminToken})
	resp, err := d.http.Post(base+"/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("login: status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		OK        bool   `json:"ok"`
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.OK || out.CSRFToken == "" {
		t.Fatalf("login: bad response (err=%v ok=%v)", err, out.OK)
	}
	d.csrf = out.CSRFToken
	return d
}

// do sends a request. Mutating requests carry the CSRF header. It decodes the
// JSON body into out when out is non-nil and returns the status code.
func (d *dashboard) do(method, path string, body any, out any) int {
	d.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			d.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, d.base+path, rdr)
	if err != nil {
		d.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", d.csrf)
	}
	resp, err := d.http.Do(req)
	if err != nil {
		d.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			d.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode
}

// TestGtbFlow_ApproveFromDashboard runs the approve-from-dashboard flow across
// a real server and a real machine.
func TestGtbFlow_ApproveFromDashboard(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e flow")
	}
	bin := gtbBinary(t)
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	env := isolatedEnv(home, strconv.Itoa(freePort(t)))
	town := filepath.Join(base, "town")
	serverURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Step 1: start gtb server on loopback with the admin token.
	serverEnv := append(append([]string{}, env...), "GTB_ADMIN_TOKEN="+adminToken)
	srv := exec.Command(bin, "server", "--listen", fmt.Sprintf("127.0.0.1:%d", port),
		"--data", filepath.Join(base, "data"), "--public-url", serverURL)
	srv.Env = serverEnv
	var srvLog bytes.Buffer
	srv.Stdout = &srvLog
	srv.Stderr = &srvLog
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() {
		if srv.Process != nil {
			_ = srv.Process.Kill()
			_ = srv.Wait()
		}
		if t.Failed() {
			t.Logf("server log:\n%s", srvLog.String())
		}
	})
	waitHealthy(t, serverURL)

	// Step 2: install the town, then join it to the server.
	if out, err := run(t, env, base, bin, "install", town, "--name", "e2e-town", "--dolt-port", strconv.Itoa(freePort(t))); err != nil {
		t.Fatalf("gtb install: %v\n%s", err, out)
	}
	dash := newDashboard(t, serverURL)
	var code struct {
		Code string `json:"code"`
	}
	if st := dash.do(http.MethodPost, "/api/v1/enroll-codes", struct{}{}, &code); st != http.StatusOK || code.Code == "" {
		t.Fatalf("enroll code: status %d", st)
	}
	if out, err := run(t, env, town, bin, "join", serverURL, "--code", code.Code); err != nil {
		t.Fatalf("gtb join: %v\n%s", err, out)
	}

	// Machine id, used to address the board and item routes.
	var machines []struct {
		ID string `json:"id"`
	}
	dash.do(http.MethodGet, "/api/v1/machines", nil, &machines)
	if len(machines) != 1 {
		t.Fatalf("machines = %d, want 1", len(machines))
	}
	machine := machines[0].ID

	// Step 3: a local repo, then rig add and the gate.
	repo := bareRepoWithCommit(t, env, base)
	if out, err := run(t, env, town, bin, "rig", "add", "demo", repo); err != nil {
		t.Fatalf("gtb rig add: %v\n%s", err, out)
	}
	enableGate(t, town, "demo")

	// The daemon runs the machine's agent loop: it delivers approvals.
	daemon := exec.Command(bin, "daemon", "run")
	daemon.Dir = town
	daemon.Env = env
	var dLog bytes.Buffer
	daemon.Stdout = &dLog
	daemon.Stderr = &dLog
	if err := daemon.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() {
		if daemon.Process != nil {
			_ = daemon.Process.Kill()
			_ = daemon.Wait()
		}
		if t.Failed() {
			t.Logf("daemon log:\n%s", dLog.String())
		}
	})

	// Step 4: item new, then write the spec.
	out, err := run(t, env, town, bin, "item", "new", "task", "demo", "--rig", "demo", "--json")
	if err != nil {
		t.Fatalf("gtb item new: %v\n%s", err, out)
	}
	var created struct {
		ID           string `json:"id"`
		ArtifactPath string `json:"artifact_path"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		t.Fatalf("item new output: %v\n%s", err, out)
	}
	if err := os.WriteFile(created.ArtifactPath, []byte("# Spec\n\nDemo spec v1.\n"), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	// Step 5: advance exits 3 while waiting for a human.
	out, err = run(t, env, town, bin, "item", "advance", created.ID, "--rig", "demo")
	if got := exitCode(err); got != 3 {
		t.Fatalf("advance exit = %d, want 3\n%s", got, out)
	}

	// Step 6: the board's needs_you lists the item.
	var board struct {
		NeedsYou []struct {
			ID     string `json:"id"`
			SHA    string `json:"-"`
			Step   string `json:"step"`
			Waits  string `json:"waiting_for_human"`
			Mach   string `json:"machine_id"`
		} `json:"needs_you"`
	}
	if st := dash.do(http.MethodGet, "/api/v1/board", nil, &board); st != http.StatusOK {
		t.Fatalf("board: status %d", st)
	}
	found := false
	for _, it := range board.NeedsYou {
		if it.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("needs_you does not contain %s", created.ID)
	}

	// The current spec sha, from the item detail.
	spec := itemSpecSHA(t, dash, machine, created.ID)

	// Step 7: approve with the current sha. The machine applies it within approveWait.
	var accepted struct {
		Key string `json:"key"`
	}
	if st := dash.do(http.MethodPost, fmt.Sprintf("/api/v1/items/%s/%s/approve", machine, created.ID),
		map[string]string{"artifact": "spec", "sha": spec}, &accepted); st != http.StatusAccepted {
		t.Fatalf("approve: status %d", st)
	}
	waitAdvanceOK(t, env, town, bin, created.ID)
	if got := itemStep(t, dash, machine, created.ID); got != "plan" {
		t.Fatalf("step after approve = %q, want plan", got)
	}

	// Step 8: edit the spec, then approve with the old sha. The result is stale_sha.
	if err := os.WriteFile(created.ArtifactPath, []byte("# Spec\n\nDemo spec v2.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var second struct {
		Key string `json:"key"`
	}
	if st := dash.do(http.MethodPost, fmt.Sprintf("/api/v1/items/%s/%s/approve", machine, created.ID),
		map[string]string{"artifact": "spec", "sha": spec}, &second); st != http.StatusAccepted {
		t.Fatalf("second approve: status %d", st)
	}
	if got := waitResult(t, dash, machine, second.Key); got != "stale_sha" {
		t.Fatalf("stale approve result = %q, want stale_sha", got)
	}
}

// waitHealthy polls the server until /health answers.
func waitHealthy(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(pollEvery)
	}
	t.Fatalf("server not healthy at %s", base)
}

// itemSpecSHA returns the sha of the item's spec artifact.
func itemSpecSHA(t *testing.T, dash *dashboard, machine, id string) string {
	t.Helper()
	var detail struct {
		Artifacts []struct {
			Name string `json:"name"`
			SHA  string `json:"sha"`
		} `json:"artifacts"`
	}
	dash.do(http.MethodGet, fmt.Sprintf("/api/v1/items/%s/%s", machine, id), nil, &detail)
	for _, a := range detail.Artifacts {
		if a.Name == "spec" && a.SHA != "" {
			return a.SHA
		}
	}
	t.Fatalf("no spec artifact sha for %s", id)
	return ""
}

// itemStep returns the item's step as the server reports it.
func itemStep(t *testing.T, dash *dashboard, machine, id string) string {
	t.Helper()
	var detail struct {
		Item struct {
			Step string `json:"step"`
		} `json:"item"`
	}
	dash.do(http.MethodGet, fmt.Sprintf("/api/v1/items/%s/%s", machine, id), nil, &detail)
	return detail.Item.Step
}

// waitAdvanceOK retries item advance until it exits 0 or approveWait passes.
// Exit 0 means the approve took effect and the gate passed.
func waitAdvanceOK(t *testing.T, env []string, town, bin, id string) {
	t.Helper()
	deadline := time.Now().Add(approveWait)
	var last string
	for {
		out, err := run(t, env, town, bin, "item", "advance", id, "--rig", "demo")
		if exitCode(err) == 0 {
			return
		}
		last = out
		if time.Now().After(deadline) {
			t.Fatalf("advance did not reach 0 within %s; last output:\n%s", approveWait, last)
		}
		time.Sleep(pollEvery)
	}
}

// waitResult polls the request status until it has a result and returns its error code.
func waitResult(t *testing.T, dash *dashboard, machine, key string) string {
	t.Helper()
	deadline := time.Now().Add(approveWait)
	for {
		var r struct {
			Status string `json:"status"`
			Result *struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			} `json:"result"`
		}
		dash.do(http.MethodGet, fmt.Sprintf("/api/v1/requests/%s/%s", machine, key), nil, &r)
		if r.Result != nil {
			if r.Result.OK {
				return "ok"
			}
			return r.Result.Error
		}
		if time.Now().After(deadline) {
			t.Fatalf("request %s has no result within %s (status %q)", key, approveWait, r.Status)
		}
		time.Sleep(pollEvery)
	}
}
