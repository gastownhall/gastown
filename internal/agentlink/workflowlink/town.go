package workflowlink

// This file wires workflowlink to a real town: rigs from mayor/rigs.json,
// the Dolt evidence store, beads-backed items, polecat sessions from tmux,
// git diffstats, and sling/unsling by exec'ing the gtb binary (argv only,
// no shell). The daemon uses NewTown; tests use the pure types above.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql" // Dolt evidence store

	"github.com/steveyegge/gastown/internal/agentlink"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workflow"
)

// execTimeout bounds a sling or unsling run.
const execTimeout = 5 * time.Minute

// Town is the production adapter set for one town.
type Town struct {
	Root string
	Logf func(format string, args ...any)

	mu   sync.Mutex
	envs map[string]workflow.GateEnv // rig -> env; the db stays open for the daemon's life
}

// NewTown returns the adapters for the town at root.
func NewTown(root string, logf func(string, ...any)) *Town {
	return &Town{Root: root, Logf: logf, envs: map[string]workflow.GateEnv{}}
}

func (t *Town) logf(format string, args ...any) {
	if t.Logf != nil {
		t.Logf(format, args...)
	}
}

// Rigs lists the town's rigs from mayor/rigs.json. A rig is gated when its
// settings enable the gated workflow.
func (t *Town) Rigs() ([]agentlink.Rig, error) {
	rc, err := config.LoadRigsConfig(filepath.Join(t.Root, "mayor", "rigs.json"))
	if err != nil {
		return nil, err
	}
	var out []agentlink.Rig
	for name, e := range rc.Rigs {
		r := agentlink.Rig{Name: name, Remote: RedactRemote(e.GitURL)}
		if rigNameRe.MatchString(name) {
			if cfg, err := workflow.LoadConfig(filepath.Join(t.Root, name)); err == nil && cfg.Enabled {
				r.Gated = true
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Env opens (and caches) the workflow environment of a gated rig, the same
// way the gtb workflow commands build theirs. It is not observed: daemon
// writes are reported by the applier's Observer wrapping, see Applier.
func (t *Town) Env(rigName string) (workflow.GateEnv, error) {
	if !rigNameRe.MatchString(rigName) {
		return workflow.GateEnv{}, fmt.Errorf("invalid rig name %q", rigName)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if env, ok := t.envs[rigName]; ok {
		return env, nil
	}
	rigPath := filepath.Join(t.Root, rigName)
	cfg, err := workflow.LoadConfig(rigPath)
	if err != nil {
		return workflow.GateEnv{}, err
	}
	beadsDir := beads.ResolveBeadsDir(rigPath)
	dbName := beads.DatabaseNameFromMetadata(beadsDir)
	if dbName == "" {
		return workflow.GateEnv{}, fmt.Errorf("rig %s has no dolt_database in %s/metadata.json", rigName, beadsDir)
	}
	dc := doltserver.DefaultConfig(t.Root)
	user := dc.User
	if user == "" {
		user = "root"
	}
	if dc.Password != "" {
		user += ":" + dc.Password
	}
	db, err := sql.Open("mysql", fmt.Sprintf("%s@tcp(%s)/%s?parseTime=true&timeout=5s", user, dc.HostPort(), dbName))
	if err != nil {
		return workflow.GateEnv{}, err
	}
	store, err := workflow.NewDoltStore(db)
	if err != nil {
		_ = db.Close()
		return workflow.GateEnv{}, err
	}
	env := workflow.GateEnv{
		TownRoot: t.Root, Rig: rigName, Store: store,
		Items: workflow.NewBeadsItems(beads.New(rigPath)), Config: cfg,
	}
	t.envs[rigName] = env
	return env, nil
}

// observedEnv returns Env(rig) wrapped so writes queue events.
func (t *Town) observedEnv(q *agentlink.Queue) func(string) (workflow.GateEnv, error) {
	return func(rigName string) (workflow.GateEnv, error) {
		env, err := t.Env(rigName)
		if err != nil {
			return env, err
		}
		return Observe(env, q, &ObserveOptions{DiffStat: t.DiffStat, Logf: t.Logf}), nil
	}
}

// repoGit returns a Git for the rig's repo base: the bare .repo.git when
// present, else mayor/rig.
func (t *Town) repoGit(rigName string) (*git.Git, error) {
	rigPath := filepath.Join(t.Root, rigName)
	if fi, err := os.Stat(filepath.Join(rigPath, ".repo.git")); err == nil && fi.IsDir() {
		return git.NewGitWithDir(filepath.Join(rigPath, ".repo.git"), ""), nil
	}
	if fi, err := os.Stat(filepath.Join(rigPath, "mayor", "rig")); err == nil && fi.IsDir() {
		return git.NewGit(filepath.Join(rigPath, "mayor", "rig")), nil
	}
	return nil, fmt.Errorf("rig %s has no repo base", rigName)
}

// DiffStat returns the diffstat of branch against the rig's default branch
// in the rig's repo base (base...branch). Branches are resolved locally
// first, then from origin.
func (t *Town) DiffStat(rigName, branch string) ([]agentlink.FileStat, error) {
	if !rigNameRe.MatchString(rigName) || branch == "" || strings.HasPrefix(branch, "-") {
		return nil, errors.New("invalid rig or branch")
	}
	g, err := t.repoGit(rigName)
	if err != nil {
		return nil, err
	}
	base := "main"
	if rc, err := rig.LoadRigConfig(filepath.Join(t.Root, rigName)); err == nil && rc.DefaultBranch != "" {
		base = rc.DefaultBranch
	}
	ref := func(b string) string {
		for _, r := range []string{"refs/heads/" + b, "refs/remotes/origin/" + b} {
			if ok, err := g.RefExists(r); err == nil && ok {
				return r
			}
		}
		return ""
	}
	baseRef, branchRef := ref(base), ref(branch)
	if baseRef == "" || branchRef == "" {
		return nil, fmt.Errorf("branch %s or base %s not found", branch, base)
	}
	out, err := g.DiffNumStat(baseRef + "..." + branchRef)
	if err != nil {
		return nil, err
	}
	return parseNumStat(out), nil
}

// parseNumStat parses `git diff --numstat` output. Binary files ("-" counts)
// get zero counts.
func parseNumStat(out string) []agentlink.FileStat {
	var fs []agentlink.FileStat
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		added, _ := strconv.Atoi(parts[0])
		deleted, _ := strconv.Atoi(parts[1])
		fs = append(fs, agentlink.FileStat{Path: parts[2], Added: added, Deleted: deleted})
	}
	return fs
}

// polecats returns the polecats of every rig, with the issue each has hooked
// (from the work bead's status=hooked + assignee, as `gt polecat list`).
func (t *Town) polecats() ([]*polecat.Polecat, error) {
	rc, err := config.LoadRigsConfig(filepath.Join(t.Root, "mayor", "rigs.json"))
	if err != nil {
		return nil, err
	}
	mgr := rig.NewManager(t.Root, rc, git.NewGit(t.Root))
	rigs, err := mgr.DiscoverRigs()
	if err != nil {
		return nil, err
	}
	var out []*polecat.Polecat
	for _, r := range rigs {
		ps, err := polecat.NewManager(r, git.NewGit(r.Path), nil).List() // nil tmux: listing only
		if err != nil {
			t.logf("agentlink: list polecats of %s: %v", r.Name, err)
			continue
		}
		out = append(out, ps...)
	}
	return out, nil
}

// Sessions lists running polecat sessions with the item each works on.
func (t *Town) Sessions() ([]agentlink.SessionView, error) {
	ps, err := t.polecats()
	if err != nil {
		return nil, err
	}
	tm := tmux.NewTmux()
	var out []agentlink.SessionView
	for _, p := range ps {
		name := session.PolecatSessionName(session.PrefixFor(p.Rig), p.Name)
		if ok, err := tm.HasSession(name); err != nil || !ok {
			continue
		}
		sv := agentlink.SessionView{Name: name, Role: string(session.RolePolecat), Rig: p.Rig, ItemID: p.Issue, State: string(p.State)}
		if at, err := tm.GetSessionActivity(name); err == nil {
			sv.LastActivity = at.UTC()
		}
		out = append(out, sv)
	}
	return out, nil
}

// Peek captures the last lines of the pane of the polecat that has itemID
// hooked in rig.
func (t *Town) Peek(_ context.Context, rigName, itemID string) (string, error) {
	ps, err := t.polecats()
	if err != nil {
		return "", err
	}
	name := sessionForItem(ps, rigName, itemID)
	if name == "" {
		return "", fmt.Errorf("no polecat session for %s", itemID)
	}
	// -J joins wrapped lines so a secret split across screen lines is
	// redacted whole (R38).
	return tmux.NewTmux().CapturePaneJoined(name, peekLines)
}

// sessionForItem returns the tmux session of the polecat in rig whose hooked
// issue is itemID, or "".
func sessionForItem(ps []*polecat.Polecat, rigName, itemID string) string {
	for _, p := range ps {
		if p.Rig == rigName && p.Issue == itemID {
			return session.PolecatSessionName(session.PrefixFor(p.Rig), p.Name)
		}
	}
	return ""
}

// execTailBytes caps the command output quoted in an ExecGtb error.
const execTailBytes = 800

// execTail redacts the whole output first, then keeps its last
// execTailBytes, dropping the line the cut lands in (R38).
func execTail(out string) string {
	red := RedactText(out)
	tail := lastBytes(red, execTailBytes)
	return SafeTail(tail, len(tail) < len(red))
}

// ExecGtb runs the gtb binary at exe with args (argv only, no shell) in the
// town root, returning an error carrying the tail of its output on failure.
func (t *Town) ExecGtb(ctx context.Context, exe string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...) //nolint:gosec // G204: fixed subcommands, validated IDs
	cmd.Dir = t.Root
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gtb %s: %w: %s", args[0], err, strings.TrimSpace(execTail(string(out))))
	}
	return nil
}

// cancel stops itemID's worker through the existing CLI paths: it finds the
// polecat that has the item hooked, runs `gtb unsling <id> <rig>/polecats/<name>
// --force` (the daemon is not the polecat, so the target must be explicit,
// and --force because cancelled work is by definition incomplete), then
// `gtb session stop <rig>/<name>` to stop the session.
func (t *Town) cancel(ctx context.Context, exe, itemID string) error {
	ps, err := t.polecats()
	if err != nil {
		return err
	}
	var p *polecat.Polecat
	for _, c := range ps {
		if c.Issue == itemID {
			p = c
			break
		}
	}
	if p == nil {
		return fmt.Errorf("item %s is not hooked by any polecat", itemID)
	}
	if err := t.ExecGtb(ctx, exe, "unsling", itemID, p.Rig+"/polecats/"+p.Name, "--force"); err != nil {
		return err
	}
	if err := t.ExecGtb(ctx, exe, "session", "stop", p.Rig+"/"+p.Name); err != nil {
		return fmt.Errorf("unslung, but stopping the session failed: %w", err)
	}
	return nil
}

// Applier returns the production applier. Its writes are observed into q.
func (t *Town) Applier(q *agentlink.Queue, exe string) *WorkflowApplier {
	return &WorkflowApplier{
		TownRoot: t.Root,
		Rigs:     t.Rigs,
		Env:      t.observedEnv(q),
		Sling: func(ctx context.Context, itemID, rigName string) error {
			return t.ExecGtb(ctx, exe, "sling", itemID, rigName)
		},
		Cancel:   func(ctx context.Context, itemID string) error { return t.cancel(ctx, exe, itemID) },
		Peek:     t.Peek,
		DiffStat: t.DiffStat,
	}
}

// Collector returns the production collector.
func (t *Town) Collector(q *agentlink.Queue) *WorkflowCollector {
	return &WorkflowCollector{
		TownRoot: t.Root, Rigs: t.Rigs, Env: t.Env, Queue: q,
		Sessions: t.Sessions, DiffStat: t.DiffStat, Logf: t.Logf,
	}
}
