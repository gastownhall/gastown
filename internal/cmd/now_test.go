package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/daemon"
	"github.com/jonbaldie/gastown/internal/testutil"
	"github.com/jonbaldie/gastown/internal/tmux"
)

func TestNowHelpDescribesFiveSecondPath(t *testing.T) {
	gtBinary := buildGT(t)
	out, err := exec.Command(gtBinary, "now", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("gt now --help: %v\n%s", err, out)
	}
	text := string(out)
	for _, want := range []string{"five seconds", "--mayor", "--workers", "--town", "--no-attach"} {
		if !strings.Contains(text, want) {
			t.Fatalf("gt now --help missing %q:\n%s", want, text)
		}
	}
}

func TestNowFailsWithoutAgentCLI(t *testing.T) {
	home := t.TempDir()
	repo := createNowGitRepo(t, filepath.Join(t.TempDir(), "proj"))
	town := filepath.Join(t.TempDir(), "town")
	bin := nowToolBin(t)
	env := nowTestEnv(t, home, bin, "now-no-agent", true)

	gtBinary := buildGT(t)
	out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach")
	if err == nil {
		t.Fatalf("gt now succeeded without an agent CLI:\n%s", out)
	}
	if !strings.Contains(out, "PATH") && !strings.Contains(out, "cursor-agent") && !strings.Contains(out, "claude") {
		t.Fatalf("error should name a missing binary:\n%s", out)
	}
	if _, statErr := os.Stat(town); !os.IsNotExist(statErr) {
		t.Fatalf("town was created after agent-missing failure")
	}
	assertNoTownFilesInRepo(t, repo)
}

func TestNowFailsWhenNotGitRepo(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	town := filepath.Join(t.TempDir(), "town")
	bin := nowAgentBin(t)
	env := nowTestEnv(t, home, bin, "now-not-git", true)

	gtBinary := buildGT(t)
	out, err := runGTCmdMayFail(t, gtBinary, dir, env, "now", "--town", town, "--no-attach",
		"--mayor", "cursor:high", "--workers", "cursor:low")
	if err == nil {
		t.Fatalf("gt now succeeded outside a git repo:\n%s", out)
	}
	if !strings.Contains(out, "git") {
		t.Fatalf("error should mention git repository:\n%s", out)
	}
	if _, statErr := os.Stat(town); !os.IsNotExist(statErr) {
		t.Fatalf("town was created for a non-git directory")
	}
}

