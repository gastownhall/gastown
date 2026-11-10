package workflowlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/steveyegge/gastown/internal/agentlink"
	"github.com/steveyegge/gastown/internal/workflow"
)

// maxPeekBytes caps the terminal.peek capture (the tail is kept).
const maxPeekBytes = 64 << 10

// peekLines is how many pane lines terminal.peek captures.
const peekLines = 200

// rigNameRe matches a rig name (as gtb's workflow commands accept it).
var rigNameRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// WorkflowApplier is the production agentlink.Applier. It applies server
// requests to the machine's gated workflow. Sling, Cancel and Peek are
// function fields so this package needs neither internal/cmd nor tmux; the
// daemon supplies them.
//
// approve and request_changes skip HumanApprove's TTY guard by design: the
// maintainer's logged-in dashboard session is the human proof (spec §7.3),
// and the server refuses those requests from API tokens.
type WorkflowApplier struct {
	TownRoot string
	// Rigs lists the town's rigs (Gated marks workflow-enabled ones).
	Rigs func() ([]agentlink.Rig, error)
	// Env opens the workflow environment of a rig.
	Env func(rig string) (workflow.GateEnv, error)
	// Sling assigns itemID to a polecat in rig (gtb sling <id> <rig>).
	Sling func(ctx context.Context, itemID, rig string) error
	// Cancel stops itemID's worker (gtb unsling <id>).
	Cancel func(ctx context.Context, itemID string) error
	// Peek returns the last lines of itemID's polecat pane.
	Peek func(ctx context.Context, rig, itemID string) (string, error)
	// DiffStat fills inspect's diffstat; nil leaves it empty.
	DiffStat DiffStatFunc
	// Now is the machine clock (expiry and approval time); nil is time.Now.
	Now func() time.Time
}

func (a *WorkflowApplier) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

// Apply implements agentlink.Applier. A request whose ExpiresAt is not after
// Now answers "expired" before anything else runs, and an unknown type
// answers "unsupported".
func (a *WorkflowApplier) Apply(ctx context.Context, r agentlink.Request) agentlink.Result {
	if !r.ExpiresAt.After(a.now()) {
		return agentlink.Fail(r.Key, agentlink.ErrCodeExpired)
	}
	var res agentlink.Result
	switch r.Type {
	case "approve", "request_changes":
		res = a.approve(r)
	case "item.create":
		res = a.create(r)
	case "item.sling":
		res = a.sling(ctx, r)
	case "item.cancel":
		res = a.cancel(ctx, r)
	case "snapshot":
		res = agentlink.Result{OK: true} // the loop resyncs on a successful snapshot
	case "inspect":
		res = a.inspect(r)
	case "terminal.peek":
		res = a.peek(ctx, r)
	default:
		res = agentlink.Fail(r.Key, agentlink.ErrCodeUnsupported)
	}
	res.Key = r.Key
	return res
}

func invalid(msg string) agentlink.Result {
	return agentlink.Result{OK: false, Error: agentlink.ErrCodeInvalid, Data: mustJSON(map[string]string{"message": msg})}
}

func failed(code string, err error) agentlink.Result {
	// Error text can quote git output; strip credentials from URLs (R34).
	return agentlink.Result{OK: false, Error: code, Data: mustJSON(map[string]string{"message": capString(RedactText(err.Error()), 1000)})}
}

// findItem locates itemID in the town's gated rigs. The item ID is validated
// first.
func (a *WorkflowApplier) findItem(itemID string) (workflow.GateEnv, *workflow.Item, agentlink.Result, bool) {
	if err := workflow.ValidateID(itemID); err != nil {
		return workflow.GateEnv{}, nil, invalid("invalid item id"), false
	}
	rigs, err := a.Rigs()
	if err != nil {
		return workflow.GateEnv{}, nil, failed(agentlink.ErrCodeFailed, err), false
	}
	var firstErr error
	for _, r := range rigs {
		if !r.Gated {
			continue
		}
		env, err := a.Env(r.Name)
		if err != nil {
			firstErr = cmpErr(firstErr, err)
			continue
		}
		it, err := env.Items.Get(itemID)
		if errors.Is(err, workflow.ErrItemNotFound) {
			continue
		}
		if err != nil {
			firstErr = cmpErr(firstErr, err)
			continue
		}
		if it.Rig != "" && it.Rig != r.Name {
			// beads routing found it from another rig's database
			continue
		}
		return env, it, agentlink.Result{}, true
	}
	if firstErr != nil {
		return workflow.GateEnv{}, nil, failed(agentlink.ErrCodeFailed, firstErr), false
	}
	return workflow.GateEnv{}, nil, agentlink.Result{OK: false, Error: agentlink.ErrCodeNotFound}, false
}

func cmpErr(first, err error) error {
	if first != nil {
		return first
	}
	return err
}

