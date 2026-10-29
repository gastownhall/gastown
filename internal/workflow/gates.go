package workflow

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
)

// ChangedFiles reports the files an item's branch changes relative to its base.
type ChangedFiles interface {
	Changed(itemID string) ([]string, error)
}

// Observer receives workflow events: EventTransition with a Transition after
// each recorded transition (Advance), and EventEvidence with the TDDRun,
// CheckRun or Approval just recorded. Implementations live outside this
// package and adapt the workflow-local types; they must not fail the caller.
type Observer interface {
	OnEvent(kind, itemID string, data any)
}

// GateEnv is everything a gate needs to evaluate an item. Observer may be nil.
// Now is the clock gtb stamps evidence with (Red, Green, Check, HumanApprove,
// RecordVerdict); nil means time.Now. Callers never supply timestamps.
type GateEnv struct {
	TownRoot, Rig string
	Store         Store
	Items         Items
	Config        *config.GatedWorkflow
	Git           ChangedFiles
	Observer      Observer
	Now           func() time.Time
}

// now returns env.Now() (or time.Now) in UTC.
func (env GateEnv) now() time.Time {
	if env.Now != nil {
		return env.Now().UTC()
	}
	return time.Now().UTC()
}

// GateResult is the outcome of evaluating an item's current step gate.
// WaitingForHuman is set when the gate is blocked on a human action, in the
// form "approve <artifact>" or "approve check commands".
type GateResult struct {
	Passed          bool
	Reasons         []string
	WaitingForHuman string
}

// GitChangedFiles is the production ChangedFiles: it diffs Base against the
// item's branch (Branch(itemID)) in the repository at RepoDir.
type GitChangedFiles struct {
	RepoDir, Base string
	Branch        func(itemID string) string
}

// Changed implements ChangedFiles using git diff --name-only Base...branch.
func (g GitChangedFiles) Changed(itemID string) ([]string, error) {
	if g.Branch == nil {
		return nil, errors.New("workflow: GitChangedFiles.Branch is nil")
	}
	return git.NewGit(g.RepoDir).DiffNameOnly(g.Base, g.Branch(itemID))
}

// Verdicts and roles that gates interpret.
const (
	verdictApprove        = "approve"
	verdictReject         = "reject"
	verdictRequestChanges = "request_changes"
	roleHuman             = "human"
	stepMerge             = Step("merge")
	reproStep             = "repro"
)

// effectiveStep returns the item's current step, the time it entered it, and
// whether the store has transition history for it. The evidence store
// arbitrates: when transitions exist, the latest transition's To and At win
// over the bead's Step and StepEnteredAt. Without history the bead's values
// are returned; callers must accept them only for the kind's first step
// (see Gate), since the bead is agent-editable.
func effectiveStep(env GateEnv, it *Item) (step Step, entered time.Time, history bool, err error) {
	trs, err := env.Store.Transitions(it.ID)
	if err != nil {
		return "", time.Time{}, false, err
	}
	if len(trs) == 0 {
		return it.Step, it.StepEnteredAt, false, nil
	}
	last := trs[len(trs)-1]
	return last.To, last.At, true, nil
}

// CurrentStep returns item it's current step as the gates see it (see
// effectiveStep) and whether the store has transition history for it.
// Callers outside the gates must not trust a step without history unless it
// is the kind's first step.
func CurrentStep(env GateEnv, it *Item) (step Step, history bool, err error) {
	step, _, history, err = effectiveStep(env, it)
	return step, history, err
}

// firstStep returns the first step of kind k, or "" for an unknown kind.
func firstStep(k Kind) Step {
	if steps := stepTables[k]; len(steps) > 0 {
		return steps[0]
	}
	return ""
}

// unquote trims surrounding whitespace and one pair of matching quotes.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		s = s[1 : len(s)-1]
	}
	return s
}

