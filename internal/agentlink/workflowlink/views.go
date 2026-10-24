// Package workflowlink adapts the gated workflow (internal/workflow) to the
// machine link (internal/agentlink): it projects items and artifacts into
// wire views, queues workflow events, builds snapshots and applies server
// requests. agentlink itself never imports workflow; this package bridges
// them.
package workflowlink

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/steveyegge/gastown/internal/agentlink"
	"github.com/steveyegge/gastown/internal/workflow"
)

// maxArtifactBytes caps an artifact sent to the server. Larger artifacts are
// left out (they stay readable on the machine).
const maxArtifactBytes = 256 << 10

// DiffStatFunc returns the diffstat of branch against the rig's base.
type DiffStatFunc func(rig, branch string) ([]agentlink.FileStat, error)

// itemView projects it. The step is the store-derived one (the latest
// transition wins over the bead), matching what the gates see. diff may be
// nil; it is only called for items with a branch, and its errors leave the
// diffstat empty.
func itemView(env workflow.GateEnv, it *workflow.Item, diff DiffStatFunc) agentlink.ItemView {
	rig := it.Rig
	if rig == "" {
		rig = env.Rig
	}
	step, entered := it.Step, it.StepEnteredAt
	if env.Store != nil {
		if trs, err := env.Store.Transitions(it.ID); err == nil && len(trs) > 0 {
			last := trs[len(trs)-1]
			step, entered = last.To, last.At
		}
	}
	iv := agentlink.ItemView{
		ID: it.ID, Rig: rig, Kind: string(it.Kind), Step: string(step),
		// Title and branch are agent-writable and the diffstat paths are git
		// output: redact them like any other free text (R37).
		Title: RedactText(it.Title), Parent: it.Parent, Branch: RedactText(it.Branch),
		WaitingForHuman: it.WaitingForHuman, StepEnteredAt: entered.UTC(),
	}
	if diff != nil && it.Branch != "" {
		if fs, err := diff(rig, it.Branch); err == nil {
			for i := range fs {
				fs[i].Path = RedactText(fs[i].Path)
			}
			iv.Diffstat = fs
		}
	}
	return iv
}

// itemArtifacts returns the item's markdown artifacts: the regular (not
// symlinked) <name>.md files directly in its item directory whose name is a
// valid artifact name, at most maxArtifactBytes each, sorted by name. Nothing
// else in the directory (sources, outputs/, logs) is ever read.
func itemArtifacts(townRoot, rig, itemID string) []agentlink.ArtifactView {
	if workflow.ValidateID(itemID) != nil {
		return nil
	}
	dir := filepath.Dir(workflow.ArtifactPath(townRoot, rig, itemID, "x"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []agentlink.ArtifactView
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || workflow.ValidateArtifactName(name) != nil || !e.Type().IsRegular() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxArtifactBytes {
			continue
		}
		content, sha, _, err := workflow.ReadArtifact(p)
		if err != nil || len(content) > maxArtifactBytes {
			continue
		}
		out = append(out, agentlink.ArtifactView{Name: name, SHA: sha, Content: string(content)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// viewHash fingerprints a view (without artifacts) for change detection.
func viewHash(iv agentlink.ItemView) string {
	iv.Artifacts = nil
	b, _ := json.Marshal(iv)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// tailTruncated reports whether a stored tail was cut at its start: it is at
// workflow.TailBytes, less up to utf8.UTFMax-1 bytes the cut may skip to
// reach a rune boundary. An output of exactly that size is also treated as
// cut, which only costs its first line.
func tailTruncated(tail string) bool {
	return len(tail) > workflow.TailBytes-utf8.UTFMax
}

// checkTail is the outbound form of a check run's tail. A failed run's tail
// is "<failing command>\n<end of its output>" (workflow capTail), so only
// the output part can be cut at its start; the command line is kept.
func checkTail(c workflow.CheckRun) string {
	if c.Passed {
		return SafeTail(c.Tail, tailTruncated(c.Tail))
	}
	cmd, out, ok := strings.Cut(c.Tail, "\n")
	if !ok {
		return SafeTail(c.Tail, false)
	}
	return SafeTail(cmd, false) + "\n" + SafeTail(out, tailTruncated(c.Tail))
}

// evidenceData projects a workflow evidence record into the JSON carried by
// an evidence event. Command lines and output tails are command output and
// go through RedactText (R37).
func evidenceData(data any) (json.RawMessage, bool) {
	var m map[string]any
	switch v := data.(type) {
	case workflow.Approval:
		m = map[string]any{
			"kind": "approval", "artifact": v.Artifact, "sha": v.SHA, "verdict": v.Verdict,
			"role": v.Role, "source": v.Source, "channel": v.Channel, "actor": v.Actor, "at": v.At.UTC(),
		}
	case workflow.TDDRun:
		m = map[string]any{
			"kind": "tdd", "plan_step": v.PlanStep, "phase": v.Phase, "cmd": SafeTail(v.Cmd, false), "exit": v.Exit,
			"tail": SafeTail(v.Tail, tailTruncated(v.Tail)), "source": v.Source, "provenance": v.Provenance, "at": v.At.UTC(),
		}
	case workflow.CheckRun:
		m = map[string]any{
			"kind": "check", "cmd_set_sha": v.CmdSetSHA, "passed": v.Passed,
			"tail": checkTail(v), "source": v.Source, "provenance": v.Provenance, "at": v.At.UTC(),
		}
	default:
		return nil, false
	}
	b, err := json.Marshal(m)
	return b, err == nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}
