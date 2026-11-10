package workflow

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"golang.org/x/term"
)

// Terminal is the interactive channel a human approves through.
type Terminal interface {
	IsTTY() bool
	Confirm(prompt string) (bool, error)
}

// AgentMarkerEnv lists environment variables set in agent sessions. Any of
// them being non-empty means the caller is not a human at a terminal.
var AgentMarkerEnv = []string{"GT_ROLE", "GT_POLECAT", "BD_ACTOR", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CODEX_SANDBOX"}

var (
	// ErrNotHuman is returned by HumanApprove when the caller looks like an
	// agent (an AgentMarkerEnv variable is set) or is not on a TTY.
	ErrNotHuman = errors.New("workflow: human approval requires an interactive terminal outside an agent session")
	// ErrNotConfirmed is returned by HumanApprove when the human did not
	// answer y or yes; nothing is recorded.
	ErrNotConfirmed = errors.New("workflow: approval not confirmed")
)

const (
	sourceHuman     = "human"
	sourceAgent     = "agent"
	channelTerminal = "terminal"
	verdictConcerns = "concerns"
)

var humanVerdictVerb = map[string]string{
	verdictApprove:        "Approve",
	verdictRequestChanges: "Request changes on",
	verdictReject:         "Reject",
}

var agentVerdicts = map[string]bool{verdictApprove: true, verdictConcerns: true, verdictReject: true}

var artifactNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// currentArtifactSHA returns the hash of item itemID's artifact as it is on
// disk now. A missing file or an invalid name is an error.
func currentArtifactSHA(env GateEnv, itemID, artifact string) (string, error) {
	if err := ValidateArtifactName(artifact); err != nil {
		return "", err
	}
	_, sha, _, err := ReadArtifact(ArtifactPath(env.TownRoot, env.Rig, itemID, artifact))
	return sha, err
}

// RequireHuman is the human guard shared by every human-only action. It
// returns ErrNotHuman (wrapped) when any AgentMarkerEnv variable is non-empty
// or t is not a TTY, checked in that order.
func RequireHuman(t Terminal, getenv func(string) string) error {
	for _, k := range AgentMarkerEnv {
		if getenv(k) != "" {
			return fmt.Errorf("%w (%s is set)", ErrNotHuman, k)
		}
	}
	if t == nil || !t.IsTTY() {
		return fmt.Errorf("%w (not a TTY)", ErrNotHuman)
	}
	return nil
}

// HumanApprove records a human verdict (approve, request_changes or reject)
// on the current content of artifact. It refuses with ErrNotHuman when any
// AgentMarkerEnv variable is non-empty or t is not a TTY (checked in that
// order, before the artifact is read), then asks the human to confirm the
// artifact sha. Only a confirmed answer is recorded, with Role and Source
// "human", Channel "terminal" and SHA the hash that was shown.
func HumanApprove(env GateEnv, t Terminal, getenv func(string) string, itemID, artifact, verdict, actor string) (Approval, error) {
	if err := validateID(itemID); err != nil {
		return Approval{}, err
	}
	if err := RequireHuman(t, getenv); err != nil {
		return Approval{}, err
	}
	verb, ok := humanVerdictVerb[verdict]
	if !ok {
		return Approval{}, fmt.Errorf("workflow: invalid human verdict %q (want approve, request_changes or reject)", verdict)
	}
	sha, err := currentArtifactSHA(env, itemID, artifact)
	if err != nil {
		return Approval{}, err
	}
	yes, err := t.Confirm(fmt.Sprintf("%s %s sha %s for %s? [y/N]", verb, artifact, sha[:12], itemID))
	if err != nil {
		return Approval{}, fmt.Errorf("workflow: reading confirmation: %w", err)
	}
	if !yes {
		return Approval{}, ErrNotConfirmed
	}
	a := Approval{
		ItemID: itemID, Artifact: artifact, SHA: sha, Verdict: verdict,
		Role: roleHuman, Source: sourceHuman, Channel: channelTerminal, Actor: actor, At: env.now(),
	}
	if err := env.Store.AddApproval(a); err != nil {
		return Approval{}, err
	}
	env.emit(EventEvidence, itemID, a)
	return a, nil
}

// RecordVerdict records an agent reviewer's verdict (approve, concerns or
// reject) on the current content of artifact. role is free-form but may not
// be "human" (case-insensitive, trimmed); Source is always "agent".
func RecordVerdict(env GateEnv, itemID, artifact, role, verdict, actor string) (Approval, error) {
	if err := validateID(itemID); err != nil {
		return Approval{}, err
	}
	role = strings.TrimSpace(role)
	if role == "" {
		return Approval{}, errors.New("workflow: reviewer role is required")
	}
	if strings.EqualFold(role, roleHuman) {
		return Approval{}, errors.New("workflow: agents cannot record verdicts as role human; use human approval")
	}
	if !agentVerdicts[verdict] {
		return Approval{}, fmt.Errorf("workflow: invalid reviewer verdict %q (want approve, concerns or reject)", verdict)
	}
	sha, err := currentArtifactSHA(env, itemID, artifact)
	if err != nil {
		return Approval{}, err
	}
	a := Approval{
		ItemID: itemID, Artifact: artifact, SHA: sha, Verdict: verdict,
		Role: role, Source: sourceAgent, Actor: actor, At: env.now(),
	}
	if err := env.Store.AddApproval(a); err != nil {
		return Approval{}, err
	}
	env.emit(EventEvidence, itemID, a)
	return a, nil
}

// StdTerminal is the production Terminal over a process's stdin and stdout.
type StdTerminal struct {
	In  *os.File
	Out io.Writer
}

// IsTTY reports whether both In and Out are terminals. An Out that is not an
// *os.File is never a TTY.
func (s StdTerminal) IsTTY() bool {
	out, ok := s.Out.(*os.File)
	return ok && s.In != nil && out != nil &&
		term.IsTerminal(int(s.In.Fd())) && term.IsTerminal(int(out.Fd()))
}

// Confirm writes prompt to Out and reads one line from In; see confirmAnswer.
func (s StdTerminal) Confirm(prompt string) (bool, error) {
	if _, err := fmt.Fprint(s.Out, prompt+" "); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(s.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return confirmAnswer(line), nil
}

// confirmAnswer reports whether line is y or yes (case-insensitive, trimmed).
func confirmAnswer(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