func TestNowStartsTownInFiveSeconds(t *testing.T) {
	requireNowStack(t)
	home := t.TempDir()
	repo := createNowGitRepo(t, filepath.Join(t.TempDir(), "proj"))
	town := filepath.Join(t.TempDir(), "town")
	bin := nowAgentBin(t)
	socket := nowSocket("happy")
	env := nowTestEnv(t, home, bin, socket, false)
	testutil.ReapOwnedDoltOnCleanup(t, town)
	stopNowDaemonOnCleanup(t, town)
	t.Cleanup(func() { killNowTmux(t, socket) })

	gtBinary := buildGT(t)
	env = append(env, "GT_DOLT_PORT="+strconv.Itoa(nowFreeTCPPort(t)))
	start := time.Now()
	out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--mayor", "cursor:grok-4.6:high", "--workers", "cursor:grok-4.6:low")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("gt now failed: %v\n%s", err, out)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("gt now took %s, want under 5s\n%s", elapsed, out)
	}
	if !strings.Contains(out, "town="+town) {
		t.Fatalf("missing town path in output:\n%s", out)
	}
	if !strings.Contains(out, "rig=proj") {
		t.Fatalf("missing rig name in output:\n%s", out)
	}
	if !strings.Contains(out, "dolt=") {
		t.Fatalf("missing dolt port in output:\n%s", out)
	}
	if strings.Contains(out, "Attaching") && strings.Contains(strings.ToLower(out), "attach-session") {
		t.Fatalf("--no-attach still looks like a tmux attach:\n%s", out)
	}
	if nowMayorHasClient(t, socket) {
		t.Fatal("--no-attach attached a client to the Mayor session")
	}
	if _, err := os.Stat(filepath.Join(town, "mayor", "town.json")); err != nil {
		t.Fatalf("town missing mayor/town.json: %v", err)
	}
	rigsJSON, err := os.ReadFile(filepath.Join(town, "mayor", "rigs.json"))
	if err != nil {
		t.Fatalf("reading mayor/rigs.json: %v", err)
	}
	if !strings.Contains(string(rigsJSON), `"proj"`) && !strings.Contains(string(rigsJSON), "proj") {
		t.Fatalf("rig proj not registered in mayor/rigs.json:\n%s", rigsJSON)
	}
	if _, err := os.Stat(filepath.Join(town, "proj", ".repo.git")); err != nil {
		t.Fatalf("rig missing local .repo.git: %v", err)
	}
	assertNoTownFilesInRepo(t, repo)
	if _, err := os.Stat(filepath.Join(repo, "settings", "config.json")); !os.IsNotExist(err) {
		t.Fatal("gt now wrote mix into the project cwd")
	}

	mix := readNowMix(t, gtBinary, town, env)
	assertRoleMix(t, mix, "mayor", "now-mayor", "high")
	assertRoleMix(t, mix, "deacon", "now-mayor", "high")
	assertRoleMix(t, mix, "witness", "now-workers", "low")
	assertRoleMix(t, mix, "polecat", "now-workers", "low")
	assertRoleMix(t, mix, "refinery", "now-workers", "low")
	assertRoleMix(t, mix, "crew", "now-workers", "low")
	assertRoleMix(t, mix, "boot", "now-workers", "low")
	assertRoleMix(t, mix, "dog", "now-workers", "low")
	assertRoleProvider(t, mix, "mayor", "cursor")
	assertRoleProvider(t, mix, "deacon", "cursor")
	assertRoleProvider(t, mix, "witness", "cursor")
	if mix.CostLikeClaudePreset {
		t.Fatalf("gt now applied a cost-tier Claude preset:\n%+v", mix)
	}

	agentOut := runGTCmdOutput(t, gtBinary, town, env, "config", "agent", "get", "now-mayor")
	if !strings.Contains(agentOut, "--model") || !strings.Contains(agentOut, "grok-4.6") {
		t.Fatalf("now-mayor alias missing --model grok-4.6:\n%s", agentOut)
	}

	if !nowMayorSessionExists(t, socket) {
		t.Fatal("Mayor session was not created")
	}

	mayorEnv := append(append([]string{}, env...),
		"GT_TOWN_ROOT="+town,
		"GT_ROLE=mayor",
		"GT_AGENT=mayor",
		"BD_ACTOR=mayor",
	)
	hookOut, hookErr := runGTCmdMayFail(t, gtBinary, town, mayorEnv, "hook")
	if hookErr != nil {
		t.Fatalf("gt hook after gt now failed: %v\n%s", hookErr, hookOut)
	}
	mailOut, mailErr := runGTCmdMayFail(t, gtBinary, town, mayorEnv, "mail", "inbox")
	if mailErr != nil {
		t.Fatalf("gt mail inbox after gt now failed: %v\n%s", mailErr, mailOut)
	}

	secondStart := time.Now()
	secondOut, secondErr := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--mayor", "cursor:grok-4.6:high", "--workers", "cursor:grok-4.6:low")
	secondElapsed := time.Since(secondStart)
	if secondErr != nil {
		t.Fatalf("second gt now failed: %v\n%s", secondErr, secondOut)
	}
	if secondElapsed > 5*time.Second {
		t.Fatalf("second gt now took %s, want under 5s\n%s", secondElapsed, secondOut)
	}

	badOut, badErr := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--mayor", "cursor:grok-4.6:xhigh")
	if badErr == nil {
		t.Fatalf("invalid effort succeeded:\n%s", badOut)
	}
	mixAfter := readNowMix(t, gtBinary, town, env)
	assertRoleMix(t, mixAfter, "mayor", "now-mayor", "high")
}