func (a *WorkflowApplier) approve(r agentlink.Request) agentlink.Result {
	var p agentlink.ApproveParams
	if err := json.Unmarshal(r.Params, &p); err != nil {
		return invalid("params must be {artifact, sha, actor}")
	}
	if workflow.ValidateID(r.ItemID) != nil {
		return invalid("invalid item id")
	}
	if workflow.ValidateArtifactName(p.Artifact) != nil {
		return invalid("invalid artifact name")
	}
	p.Actor = strings.TrimSpace(p.Actor)
	if p.SHA == "" || p.Actor == "" {
		return invalid("sha and actor are required")
	}
	env, it, res, ok := a.findItem(r.ItemID)
	if !ok {
		return res
	}
	rig := it.Rig
	if rig == "" {
		rig = env.Rig
	}
	_, sha, _, err := workflow.ReadArtifact(workflow.ArtifactPath(a.TownRoot, rig, it.ID, p.Artifact))
	if err != nil {
		return agentlink.Result{OK: false, Error: agentlink.ErrCodeNotFound, Data: mustJSON(map[string]string{"message": "artifact not found"})}
	}
	if sha != p.SHA {
		return agentlink.Result{OK: false, Error: agentlink.ErrCodeStaleSHA, Data: mustJSON(map[string]string{"sha": sha})}
	}
	ap := workflow.Approval{
		ItemID: it.ID, Artifact: p.Artifact, SHA: sha, Verdict: r.Type,
		Role: "human", Source: "human", Channel: "dashboard", Actor: p.Actor, At: a.now(),
	}
	if err := env.Store.AddApproval(ap); err != nil {
		return failed(agentlink.ErrCodeFailed, err)
	}
	if env.Observer != nil {
		env.Observer.OnEvent(workflow.EventEvidence, it.ID, ap)
	}
	return agentlink.Result{OK: true, Data: mustJSON(map[string]string{"sha": sha})}
}

type createParams struct {
	Rig    string `json:"rig"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Parent string `json:"parent"`
}

func (a *WorkflowApplier) create(r agentlink.Request) agentlink.Result {
	var p createParams
	if err := json.Unmarshal(r.Params, &p); err != nil {
		return invalid("params must be {rig, kind, title, parent}")
	}
	if !rigNameRe.MatchString(p.Rig) {
		return invalid("invalid rig name")
	}
	kind := workflow.Kind(p.Kind)
	if len(workflow.Steps(kind)) == 0 {
		return invalid("kind must be task, fix or epic")
	}
	title := strings.Join(strings.Fields(p.Title), " ")
	if title == "" || len(title) > 500 {
		return invalid("title is required (at most 500 bytes)")
	}
	if p.Parent != "" && workflow.ValidateID(p.Parent) != nil {
		return invalid("invalid parent id")
	}
	rigs, err := a.Rigs()
	if err != nil {
		return failed(agentlink.ErrCodeFailed, err)
	}
	gated := false
	for _, rg := range rigs {
		if rg.Name == p.Rig && rg.Gated {
			gated = true
		}
	}
	if !gated {
		return invalid("rig is not a gated rig on this machine")
	}
	env, err := a.Env(p.Rig)
	if err != nil {
		return failed(agentlink.ErrCodeFailed, err)
	}
	it, err := env.Items.Create(p.Rig, kind, title, p.Parent)
	if err != nil {
		return failed(agentlink.ErrCodeFailed, err)
	}
	step := workflow.Steps(kind)[0]
	path := workflow.ArtifactPath(a.TownRoot, p.Rig, it.ID, workflow.ArtifactFor(kind, step))
	if err := workflow.WriteStub(path, it.ID, p.Parent, kind, title); err != nil {
		return failed(agentlink.ErrCodeFailed, fmt.Errorf("item %s created but stub not written: %w", it.ID, err))
	}
	return agentlink.Result{OK: true, Data: mustJSON(map[string]string{
		"id": it.ID, "rig": p.Rig, "kind": string(kind), "step": string(step),
	})}
}

func (a *WorkflowApplier) sling(ctx context.Context, r agentlink.Request) agentlink.Result {
	if a.Sling == nil {
		return agentlink.Fail(r.Key, agentlink.ErrCodeUnsupported)
	}
	env, it, res, ok := a.findItem(r.ItemID)
	if !ok {
		return res
	}
	rig := it.Rig
	if rig == "" {
		rig = env.Rig
	}
	if err := a.Sling(ctx, it.ID, rig); err != nil {
		return failed(agentlink.ErrCodeFailed, err)
	}
	return agentlink.Result{OK: true}
}

func (a *WorkflowApplier) cancel(ctx context.Context, r agentlink.Request) agentlink.Result {
	if a.Cancel == nil {
		return agentlink.Fail(r.Key, agentlink.ErrCodeUnsupported)
	}
	_, it, res, ok := a.findItem(r.ItemID)
	if !ok {
		return res
	}
	if err := a.Cancel(ctx, it.ID); err != nil {
		return failed(agentlink.ErrCodeFailed, err)
	}
	return agentlink.Result{OK: true}
}

func (a *WorkflowApplier) inspect(r agentlink.Request) agentlink.Result {
	env, it, res, ok := a.findItem(r.ItemID)
	if !ok {
		return res
	}
	iv := itemView(env, it, a.DiffStat)
	iv.Artifacts = itemArtifacts(a.TownRoot, iv.Rig, it.ID)
	return agentlink.Result{OK: true, Data: mustJSON(iv)}
}

func (a *WorkflowApplier) peek(ctx context.Context, r agentlink.Request) agentlink.Result {
	if a.Peek == nil {
		return agentlink.Fail(r.Key, agentlink.ErrCodeUnsupported)
	}
	env, it, res, ok := a.findItem(r.ItemID)
	if !ok {
		return res
	}
	rig := it.Rig
	if rig == "" {
		rig = env.Rig
	}
	out, err := a.Peek(ctx, rig, it.ID)
	if err != nil {
		return failed(agentlink.ErrCodeNotFound, err)
	}
	// Redact before capping so a secret cut at the cap boundary cannot
	// survive in part (R37).
	red := RedactText(out)
	lines := lastBytes(red, maxPeekBytes)
	return agentlink.Result{OK: true, Data: mustJSON(map[string]string{"lines": SafeTail(lines, len(lines) < len(red))})}
}

// lastBytes returns the longest suffix of s of at most n bytes that starts on
// a rune boundary.
func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// capString returns the longest prefix of s of at most n bytes that ends on
// a rune boundary.
func capString(s string, n int) string {
	return workflow.FirstBytes(s, n)
}
