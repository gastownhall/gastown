package cmd

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/worker"
)

func writeTownMarker(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mayor", "town.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveRigsConfig(filepath.Join(root, "mayor", "rigs.json"), &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs: map[string]config.RigEntry{
			"gastown": {
				GitURL:    "https://example.com/gastown.git",
				LocalRepo: filepath.Join(root, "gastown"),
				BeadsConfig: &config.BeadsConfig{
					Repo:   "local",
					Prefix: "gt",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return root
}

var cachedTestAgent string

func buildTestAgent(t *testing.T) string {
	t.Helper()
	if cachedTestAgent != "" {
		if _, err := os.Stat(cachedTestAgent); err == nil {
			return cachedTestAgent
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := wd
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("could not find go.mod")
		}
		root = parent
	}
	out := filepath.Join(os.TempDir(), "gt-test-agent-contract")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/gt-test-agent")
	cmd.Dir = root
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build gt-test-agent: %v\n%s", err, data)
	}
	cachedTestAgent = out
	return out
}

type workerTown struct {
	root    string
	gt      string
	agent   string
	server  *worker.Worker
	session string
	bead    string
	runID   string
	ctl     string
	cmd     *exec.Cmd
}

func setupWorkerTown(t *testing.T, sessionName, beadID string) *workerTown {
	t.Helper()
	t.Setenv("GT_WORKER_HEALTH_GRACE", "300ms")
	t.Setenv("GT_WORKER_ACK_WAIT", "2s")
	t.Setenv("GT_TEST_NUDGE_LOG", "")

	root := writeTownMarker(t)

	srv, err := worker.Listen(root, worker.NewTmuxAdapter())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	return &workerTown{
		root:    root,
		gt:      buildGT(t),
		agent:   buildTestAgent(t),
		server:  srv,
		session: sessionName,
		bead:    beadID,
	}
}

func (wt *workerTown) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(wt.gt, args...)
	cmd.Dir = wt.root
	cmd.Env = workerTestEnv(wt.root)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stdout.String()
	if err != nil {
		return out + stderr.String(), err
	}
	return out, nil
}

func (wt *workerTown) startRun(t *testing.T, role, agentType string) {
	t.Helper()
	out, err := wt.run(t, "worker", "start-run",
		"--town", wt.root,
		"--session", wt.session,
		"--bead", wt.bead,
		"--role", role,
		"--rig", "gastown",
		"--agent", "toast",
		"--agent-type", agentType,
	)
	if err != nil {
		t.Fatalf("start-run: %v\n%s", err, out)
	}
	wt.runID = lastNonEmptyLine(out)
	if wt.runID == "" {
		t.Fatalf("start-run printed empty run_id\n%s", out)
	}
}

func (wt *workerTown) startAgent(t *testing.T, state string, extra ...string) {
	t.Helper()
	wt.ctl = filepath.Join(wt.root, ".runtime", "worker", "agents", strings.ReplaceAll(wt.session, "/", "_"))
	args := []string{
		"--town", wt.root,
		"--run-id", wt.runID,
		"--session", wt.session,
		"--bead", wt.bead,
		"--agent-type", "pi",
		"--state", state,
		"--ctl-dir", wt.ctl,
	}
	args = append(args, extra...)
	cmd := exec.Command(wt.agent, args...)
	cmd.Dir = wt.root
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start test-agent: %v", err)
	}
	wt.cmd = cmd
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	waitFile(t, filepath.Join(wt.ctl, "ready"), 5*time.Second)
	waitEvent(t, wt.root, state, wt.runID, 5*time.Second)
}

func (wt *workerTown) ctlWrite(t *testing.T, line string) {
	t.Helper()
	path := filepath.Join(wt.ctl, "inbox")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func workerTestEnv(townRoot string) []string {
	var env []string
	for _, e := range os.Environ() {
		switch {
		case strings.HasPrefix(e, "GT_ROLE="),
			strings.HasPrefix(e, "GT_POLECAT="),
			strings.HasPrefix(e, "GT_TEST_NUDGE_LOG="):
			continue
		default:
			env = append(env, e)
		}
	}
	return append(env, "GT_TOWN_ROOT="+townRoot)
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return ""
}

func waitFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func waitEvent(t *testing.T, townRoot, eventType, runID string, timeout time.Duration) worker.Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evs, err := worker.ReadEvents(townRoot)
		if err == nil {
			for _, ev := range evs {
				if ev.Type == eventType && (runID == "" || ev.RunID == runID) {
					return ev
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	evs, _ := worker.ReadEvents(townRoot)
	data, _ := os.ReadFile(filepath.Join(townRoot, ".runtime", "worker", "events.jsonl"))
	t.Fatalf("timed out waiting for event %s run=%s\nparsed=%+v\nraw=%s", eventType, runID, evs, data)
	return worker.Event{}
}

func waitJSONL(t *testing.T, path string, n int, timeout time.Duration) [][]byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		lines := readJSONL(path)
		if len(lines) >= n {
			return lines
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d jsonl lines in %s", n, path)
	return nil
}

func readJSONL(path string) [][]byte {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var lines [][]byte
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			lines = append(lines, []byte(line))
		}
	}
	return lines
}

func TestWorkerSlingPrime_ReadyAndAccepted(t *testing.T) {
	wt := setupWorkerTown(t, "gt-worker-prime", "gt-abc1")
	wt.startRun(t, "polecat", "pi")
	wt.startAgent(t, worker.EventReady)

	out, err := wt.run(t, "worker", "deliver", wt.session,
		"--town", wt.root,
		"-m", "Hook: "+wt.bead,
		"--source", worker.SourcePrime,
		"--priority", worker.PrioritySystem,
		"--json",
	)
	if err != nil {
		t.Fatalf("first prompt: %v\n%s", err, out)
	}
	var d worker.Delivery
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("delivery json: %v\n%s", err, out)
	}
	if !d.Accepted && !d.Queued {
		t.Fatalf("first prompt neither accepted nor queued: %+v", d)
	}
	if d.Adapter != worker.AdapterProtocol {
		t.Fatalf("adapter = %q, want protocol", d.Adapter)
	}

	ready := waitEvent(t, wt.root, worker.EventReady, wt.runID, time.Second)
	if ready.BeadID != wt.bead && ready.RunID != wt.runID {
		t.Fatalf("ready event missing bead/run: %+v", ready)
	}
	run, err := worker.ReadRun(wt.root, wt.runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.BeadID != wt.bead {
		t.Fatalf("hook bead = %q, want %q", run.BeadID, wt.bead)
	}
}

func TestWorkerNormalNudge_QueuesWhileBusyThenDelivers(t *testing.T) {
	wt := setupWorkerTown(t, "gt-worker-nudge", "gt-nud1")
	wt.startRun(t, "polecat", "pi")
	wt.startAgent(t, worker.EventBusy)

	out, err := wt.run(t, "nudge", wt.session, "-m", "status please",
		"--mode", "wait-idle", "--priority", "normal")
	if err != nil {
		t.Fatalf("normal nudge: %v\n%s", err, out)
	}
	if !strings.Contains(out, "queued") && !strings.Contains(out, "Nudged") {
		t.Fatalf("nudge output missing queued/nudged: %s", out)
	}
	waitEvent(t, wt.root, "queued", wt.runID, 3*time.Second)
	if _, err := os.Stat(filepath.Join(wt.ctl, "interrupted")); err == nil {
		t.Fatal("normal nudge set interrupt flag")
	}
	if lines := readJSONL(filepath.Join(wt.ctl, "prompts.jsonl")); len(lines) != 0 {
		t.Fatalf("busy session received prompt before idle: %s", lines)
	}

	wt.ctlWrite(t, worker.EventIdle)
	waitJSONL(t, filepath.Join(wt.ctl, "prompts.jsonl"), 1, 5*time.Second)
	waitEvent(t, wt.root, "delivered", wt.runID, 3*time.Second)
}

func TestWorkerMail_NoNewTurnWhileBusy(t *testing.T) {
	wt := setupWorkerTown(t, "gt-toast", "gt-mail1")
	wt.startRun(t, "polecat", "pi")
	wt.startAgent(t, worker.EventBusy)

	box := mail.NewMailbox(filepath.Join(wt.root, "mail", "toast"))
	msg := mail.NewMessage("mayor/", "gastown/polecats/toast", "hook update", "please read")
	if err := box.Append(msg); err != nil {
		t.Fatalf("persist mail: %v", err)
	}
	got, err := box.Get(msg.ID)
	if err != nil || got == nil {
		t.Fatalf("mail record missing after append: %v", err)
	}

	out, err := wt.run(t, "worker", "deliver", wt.session,
		"--town", wt.root,
		"-m", "You have new mail from mayor/: hook update",
		"--source", worker.SourceMail,
		"--priority", worker.PriorityNormal,
		"--json",
	)
	if err != nil {
		t.Fatalf("mail deliver: %v\n%s", err, out)
	}
	var d worker.Delivery
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("delivery json: %v\n%s", err, out)
	}
	if !d.Queued {
		t.Fatalf("mail to busy session must queue, got %+v", d)
	}
	if _, err := os.Stat(filepath.Join(wt.ctl, "interrupted")); err == nil {
		t.Fatal("mail started a new turn (interrupt flag)")
	}
	if lines := readJSONL(filepath.Join(wt.ctl, "prompts.jsonl")); len(lines) != 0 {
		t.Fatalf("mail opened a turn while busy: %s", lines)
	}

	wt.ctlWrite(t, worker.EventIdle)
	lines := waitJSONL(t, filepath.Join(wt.ctl, "prompts.jsonl"), 1, 5*time.Second)
	if !strings.Contains(string(lines[0]), "You have new mail") {
		t.Fatalf("mail not visible after idle: %s", lines[0])
	}
}

func TestWorkerCosts_ByRoleIncludesPi(t *testing.T) {
	wt := setupWorkerTown(t, "gt-worker-cost", "gt-cost1")
	wt.startRun(t, "polecat", "pi")
	wt.startAgent(t, worker.EventReady)
	wt.ctlWrite(t, "telemetry 0.25")
	waitFile(t, filepath.Join(wt.ctl, "telemetry"), 5*time.Second)
	recs, err := worker.ReadCosts(wt.root)
	if err != nil || len(recs) == 0 {
		errData, _ := os.ReadFile(filepath.Join(wt.ctl, "telemetry.err"))
		t.Fatalf("production cost store empty after telemetry: %v errfile=%s", err, errData)
	}

	out, err := wt.run(t, "costs", "--by-role", "--json")
	if err != nil {
		t.Fatalf("gt costs: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"gt-cost1"`) {
		t.Fatalf("costs missing bead id:\n%s\nstore=%+v", out, recs)
	}
	if !strings.Contains(out, `"pi"`) {
		t.Fatalf("costs missing agent type:\n%s", out)
	}
	if !strings.Contains(out, "0.25") {
		t.Fatalf("costs missing non-zero cost from store:\n%s", out)
	}
	recs, err = worker.ReadCosts(wt.root)
	if err != nil || len(recs) == 0 {
		t.Fatalf("production cost store empty: %v", err)
	}
	if recs[0].BeadID != "gt-cost1" || recs[0].AgentType != "pi" || recs[0].CostUSD != 0.25 {
		t.Fatalf("cost record = %+v", recs[0])
	}
}

func TestWorkerFailClosedDeliver_UnknownState(t *testing.T) {
	wt := setupWorkerTown(t, "gt-worker-dead", "gt-dead1")
	wt.startRun(t, "polecat", "pi")
	wt.startAgent(t, worker.EventReady)

	pidData, err := os.ReadFile(filepath.Join(wt.ctl, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		t.Fatal(err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find test-agent: %v", err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill test-agent: %v", err)
	}
	_, _ = wt.cmd.Process.Wait()

	out, err := wt.run(t, "nudge", wt.session, "-m", "are you there",
		"--mode", "wait-idle", "--priority", "normal")
	if err == nil {
		t.Fatalf("nudge after silent death must fail, got:\n%s", out)
	}
	run, rerr := worker.ReadRun(wt.root, wt.runID)
	if rerr != nil {
		t.Fatalf("run missing after failed nudge: %v", rerr)
	}
	if run.State == worker.StateStopped {
		t.Fatal("failed nudge must not stop the session")
	}
}

func TestWorkerFailClosedAuthorize_Unreachable(t *testing.T) {
	wt := setupWorkerTown(t, "gt-worker-auth", "gt-auth1")
	wt.startRun(t, "polecat", "pi")
	wt.startAgent(t, worker.EventReady)
	if err := wt.server.Close(); err != nil {
		t.Fatal(err)
	}
	wt.ctlWrite(t, "authorize git push --force")
	lines := waitJSONL(t, filepath.Join(wt.ctl, "authorize.jsonl"), 1, 5*time.Second)
	var dec worker.AuthorizeDecision
	if err := json.Unmarshal(lines[0], &dec); err != nil {
		t.Fatal(err)
	}
	if dec.Allowed {
		t.Fatal("unreachable authorize must deny")
	}
	if dec.Reason == "" {
		t.Fatal("deny must include a reason")
	}
}

func TestWorkerWitness_BusyBlocksStallAndHealthTimeout(t *testing.T) {
	sessionName := session.PolecatSessionName(session.PrefixFor("gastown"), "toast")
	wt := setupWorkerTown(t, sessionName, "gt-wit1")
	if err := os.MkdirAll(filepath.Join(wt.root, "gastown", "polecats", "toast"), 0o755); err != nil {
		t.Fatal(err)
	}
	wt.startRun(t, "polecat", "pi")
	wt.startAgent(t, worker.EventBusy)

	busy := witness.DetectStalledPolecats(wt.root, "gastown")
	for _, s := range busy.Stalled {
		if s.PolecatName == "toast" {
			t.Fatalf("busy session received stall action %+v", s)
		}
	}

	if err := wt.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = wt.cmd.Process.Wait()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h, err := worker.StoreHealth(wt.root, sessionName, 0)
		if err == nil && h.Status == worker.HealthUnhealthy {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h, err := worker.StoreHealth(wt.root, sessionName, 0)
	if err != nil || h.Status != worker.HealthUnhealthy {
		t.Fatalf("expected unhealthy after grace, got %+v err=%v", h, err)
	}

	stalled := witness.DetectStalledPolecats(wt.root, "gastown")
	found := false
	for _, s := range stalled.Stalled {
		if s.PolecatName == "toast" && s.StallType == "unhealthy" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected unhealthy in persisted stall state, got %+v", stalled.Stalled)
	}
	if worker.StoppedWithoutDone(wt.root, wt.bead) {
		t.Fatal("missing pane/health must not count as stopped-without-done")
	}
}

func TestWorkerOneSessionPerBead(t *testing.T) {
	wt := setupWorkerTown(t, "gt-worker-once", "gt-once1")
	wt.startRun(t, "polecat", "pi")
	wt.startAgent(t, worker.EventReady)

	out, err := wt.run(t, "worker", "start-run",
		"--town", wt.root,
		"--session", "gt-worker-once-2",
		"--bead", wt.bead,
		"--role", "polecat",
	)
	if err == nil {
		t.Fatalf("second start-run must fail, got %s", out)
	}
	if !strings.Contains(out, "live run") && !strings.Contains(out, "already") {
		t.Fatalf("second start-run error = %s", out)
	}

	slingOut, slingErr := wt.run(t, "sling", wt.bead, "gastown")
	if slingErr == nil {
		t.Fatalf("second sling must fail, got %s", slingOut)
	}
	if !strings.Contains(slingOut, "live run") {
		t.Fatalf("sling error missing live run: %s", slingOut)
	}

	live, err := worker.LiveRunFromStore(wt.root, wt.bead)
	if err != nil || live == nil {
		t.Fatalf("live run missing: %v", err)
	}
	if live.RunID != wt.runID {
		t.Fatalf("run_id changed: %s vs %s", live.RunID, wt.runID)
	}
}

func TestWorkerTmuxAdapter_NoProtocolClient(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("GT_WORKER_HEALTH_GRACE", "300ms")
	root := writeTownMarker(t)
	socket := "gt-worker-" + strings.ReplaceAll(t.Name(), "/", "-")
	sessionName := "gt-tmux-fallback"
	cmd := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", sessionName, "sh", "-c", "cat")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("tmux", "-L", socket, "has-session", "-t", sessionName).Run() == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	tm := tmux.NewTmuxWithSocket(socket)
	srv, err := worker.Listen(root, &worker.TmuxAdapter{T: tm})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	gt := buildGT(t)
	cmdStart := exec.Command(gt, "worker", "start-run",
		"--town", root, "--session", sessionName, "--bead", "gt-tmux1", "--role", "polecat",
	)
	cmdStart.Dir = root
	var stdout strings.Builder
	cmdStart.Stdout = &stdout
	cmdStart.Stderr = nil
	if err := cmdStart.Run(); err != nil {
		t.Fatalf("start-run without protocol client: %v\n%s", err, stdout.String())
	}
	runID := strings.TrimSpace(stdout.String())
	run, err := worker.ReadRun(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Adapter != worker.AdapterTmux {
		t.Fatalf("adapter = %q, want tmux", run.Adapter)
	}

	del, err := exec.Command(gt, "worker", "deliver", sessionName,
		"--town", root, "-m", "hello tmux", "--source", worker.SourceNudge, "--json",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux deliver: %v\n%s", err, del)
	}
	if !strings.Contains(string(del), `"adapter": "tmux"`) && !strings.Contains(string(del), `"adapter":"tmux"`) {
		t.Fatalf("deliver did not use tmux adapter:\n%s", del)
	}
}

func TestWorkerAdapterChoice_ProtocolWhenConnected(t *testing.T) {
	wt := setupWorkerTown(t, "gt-worker-choice", "gt-ch1")
	wt.startRun(t, "polecat", "cursor")
	wt.startAgent(t, worker.EventReady)
	out, err := wt.run(t, "worker", "deliver", wt.session,
		"--town", wt.root, "-m", "hello protocol",
		"--source", worker.SourceNudge, "--priority", worker.PriorityUrgent, "--json",
	)
	if err != nil {
		t.Fatalf("protocol deliver: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"adapter": "protocol"`) && !strings.Contains(out, `"adapter":"protocol"`) {
		t.Fatalf("connected agent must use protocol adapter:\n%s", out)
	}
	ev := waitEvent(t, wt.root, "delivered", wt.runID, 3*time.Second)
	if ev.Payload["adapter"] != nil && ev.Payload["adapter"] != worker.AdapterProtocol {
		t.Fatalf("persisted adapter = %v", ev.Payload["adapter"])
	}
}