func TestNowPicksFreeDoltPortWhenBusy(t *testing.T) {
	requireNowStack(t)
	home := t.TempDir()
	bin := nowAgentBin(t)

	repoA := createNowGitRepo(t, filepath.Join(t.TempDir(), "alpha"))
	townA := filepath.Join(t.TempDir(), "town-a")
	socketA := nowSocket("porta")
	envA := nowTestEnv(t, home, bin, socketA, false)
	testutil.ReapOwnedDoltOnCleanup(t, townA)
	stopNowDaemonOnCleanup(t, townA)
	t.Cleanup(func() { killNowTmux(t, socketA) })

	gtBinary := buildGT(t)
	outA, err := runGTCmdMayFail(t, gtBinary, repoA, envA, "now", "--town", townA, "--no-attach",
		"--mayor", "cursor:high", "--workers", "cursor:low")
	if err != nil {
		t.Fatalf("first gt now failed: %v\n%s", err, outA)
	}
	portA := doltPortFromNowOutput(t, outA)

	repoB := createNowGitRepo(t, filepath.Join(t.TempDir(), "beta"))
	townB := filepath.Join(t.TempDir(), "town-b")
	socketB := nowSocket("portb")
	envB := nowTestEnv(t, home, bin, socketB, false)
	testutil.ReapOwnedDoltOnCleanup(t, townB)
	stopNowDaemonOnCleanup(t, townB)
	t.Cleanup(func() { killNowTmux(t, socketB) })

	outB, err := runGTCmdMayFail(t, gtBinary, repoB, envB, "now", "--town", townB, "--no-attach",
		"--mayor", "cursor:high", "--workers", "cursor:low")
	if err != nil {
		t.Fatalf("second gt now failed: %v\n%s", err, outB)
	}
	portB := doltPortFromNowOutput(t, outB)
	if portA == portB {
		t.Fatalf("both towns used Dolt port %d", portA)
	}

	dataA := filepath.Join(townA, ".dolt-data")
	dataB := filepath.Join(townB, ".dolt-data")
	cfgB, err := os.ReadFile(filepath.Join(dataB, "config.yaml"))
	if err != nil {
		t.Fatalf("reading town B config.yaml: %v", err)
	}
	if strings.Contains(string(cfgB), dataA) {
		t.Fatalf("town B Dolt config points at town A data dir:\n%s", cfgB)
	}
}

func TestNowFailsWhenRepoHasNoCommits(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args[1:], err, out)
		}
	}
	town := filepath.Join(t.TempDir(), "town")
	bin := nowAgentBin(t)
	env := nowTestEnv(t, home, bin, "now-empty", true)

	gtBinary := buildGT(t)
	out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--mayor", "cursor:high", "--workers", "cursor:low")
	if err == nil {
		t.Fatalf("gt now succeeded on an empty repo:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "empty") {
		t.Fatalf("error should mention empty repository:\n%s", out)
	}
	if _, statErr := os.Stat(town); !os.IsNotExist(statErr) {
		t.Fatalf("town was created for an empty repository")
	}
}

func TestNowRejectsUnknownRuntimeWithoutWritingTown(t *testing.T) {
	home := t.TempDir()
	repo := createNowGitRepo(t, filepath.Join(t.TempDir(), "proj"))
	town := filepath.Join(t.TempDir(), "town")
	bin := nowAgentBin(t)
	env := nowTestEnv(t, home, bin, "now-unknown", true)

	gtBinary := buildGT(t)
	out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--mayor", "not-a-runtime:high")
	if err == nil {
		t.Fatalf("unknown runtime succeeded:\n%s", out)
	}
	if !strings.Contains(out, "unknown runtime") {
		t.Fatalf("error should mention unknown runtime:\n%s", out)
	}
	if _, statErr := os.Stat(town); !os.IsNotExist(statErr) {
		t.Fatalf("town was created after unknown runtime")
	}
}

