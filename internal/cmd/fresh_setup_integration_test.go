//go:build integration

package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jonbaldie/gastown/internal/beads"
)

var freshSetupIntegrationCounter atomic.Int32

func TestFreshInstallRigPolecatHookIntegration(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping fresh setup integration test")
	}
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping fresh setup integration test")
	}

	tmpDir := resolveSymlinks(t, t.TempDir())
	hqPath := filepath.Join(tmpDir, "town")
	doltPort := freeTCPPort(t)
	doltPortString := strconv.Itoa(doltPort)

	// createAutoConvoy and related helpers shell out with the process env.
	t.Setenv("GT_DOLT_PORT", doltPortString)
	t.Setenv("BEADS_DOLT_PORT", doltPortString)

	env := freshSetupIntegrationEnv(tmpDir, doltPortString)
	configureGitIdentityForEnv(t, env)

	gtBinary := buildGT(t)
	runFreshSetupCmd(t, "", env, gtBinary, "install", hqPath, "--name", "test-town", "--git", "--dolt-port", doltPortString)
	t.Cleanup(func() {
		cmd := exec.Command(gtBinary, "dolt", "stop")
		cmd.Dir = hqPath
		cmd.Env = env
		_ = cmd.Run()
	})

	assertTownBeadsPrefix(t, hqPath)

	repoDir := createFreshSetupSourceRepo(t, tmpDir)
	repoURL := (&url.URL{Scheme: "file", Path: repoDir}).String()
	rigName := "testrig"
	prefix := fmt.Sprintf("fs%d", freshSetupIntegrationCounter.Add(1))
	runFreshSetupCmd(t, hqPath, env, gtBinary, "rig", "add", rigName, repoURL, "--prefix", prefix, "--branch", "main")

	rigPath := filepath.Join(hqPath, rigName)
	assertFreshSetupRoute(t, hqPath, "hq-", ".")
	assertFreshSetupRoute(t, hqPath, "hq-cv-", ".")
	assertFreshSetupRoutePathExists(t, hqPath, prefix+"-")
	assertBeadsRedirectResolves(t, filepath.Join(rigPath, ".beads"))

	polecatName := "toast"
	runFreshSetupCmd(t, hqPath, env, gtBinary, "polecat", "add", rigName, polecatName)
	polecatWorktree := filepath.Join(rigPath, "polecats", polecatName, rigName)
	assertBeadsRedirectResolves(t, filepath.Join(polecatWorktree, ".beads"))

	issue := createFreshSetupIssue(t, rigPath, env, "Fresh setup hook smoke")
	if !strings.HasPrefix(issue.ID, prefix+"-") {
		t.Fatalf("created issue ID %q does not use rig prefix %q", issue.ID, prefix+"-")
	}

	agentID := rigName + "/polecats/" + polecatName
	runFreshSetupCmd(t, rigPath, env, "bd", "update", issue.ID, "--status=hooked", "--assignee="+agentID)
	shown := showFreshSetupIssue(t, rigPath, env, issue.ID)
	if shown.Status != beads.StatusHooked || shown.Assignee != agentID {
		t.Fatalf("issue hook state = status %q assignee %q, want status %q assignee %q",
			shown.Status, shown.Assignee, beads.StatusHooked, agentID)
	}

	runFreshSetupCmd(t, polecatWorktree, env, "bd", "list")
	runFreshSetupCmd(t, polecatWorktree, env, "bd", "show", issue.ID)

	hookJSON := runFreshSetupOutputCmd(t, polecatWorktree, env, gtBinary, "hook", "--json")
	var hookStatus MoleculeStatusInfo
	if err := json.Unmarshal([]byte(hookJSON), &hookStatus); err != nil {
		t.Fatalf("parse gt hook --json output: %v\n%s", err, hookJSON)
	}
	if hookStatus.Target != agentID || !hookStatus.HasWork || hookStatus.PinnedBead == nil || hookStatus.PinnedBead.ID != issue.ID {
		t.Fatalf("gt hook --json = target %q has_work %v pinned %+v, want %s hooked to %s",
			hookStatus.Target, hookStatus.HasWork, hookStatus.PinnedBead, issue.ID, agentID)
	}

	withWorkingDir(t, hqPath, func() {
		convoyID, err := createAutoConvoy(issue.ID, issue.Title, false, "mr", "main")
		if err != nil {
			t.Fatalf("create auto convoy: %v", err)
		}
		if !strings.HasPrefix(convoyID, "hq-cv-") {
			t.Fatalf("convoy ID %q does not use hq-cv- prefix", convoyID)
		}
		runFreshSetupCmd(t, hqPath, env, "bd", "show", convoyID)
		if got := isTrackedByConvoy(issue.ID); got != convoyID {
			t.Fatalf("isTrackedByConvoy(%s) = %q, want %q", issue.ID, got, convoyID)
		}
	})
}

type freshSetupIssue struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Assignee string `json:"assignee"`
}

func freshSetupIntegrationEnv(homeDir, doltPort string) []string {
	clean := make([]string, 0, len(os.Environ())+3)
	for _, entry := range beads.StripBDTargetEnv(os.Environ()) {
		if strings.HasPrefix(entry, "GT_") || strings.HasPrefix(entry, "BD_") {
			continue
		}
		if strings.HasPrefix(entry, "HOME=") {
			continue
		}
		clean = append(clean, entry)
	}
	return append(clean, "HOME="+homeDir, "GT_DOLT_PORT="+doltPort, "BEADS_DOLT_PORT="+doltPort, "BEADS_DOLT_SERVER_PORT="+doltPort)
}

func configureGitIdentityForEnv(t *testing.T, env []string) {
	t.Helper()
	for _, args := range [][]string{
		{"config", "--global", "user.name", "Test User"},
		{"config", "--global", "user.email", "test@test.com"},
	} {
		runFreshSetupCmd(t, "", env, "git", args...)
	}
}