// gateConfig returns env.Config with zero budgets replaced by defaults, so a
// config not produced by LoadConfig does not fail every budget check.
func gateConfig(env GateEnv) config.GatedWorkflow {
	var c config.GatedWorkflow
	if env.Config != nil {
		c = *env.Config
	}
	if c.MaxFixFiles <= 0 {
		c.MaxFixFiles = DefaultMaxFixFiles
	}
	if c.MaxFixRounds <= 0 {
		c.MaxFixRounds = DefaultMaxFixRounds
	}
	if c.MaxPlanLoops <= 0 {
		c.MaxPlanLoops = DefaultMaxPlanLoops
	}
	return c
}

type gateCtx struct {
	env     GateEnv
	it      *Item
	step    Step
	entered time.Time
	cfg     config.GatedWorkflow
	res     GateResult
}

func (g *gateCtx) fail(format string, args ...any) {
	g.res.Reasons = append(g.res.Reasons, fmt.Sprintf(format, args...))
}

func (g *gateCtx) wait(reason string) {
	if g.res.WaitingForHuman == "" {
		g.res.WaitingForHuman = reason
	}
}

// artifact reads an item artifact; ok is false (with a reason recorded) when
// it cannot be read.
func (g *gateCtx) artifact(name string) (content []byte, sha string, fm map[string]string, ok bool) {
	content, sha, fm, err := ReadArtifact(ArtifactPath(g.env.TownRoot, g.env.Rig, g.it.ID, name))
	switch {
	case errors.Is(err, os.ErrNotExist):
		g.fail("%s.md missing", name)
		return nil, "", nil, false
	case err != nil:
		g.fail("%s.md unreadable: %v", name, err)
		return nil, "", nil, false
	}
	return content, sha, fm, true
}

// decision is the state of the human decisions on one artifact relative to
// its current sha.
type decision int

const (
	neverApproved       decision = iota // no human decision at all
	approvedCurrent                     // latest human decision approves the current sha
	approvedStale                       // latest human decision approves an older sha
	changesRequested                    // latest human decision is reject/request_changes on the current sha
	changesRequestedOld                 // as above, but the artifact was revised since
)

// isHumanDecision reports whether a counts as a human decision: both the
// role and the recording source must be human (defence in depth against
// rows written outside HumanApprove).
func isHumanDecision(a Approval) bool { return a.Role == roleHuman && a.Source == sourceHuman }

// isVerifiedTDD reports whether r is TDD evidence gtb itself produced.
func isVerifiedTDD(r TDDRun) bool { return r.Source == sourceGTB && r.Provenance == provenanceVerified }

// humanDecision classifies the latest human decision on artifact name (by
// At, the last appended winning ties) against the current sha. Only
// isHumanDecision records with verdict approve, reject or request_changes
// count.
func humanDecision(name, sha string, approvals []Approval) decision {
	var decisions []Approval
	for _, a := range approvals {
		if a.Artifact != name || !isHumanDecision(a) {
			continue
		}
		switch a.Verdict {
		case verdictApprove, verdictReject, verdictRequestChanges:
			decisions = append(decisions, a)
		}
	}
	sort.SliceStable(decisions, func(i, j int) bool { return decisions[i].At.Before(decisions[j].At) })
	if len(decisions) == 0 {
		return neverApproved
	}
	last := decisions[len(decisions)-1]
	switch {
	case last.Verdict == verdictApprove && last.SHA == sha:
		return approvedCurrent
	case last.Verdict == verdictApprove:
		return approvedStale
	case last.SHA == sha:
		return changesRequested
	default:
		return changesRequestedOld
	}
}

// HumanApprovedCurrent reports whether the latest human decision on artifact
// name approves sha (the artifact's current hash). Only records with
// Role=="human" and Source=="human" count; a later request_changes or reject,
// or an approval of an older sha, means false.
func HumanApprovedCurrent(name, sha string, approvals []Approval) bool {
	return humanDecision(name, sha, approvals) == approvedCurrent
}