func TestNowNameSetsRigName(t *testing.T) {
	requireNowStack(t)
	home := t.TempDir()
	repo := createNowGitRepo(t, filepath.Join(t.TempDir(), "proj"))
	town := filepath.Join(t.TempDir(), "town")
	bin := nowAgentBin(t)
	socket := nowSocket("name")
	env := nowTestEnv(t, home, bin, socket, false)
	env = append(env, "GT_DOLT_PORT="+strconv.Itoa(nowFreeTCPPort(t)))
	testutil.ReapOwnedDoltOnCleanup(t, town)
	stopNowDaemonOnCleanup(t, town)
	t.Cleanup(func() { killNowTmux(t, socket) })

	gtBinary := buildGT(t)
	out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--name", "custom_rig", "--mayor", "cursor:high", "--workers", "cursor:low")
	if err != nil {
		t.Fatalf("gt now --name failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "rig=custom_rig") {
		t.Fatalf("missing custom rig name:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(town, "custom_rig", ".repo.git")); err != nil {
		t.Fatalf("custom rig missing .repo.git: %v", err)
	}
}

func TestNowAcceptsPathArgument(t *testing.T) {
	requireNowStack(t)
	home := t.TempDir()
	repo := createNowGitRepo(t, filepath.Join(t.TempDir(), "proj"))
	town := filepath.Join(t.TempDir(), "town")
	cwd := t.TempDir()
	bin := nowAgentBin(t)
	socket := nowSocket("path")
	env := nowTestEnv(t, home, bin, socket, false)
	env = append(env, "GT_DOLT_PORT="+strconv.Itoa(nowFreeTCPPort(t)))
	testutil.ReapOwnedDoltOnCleanup(t, town)
	stopNowDaemonOnCleanup(t, town)
	t.Cleanup(func() { killNowTmux(t, socket) })

	gtBinary := buildGT(t)
	out, err := runGTCmdMayFail(t, gtBinary, cwd, env, "now", repo, "--town", town, "--no-attach",
		"--mayor", "cursor:high", "--workers", "cursor:low")
	if err != nil {
		t.Fatalf("gt now [path] failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "rig=proj") {
		t.Fatalf("path argument did not register the repo:\n%s", out)
	}
	assertNoTownFilesInRepo(t, repo)
}

func TestNowRejectsTwoTokenInvalidEffort(t *testing.T) {
	home := t.TempDir()
	repo := createNowGitRepo(t, filepath.Join(t.TempDir(), "proj"))
	town := filepath.Join(t.TempDir(), "town")
	bin := nowAgentBin(t)
	env := nowTestEnv(t, home, bin, "now-xhigh", true)

	gtBinary := buildGT(t)
	out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--mayor", "cursor:xhigh")
	if err == nil {
		t.Fatalf("cursor:xhigh succeeded:\n%s", out)
	}
	if !strings.Contains(out, "invalid effort") {
		t.Fatalf("error should mention invalid effort:\n%s", out)
	}
	if _, statErr := os.Stat(town); !os.IsNotExist(statErr) {
		t.Fatalf("town was created after invalid effort")
	}
	assertNoTownFilesInRepo(t, repo)
}

func TestNowStoresSlashInModel(t *testing.T) {
	requireNowStack(t)
	home := t.TempDir()
	repo := createNowGitRepo(t, filepath.Join(t.TempDir(), "proj"))
	town := filepath.Join(t.TempDir(), "town")
	bin := nowAgentBin(t)
	socket := nowSocket("slash")
	env := nowTestEnv(t, home, bin, socket, false)
	env = append(env, "GT_DOLT_PORT="+strconv.Itoa(nowFreeTCPPort(t)))
	testutil.ReapOwnedDoltOnCleanup(t, town)
	stopNowDaemonOnCleanup(t, town)
	t.Cleanup(func() { killNowTmux(t, socket) })

	gtBinary := buildGT(t)
	out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--mayor", "cursor:org/model:high", "--workers", "cursor:low")
	if err != nil {
		t.Fatalf("gt now with slash model failed: %v\n%s", err, out)
	}
	agentOut := runGTCmdOutput(t, gtBinary, town, env, "config", "agent", "get", "now-mayor")
	if !strings.Contains(agentOut, "--model") || !strings.Contains(agentOut, "org/model") {
		t.Fatalf("now-mayor alias missing --model org/model:\n%s", agentOut)
	}
}

func TestNowPiMayorKeepsSessionAlive(t *testing.T) {
	requireNowStack(t)
	home := t.TempDir()
	repo := createNowGitRepo(t, filepath.Join(t.TempDir(), "proj"))
	town := filepath.Join(t.TempDir(), "town")
	bin := nowPiBinStay(t)
	socket := nowSocket("pi")
	env := nowTestEnv(t, home, bin, socket, false)
	env = append(env, "GT_DOLT_PORT="+strconv.Itoa(nowFreeTCPPort(t)))
	testutil.ReapOwnedDoltOnCleanup(t, town)
	stopNowDaemonOnCleanup(t, town)
	t.Cleanup(func() { killNowTmux(t, socket) })

	gtBinary := buildGT(t)
	out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--mayor", "pi:low", "--workers", "pi:low")
	if err != nil {
		t.Fatalf("gt now --mayor pi:low failed: %v\n%s", err, out)
	}
	hook := filepath.Join(town, "mayor", ".pi", "extensions", "gastown-hooks.js")
	if _, err := os.Stat(hook); err != nil {
		t.Fatalf("mayor Pi hook missing: %v", err)
	}
	if !nowMayorSessionExists(t, socket) {
		t.Fatal("Mayor session missing after gt now --mayor pi")
	}
	if nowMayorPaneDead(t, socket) {
		t.Fatal("Mayor pane died; Pi needs gastown-hooks.js before start")
	}
}

func TestNowRefusesConvertingRepoIntoTownHQ(t *testing.T) {
	home := t.TempDir()
	repo := createNowGitRepo(t, filepath.Join(t.TempDir(), "proj"))
	bin := nowAgentBin(t)
	gtBinary := buildGT(t)

	cases := []struct {
		name string
		env  []string
		args []string
	}{
		{
			name: "--town",
			env:  nowTestEnv(t, home, bin, "now-self-town", true),
			args: []string{"now", "--town", repo, "--no-attach", "--mayor", "cursor:high", "--workers", "cursor:low"},
		},
		{
			name: "GT_TOWN_ROOT",
			env:  append(nowTestEnv(t, home, bin, "now-self-root", true), "GT_TOWN_ROOT="+repo),
			args: []string{"now", "--no-attach", "--mayor", "cursor:high", "--workers", "cursor:low"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runGTCmdMayFail(t, gtBinary, repo, tc.env, tc.args...)
			if err == nil {
				t.Fatalf("gt now converted the project repo into a Town HQ:\n%s", out)
			}
			if !strings.Contains(out, "Town HQ") && !strings.Contains(out, "convert") {
				t.Fatalf("error should refuse Town HQ conversion:\n%s", out)
			}
			assertNoTownFilesInRepo(t, repo)
		})
	}
}

func TestNowRefusesTownHQUnlessRegisteredRig(t *testing.T) {
	requireNowStack(t)
	home := t.TempDir()
	repo := createNowGitRepo(t, filepath.Join(t.TempDir(), "proj"))
	town := filepath.Join(t.TempDir(), "town")
	bin := nowAgentBin(t)
	socket := nowSocket("hq")
	env := nowTestEnv(t, home, bin, socket, false)
	env = append(env, "GT_DOLT_PORT="+strconv.Itoa(nowFreeTCPPort(t)))
	testutil.ReapOwnedDoltOnCleanup(t, town)
	stopNowDaemonOnCleanup(t, town)
	t.Cleanup(func() { killNowTmux(t, socket) })

	gtBinary := buildGT(t)
	if out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--mayor", "cursor:high", "--workers", "cursor:low"); err != nil {
		t.Fatalf("setup gt now failed: %v\n%s", err, out)
	}

	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = town
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args[1:], err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(town, "README.md"), []byte("town\n"), 0644); err != nil {
		t.Fatalf("write town README: %v", err)
	}
	for _, args := range [][]string{{"git", "add", "."}, {"git", "commit", "-m", "town"}} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = town
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args[1:], err, out)
		}
	}

	out, err := runGTCmdMayFail(t, gtBinary, town, env, "now", "--town", town, "--no-attach",
		"--mayor", "cursor:high", "--workers", "cursor:low")
	if err == nil {
		t.Fatalf("gt now from Town HQ succeeded:\n%s", out)
	}
	if !strings.Contains(out, "Town HQ") {
		t.Fatalf("error should mention Town HQ:\n%s", out)
	}
}

