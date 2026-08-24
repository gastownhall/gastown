package beads

// This test pins the canonical runtime contract for wisp-backed escalation
// visibility, which was broken by a silent regression in the escalation listing
// helpers (hq-vcv).
//
// Runtime reality (verified against the live Dolt server, database hq):
//  1. gt escalate creates escalations as ephemeral wisps: --ephemeral
//     --wisp-type=escalation --labels=gt:escalation. The wisp is the canonical
//     escalation record (ack/close operate on it).
//  2. Mail delivery creates a durable mirror: labels gt:message + gt:escalation
//     + msg-type:escalation + escalation:<wisp-id>. The mirror is an artefact of
//     delivery, not an escalation record.
//  3. bd list hides ephemeral wisps by default; they appear only when
//     --include-infra is passed. bd mol wisp list --type=escalation does NOT
//     surface them (it returns 0 escalation-typed wisps).
//
// Therefore ListEscalations MUST pass --include-infra to bd list, otherwise it
// only sees the mail mirrors — all of which carry gt:message — and
// filterEscalationRecords (correctly) drops every one, yielding an empty list.
// That was hq-vcv: "No escalations found" printed while 29 open escalation
// wisps existed.
//
// The stub below emulates exactly that db-side filtering: without
// --include-infra it returns only the mail mirror; with --include-infra it also
// returns the wisp escalation. The assertions encode the contract directly, so
// removing --include-infra makes the test fail (RED) and adding it makes it pass
// (GREEN). The stub mirrors the real bd behaviour rather than papering over it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wispListStubScript writes a bd stub that emulates the runtime's visibility
// rule: ephemeral wisp-backed escalations are returned ONLY when the caller
// passes --include-infra; mail mirrors are always returned. The script echoes
// every argv entry to argsPath so a test can assert on the flags, then emits the
// requested JSON.
func wispListStubScript(t *testing.T, argsPath string) string {
	t.Helper()
	return `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsPath + `"
done
# Runtime contract: --include-infra is required to surface ephemeral wisps.
# Scan argv for the flag.
has_infra=0
for a in "$@"; do
  if [ "$a" = "--include-infra" ]; then has_infra=1; fi
done
if [ "$has_infra" -eq 1 ]; then
cat <<'EOF'
[
  {"id":"hq-wisp-0001","title":"wisp-backed escalation","status":"open","priority":2,"type":"task","labels":["gt:escalation","severity:high"],"ephemeral":true,"wisp_type":"escalation","description":"wisp-backed escalation\n\nseverity: high\nreason: root cause\nescalated_by: gastown/deacon\nescalated_at: 2026-08-23T00:00:00Z\nacked_by: null\nacked_at: null","created_at":"2026-08-23T00:00:00Z"},
  {"id":"hq-mirror-0001","title":"mail mirror","status":"open","priority":2,"type":"task","labels":["gt:escalation","gt:message","msg-type:escalation"],"ephemeral":false,"description":"mail body","created_at":"2026-08-23T00:00:00Z"}
]
EOF
else
cat <<'EOF'
[
  {"id":"hq-mirror-0001","title":"mail mirror","status":"open","priority":2,"type":"task","labels":["gt:escalation","gt:message","msg-type:escalation"],"ephemeral":false,"description":"mail body","created_at":"2026-08-23T00:00:00Z"}
]
EOF
fi
exit 0
`
}

func setupWispListStub(t *testing.T) (*Beads, string) {
	t.Helper()
	stubDir := t.TempDir()
	argsPath := filepath.Join(stubDir, "args.txt")
	stubPath := filepath.Join(stubDir, "bd")
	script := wispListStubScript(t, argsPath)
	if err := os.WriteFile(stubPath, []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetBdAllowStaleCacheForTest()
	b := NewIsolated(t.TempDir())
	return b, argsPath
}

// TestListEscalationsEnumeratesWispBacked is the regression test for hq-vcv.
// Without --include-infra it must FAIL (the wisp is invisible); with the fix it
// must PASS and return the wisp, dropping the gt:message mail mirror.
func TestListEscalationsEnumeratesWispBacked(t *testing.T) {
	b, argsPath := setupWispListStub(t)

	issues, err := b.ListEscalations()
	if err != nil {
		t.Fatalf("ListEscalations: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("ListEscalations returned %d issues, want 1 (the wisp-backed escalation; mail mirror filtered)\n got: %+v", len(issues), ids(issues))
	}
	if issues[0].ID != "hq-wisp-0001" {
		t.Fatalf("ListEscalations returned %q, want hq-wisp-0001", issues[0].ID)
	}

	args := readArgs(t, argsPath)
	if !contains(args, "--include-infra") {
		t.Fatalf("ListEscalations MUST pass --include-infra so bd list surfaces ephemeral wisps; args: %v", args)
	}
}

// TestListEscalationsByFingerprintSeeksWisps confirms the dedup path also
// queries the wisps table (fingerprint lives on the wisp, not the mirror).
func TestListEscalationsByFingerprintSeeksWisps(t *testing.T) {
	b, argsPath := setupWispListStub(t)

	matches, err := b.ListEscalationsByFingerprint("escalation-fp:deadbeef")
	if err != nil {
		t.Fatalf("ListEscalationsByFingerprint: %v", err)
	}
	if len(matches) != 1 || matches[0].ID != "hq-wisp-0001" {
		t.Fatalf("ListEscalationsByFingerprint returned %d matches (%s), want wisp hq-wisp-0001", len(matches), ids(matches))
	}
	args := readArgs(t, argsPath)
	if !contains(args, "--include-infra") {
		t.Fatalf("ListEscalationsByFingerprint MUST pass --include-infra; args: %v", args)
	}
}

func ids(issues []*Issue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.ID)
	}
	return out
}

func contains(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}

func readArgs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	return strings.Fields(strings.TrimSpace(string(data)))
}

// Guard: the stub must emit valid JSON so a real bd.ListOptions round-trip
// stays well-typed. Runs the stub with --include-infra and asserts the
// output unmarshals as an issue slice -- keeping the regression honest.
func TestWispListStubEmitsValidJSON(t *testing.T) {
	b, _ := setupWispListStub(t)
	out, err := b.run("list", "--label=gt:escalation", "--status=open", "--include-infra", "--json")
	if err != nil {
		t.Fatalf("stub bd list: %v", err)
	}
	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		t.Fatalf("stub emitted non-JSON: %v; output: %s", err, out)
	}
}