// approved is the step artifact gate: artifact name must be human-approved
// at its current sha. A newer request_changes or reject blocks, and also
// waits for a human when the artifact has been revised since that request.
func (g *gateCtx) approved(name, sha string, approvals []Approval) {
	switch humanDecision(name, sha, approvals) {
	case approvedCurrent:
	case changesRequested:
		g.fail("changes requested on %s", name)
	case changesRequestedOld:
		g.fail("changes requested on %s", name)
		g.wait("approve " + name)
	default:
		g.fail("%s.md not approved", name)
		g.wait("approve " + name)
	}
}

// approvedContent reads artifact name for a gate that relies on its content
// (an obligation the agent could otherwise shrink by editing it). ok is true
// only when the latest human decision approves the current sha; otherwise a
// reason is recorded and the gate waits for a human approval.
func (g *gateCtx) approvedContent(name string) (content []byte, fm map[string]string, ok bool) {
	content, sha, fm, ok := g.artifact(name)
	if !ok {
		return nil, nil, false
	}
	approvals, err := g.env.Store.Approvals(g.it.ID)
	if err != nil {
		g.fail("reading approvals: %v", err)
		return nil, nil, false
	}
	switch humanDecision(name, sha, approvals) {
	case approvedCurrent:
		return content, fm, true
	case neverApproved:
		g.fail("approve %s", name)
	case changesRequested, changesRequestedOld:
		g.fail("changes requested on %s", name)
	default:
		g.fail("%s.md changed since approval", name)
	}
	g.wait("approve " + name)
	return nil, nil, false
}

// Gate evaluates whether item it may leave its current step. The current
// step is the store-derived one (see effectiveStep), not necessarily it.Step.
func Gate(env GateEnv, it *Item) GateResult {
	g := &gateCtx{env: env, it: it, cfg: gateConfig(env)}
	step, entered, history, err := effectiveStep(env, it)
	if err != nil {
		g.fail("reading transitions: %v", err)
		return g.res
	}
	g.step, g.entered = step, entered
	if !history && step != firstStep(it.Kind) {
		// The bead alone cannot place an item past its first step: the
		// store accepts any first transition, so trusting it would skip gates.
		g.fail("no transition history for step %s", step)
		return g.res
	}

	switch step {
	case StepDone:
		g.fail("item is done")
		return g.res
	case stepMerge:
		g.fail("merge is performed by the refinery")
		return g.res
	}
	if !stepKnown(it.Kind, step) {
		g.fail("unknown step %s for kind %s", step, it.Kind)
		return g.res
	}

	approvals, err := env.Store.Approvals(it.ID)
	if err != nil {
		g.fail("reading approvals: %v", err)
		return g.res
	}

	var (
		fm    map[string]string
		sha   string
		haveA bool
	)
	if name := ArtifactFor(it.Kind, step); name != "" {
		if _, sha, fm, haveA = g.artifact(name); haveA {
			g.approved(name, sha, approvals)
		}
	}

	switch {
	case it.Kind == KindTask && step == "plan":
		g.planGate(sha, haveA, approvals)
	case it.Kind == KindTask && step == "execute":
		g.executeGate()
	case step == "verify" && (it.Kind == KindTask || it.Kind == KindFix):
		g.verifyGate()
	case it.Kind == KindFix && step == "reproduce":
		g.reproduceGate()
	case it.Kind == KindFix && step == "fix":
		g.fixGate()
	case it.Kind == KindTask && step == "review":
		if haveA {
			if _, err := ReviewBump(fm); err != nil {
				g.fail("%v", err)
			}
		}
	case it.Kind == KindEpic && step == "breakdown":
		g.breakdownGate()
	case it.Kind == KindEpic && step == "tasks":
		g.tasksGate()
	}

	g.res.Passed = len(g.res.Reasons) == 0
	return g.res
}