func TestNowDefaultRuntimeUsesFirstOnPATH(t *testing.T) {
	requireNowStack(t)
	home := t.TempDir()
	repo := createNowGitRepo(t, filepath.Join(t.TempDir(), "proj"))
	town := filepath.Join(t.TempDir(), "town")
	bin := nowAgentBin(t)
	socket := nowSocket("default")
	env := nowTestEnv(t, home, bin, socket, false)
	env = append(env, "GT_DOLT_PORT="+strconv.Itoa(nowFreeTCPPort(t)))
	testutil.ReapOwnedDoltOnCleanup(t, town)
	stopNowDaemonOnCleanup(t, town)
	t.Cleanup(func() { killNowTmux(t, socket) })

	gtBinary := buildGT(t)
	out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach")
	if err != nil {
		t.Fatalf("gt now with default runtime failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "mix=cursor:high / cursor:low") {
		t.Fatalf("default mix should be cursor high/low:\n%s", out)
	}
	mix := readNowMix(t, gtBinary, town, env)
	assertRoleMix(t, mix, "mayor", "now-mayor", "high")
	assertRoleMix(t, mix, "witness", "now-workers", "low")
}

func TestNowMayorFlagsRestartMayorAndWorkersPersist(t *testing.T) {
	requireNowStack(t)
	home := t.TempDir()
	repo := createNowGitRepo(t, filepath.Join(t.TempDir(), "proj"))
	town := filepath.Join(t.TempDir(), "town")
	bin := nowAgentBin(t)
	socket := nowSocket("restart")
	env := nowTestEnv(t, home, bin, socket, false)
	env = append(env, "GT_DOLT_PORT="+strconv.Itoa(nowFreeTCPPort(t)))
	testutil.ReapOwnedDoltOnCleanup(t, town)
	stopNowDaemonOnCleanup(t, town)
	t.Cleanup(func() { killNowTmux(t, socket) })

	gtBinary := buildGT(t)
	if out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--mayor", "cursor:high", "--workers", "cursor:low"); err != nil {
		t.Fatalf("first gt now failed: %v\n%s", err, out)
	}
	created := nowSessionCreated(t, socket, "hq-mayor")
	if nowWitnessSessionExists(t, socket) {
		t.Fatal("Witness started before attach")
	}

	if out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--workers", "cursor:low"); err != nil {
		t.Fatalf("worker-only gt now failed: %v\n%s", err, out)
	}
	if nowSessionCreated(t, socket, "hq-mayor") != created {
		t.Fatal("worker flags restarted the Mayor session")
	}
	if nowWitnessSessionExists(t, socket) {
		t.Fatal("worker flags started Witness without --restart-workers")
	}

	if out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--mayor", "cursor:grok-4.6:high"); err != nil {
		t.Fatalf("mayor-change gt now failed: %v\n%s", err, out)
	}
	if nowSessionCreated(t, socket, "hq-mayor") == created {
		t.Fatal("new --mayor flags did not restart the Mayor session")
	}
	if !nowMayorSessionExists(t, socket) {
		t.Fatal("Mayor session missing after mayor mix change")
	}
}

