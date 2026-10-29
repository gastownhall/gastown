package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Observer event kinds. Advance emits EventTransition with a Transition;
// Red, Green, Check, HumanApprove and RecordVerdict emit EventEvidence with
// the TDDRun, CheckRun or Approval they recorded.
const (
	EventTransition = "transition"
	EventEvidence   = "evidence"
)

// emit calls env.Observer when it is set.
func (env GateEnv) emit(kind, itemID string, data any) {
	if env.Observer != nil {
		env.Observer.OnEvent(kind, itemID, data)
	}
}

// ValidateArtifactName checks an artifact name (spec, plan, rca, ...):
// a lower-case letter followed by lower-case letters, digits, _ or -.
func ValidateArtifactName(name string) error {
	if !artifactNameRe.MatchString(name) {
		return fmt.Errorf("workflow: invalid artifact name %q", name)
	}
	return nil
}

// WriteStub creates the first-step artifact of a new item at path (never
// overwriting a file), creating parent directories with mode 0700 and the
// file with 0600. It is what `gtb item new` writes.
func WriteStub(path, id, parent string, kind Kind, title string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "---\nid: %s\nstatus: draft\n", id)
	if parent != "" {
		fmt.Fprintf(&b, "parent: %s\n", parent)
	}
	fmt.Fprintf(&b, "kind: %s\n---\n# %s\n", kind, title)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
