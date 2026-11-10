package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/workflow"
)

// Exit codes of the gated workflow commands.
const (
	wfExitError    = 1 // any other error
	wfExitRefused  = 3 // gate refused, run refused, check not passing
	wfExitConflict = 4 // workflow.ErrStepConflict
)

const (
	wfDefaultTimeout = 10 * time.Minute
	msgNotHuman      = "approvals must come from a human"
	msgNotConfirmed  = "not approved"
)

// wfAnnotationRequiresEnabled marks a command that refuses disabled rigs, so
// newWorkflowEnv can refuse before opening the rig's database.
const wfAnnotationRequiresEnabled = "gtb-workflow-requires-enabled"

func errWorkflowDisabled(rigName string) error {
	return fmt.Errorf("gated workflow is not enabled for rig %s (set workflow.gated.enabled in settings/config.json)", rigName)
}

var wfRigNameRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// newWorkflowEnv builds the workflow environment for rig ("" infers it from
// the current directory). It is the only place the workflow commands touch
// real infrastructure (Dolt, bd, git); tests replace it with an in-memory env.
var newWorkflowEnv = func(cmd *cobra.Command, rigName string) (workflow.GateEnv, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return workflow.GateEnv{}, err
	}
	townRoot := beads.FindTownRoot(cwd)
	if townRoot == "" {
		return workflow.GateEnv{}, errors.New("not in a Gas Town workspace (no mayor/town.json above the current directory)")
	}
	if rigName == "" {
		if rigName, err = inferRigFromCwd(townRoot); err != nil {
			return workflow.GateEnv{}, fmt.Errorf("cannot determine rig (use --rig): %w", err)
		}
	}
	if err := validateWFRig(rigName); err != nil {
		return workflow.GateEnv{}, err
	}
	rigPath := filepath.Join(townRoot, rigName)
	if fi, err := os.Stat(rigPath); err != nil || !fi.IsDir() {
		return workflow.GateEnv{}, fmt.Errorf("rig %s not found at %s", rigName, rigPath)
	}
	cfg, err := workflow.LoadConfig(rigPath)
	if err != nil {
		return workflow.GateEnv{}, fmt.Errorf("loading workflow config for rig %s: %w", rigName, err)
	}
	// Refuse a disabled rig before touching Dolt so the user sees why.
	if !cfg.Enabled && cmd != nil && cmd.Annotations[wfAnnotationRequiresEnabled] == "true" {
		return workflow.GateEnv{}, errWorkflowDisabled(rigName)
	}

	// beads.New(rigPath) resolves BEADS_DIR through ResolveBeadsDir(rigPath),
	// i.e. the rig's own .beads (following its redirect).
	beadsDir := beads.ResolveBeadsDir(rigPath)
	dbName := beads.DatabaseNameFromMetadata(beadsDir)
	if dbName == "" {
		return workflow.GateEnv{}, fmt.Errorf("rig %s has no dolt_database in %s/metadata.json", rigName, beadsDir)
	}
	dsn := buildDoltDSNFromConfig(doltserver.DefaultConfig(townRoot), dbName, dsnOpts{ParseTime: true, Timeout: "5s"})
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return workflow.GateEnv{}, fmt.Errorf("opening rig database %s: %w", dbName, err)
	}
	store, err := workflow.NewDoltStore(db)
	if err != nil {
		_ = db.Close()
		return workflow.GateEnv{}, err
	}

	// Git is left nil: commands that need the item's checkout attach it
	// through requireCheckout, and the fix gate fails on a nil Git.
	return observeIfJoined(workflow.GateEnv{
		TownRoot: townRoot,
		Rig:      rigName,
		Store:    store,
		Items:    workflow.NewBeadsItems(beads.New(rigPath)),
		Config:   cfg,
	}), nil
}

// workflowCheckoutDir returns the item's worktree: the git toplevel of the
// current directory, which must be a worktree under <townRoot>/<rig>/ other
// than the rig root itself, mayor/rig or refinery/rig. There is no fallback.
var workflowCheckoutDir = func(townRoot, rigName string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return resolveCheckout(townRoot, rigName, cwd)
}

// resolveCheckout implements workflowCheckoutDir for an explicit cwd.
func resolveCheckout(townRoot, rigName, cwd string) (string, error) {
	top, ok := gitToplevel(cwd)
	if !ok {
		return "", errNotWorktree(cwd)
	}
	rigPath := filepath.Join(townRoot, rigName)
	if !pathWithin(rigPath, top) || samePath(rigPath, top) {
		return "", errNotWorktree(top)
	}
	for _, shared := range []string{filepath.Join(rigPath, "mayor", "rig"), filepath.Join(rigPath, "refinery", "rig")} {
		if pathWithin(shared, top) {
			return "", errNotWorktree(top)
		}
	}
	return top, nil
}