func TestNowRestartWorkersStartsWitness(t *testing.T) {
	requireNowStack(t)
	home := t.TempDir()
	repo := createNowGitRepo(t, filepath.Join(t.TempDir(), "proj"))
	town := filepath.Join(t.TempDir(), "town")
	bin := nowAgentBinStay(t)
	socket := nowSocket("rw")
	env := nowTestEnv(t, home, bin, socket, false)
	env = append(env, "GT_DOLT_PORT="+strconv.Itoa(nowFreeTCPPort(t)))
	testutil.ReapOwnedDoltOnCleanup(t, town)
	stopNowDaemonOnCleanup(t, town)
	t.Cleanup(func() { killNowTmux(t, socket) })

	gtBinary := buildGT(t)
	if out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--mayor", "cursor:high", "--workers", "cursor:low"); err != nil {
		t.Fatalf("first gt now failed: %v\n%s", err, out)
	}
	if nowWitnessSessionExists(t, socket) {
		t.Fatal("Witness started before attach")
	}

	if out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--workers", "cursor:low", "--restart-workers"); err != nil {
		t.Fatalf("gt now --restart-workers failed: %v\n%s", err, out)
	}
	if !nowWitnessSessionExists(t, socket) {
		t.Fatal("--restart-workers did not start Witness")
	}
}

func TestNowDoctorFailsWhenMayorBinaryMissing(t *testing.T) {
	requireNowStack(t)
	home := t.TempDir()
	repo := createNowGitRepo(t, filepath.Join(t.TempDir(), "proj"))
	town := filepath.Join(t.TempDir(), "town")
	bin := nowAgentBin(t)
	socket := nowSocket("doctor")
	env := nowTestEnv(t, home, bin, socket, false)
	env = append(env, "GT_DOLT_PORT="+strconv.Itoa(nowFreeTCPPort(t)))
	testutil.ReapOwnedDoltOnCleanup(t, town)
	stopNowDaemonOnCleanup(t, town)
	t.Cleanup(func() { killNowTmux(t, socket) })

	gtBinary := buildGT(t)
	if out, err := runGTCmdMayFail(t, gtBinary, repo, env, "now", "--town", town, "--no-attach",
		"--mayor", "cursor:high", "--workers", "cursor:low"); err != nil {
		t.Fatalf("gt now failed: %v\n%s", err, out)
	}

	isolated := nowToolBin(t)
	doctorEnv := nowTestEnv(t, home, isolated, socket, true)
	out, err := runGTCmdMayFail(t, gtBinary, town, doctorEnv, "doctor")
	if err == nil {
		t.Fatalf("gt doctor succeeded without the Mayor binary:\n%s", out)
	}
	if !strings.Contains(out, "mayor-binary") && !strings.Contains(out, "cursor-agent") {
		t.Fatalf("doctor should fail on the missing Mayor binary:\n%s", out)
	}
}