func stepKnown(k Kind, s Step) bool {
	for _, st := range Steps(k) {
		if st == s {
			return true
		}
	}
	return false
}

func (g *gateCtx) planGate(sha string, haveA bool, approvals []Approval) {
	if haveA {
		for _, a := range approvals {
			if a.Artifact == "plan" && a.Verdict == verdictReject && a.SHA == sha {
				g.fail("plan.md rejected by %s", a.Role)
				break
			}
		}
	}
	trs, err := g.env.Store.Transitions(g.it.ID)
	if err != nil {
		g.fail("reading transitions: %v", err)
		return
	}
	entries := 0
	for _, t := range trs {
		if t.To == "plan" {
			entries++
		}
	}
	if reentries := entries - 1; reentries >= g.cfg.MaxPlanLoops {
		g.fail("plan loop budget %d exhausted", g.cfg.MaxPlanLoops)
	}
}

// tddOrdered returns the item's TDD runs sorted by At (stable, so append
// order breaks ties).
func (g *gateCtx) tddOrdered() ([]TDDRun, bool) {
	runs, err := g.env.Store.TDD(g.it.ID)
	if err != nil {
		g.fail("reading tdd runs: %v", err)
		return nil, false
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].At.Before(runs[j].At) })
	return runs, true
}

// redThenGreen reports whether planStep has a gtb-verified red followed
// later by a gtb-verified green in runs (sorted by At).
func redThenGreen(runs []TDDRun, planStep string) bool {
	red := false
	for _, r := range runs {
		if r.PlanStep != planStep || !isVerifiedTDD(r) {
			continue
		}
		switch r.Phase {
		case "red":
			red = true
		case "green":
			if red {
				return true
			}
		}
	}
	return false
}

func (g *gateCtx) executeGate() {
	_, fm, ok := g.approvedContent("plan")
	if !ok {
		return
	}
	var steps []string
	for _, s := range strings.Split(unquote(fm["steps"]), ",") {
		if s = unquote(s); s != "" {
			steps = append(steps, s)
		}
	}
	if len(steps) == 0 {
		g.fail("plan.md must list steps")
		return
	}
	runs, ok := g.tddOrdered()
	if !ok {
		return
	}
	for _, s := range steps {
		if !redThenGreen(runs, s) {
			g.fail("plan step %s has no verified red then green", s)
		}
	}
}

func (g *gateCtx) verifyGate() {
	if !ChecksApproved(&g.cfg) {
		g.fail("check commands not approved")
		g.wait("approve check commands")
		return
	}
	checks, err := g.env.Store.Checks(g.it.ID)
	if err != nil {
		g.fail("reading checks: %v", err)
		return
	}
	want := CheckCommandsSHA(g.cfg.CheckCommands)
	for _, c := range checks {
		if c.Source == sourceGTB && c.Passed && c.CmdSetSHA == want && !c.At.Before(g.entered) {
			return
		}
	}
	g.fail("no passing check since entering %s", g.step)
}

func (g *gateCtx) reproduceGate() {
	runs, ok := g.tddOrdered()
	if !ok {
		return
	}
	for _, r := range runs {
		if r.PlanStep == reproStep && r.Phase == "red" && isVerifiedTDD(r) {
			return
		}
	}
	path := ArtifactPath(g.env.TownRoot, g.env.Rig, g.it.ID, "report")
	if _, _, fm, err := ReadArtifact(path); err == nil && unquote(fm["repro"]) == "manual" {
		// A manual repro is only as good as the human-approved report.
		g.approvedContent("report")
		return
	}
	g.fail("no verified red for repro (or set repro: manual in report.md)")
}

func (g *gateCtx) fixGate() {
	if runs, ok := g.tddOrdered(); ok && !redThenGreen(runs, reproStep) {
		g.fail("repro has no verified red then green")
	}
	if g.env.Git == nil {
		g.fail("changed files: no git source configured")
		return
	}
	files, err := g.env.Git.Changed(g.it.ID)
	if err != nil {
		g.fail("changed files: %v", err)
		return
	}
	if len(files) > g.cfg.MaxFixFiles {
		g.fail("%d files changed, budget %d", len(files), g.cfg.MaxFixFiles)
	}
}