func errNotWorktree(dir string) error {
	return fmt.Errorf("run from the item's worktree (polecat or crew checkout); got %s", dir)
}

// gitToplevel returns the toplevel of the git worktree containing dir.
func gitToplevel(dir string) (string, bool) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", false
	}
	top := strings.TrimSpace(string(out))
	return top, top != ""
}

// workflowBranch returns the checked-out branch of dir ("HEAD" if detached
// or unknown).
var workflowBranch = func(dir string) string {
	b, err := git.NewGit(dir).CurrentBranch()
	if err != nil || b == "" {
		return "HEAD"
	}
	return b
}

// checkoutInfo is the worktree a command ran against.
type checkoutInfo struct {
	Dir    string `json:"dir"`
	Branch string `json:"branch"`
}

func (c checkoutInfo) line() string { return fmt.Sprintf("checkout: %s (%s)\n", c.Dir, c.Branch) }

// requireCheckout resolves the item's worktree from the cwd (R15), checks it
// is the worktree sling bound to the item (R19: its branch must equal the
// item's gtb:branch label) and, when env has no Git source yet, attaches one
// diffing the rig's default branch against the worktree's branch.
func requireCheckout(env *workflow.GateEnv, it *workflow.Item) (checkoutInfo, error) {
	if it.Branch == "" {
		return checkoutInfo{}, fmt.Errorf("item %s has no worktree; sling it to a polecat first", it.ID)
	}
	dir, err := workflowCheckoutDir(env.TownRoot, env.Rig)
	if err != nil {
		return checkoutInfo{}, err
	}
	co := checkoutInfo{Dir: dir, Branch: workflowBranch(dir)}
	if co.Branch != it.Branch {
		return checkoutInfo{}, fmt.Errorf("checkout branch %s is not item %s's branch %s", co.Branch, it.ID, it.Branch)
	}
	if env.Git == nil {
		base := "origin/main"
		if rc, err := rig.LoadRigConfig(filepath.Join(env.TownRoot, env.Rig)); err == nil && rc.DefaultBranch != "" {
			base = "origin/" + rc.DefaultBranch
		}
		branch := co.Branch
		env.Git = workflow.GitChangedFiles{RepoDir: dir, Base: base, Branch: func(string) string { return branch }}
	}
	return co, nil
}

// workflowActor names whoever runs the command: git user.name, else $USER.
var workflowActor = func() string {
	if out, err := exec.Command("git", "config", "user.name").Output(); err == nil {
		if name := strings.TrimSpace(string(out)); name != "" {
			return name
		}
	}
	return os.Getenv("USER")
}

// workflowTerminal is the terminal human approvals are confirmed on; out is
// where the prompt goes (stderr under --json, so stdout carries only JSON).
var workflowTerminal = func(out io.Writer) workflow.Terminal {
	return workflow.StdTerminal{In: os.Stdin, Out: out}
}

// promptOut is stdout normally and stderr under --json.
func (o *wfOpts) promptOut(cmd *cobra.Command) io.Writer {
	if o.json {
		return cmd.ErrOrStderr()
	}
	return cmd.OutOrStdout()
}

// workflowRunner returns the Runner red, green and check execute through.
var workflowRunner = func(env workflow.GateEnv, itemID string) workflow.Runner {
	return workflow.ExecRunner{OutputDir: workflow.OutputDir(env.TownRoot, env.Rig, itemID)}
}

// resolvePath resolves symlinks in p's nearest existing ancestor and
// re-appends the rest, so existing and not-yet-existing paths compare alike.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	base := nearestExisting(p)
	q, err := filepath.EvalSymlinks(base)
	if err != nil {
		return p
	}
	rest, err := filepath.Rel(base, p)
	if err != nil {
		return p
	}
	return filepath.Join(q, rest)
}

func samePath(a, b string) bool { return resolvePath(a) == resolvePath(b) }