func requireNowStack(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if _, err := exec.LookPath("dolt"); err != nil && !fileExists(filepath.Join(os.Getenv("HOME"), "go", "bin", "dolt")) {
		if _, err := exec.LookPath("dolt"); err != nil {
			t.Skip("dolt not installed")
		}
	}
	if _, err := lookPathExtra("bd"); err != nil {
		t.Skip("bd not installed")
	}
}

// nowSocket names a tmux socket for one test in one test-binary run. The PID
// keeps a town daemon that escaped an earlier run from finding this run's
// sessions: it would kill and respawn them under a shared socket name.
func nowSocket(name string) string {
	return fmt.Sprintf("nowt-%s-%d", name, os.Getpid())
}

// stopNowDaemonOnCleanup stops the town daemon that gt now started. A daemon
// detaches from its caller, so without this it outlives the test and keeps
// patrolling the town: it respawns and kills tmux sessions, and restarts Dolt,
// long after the town directory is gone. It stops only the daemon that owns
// townRoot, so a production daemon is never touched.
func stopNowDaemonOnCleanup(t *testing.T, townRoot string) {
	t.Helper()
	t.Cleanup(func() {
		if err := daemon.StopDaemon(townRoot); err != nil {
			t.Logf("town daemon cleanup skipped: %v", err)
		}
	})
}

func nowTestEnv(t *testing.T, home, bin, socket string, isolated bool) []string {
	t.Helper()
	writeNowHomeGitConfig(t, home)
	path := bin
	if isolated {
		path = strings.Join([]string{bin, "/usr/local/bin", "/usr/bin", "/bin"}, string(os.PathListSeparator))
	} else {
		path = bin + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	env := testutil.CleanGTEnv()
	filtered := make([]string, 0, len(env)+4)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "HOME", "PATH", "GT_TMUX_SOCKET", "BEADS_DOLT_AUTO_START", "GT_DOLT_PORT", "GT_TOWN_ROOT", "GT_ROOT":
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered,
		"HOME="+home,
		"PATH="+path,
		"GT_TMUX_SOCKET="+socket,
		"BEADS_DOLT_AUTO_START=0",
	)
}

func writeNowHomeGitConfig(t *testing.T, home string) {
	t.Helper()
	content := "[user]\n\tname = Test User\n\temail = test@test.com\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(content), 0644); err != nil {
		t.Fatalf("write gitconfig: %v", err)
	}
}

func nowToolBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"git", "tmux", "dolt", "bd", "sh", "bash", "true", "false", "ps", "lsof", "ss"} {
		src, err := lookPathExtra(name)
		if err != nil {
			continue
		}
		if err := os.Symlink(src, filepath.Join(dir, name)); err != nil {
			t.Fatalf("symlink %s: %v", name, err)
		}
	}
	return dir
}

func nowAgentBinStay(t *testing.T) string {
	t.Helper()
	dir := nowToolBin(t)
	script := "#!/bin/sh\nexec sleep 3600\n"
	if err := os.WriteFile(filepath.Join(dir, "cursor-agent"), []byte(script), 0755); err != nil {
		t.Fatalf("write cursor-agent: %v", err)
	}
	return dir
}

func nowPiBinStay(t *testing.T) string {
	t.Helper()
	dir := nowToolBin(t)
	script := `#!/bin/sh
file=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-e" ]; then
    file="$arg"
  fi
  prev="$arg"
done
if [ -n "$file" ] && [ ! -f "$file" ]; then
  echo "Extension path does not exist: $file" >&2
  exit 1
fi
exec sleep 3600
`
	if err := os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0755); err != nil {
		t.Fatalf("write pi: %v", err)
	}
	return dir
}

func nowAgentBin(t *testing.T) string {
	t.Helper()
	dir := nowToolBin(t)
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "cursor-agent"), []byte(script), 0755); err != nil {
		t.Fatalf("write cursor-agent: %v", err)
	}
	return dir
}

func createNowGitRepo(t *testing.T, repoDir string) string {
	t.Helper()
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args[1:], err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# proj\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{{"git", "add", "."}, {"git", "commit", "-m", "init"}} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args[1:], err, out)
		}
	}
	if resolved, err := filepath.EvalSymlinks(repoDir); err == nil {
		return resolved
	}
	return repoDir
}