func createFreshSetupSourceRepo(t *testing.T, tmpDir string) string {
	t.Helper()
	repoDir := filepath.Join(tmpDir, "source-repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("mkdir source repo: %v", err)
	}

	runFreshSetupCmd(t, repoDir, nil, "git", "init", "--initial-branch=main")
	runFreshSetupCmd(t, repoDir, nil, "git", "config", "user.email", "test@test.com")
	runFreshSetupCmd(t, repoDir, nil, "git", "config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Fresh setup fixture\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	runFreshSetupCmd(t, repoDir, nil, "git", "add", ".")
	runFreshSetupCmd(t, repoDir, nil, "git", "commit", "-m", "Initial commit")
	return repoDir
}

func createFreshSetupIssue(t *testing.T, dir string, env []string, title string) freshSetupIssue {
	t.Helper()
	out := runFreshSetupOutputCmd(t, dir, env, "bd", "create", "--json", "--title", title, "--type", "task", "--priority", "2", "--description", "Fresh setup integration test issue")
	var issue freshSetupIssue
	if err := json.Unmarshal([]byte(out), &issue); err != nil {
		t.Fatalf("parse bd create output: %v\n%s", err, out)
	}
	if issue.ID == "" {
		t.Fatalf("bd create returned empty ID: %s", out)
	}
	return issue
}

func showFreshSetupIssue(t *testing.T, dir string, env []string, id string) freshSetupIssue {
	t.Helper()
	out := runFreshSetupOutputCmd(t, dir, env, "bd", "show", id, "--json")
	var issue freshSetupIssue
	if err := json.Unmarshal([]byte(out), &issue); err == nil {
		return issue
	}

	var issues []freshSetupIssue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		t.Fatalf("parse bd show output: %v\n%s", err, out)
	}
	if len(issues) != 1 {
		t.Fatalf("bd show %s returned %d issues, want 1\n%s", id, len(issues), out)
	}
	return issues[0]
}

func assertTownBeadsPrefix(t *testing.T, hqPath string) {
	t.Helper()
	configPath := filepath.Join(hqPath, ".beads", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read town beads config: %v", err)
	}
	text := string(data)
	for _, want := range []string{"prefix: hq", "issue-prefix: hq"} {
		if !strings.Contains(text, want) {
			t.Fatalf("town beads config missing %q:\n%s", want, text)
		}
	}
}

func assertFreshSetupRoute(t *testing.T, hqPath, prefix, wantPath string) {
	t.Helper()
	routePath, ok := freshSetupRoutePath(t, hqPath, prefix)
	if !ok {
		t.Fatalf("routes.jsonl missing route for prefix %q", prefix)
	}
	if routePath != wantPath {
		t.Fatalf("route %q path = %q, want %q", prefix, routePath, wantPath)
	}
}

func assertFreshSetupRoutePathExists(t *testing.T, hqPath, prefix string) {
	t.Helper()
	routePath, ok := freshSetupRoutePath(t, hqPath, prefix)
	if !ok {
		t.Fatalf("routes.jsonl missing route for prefix %q", prefix)
	}
	if _, err := os.Stat(filepath.Join(hqPath, routePath)); err != nil {
		t.Fatalf("route %q points to invalid path %q: %v", prefix, routePath, err)
	}
}

func freshSetupRoutePath(t *testing.T, hqPath, prefix string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(hqPath, ".beads", "routes.jsonl"))
	if err != nil {
		t.Fatalf("read routes.jsonl: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var route struct {
			Prefix string `json:"prefix"`
			Path   string `json:"path"`
		}
		if err := json.Unmarshal([]byte(line), &route); err != nil {
			t.Fatalf("parse route %q: %v", line, err)
		}
		if route.Prefix == prefix {
			return route.Path, true
		}
	}
	return "", false
}

func assertBeadsRedirectResolves(t *testing.T, beadsDir string) {
	t.Helper()
	resolved := resolveBeadsRedirect(t, beadsDir)
	if _, err := os.Stat(resolved); err != nil {
		t.Fatalf("beads redirect %s resolved to invalid path %s: %v", beadsDir, resolved, err)
	}
	if _, err := os.Stat(filepath.Join(resolved, "config.yaml")); err != nil {
		t.Fatalf("resolved beads dir %s missing config.yaml: %v", resolved, err)
	}
}

func resolveBeadsRedirect(t *testing.T, beadsDir string) string {
	t.Helper()
	current := beadsDir
	for range 8 {
		redirectPath := filepath.Join(current, "redirect")
		data, err := os.ReadFile(redirectPath)
		if os.IsNotExist(err) {
			return current
		}
		if err != nil {
			t.Fatalf("read redirect %s: %v", redirectPath, err)
		}
		target := strings.TrimSpace(string(data))
		if target == "" {
			t.Fatalf("empty redirect in %s", redirectPath)
		}
		if filepath.IsAbs(target) {
			current = filepath.Clean(target)
		} else {
			current = filepath.Clean(filepath.Join(filepath.Dir(current), target))
		}
	}
	t.Fatalf("redirect chain from %s exceeded maximum depth", beadsDir)
	return ""
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate free TCP port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func withWorkingDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore working directory %s: %v", oldWD, err)
		}
	}()
	fn()
}

func runFreshSetupCmd(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func runFreshSetupOutputCmd(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.Output()
	if err != nil {
		debugCmd := exec.Command(name, args...)
		if dir != "" {
			debugCmd.Dir = dir
		}
		if env != nil {
			debugCmd.Env = env
		}
		debugOut, _ := debugCmd.CombinedOutput()
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, debugOut)
	}
	return string(out)
}