// pathWithin reports whether p is dir or below it, after resolving symlinks.
func pathWithin(dir, p string) bool {
	rel, err := filepath.Rel(resolvePath(dir), resolvePath(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validateWFRig(name string) error {
	if !wfRigNameRe.MatchString(name) {
		return fmt.Errorf("invalid rig name %q (letters, digits and underscores only)", name)
	}
	return nil
}

func validateWFKind(k string) (workflow.Kind, error) {
	switch kind := workflow.Kind(k); kind {
	case workflow.KindTask, workflow.KindFix, workflow.KindEpic:
		return kind, nil
	}
	return "", fmt.Errorf("unknown kind %q (want task, fix or epic)", k)
}

// ctxRunner runs every command under ctx (the --timeout deadline), whatever
// context the workflow package passes.
type ctxRunner struct {
	ctx context.Context
	r   workflow.Runner
}

func (c ctxRunner) Run(_ context.Context, dir string, argv []string) (int, string, error) {
	return c.r.Run(c.ctx, dir, argv)
}

// ---- output plumbing ----

// wfResult is a command's outcome: the exit code, the human text for stdout
// and the single JSON object emitted under --json.
type wfResult struct {
	code  int
	human string
	json  any
}

type wfHandler func(cmd *cobra.Command, args []string, o *wfOpts) (*wfResult, error)

// wfOpts holds the flags shared by every workflow command.
type wfOpts struct {
	json bool
	rig  string
}

// env validates --rig, builds the environment and enforces rig gating.
func (o *wfOpts) env(cmd *cobra.Command, requireEnabled bool) (workflow.GateEnv, error) {
	if o.rig != "" {
		if err := validateWFRig(o.rig); err != nil {
			return workflow.GateEnv{}, err
		}
	}
	if requireEnabled {
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		cmd.Annotations[wfAnnotationRequiresEnabled] = "true"
	}
	env, err := newWorkflowEnv(cmd, o.rig)
	if err != nil {
		return workflow.GateEnv{}, err
	}
	if requireEnabled && (env.Config == nil || !env.Config.Enabled) {
		return workflow.GateEnv{}, errWorkflowDisabled(env.Rig)
	}
	return env, nil
}

// wfCommand wires a handler into a cobra command with --json and --rig and
// maps its outcome onto stdout, stderr and the exit code.
func wfCommand(c *cobra.Command, h wfHandler) (*cobra.Command, *wfOpts) {
	o := &wfOpts{}
	c.GroupID = GroupWork
	c.SilenceUsage = true
	c.Flags().BoolVar(&o.json, "json", false, "Output one JSON object on stdout")
	c.Flags().StringVar(&o.rig, "rig", "", "Rig name (default: inferred from the current directory)")
	c.RunE = func(cmd *cobra.Command, args []string) error {
		res, err := h(cmd, args, o)
		return o.finish(cmd, res, err)
	}
	return c, o
}

func (o *wfOpts) finish(cmd *cobra.Command, res *wfResult, err error) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err != nil {
		code := wfExitError
		if errors.Is(err, workflow.ErrStepConflict) {
			code = wfExitConflict
		}
		fmt.Fprintln(errOut, err)
		if o.json {
			writeWFJSON(out, map[string]any{"error": err.Error(), "exit": code})
		}
		cmd.SilenceErrors = true
		return NewSilentExit(code)
	}
	if o.json {
		writeWFJSON(out, res.json)
	} else if res.human != "" {
		fmt.Fprint(out, res.human)
	}
	if res.code != 0 {
		cmd.SilenceErrors = true
		return NewSilentExit(res.code)
	}
	return nil
}

func writeWFJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// gateJSON is the JSON form of a workflow.GateResult.
type gateJSON struct {
	Passed          bool     `json:"passed"`
	Reasons         []string `json:"reasons"`
	WaitingForHuman string   `json:"waiting_for_human"`
}

func toGateJSON(g workflow.GateResult) gateJSON {
	r := g.Reasons
	if r == nil {
		r = []string{}
	}
	return gateJSON{Passed: g.Passed, Reasons: r, WaitingForHuman: g.WaitingForHuman}
}

// refusalText renders reasons one per line as "- <reason>" and the
// waiting-for line.
func refusalText(reasons []string, waiting string) string {
	var b strings.Builder
	for _, r := range reasons {
		fmt.Fprintf(&b, "- %s\n", r)
	}
	if waiting != "" {
		fmt.Fprintf(&b, "waiting for: %s\n", waiting)
	}
	return b.String()
}

// effectiveStep mirrors the workflow's rule: the latest stored transition
// wins over the bead's step.
func effectiveStep(env workflow.GateEnv, it *workflow.Item) workflow.Step {
	if trs, err := env.Store.Transitions(it.ID); err == nil && len(trs) > 0 {
		return trs[len(trs)-1].To
	}
	return it.Step
}

// humanGuardErr maps the human-guard errors onto their fixed messages.
func humanGuardErr(err error) error {
	switch {
	case errors.Is(err, workflow.ErrNotHuman):
		return errors.New(msgNotHuman)
	case errors.Is(err, workflow.ErrNotConfirmed):
		return errors.New(msgNotConfirmed)
	}
	return err
}

// ---- commands ----

func init() {
	rootCmd.AddCommand(newWorkflowCommands()...)
}

// newWorkflowCommands builds fresh instances of the gated workflow commands.
func newWorkflowCommands() []*cobra.Command {
	return []*cobra.Command{
		newItemCmd(),
		newAdvanceCmd(),
		newHumanVerdictCmd("approve", "approve", "Approve an item artifact at its current sha (humans only)"),
		newHumanVerdictCmd("request-changes", "request_changes", "Request changes on an item artifact (humans only)"),
		newApproveChecksCmd(),
		newVerdictCmd(),
		newRedCmd(),
		newGreenCmd(),
		newCheckCmd(),
	}
}

func newItemCmd() *cobra.Command {
	item := &cobra.Command{
		Use:     "item",
		Short:   "Create and inspect gated workflow items",
		GroupID: GroupWork,
		RunE:    requireSubcommand,
	}
	for _, sub := range []*cobra.Command{newItemNewCmd(), newItemShowCmd(), newItemListCmd()} {
		sub.GroupID = "" // groups are defined on the root only
		item.AddCommand(sub)
	}
	return item
}

func newItemNewCmd() *cobra.Command {
	var parent string
	c, _ := wfCommand(&cobra.Command{
		Use:   "new <task|fix|epic> <title>",
		Short: "Create a gated item at its first step with a draft artifact stub",
		Args:  cobra.ExactArgs(2),
	}, func(cmd *cobra.Command, args []string, o *wfOpts) (*wfResult, error) {
		kind, err := validateWFKind(args[0])
		if err != nil {
			return nil, err
		}
		title := strings.Join(strings.Fields(args[1]), " ")
		if title == "" {
			return nil, errors.New("title is required")
		}
		if parent != "" {
			if err := workflow.ValidateID(parent); err != nil {
				return nil, err
			}
		}
		env, err := o.env(cmd, true)
		if err != nil {
			return nil, err
		}
		// The item ID is not known yet; its dir sits directly in itemsDir.
		itemsDir := filepath.Dir(filepath.Dir(workflow.ArtifactPath(env.TownRoot, env.Rig, "x", "x")))
		// An HQ repo at the town root is fine (HQGitignore ignores .gtb/);
		// a rig-level repo (adopted rig root or deeper) is not.
		if top, ok := gitToplevel(nearestExisting(itemsDir)); ok && !samePath(top, env.TownRoot) {
			return nil, fmt.Errorf("artifact dir %s is inside git worktree %s; gtb keeps artifacts outside repos", itemsDir, top)
		}
		it, err := env.Items.Create(env.Rig, kind, title, parent)
		if err != nil {
			return nil, err
		}
		step := workflow.Steps(kind)[0]
		name := workflow.ArtifactFor(kind, step)
		path := workflow.ArtifactPath(env.TownRoot, env.Rig, it.ID, name)
		if err := workflow.WriteStub(path, it.ID, parent, kind, title); err != nil {
			return nil, fmt.Errorf("item %s created but stub not written: %w", it.ID, err)
		}
		return &wfResult{
			human: fmt.Sprintf("created %s %s at step %s\nartifact: %s\n", kind, it.ID, step, path),
			json: map[string]any{
				"id": it.ID, "rig": env.Rig, "kind": kind, "step": step,
				"title": title, "parent": parent, "artifact_path": path,
			},
		}, nil
	})
	c.Flags().StringVar(&parent, "parent", "", "Parent item ID")
	return c
}

// nearestExisting returns p or its nearest existing ancestor.
func nearestExisting(p string) string {
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

type artifactJSON struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	SHA    string `json:"sha"`
	Exists bool   `json:"exists"`
}

func newItemShowCmd() *cobra.Command {
	c, _ := wfCommand(&cobra.Command{
		Use:   "show <id>",
		Short: "Show an item's step, gate result and artifacts",
		Args:  cobra.ExactArgs(1),
	}, func(cmd *cobra.Command, args []string, o *wfOpts) (*wfResult, error) {
		if err := workflow.ValidateID(args[0]); err != nil {
			return nil, err
		}
		env, err := o.env(cmd, false)
		if err != nil {
			return nil, err
		}
		it, err := env.Items.Get(args[0])
		if err != nil {
			return nil, err
		}
		gate := workflow.Gate(env, it)
		step := effectiveStep(env, it)
		current := ""
		if name := workflow.ArtifactFor(it.Kind, step); name != "" {
			current = workflow.ArtifactPath(env.TownRoot, env.Rig, it.ID, name)
		}
		arts := []artifactJSON{}
		for _, s := range workflow.Steps(it.Kind) {
			name := workflow.ArtifactFor(it.Kind, s)
			if name == "" {
				continue
			}
			a := artifactJSON{Name: name, Path: workflow.ArtifactPath(env.TownRoot, env.Rig, it.ID, name)}
			if _, err := os.Stat(a.Path); err == nil {
				a.Exists = true
				if _, sha, _, err := workflow.ReadArtifact(a.Path); err == nil {
					a.SHA = sha
				}
			}
			arts = append(arts, a)
		}

		var b strings.Builder
		fmt.Fprintf(&b, "id:    %s\nkind:  %s\nstep:  %s\ntitle: %s\n", it.ID, it.Kind, step, it.Title)
		if it.Parent != "" {
			fmt.Fprintf(&b, "parent: %s\n", it.Parent)
		}
		if gate.Passed {
			b.WriteString("gate:  passed\n")
		} else {
			b.WriteString("gate:  refused\n")
			for _, r := range gate.Reasons {
				fmt.Fprintf(&b, "  - %s\n", r)
			}
		}
		b.WriteString("artifacts:\n")
		for _, a := range arts {
			sha := "(missing)"
			if a.SHA != "" {
				sha = a.SHA
			} else if a.Exists {
				sha = "(unreadable)"
			}
			fmt.Fprintf(&b, "  %s  %s  %s\n", a.Name, a.Path, sha)
		}
		if w := firstNonEmpty(gate.WaitingForHuman, it.WaitingForHuman); w != "" {
			fmt.Fprintf(&b, "waiting for: %s\n", w)
		}
		return &wfResult{human: b.String(), json: map[string]any{
			"id": it.ID, "rig": env.Rig, "kind": it.Kind, "step": step, "title": it.Title,
			"parent": it.Parent, "waiting_for_human": it.WaitingForHuman,
			"gate": toGateJSON(gate), "artifact_path": current, "artifacts": arts,
		}}, nil
	})
	return c
}

func newItemListCmd() *cobra.Command {
	c, _ := wfCommand(&cobra.Command{
		Use:   "list",
		Short: "List the rig's gated items",
		Args:  cobra.NoArgs,
	}, func(cmd *cobra.Command, _ []string, o *wfOpts) (*wfResult, error) {
		env, err := o.env(cmd, false)
		if err != nil {
			return nil, err
		}
		items, err := env.Items.List(env.Rig)
		if err != nil {
			return nil, err
		}
		rows := []map[string]any{}
		var b strings.Builder
		for _, it := range items {
			step := effectiveStep(env, it)
			rows = append(rows, map[string]any{
				"id": it.ID, "kind": it.Kind, "step": step, "title": it.Title,
				"parent": it.Parent, "waiting_for_human": it.WaitingForHuman,
			})
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s", it.ID, it.Kind, step, it.Title)
			if it.WaitingForHuman != "" {
				fmt.Fprintf(&b, "\t(waiting for: %s)", it.WaitingForHuman)
			}
			b.WriteString("\n")
		}
		if len(items) == 0 {
			fmt.Fprintf(&b, "no gated items in rig %s\n", env.Rig)
		}
		return &wfResult{human: b.String(), json: map[string]any{"rig": env.Rig, "items": rows}}, nil
	})
	return c
}

func newAdvanceCmd() *cobra.Command {
	c, _ := wfCommand(&cobra.Command{
		Use:   "advance <id>",
		Short: "Move an item to its next step if the current step's gate passes",
		Long: `Move an item to its next step if the current step's gate passes.

Exit codes: 0 moved, 3 gate refused (reasons printed), 4 step conflict
(another actor advanced the item first), 1 any other error.`,
		Args: cobra.ExactArgs(1),
	}, func(cmd *cobra.Command, args []string, o *wfOpts) (*wfResult, error) {
		if err := workflow.ValidateID(args[0]); err != nil {
			return nil, err
		}
		env, err := o.env(cmd, true)
		if err != nil {
			return nil, err
		}
		cur, err := env.Items.Get(args[0])
		if err != nil {
			return nil, err
		}
		// Only the fix gate reads git (the fix-files budget); it must diff
		// the item's own worktree, never a shared clone.
		var co *checkoutInfo
		if effectiveStep(env, cur) == "fix" {
			c, err := requireCheckout(&env, cur)
			if err != nil {
				return nil, err
			}
			co = &c
		}
		it, gate, err := workflow.Advance(env, args[0], workflowActor())
		if err != nil {
			return nil, err
		}
		js := map[string]any{"id": it.ID, "moved": gate.Passed, "step": it.Step, "gate": toGateJSON(gate)}
		res := &wfResult{json: js}
		if co != nil {
			js["checkout"] = *co
			res.human = co.line()
		}
		if !gate.Passed {
			res.code = wfExitRefused
			res.human += fmt.Sprintf("%s: gate refused at %s\n", it.ID, it.Step) + refusalText(gate.Reasons, gate.WaitingForHuman)
			return res, nil
		}
		res.human += fmt.Sprintf("%s: advanced to %s\n", it.ID, it.Step)
		return res, nil
	})
	return c
}

func newHumanVerdictCmd(use, verdict, short string) *cobra.Command {
	c, _ := wfCommand(&cobra.Command{
		Use:   use + " <id> <artifact>",
		Short: short,
		Long: short + `.

Refused when run from an agent session or without an interactive terminal.
The artifact's sha is shown and must be confirmed.`,
		Args: cobra.ExactArgs(2),
	}, func(cmd *cobra.Command, args []string, o *wfOpts) (*wfResult, error) {
		if err := workflow.ValidateID(args[0]); err != nil {
			return nil, err
		}
		// Refuse agents before building the environment.
		term := workflowTerminal(o.promptOut(cmd))
		if err := workflow.RequireHuman(term, os.Getenv); err != nil {
			return nil, humanGuardErr(err)
		}
		env, err := o.env(cmd, true)
		if err != nil {
			return nil, err
		}
		a, err := workflow.HumanApprove(env, term, os.Getenv, args[0], args[1], verdict, workflowActor())
		if err != nil {
			return nil, humanGuardErr(err)
		}
		return &wfResult{
			human: fmt.Sprintf("recorded %s on %s %s sha %s\n", verdict, a.ItemID, a.Artifact, a.SHA[:12]),
			json:  map[string]any{"id": a.ItemID, "artifact": a.Artifact, "sha": a.SHA, "verdict": a.Verdict, "actor": a.Actor},
		}, nil
	})
	return c
}

func newApproveChecksCmd() *cobra.Command {
	c, _ := wfCommand(&cobra.Command{
		Use:   "approve-checks",
		Short: "Approve the rig's check commands (humans only)",
		Long: `Approve the rig's check commands (humans only).

Pins check_commands_approved_sha in the rig's settings/config.json to the
sha256 of the current check_commands. Any later edit voids the approval.`,
		Args: cobra.NoArgs,
	}, func(cmd *cobra.Command, _ []string, o *wfOpts) (*wfResult, error) {
		term := workflowTerminal(o.promptOut(cmd))
		if err := workflow.RequireHuman(term, os.Getenv); err != nil {
			return nil, humanGuardErr(err)
		}
		env, err := o.env(cmd, true)
		if err != nil {
			return nil, err
		}
		path := config.RigSettingsPath(filepath.Join(env.TownRoot, env.Rig))
		cmds, err := gatedCheckCommands(path)
		if err != nil {
			return nil, err
		}
		sha := workflow.CheckCommandsSHA(cmds)
		list := o.promptOut(cmd)
		fmt.Fprintf(list, "Check commands for rig %s:\n", env.Rig)
		for i, c := range cmds {
			fmt.Fprintf(list, "  %d. %s\n", i+1, c)
		}
		yes, err := term.Confirm(fmt.Sprintf("Approve check commands sha %s for rig %s? [y/N]", sha[:12], env.Rig))
		if err != nil {
			return nil, fmt.Errorf("reading confirmation: %w", err)
		}
		if !yes {
			return nil, errors.New(msgNotConfirmed)
		}
		// Reload so an edit made while the prompt was open is not approved.
		settings, err := config.LoadRigSettings(path)
		if err != nil {
			return nil, err
		}
		var g *config.GatedWorkflow
		if settings.Workflow != nil {
			g = settings.Workflow.Gated
		}
		if g == nil || workflow.CheckCommandsSHA(g.CheckCommands) != sha {
			return nil, errors.New("check commands changed while confirming; run approve-checks again")
		}
		g.CheckCommandsApprovedSHA = sha
		if err := config.SaveRigSettings(path, settings); err != nil {
			return nil, err
		}
		return &wfResult{
			human: fmt.Sprintf("approved check commands sha %s for rig %s\n", sha[:12], env.Rig),
			json:  map[string]any{"rig": env.Rig, "check_commands": cmds, "check_commands_approved_sha": sha},
		}, nil
	})
	return c
}

// gatedCheckCommands reads workflow.gated.check_commands from the settings
// file at path.
func gatedCheckCommands(path string) ([]string, error) {
	settings, err := config.LoadRigSettings(path)
	if err != nil {
		return nil, err
	}
	if settings.Workflow == nil || settings.Workflow.Gated == nil || len(settings.Workflow.Gated.CheckCommands) == 0 {
		return nil, fmt.Errorf("no workflow.gated.check_commands in %s", path)
	}
	return settings.Workflow.Gated.CheckCommands, nil
}

func newVerdictCmd() *cobra.Command {
	c, _ := wfCommand(&cobra.Command{
		Use:   "verdict <id> <artifact> <role> <approve|concerns|reject>",
		Short: "Record a reviewer agent's verdict on an item artifact",
		Args:  cobra.ExactArgs(4),
	}, func(cmd *cobra.Command, args []string, o *wfOpts) (*wfResult, error) {
		if err := workflow.ValidateID(args[0]); err != nil {
			return nil, err
		}
		env, err := o.env(cmd, true)
		if err != nil {
			return nil, err
		}
		a, err := workflow.RecordVerdict(env, args[0], args[1], args[2], args[3], workflowActor())
		if err != nil {
			return nil, err
		}
		return &wfResult{
			human: fmt.Sprintf("recorded %s %s on %s %s sha %s\n", a.Role, a.Verdict, a.ItemID, a.Artifact, a.SHA[:12]),
			json:  map[string]any{"id": a.ItemID, "artifact": a.Artifact, "sha": a.SHA, "role": a.Role, "verdict": a.Verdict, "actor": a.Actor},
		}, nil
	})
	return c
}

// runSetup validates the item, builds the env, resolves the item's worktree
// and returns a timeout-bound runner. cancel must be called.
func runSetup(cmd *cobra.Command, o *wfOpts, itemID string, timeout time.Duration) (env workflow.GateEnv, co checkoutInfo, r workflow.Runner, cancel context.CancelFunc, err error) {
	if err = workflow.ValidateID(itemID); err != nil {
		return
	}
	if timeout <= 0 {
		err = fmt.Errorf("--timeout must be positive, got %s", timeout)
		return
	}
	if env, err = o.env(cmd, true); err != nil {
		return
	}
	it, err := env.Items.Get(itemID)
	if err != nil {
		return
	}
	if co, err = requireCheckout(&env, it); err != nil {
		return
	}
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	return env, co, ctxRunner{ctx: ctx, r: workflowRunner(env, itemID)}, cancel, nil
}

func tddResult(co checkoutInfo, run workflow.TDDRun, err error) (*wfResult, error) {
	if err != nil && !errors.Is(err, workflow.ErrRefused) {
		return nil, err
	}
	js := map[string]any{
		"id": run.ItemID, "plan_step": run.PlanStep, "phase": run.Phase, "exit": run.Exit,
		"test_files": run.TestFiles, "tail": run.Tail, "refused": err != nil, "checkout": co,
	}
	res := &wfResult{json: js}
	if err != nil {
		res.code = wfExitRefused
		js["reason"] = err.Error()
		res.human = co.line() + fmt.Sprintf("refused: %v\n%s\n", err, strings.TrimRight(run.Tail, "\n"))
		return res, nil
	}
	res.human = co.line() + fmt.Sprintf("%s recorded for %s %s (exit %d)\n", run.Phase, run.ItemID, run.PlanStep, run.Exit)
	return res, nil
}

func newRedCmd() *cobra.Command {
	var (
		tests   string
		timeout time.Duration
	)
	c, _ := wfCommand(&cobra.Command{
		Use:   "red <id> <plan-step> --tests <glob,...> -- <argv...>",
		Short: "Run a test command that must fail and record a verified red",
		Long: `Run a test command (argv, no shell) in the item's checkout. It must
fail; a passing command is recorded as refused (exit 3). --tests lists globs,
relative to the checkout, for the test files whose contents green re-checks.`,
		Args: cobra.MinimumNArgs(3),
	}, func(cmd *cobra.Command, args []string, o *wfOpts) (*wfResult, error) {
		dash := cmd.ArgsLenAtDash()
		if dash != 2 || len(args) < 3 {
			return nil, errors.New("usage: red <id> <plan-step> --tests <glob,...> -- <argv...>")
		}
		if strings.TrimSpace(tests) == "" {
			return nil, errors.New("--tests is required")
		}
		env, co, r, cancel, err := runSetup(cmd, o, args[0], timeout)
		if err != nil {
			return nil, err
		}
		defer cancel()
		files, err := expandTestGlobs(co.Dir, tests)
		if err != nil {
			return nil, err
		}
		run, err := workflow.Red(env, r, args[0], args[1], co.Dir, args[2:], files)
		return tddResult(co, run, err)
	})
	c.Flags().StringVar(&tests, "tests", "", "Comma-separated test file globs, relative to the checkout")
	c.Flags().DurationVar(&timeout, "timeout", wfDefaultTimeout, "Timeout for the test command")
	return c
}

// expandTestGlobs expands the comma list of globs relative to dir and
// returns dir-relative paths. A glob that matches nothing is an error.
func expandTestGlobs(dir, list string) ([]string, error) {
	var files []string
	for _, pat := range strings.Split(list, ",") {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if filepath.IsAbs(pat) {
			return nil, fmt.Errorf("--tests glob %q must be relative to the checkout", pat)
		}
		matches, err := filepath.Glob(filepath.Join(dir, pat))
		if err != nil {
			return nil, fmt.Errorf("--tests glob %q: %w", pat, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("--tests glob %q matched no files in %s", pat, dir)
		}
		for _, m := range matches {
			rel, err := filepath.Rel(dir, m)
			if err != nil {
				return nil, err
			}
			files = append(files, rel)
		}
	}
	if len(files) == 0 {
		return nil, errors.New("--tests is required")
	}
	return files, nil
}

func newGreenCmd() *cobra.Command {
	var timeout time.Duration
	c, _ := wfCommand(&cobra.Command{
		Use:   "green <id> <plan-step>",
		Short: "Re-run the plan step's red command, which must now pass",
		Args:  cobra.ExactArgs(2),
	}, func(cmd *cobra.Command, args []string, o *wfOpts) (*wfResult, error) {
		env, co, r, cancel, err := runSetup(cmd, o, args[0], timeout)
		if err != nil {
			return nil, err
		}
		defer cancel()
		run, err := workflow.Green(env, r, args[0], args[1], co.Dir)
		return tddResult(co, run, err)
	})
	c.Flags().DurationVar(&timeout, "timeout", wfDefaultTimeout, "Timeout for the test command")
	return c
}

func newCheckCmd() *cobra.Command {
	var timeout time.Duration
	c, _ := wfCommand(&cobra.Command{
		Use:   "check <id>",
		Short: "Run the rig's approved check commands and record the result",
		Long: `Run the rig's human-approved check commands in the item's checkout and
record the result. Exit 0 when every command passes, 3 when one fails or the
commands are not approved, 1 on any other error.`,
		Args: cobra.ExactArgs(1),
	}, func(cmd *cobra.Command, args []string, o *wfOpts) (*wfResult, error) {
		env, co, r, cancel, err := runSetup(cmd, o, args[0], timeout)
		if err != nil {
			return nil, err
		}
		defer cancel()
		run, err := workflow.Check(env, r, args[0], co.Dir)
		if errors.Is(err, workflow.ErrChecksNotApproved) {
			return &wfResult{
				code:  wfExitRefused,
				human: co.line() + refusalText([]string{"check commands not approved"}, "approve check commands"),
				json: map[string]any{"id": args[0], "passed": false, "approved": false,
					"waiting_for_human": "approve check commands", "checkout": co},
			}, nil
		}
		if err != nil {
			return nil, err
		}
		res := &wfResult{json: map[string]any{
			"id": run.ItemID, "passed": run.Passed, "approved": true, "cmd_set_sha": run.CmdSetSHA, "tail": run.Tail, "checkout": co,
		}}
		if !run.Passed {
			res.code = wfExitRefused
			res.human = co.line() + fmt.Sprintf("check failed for %s\n%s\n", run.ItemID, strings.TrimRight(run.Tail, "\n"))
			return res, nil
		}
		res.human = co.line() + fmt.Sprintf("checks passed for %s\n", run.ItemID)
		return res, nil
	})
	c.Flags().DurationVar(&timeout, "timeout", wfDefaultTimeout, "Timeout for the check commands")
	return c
}