var (
	acLineRe   = regexp.MustCompile(`(?m)^AC(\d+)\b`)
	dependsRe  = regexp.MustCompile(`(?m)^\s*depends:\s*(\S+)\s*->\s*(\S+)\s*$`)
	acTokenFmt = `\bAC%s\b`
)

// body returns content with any leading frontmatter block removed.
func body(content []byte) string {
	lines := strings.Split(string(normalize(content)), "\n")
	if end := frontmatterEnd(lines); end > 0 {
		lines = lines[end+1:]
	}
	return strings.Join(lines, "\n")
}

func (g *gateCtx) breakdownGate() {
	// The breakdown's own approval is checked by the step artifact gate.
	epic, _, ok := g.approvedContent("epic")
	if !ok {
		return
	}
	path := ArtifactPath(g.env.TownRoot, g.env.Rig, g.it.ID, "breakdown")
	bd, _, _, err := ReadArtifact(path)
	if err != nil {
		return // already reported by the artifact gate
	}
	breakdown := string(normalize(bd))

	seen := map[string]bool{}
	var missing []string
	for _, m := range acLineRe.FindAllStringSubmatch(body(epic), -1) {
		n := m[1]
		if seen[n] {
			continue
		}
		seen[n] = true
		if !regexp.MustCompile(fmt.Sprintf(acTokenFmt, n)).MatchString(breakdown) {
			missing = append(missing, "AC"+n)
		}
	}
	sort.SliceStable(missing, func(i, j int) bool {
		a, _ := strconv.Atoi(missing[i][2:])
		b, _ := strconv.Atoi(missing[j][2:])
		return a < b
	})
	if len(missing) > 0 {
		g.fail("breakdown.md does not cover %s", strings.Join(missing, ", "))
	}

	edges := map[string][]string{}
	for _, m := range dependsRe.FindAllStringSubmatch(breakdown, -1) {
		edges[m[1]] = append(edges[m[1]], m[2])
	}
	if cycle := findCycle(edges); cycle != nil {
		g.fail("dependency cycle: %s", strings.Join(cycle, " -> "))
	}
}

// findCycle returns one cycle in the graph as a path whose first and last
// nodes are equal, or nil. Nodes are visited in sorted order so the result
// is deterministic.
func findCycle(edges map[string][]string) []string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var cycle []string
	var visit func(n string) bool
	visit = func(n string) bool {
		color[n] = grey
		stack = append(stack, n)
		for _, m := range edges[n] {
			switch color[m] {
			case grey:
				for i, s := range stack {
					if s == m {
						cycle = append(append([]string(nil), stack[i:]...), m)
						return true
					}
				}
			case white:
				if visit(m) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	nodes := make([]string, 0, len(edges))
	for n := range edges {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	for _, n := range nodes {
		if color[n] == white && visit(n) {
			return cycle
		}
	}
	return nil
}

func (g *gateCtx) tasksGate() {
	all, err := g.env.Items.List(g.env.Rig)
	if err != nil {
		g.fail("listing items: %v", err)
		return
	}
	children := 0
	for _, c := range all {
		if c.Parent != g.it.ID {
			continue
		}
		children++
		// Only the store's history counts: a bead labelled done is not.
		s, _, history, err := effectiveStep(g.env, c)
		switch {
		case err != nil:
			g.fail("child %s: reading transitions: %v", c.ID, err)
		case !history:
			g.fail("child %s has no transition history, not done", c.ID)
		case s != StepDone:
			g.fail("child %s is at %s, not done", c.ID, s)
		}
	}
	if children == 0 {
		g.fail("epic has no child items")
	}
}
