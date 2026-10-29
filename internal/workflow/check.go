package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrChecksNotApproved is returned by Check when the rig's check commands do
// not match their human-approved hash.
var ErrChecksNotApproved = errors.New("workflow: check commands not approved")

// Check runs every configured check command in dir and records one verified
// CheckRun. It refuses, running nothing, unless ChecksApproved.
//
// Each command runs as []string{"sh", "-c", cmd} through r. This is the only
// place gtb uses a shell: check commands are human-approved rig config (their
// hash is pinned by CheckCommandsApprovedSHA), unlike agent-supplied TDD argv.
// Execution stops at the first failing command; Passed is true only if every
// command exits 0. The tail is the failing command's text, a newline and its
// tail (capped by capTail), or the last command's tail when all pass.
func Check(env GateEnv, r Runner, itemID, dir string) (CheckRun, error) {
	if err := validateID(itemID); err != nil {
		return CheckRun{}, err
	}
	cfg := env.Config
	if cfg == nil || !ChecksApproved(cfg) {
		return CheckRun{}, ErrChecksNotApproved
	}
	cmds := append([]string(nil), cfg.CheckCommands...)
	if len(cmds) == 0 {
		return CheckRun{}, errors.New("workflow: no check commands configured")
	}
	run := CheckRun{
		ItemID: itemID, CmdSetSHA: CheckCommandsSHA(cmds), Passed: true,
		Source: sourceGTB, Provenance: provenanceVerified,
	}
	for _, c := range cmds {
		exit, tail, err := r.Run(context.Background(), dir, []string{"sh", "-c", c})
		if err != nil {
			return CheckRun{}, fmt.Errorf("workflow: running check %q: %w", c, err)
		}
		run.Tail = tail
		if exit != 0 {
			run.Passed = false
			run.Tail = capTail(c, tail)
			break
		}
	}
	run.At = env.now()
	if err := env.Store.AddCheck(run); err != nil {
		return CheckRun{}, err
	}
	env.emit(EventEvidence, itemID, run)
	return run, nil
}

// cmdLineFlattener turns a multi-line check command into one line (R39).
var cmdLineFlattener = strings.NewReplacer("\r\n", " ⏎ ", "\n", " ⏎ ", "\r", " ⏎ ")

// capTail joins cmd, "\n" and out into at most tailBytes bytes without
// splitting a rune. Newlines in cmd are flattened to " ⏎ " so the command
// prefix is always exactly one line and the first "\n" always ends it
// (R39; workflowlink splits there). It keeps as much of the start of cmd as
// fits (the newline included), then fills the remaining budget with the end
// of out.
func capTail(cmd, out string) string {
	prefix := FirstBytes(cmdLineFlattener.Replace(cmd)+"\n", tailBytes)
	return prefix + lastBytes(out, tailBytes-len(prefix))
}