func assertNoTownFilesInRepo(t *testing.T, repo string) {
	t.Helper()
	for _, name := range []string{"mayor", "deacon", "AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(repo, name)); !os.IsNotExist(err) {
			t.Fatalf("project git repository gained %s", name)
		}
	}
}

type nowMixReport struct {
	config.TownMix
	CostLikeClaudePreset bool
}

func readNowMix(t *testing.T, gtBinary, town string, env []string) nowMixReport {
	t.Helper()
	output := runGTCmdOutput(t, gtBinary, town, env, "config", "mix", "--json")
	var mix config.TownMix
	if err := json.Unmarshal([]byte(output), &mix); err != nil {
		t.Fatalf("unmarshal mix JSON: %v\n%s", err, output)
	}
	report := nowMixReport{TownMix: mix}
	for _, entry := range mix.Roles {
		if strings.Contains(entry.Agent, "claude") || entry.Agent == "opus" {
			report.CostLikeClaudePreset = true
		}
	}
	return report
}

func assertRoleMix(t *testing.T, mix nowMixReport, role, agent, effort string) {
	t.Helper()
	for _, entry := range mix.Roles {
		if entry.Name == role {
			if entry.Agent != agent {
				t.Fatalf("role %s agent = %q, want %q", role, entry.Agent, agent)
			}
			if entry.Effort != effort {
				t.Fatalf("role %s effort = %q, want %q", role, entry.Effort, effort)
			}
			return
		}
	}
	t.Fatalf("role %s missing from mix", role)
}

func assertRoleProvider(t *testing.T, mix nowMixReport, role, provider string) {
	t.Helper()
	for _, entry := range mix.Roles {
		if entry.Name == role {
			if entry.Provider != provider {
				t.Fatalf("role %s provider = %q, want %q", role, entry.Provider, provider)
			}
			return
		}
	}
	t.Fatalf("role %s missing from mix", role)
}

func nowMayorSessionExists(t *testing.T, socket string) bool {
	t.Helper()
	return nowSessionExists(t, socket, "hq-mayor")
}

func nowMayorHasClient(t *testing.T, socket string) bool {
	t.Helper()
	cmd := exec.Command("tmux", "-L", socket, "list-clients", "-t", "hq-mayor")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

func nowMayorPaneDead(t *testing.T, socket string) bool {
	t.Helper()
	cmd := exec.Command("tmux", "-L", socket, "display-message", "-t", "hq-mayor", "-p", "#{pane_dead}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) == "1"
}

func nowWitnessSessionExists(t *testing.T, socket string) bool {
	t.Helper()
	tm := tmux.NewTmuxWithSocket(socket)
	sessions, err := tm.ListSessions()
	if err != nil {
		t.Logf("ListSessions: %v", err)
		return false
	}
	for _, name := range sessions {
		if strings.Contains(name, "witness") {
			return true
		}
	}
	return false
}

func nowSessionExists(t *testing.T, socket, name string) bool {
	t.Helper()
	tm := tmux.NewTmuxWithSocket(socket)
	ok, err := tm.HasSession(name)
	if err != nil {
		t.Logf("HasSession: %v", err)
		return false
	}
	return ok
}

func nowSessionCreated(t *testing.T, socket, session string) string {
	t.Helper()
	cmd := exec.Command("tmux", "-L", socket, "display-message", "-t", session, "-p", "#{session_created}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("session_created for %s: %v\n%s", session, err, out)
	}
	return strings.TrimSpace(string(out))
}

func doltPortFromNowOutput(t *testing.T, out string) int {
	t.Helper()
	for _, field := range strings.Fields(out) {
		after, ok := strings.CutPrefix(field, "dolt=")
		if !ok {
			continue
		}
		port, err := strconv.Atoi(strings.TrimRight(after, "\n"))
		if err != nil {
			t.Fatalf("parse dolt port from %q: %v", field, err)
		}
		return port
	}
	t.Fatalf("no dolt= field in output:\n%s", out)
	return 0
}

func killNowTmux(t *testing.T, socket string) {
	t.Helper()
	cmd := exec.Command("tmux", "-L", socket, "kill-server")
	_ = cmd.Run()
}

func lookPathExtra(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	candidates := []string{
		filepath.Join(os.Getenv("HOME"), "go", "bin", name),
		filepath.Join("/home/ubuntu/go/bin", name),
		filepath.Join("/usr/local/bin", name),
		filepath.Join("/exec-daemon", name),
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

func nowFreeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}
	return port
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
