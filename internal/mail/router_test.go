package mail

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/testutil"
	"github.com/steveyegge/gastown/internal/tmux"
)

func TestDetectTownRoot(t *testing.T) {
	// Unset GT_TOWN_ROOT/GT_ROOT so tests exercise workspace.Find fallback.
	// (The real session always has these set; this tests the detection logic itself.)
	t.Setenv("GT_TOWN_ROOT", "")
	t.Setenv("GT_ROOT", "")

	// Create temp directory structure
	tmpDir := t.TempDir()
	townRoot := filepath.Join(tmpDir, "town")
	mayorDir := filepath.Join(townRoot, "mayor")
	rigDir := filepath.Join(townRoot, "gastown", "polecats", "test")

	// Create mayor/town.json marker
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		startDir string
		want     string
	}{
		{
			name:     "from town root",
			startDir: townRoot,
			want:     townRoot,
		},
		{
			name:     "from rig subdirectory",
			startDir: rigDir,
			want:     townRoot,
		},
		{
			name:     "from mayor directory",
			startDir: mayorDir,
			want:     townRoot,
		},
		{
			name:     "from non-town directory",
			startDir: tmpDir,
			want:     "", // No town.json marker above tmpDir
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectTownRoot(tt.startDir)
			if got != tt.want {
				t.Errorf("detectTownRoot(%q) = %q, want %q", tt.startDir, got, tt.want)
			}
		})
	}
}

// TestDetectTownRoot_PrefersEnvVar verifies that GT_TOWN_ROOT takes priority
// over workspace detection, preventing rig-level mayor/town.json from being
// mistaken for the town root.
func TestDetectTownRoot_PrefersEnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	// Outer town root (the actual town)
	outerTown := filepath.Join(tmpDir, "town")
	if err := os.MkdirAll(filepath.Join(outerTown, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outerTown, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Nested rig that also has a mayor/town.json (the trap)
	nestedRig := filepath.Join(outerTown, "gastown")
	if err := os.MkdirAll(filepath.Join(nestedRig, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedRig, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("env var overrides nested workspace detection", func(t *testing.T) {
		t.Setenv("GT_TOWN_ROOT", outerTown)
		// Starting from the nested rig would normally find the rig's own
		// mayor/town.json first. With GT_TOWN_ROOT set, we get the outer town.
		got := detectTownRoot(nestedRig)
		if got != outerTown {
			t.Errorf("detectTownRoot(%q) = %q, want %q (outer town root via env var)", nestedRig, got, outerTown)
		}
	})

	t.Run("GT_ROOT also works", func(t *testing.T) {
		t.Setenv("GT_TOWN_ROOT", "")
		t.Setenv("GT_ROOT", outerTown)
		got := detectTownRoot(nestedRig)
		if got != outerTown {
			t.Errorf("detectTownRoot(%q) = %q, want %q (outer town root via GT_ROOT)", nestedRig, got, outerTown)
		}
	})

	t.Run("falls back to workspace.Find without env vars", func(t *testing.T) {
		t.Setenv("GT_TOWN_ROOT", "")
		t.Setenv("GT_ROOT", "")
		// Without env vars, starting from the nested rig finds the nested
		// mayor/town.json (the bug this fix addresses — documenting current behavior).
		got := detectTownRoot(nestedRig)
		// workspace.Find returns the nested rig since it stops at first primary marker
		if got != nestedRig {
			t.Logf("detectTownRoot fallback returned %q (expected %q for nested-workspace scenario)", got, nestedRig)
		}
	})
}

func TestIsTownLevelAddress(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{"mayor", true},
		{"mayor/", true},
		{"deacon", true},
		{"deacon/", true},
		{"overseer", true},
		{"gastown/refinery", false},
		{"gastown/polecats/Toast", false},
		{"gastown/", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got := isTownLevelAddress(tt.address)
			if got != tt.want {
				t.Errorf("isTownLevelAddress(%q) = %v, want %v", tt.address, got, tt.want)
			}
		})
	}
}

func TestAddressToSessionIDs(t *testing.T) {
	// Set up prefix registry for test
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	defer session.SetDefaultRegistry(old)

	tests := []struct {
		address string
		want    []string
	}{
		// Overseer (human operator) - single session
		{"overseer", []string{"hq-overseer"}},

		// Town-level addresses - single session
		{"mayor", []string{"hq-mayor"}},
		{"mayor/", []string{"hq-mayor"}},
		{"deacon", []string{"hq-deacon"}},

		// Rig singletons - single session (no crew/polecat ambiguity)
		{"gastown/refinery", []string{"gt-refinery"}},
		{"beads/witness", []string{"bd-witness"}},

		// Ambiguous addresses - try both crew and polecat variants
		{"gastown/Toast", []string{"gt-crew-Toast", "gt-Toast"}},
		{"beads/ruby", []string{"bd-crew-ruby", "bd-ruby"}},

		// Explicit crew/polecat - single session
		{"gastown/crew/max", []string{"gt-crew-max"}},
		{"gastown/polecat/nux", []string{"gt-nux"}},
		{"gastown/polecats/nux", []string{"gt-nux"}},

		// Invalid addresses - empty result
		{"gastown/", nil}, // Empty target
		{"gastown", nil},  // No slash
		{"", nil},         // Empty address
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got := AddressToSessionIDs(tt.address)
			if len(got) != len(tt.want) {
				t.Errorf("AddressToSessionIDs(%q) = %v, want %v", tt.address, got, tt.want)
				return
			}
			for i, v := range got {
				if v != tt.want[i] {
					t.Errorf("AddressToSessionIDs(%q)[%d] = %q, want %q", tt.address, i, v, tt.want[i])
				}
			}
		})
	}
}

func TestIsSelfMail(t *testing.T) {
	tests := []struct {
		from string
		to   string
		want bool
	}{
		{"mayor/", "mayor/", true},
		{"mayor", "mayor/", true},
		{"mayor/", "mayor", true},
		{"gastown/Toast", "gastown/Toast", true},
		{"gastown/Toast/", "gastown/Toast", true},
		{"gastown/crew/max", "gastown/max", true},
		{"gastown/max", "gastown/crew/max", true},
		{"gastown/polecats/Toast", "gastown/Toast", true},
		{"gastown/Toast", "gastown/polecats/Toast", true},
		{"gastown/crew/max", "gastown/polecats/max", true},
		{"mayor/", "deacon/", false},
		{"gastown/Toast", "gastown/Nux", false},
		{"gastown/crew/max", "gastown/crew/nux", false},
		{"", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.from+"->"+tt.to, func(t *testing.T) {
			got := isSelfMail(tt.from, tt.to)
			if got != tt.want {
				t.Errorf("isSelfMail(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestShouldBeWisp(t *testing.T) {
	r := &Router{}

	tests := []struct {
		name string
		msg  *Message
		want bool
	}{
		{
			name: "explicit wisp flag",
			msg:  &Message{Subject: "Regular message", Wisp: true},
			want: true,
		},
		{
			name: "POLECAT_STARTED subject",
			msg:  &Message{Subject: "POLECAT_STARTED: Toast"},
			want: true,
		},
		{
			name: "polecat_done subject (lowercase)",
			msg:  &Message{Subject: "polecat_done: work complete"},
			want: true,
		},
		{
			name: "NUDGE subject",
			msg:  &Message{Subject: "NUDGE: check your hook"},
			want: true,
		},
		{
			name: "START_WORK subject",
			msg:  &Message{Subject: "START_WORK: gt-123"},
			want: true,
		},
		{
			name: "regular message",
			msg:  &Message{Subject: "Please review this PR"},
			want: false,
		},
		{
			name: "LIFECYCLE:Shutdown subject",
			msg:  &Message{Subject: "LIFECYCLE:Shutdown capable"},
			want: true,
		},
		{
			name: "LIFECYCLE: polecat requesting shutdown",
			msg:  &Message{Subject: "LIFECYCLE: polecat-nux requesting shutdown"},
			want: true,
		},
		{
			name: "MERGED subject",
			msg:  &Message{Subject: "MERGED nux"},
			want: true,
		},
		{
			name: "MERGE_READY subject",
			msg:  &Message{Subject: "MERGE_READY nux"},
			want: true,
		},
		{
			name: "MERGE_FAILED subject",
			msg:  &Message{Subject: "MERGE_FAILED nux"},
			want: true,
		},
		{
			name: "handoff message (not auto-wisp)",
			msg:  &Message{Subject: "HANDOFF: context notes"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.shouldBeWisp(tt.msg)
			if got != tt.want {
				t.Errorf("shouldBeWisp(%v) = %v, want %v", tt.msg.Subject, got, tt.want)
			}
		})
	}
}

func TestResolveBeadsDir(t *testing.T) {
	// With town root set
	r := NewRouterWithTownRoot("/work/dir", "/home/user/gt")
	got := r.resolveBeadsDir()
	want := "/home/user/gt/.beads"
	if filepath.ToSlash(got) != want {
		t.Errorf("resolveBeadsDir with townRoot = %q, want %q", got, want)
	}

	// Without town root (fallback to workDir)
	r2 := &Router{workDir: "/work/dir", townRoot: ""}
	got2 := r2.resolveBeadsDir()
	want2 := "/work/dir/.beads"
	if filepath.ToSlash(got2) != want2 {
		t.Errorf("resolveBeadsDir without townRoot = %q, want %q", got2, want2)
	}
}

func TestSendFromCrewWorkspace_AvoidsEphemeralPrefixMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a bash bd stub")
	}

	tmpDir := t.TempDir()
	townRoot := filepath.Join(tmpDir, "town")
	senderDir := filepath.Join(townRoot, "barnaby", "crew", "tom")
	recipientDir := filepath.Join(townRoot, "barnaby", "crew", "troy")
	mayorDir := filepath.Join(townRoot, "mayor")
	townBeadsDir := filepath.Join(townRoot, ".beads")

	for _, dir := range []string{senderDir, recipientDir, mayorDir, townBeadsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townBeadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("write beads.db: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	// Write sentinel files so beads.EnsureCustomTypes skips bd config calls.
	typesList := strings.Join(constants.BeadsCustomTypesList(), ",")
	if err := os.WriteFile(filepath.Join(townBeadsDir, ".gt-types-configured"), []byte(typesList+"\n"), 0644); err != nil {
		t.Fatalf("write types sentinel: %v", err)
	}

	// Stub bd to reproduce the old behavior where --id msg-* with --ephemeral
	// would fail prefix validation before ephemeral handling.
	// The fix: sendToSingle no longer passes --id to bd create.
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	bdStub := filepath.Join(binDir, "bd")
	script := `#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "config" || "${1:-}" == "init" ]]; then
  exit 0
fi

if [[ "${1:-}" == "list" ]]; then
  echo "[]"
  exit 0
fi

if [[ "${1:-}" == "mol" && "${2:-}" == "wisp" && "${3:-}" == "list" ]]; then
  echo "[]"
  exit 0
fi

if [[ "${1:-}" == "create" ]]; then
  has_ephemeral=false
  msg_id=""
  i=1
  while [[ $i -le $# ]]; do
    arg="${!i}"
    if [[ "$arg" == "--ephemeral" ]]; then
      has_ephemeral=true
    elif [[ "$arg" == "--id" ]]; then
      ((i++))
      msg_id="${!i:-}"
    elif [[ "$arg" == --id=* ]]; then
      msg_id="${arg#--id=}"
    fi
    ((i++))
  done

  if [[ "$has_ephemeral" == "true" && "$msg_id" == msg-* ]]; then
    echo "prefix mismatch: database uses 'hq-' (allowed: hq,hq-cv) but ID '$msg_id' doesn't match any allowed prefix" >&2
    exit 1
  fi

  echo "hq-testmail-1"
  exit 0
fi

echo "unsupported bd args: $*" >&2
exit 1
`
	if err := os.WriteFile(bdStub, []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := NewRouter(senderDir)
	msg := &Message{
		From:           "barnaby/crew/tom",
		To:             "barnaby/troy",
		Subject:        "Test message",
		Body:           "Hello",
		Wisp:           true,
		SuppressNotify: true,
	}

	if err := r.Send(msg); err != nil {
		t.Fatalf("send from crew workspace should succeed without prefix mismatch: %v", err)
	}
}

func TestNewRouterWithTownRoot(t *testing.T) {
	r := NewRouterWithTownRoot("/work/rig", "/home/gt")
	if filepath.ToSlash(r.workDir) != "/work/rig" {
		t.Errorf("workDir = %q, want '/work/rig'", r.workDir)
	}
	if filepath.ToSlash(r.townRoot) != "/home/gt" {
		t.Errorf("townRoot = %q, want '/home/gt'", r.townRoot)
	}
}

// ============ Mailing List Tests ============

func TestIsListAddress(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{"list:oncall", true},
		{"list:cleanup/gastown", true},
		{"list:", true}, // Edge case: empty list name (will fail on expand)
		{"mayor/", false},
		{"gastown/witness", false},
		{"listoncall", false}, // Missing colon
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got := isListAddress(tt.address)
			if got != tt.want {
				t.Errorf("isListAddress(%q) = %v, want %v", tt.address, got, tt.want)
			}
		})
	}
}

func TestParseListName(t *testing.T) {
	tests := []struct {
		address string
		want    string
	}{
		{"list:oncall", "oncall"},
		{"list:cleanup/gastown", "cleanup/gastown"},
		{"list:", ""},
		{"list:alerts", "alerts"},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got := parseListName(tt.address)
			if got != tt.want {
				t.Errorf("parseListName(%q) = %q, want %q", tt.address, got, tt.want)
			}
		})
	}
}

func TestIsQueueAddress(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{"queue:work", true},
		{"queue:gastown/polecats", true},
		{"queue:", true}, // Edge case: empty queue name (will fail on expand)
		{"mayor/", false},
		{"gastown/witness", false},
		{"queuework", false}, // Missing colon
		{"list:oncall", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got := isQueueAddress(tt.address)
			if got != tt.want {
				t.Errorf("isQueueAddress(%q) = %v, want %v", tt.address, got, tt.want)
			}
		})
	}
}

func TestParseQueueName(t *testing.T) {
	tests := []struct {
		address string
		want    string
	}{
		{"queue:work", "work"},
		{"queue:gastown/polecats", "gastown/polecats"},
		{"queue:", ""},
		{"queue:priority-high", "priority-high"},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got := parseQueueName(tt.address)
			if got != tt.want {
				t.Errorf("parseQueueName(%q) = %q, want %q", tt.address, got, tt.want)
			}
		})
	}
}

func TestExpandList(t *testing.T) {
	// Create temp directory with messaging config
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write messaging.json with test lists
	configContent := `{
  "type": "messaging",
  "version": 1,
  "lists": {
    "oncall": ["mayor/", "gastown/witness"],
    "cleanup/gastown": ["gastown/witness", "deacon/"]
  }
}`
	if err := os.WriteFile(filepath.Join(configDir, "messaging.json"), []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	r := NewRouterWithTownRoot(tmpDir, tmpDir)

	tests := []struct {
		name      string
		listName  string
		want      []string
		wantErr   bool
		errString string
	}{
		{
			name:     "oncall list",
			listName: "oncall",
			want:     []string{"mayor/", "gastown/witness"},
		},
		{
			name:     "cleanup/gastown list",
			listName: "cleanup/gastown",
			want:     []string{"gastown/witness", "deacon/"},
		},
		{
			name:      "unknown list",
			listName:  "nonexistent",
			wantErr:   true,
			errString: "unknown mailing list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.expandList(tt.listName)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expandList(%q) expected error, got nil", tt.listName)
				} else if tt.errString != "" && !contains(err.Error(), tt.errString) {
					t.Errorf("expandList(%q) error = %v, want containing %q", tt.listName, err, tt.errString)
				}
				return
			}
			if err != nil {
				t.Errorf("expandList(%q) unexpected error: %v", tt.listName, err)
				return
			}
			if len(got) != len(tt.want) {
				t.Errorf("expandList(%q) = %v, want %v", tt.listName, got, tt.want)
				return
			}
			for i, addr := range got {
				if addr != tt.want[i] {
					t.Errorf("expandList(%q)[%d] = %q, want %q", tt.listName, i, addr, tt.want[i])
				}
			}
		})
	}
}

func TestExpandListNoTownRoot(t *testing.T) {
	r := &Router{workDir: "/tmp", townRoot: ""}
	_, err := r.expandList("oncall")
	if err == nil {
		t.Error("expandList with no townRoot should error")
	}
	if !contains(err.Error(), "no town root") {
		t.Errorf("expandList error = %v, want containing 'no town root'", err)
	}
}

func TestExpandQueue(t *testing.T) {
	// Create temp directory with messaging config
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write messaging.json with test queues
	configContent := `{
  "type": "messaging",
  "version": 1,
  "queues": {
    "work/gastown": {"workers": ["gastown/polecats/*"], "max_claims": 3},
    "priority-high": {"workers": ["mayor/", "gastown/witness"]}
  }
}`
	if err := os.WriteFile(filepath.Join(configDir, "messaging.json"), []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	r := NewRouterWithTownRoot(tmpDir, tmpDir)

	tests := []struct {
		name        string
		queueName   string
		wantWorkers []string
		wantMax     int
		wantErr     bool
		errString   string
	}{
		{
			name:        "work/gastown queue",
			queueName:   "work/gastown",
			wantWorkers: []string{"gastown/polecats/*"},
			wantMax:     3,
		},
		{
			name:        "priority-high queue",
			queueName:   "priority-high",
			wantWorkers: []string{"mayor/", "gastown/witness"},
			wantMax:     0, // Not specified, defaults to 0
		},
		{
			name:      "unknown queue",
			queueName: "nonexistent",
			wantErr:   true,
			errString: "unknown queue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.expandQueue(tt.queueName)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expandQueue(%q) expected error, got nil", tt.queueName)
				} else if tt.errString != "" && !contains(err.Error(), tt.errString) {
					t.Errorf("expandQueue(%q) error = %v, want containing %q", tt.queueName, err, tt.errString)
				}
				return
			}
			if err != nil {
				t.Errorf("expandQueue(%q) unexpected error: %v", tt.queueName, err)
				return
			}
			if len(got.Workers) != len(tt.wantWorkers) {
				t.Errorf("expandQueue(%q).Workers = %v, want %v", tt.queueName, got.Workers, tt.wantWorkers)
				return
			}
			for i, worker := range got.Workers {
				if worker != tt.wantWorkers[i] {
					t.Errorf("expandQueue(%q).Workers[%d] = %q, want %q", tt.queueName, i, worker, tt.wantWorkers[i])
				}
			}
			if got.MaxClaims != tt.wantMax {
				t.Errorf("expandQueue(%q).MaxClaims = %d, want %d", tt.queueName, got.MaxClaims, tt.wantMax)
			}
		})
	}
}

func TestExpandQueueNoTownRoot(t *testing.T) {
	r := &Router{workDir: "/tmp", townRoot: ""}
	_, err := r.expandQueue("work")
	if err == nil {
		t.Error("expandQueue with no townRoot should error")
	}
	if !contains(err.Error(), "no town root") {
		t.Errorf("expandQueue error = %v, want containing 'no town root'", err)
	}
}

// ============ Announce Address Tests ============

func TestIsAnnounceAddress(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{"announce:bulletin", true},
		{"announce:gastown/updates", true},
		{"announce:", true}, // Edge case: empty announce name (will fail on expand)
		{"mayor/", false},
		{"gastown/witness", false},
		{"announcebulletin", false}, // Missing colon
		{"list:oncall", false},
		{"queue:work", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got := isAnnounceAddress(tt.address)
			if got != tt.want {
				t.Errorf("isAnnounceAddress(%q) = %v, want %v", tt.address, got, tt.want)
			}
		})
	}
}

func TestParseAnnounceName(t *testing.T) {
	tests := []struct {
		address string
		want    string
	}{
		{"announce:bulletin", "bulletin"},
		{"announce:gastown/updates", "gastown/updates"},
		{"announce:", ""},
		{"announce:priority-alerts", "priority-alerts"},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got := parseAnnounceName(tt.address)
			if got != tt.want {
				t.Errorf("parseAnnounceName(%q) = %q, want %q", tt.address, got, tt.want)
			}
		})
	}
}

// contains checks if s contains substr (helper for error checking)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ============ @group Address Tests ============

func TestIsGroupAddress(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{"@rig/gastown", true},
		{"@town", true},
		{"@witnesses", true},
		{"@crew/gastown", true},
		{"@dogs", true},
		{"@overseer", true},
		{"@polecats/gastown", true},
		{"mayor/", false},
		{"gastown/Toast", false},
		{"", false},
		{"rig/gastown", false}, // Missing @
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got := isGroupAddress(tt.address)
			if got != tt.want {
				t.Errorf("isGroupAddress(%q) = %v, want %v", tt.address, got, tt.want)
			}
		})
	}
}

func TestParseGroupAddress(t *testing.T) {
	tests := []struct {
		address      string
		wantType     GroupType
		wantRoleType string
		wantRig      string
		wantNil      bool
	}{
		// Special patterns
		{"@overseer", GroupTypeOverseer, "", "", false},
		{"@town", GroupTypeTown, "", "", false},

		// Role-based patterns (all agents of a role type)
		{"@witnesses", GroupTypeRole, "witness", "", false},
		{"@dogs", GroupTypeRole, "dog", "", false},
		{"@refineries", GroupTypeRole, "refinery", "", false},
		{"@deacons", GroupTypeRole, "deacon", "", false},

		// Rig pattern (all agents in a rig)
		{"@rig/gastown", GroupTypeRig, "", "gastown", false},
		{"@rig/beads", GroupTypeRig, "", "beads", false},

		// Rig+role patterns
		{"@crew/gastown", GroupTypeRigRole, "crew", "gastown", false},
		{"@polecats/gastown", GroupTypeRigRole, "polecat", "gastown", false},

		// Invalid patterns
		{"mayor/", "", "", "", true},
		{"@invalid", "", "", "", true},
		{"@crew/", "", "", "", true}, // Empty rig
		{"@rig", "", "", "", true},   // Missing rig name
		{"", "", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got := parseGroupAddress(tt.address)

			if tt.wantNil {
				if got != nil {
					t.Errorf("parseGroupAddress(%q) = %+v, want nil", tt.address, got)
				}
				return
			}

			if got == nil {
				t.Errorf("parseGroupAddress(%q) = nil, want non-nil", tt.address)
				return
			}

			if got.Type != tt.wantType {
				t.Errorf("parseGroupAddress(%q).Type = %q, want %q", tt.address, got.Type, tt.wantType)
			}
			if got.RoleType != tt.wantRoleType {
				t.Errorf("parseGroupAddress(%q).RoleType = %q, want %q", tt.address, got.RoleType, tt.wantRoleType)
			}
			if got.Rig != tt.wantRig {
				t.Errorf("parseGroupAddress(%q).Rig = %q, want %q", tt.address, got.Rig, tt.wantRig)
			}
			if got.Original != tt.address {
				t.Errorf("parseGroupAddress(%q).Original = %q, want %q", tt.address, got.Original, tt.address)
			}
		})
	}
}

func TestAgentBeadToAddress(t *testing.T) {
	tests := []struct {
		name string
		bead *agentBead
		want string
	}{
		{
			name: "nil bead",
			bead: nil,
			want: "",
		},
		{
			name: "town-level mayor",
			bead: &agentBead{ID: "gt-mayor"},
			want: "mayor/",
		},
		{
			name: "town-level deacon",
			bead: &agentBead{ID: "gt-deacon"},
			want: "deacon/",
		},
		{
			name: "rig singleton witness",
			bead: &agentBead{ID: "gt-gastown-witness"},
			want: "gastown/witness",
		},
		{
			name: "rig singleton refinery",
			bead: &agentBead{ID: "gt-gastown-refinery"},
			want: "gastown/refinery",
		},
		{
			name: "rig crew worker",
			bead: &agentBead{ID: "gt-gastown-crew-max"},
			want: "gastown/max",
		},
		{
			name: "rig polecat worker",
			bead: &agentBead{ID: "gt-gastown-polecat-Toast"},
			want: "gastown/Toast",
		},
		{
			name: "rig polecat with hyphenated name",
			bead: &agentBead{ID: "gt-gastown-polecat-my-agent"},
			want: "gastown/my-agent",
		},
		{
			name: "non-gt prefix with description",
			bead: &agentBead{
				ID:          "bd-beads-crew-beavis",
				Description: "Crew worker beavis in beads.\n\nrole_type: crew\nrig: beads\nagent_state: idle",
			},
			want: "beads/beavis",
		},
		{
			name: "non-gt prefix singleton with description",
			bead: &agentBead{
				ID:          "bd-beads-witness",
				Description: "Witness for beads.\n\nrole_type: witness\nrig: beads\nagent_state: idle",
			},
			want: "beads/witness",
		},
		{
			name: "non-gt prefix no description fallback crew",
			bead: &agentBead{ID: "bd-beads-crew-beavis"},
			want: "beads/beavis",
		},
		{
			name: "non-gt prefix no description fallback witness",
			bead: &agentBead{ID: "bd-beads-witness"},
			want: "beads/witness",
		},
		{
			name: "non-gt prefix no description fallback refinery",
			bead: &agentBead{ID: "db-debt_buying-refinery"},
			want: "debt_buying/refinery",
		},
		{
			name: "non-gt prefix no description fallback polecat",
			bead: &agentBead{ID: "ppf-pyspark_pipeline_framework-polecat-Toast"},
			want: "pyspark_pipeline_framework/Toast",
		},
		{
			name: "malformed singleton witness with name segment",
			bead: &agentBead{ID: "bd-beads-witness-extra"},
			want: "",
		},
		{
			name: "malformed singleton refinery with name segment",
			bead: &agentBead{ID: "bd-beads-refinery-extra"},
			want: "",
		},
		{
			name: "hyphenated agent name via fallback",
			bead: &agentBead{ID: "bd-beads-crew-my-agent"},
			want: "beads/my-agent",
		},
		{
			name: "empty ID",
			bead: &agentBead{ID: ""},
			want: "",
		},
		{
			name: "hq-dog with location in description",
			bead: &agentBead{
				ID:          "hq-dog-alpha",
				Description: "Dog: alpha\n\nrole_type: dog\nrig: town\nlocation: deacon/dogs/alpha",
			},
			want: "deacon/dogs/alpha",
		},
		{
			name: "hq-dog without description returns empty",
			bead: &agentBead{
				ID: "hq-dog-bravo",
			},
			want: "",
		},
		{
			name: "hq-dog with location takes priority over role_type+rig",
			bead: &agentBead{
				ID:          "hq-dog-charlie",
				Description: "Dog: charlie\n\nrole_type: dog\nrig: town\nlocation: deacon/dogs/charlie",
			},
			want: "deacon/dogs/charlie",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agentBeadToAddress(tt.bead)
			if got != tt.want {
				t.Errorf("agentBeadToAddress(%+v) = %q, want %q", tt.bead, got, tt.want)
			}
		})
	}
}

func TestParseAgentAddressFromDescription(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want string
	}{
		{
			name: "location field returns address directly",
			desc: "Dog: alpha\n\nrole_type: dog\nrig: town\nlocation: deacon/dogs/alpha",
			want: "deacon/dogs/alpha",
		},
		{
			name: "location null falls back to role_type+rig",
			desc: "Some agent\n\nrole_type: witness\nrig: myrig\nlocation: null",
			want: "myrig/witness",
		},
		{
			name: "no location uses role_type+rig",
			desc: "Some agent\n\nrole_type: polecat\nrig: gastown",
			want: "gastown/polecat",
		},
		{
			name: "town-level agent no rig",
			desc: "Mayor\n\nrole_type: mayor\nrig: null",
			want: "mayor/",
		},
		{
			name: "empty description",
			desc: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAgentAddressFromDescription(tt.desc)
			if got != tt.want {
				t.Errorf("parseAgentAddressFromDescription(%q) = %q, want %q", tt.desc, got, tt.want)
			}
		})
	}
}

func TestExpandAnnounce(t *testing.T) {
	// Create temp directory with messaging config
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write messaging.json with test announces
	configContent := `{
  "type": "messaging",
  "version": 1,
  "announces": {
    "alerts": {"readers": ["@town"], "retain_count": 10},
    "status/gastown": {"readers": ["gastown/witness", "mayor/"], "retain_count": 5}
  }
}`
	if err := os.WriteFile(filepath.Join(configDir, "messaging.json"), []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	r := NewRouterWithTownRoot(tmpDir, tmpDir)

	tests := []struct {
		name         string
		announceName string
		wantReaders  []string
		wantRetain   int
		wantErr      bool
		errString    string
	}{
		{
			name:         "alerts announce",
			announceName: "alerts",
			wantReaders:  []string{"@town"},
			wantRetain:   10,
		},
		{
			name:         "status/gastown announce",
			announceName: "status/gastown",
			wantReaders:  []string{"gastown/witness", "mayor/"},
			wantRetain:   5,
		},
		{
			name:         "unknown announce",
			announceName: "nonexistent",
			wantErr:      true,
			errString:    "unknown announce channel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.expandAnnounce(tt.announceName)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expandAnnounce(%q) expected error, got nil", tt.announceName)
				} else if tt.errString != "" && !contains(err.Error(), tt.errString) {
					t.Errorf("expandAnnounce(%q) error = %v, want containing %q", tt.announceName, err, tt.errString)
				}
				return
			}
			if err != nil {
				t.Errorf("expandAnnounce(%q) unexpected error: %v", tt.announceName, err)
				return
			}
			if len(got.Readers) != len(tt.wantReaders) {
				t.Errorf("expandAnnounce(%q).Readers = %v, want %v", tt.announceName, got.Readers, tt.wantReaders)
				return
			}
			for i, reader := range got.Readers {
				if reader != tt.wantReaders[i] {
					t.Errorf("expandAnnounce(%q).Readers[%d] = %q, want %q", tt.announceName, i, reader, tt.wantReaders[i])
				}
			}
			if got.RetainCount != tt.wantRetain {
				t.Errorf("expandAnnounce(%q).RetainCount = %d, want %d", tt.announceName, got.RetainCount, tt.wantRetain)
			}
		})
	}
}

func TestExpandAnnounceNoTownRoot(t *testing.T) {
	r := &Router{workDir: "/tmp", townRoot: ""}
	_, err := r.expandAnnounce("alerts")
	if err == nil {
		t.Error("expandAnnounce with no townRoot should error")
	}
	if !contains(err.Error(), "no town root") {
		t.Errorf("expandAnnounce error = %v, want containing 'no town root'", err)
	}
}

// ============ Recipient Validation Tests ============

func TestValidateRecipient(t *testing.T) {
	// Skip if bd CLI is not available or not functional (e.g., missing DLLs on Windows CI)
	if out, err := exec.Command("bd", "version").CombinedOutput(); err != nil {
		t.Skipf("bd CLI not functional, skipping test: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	// Start an ephemeral Dolt container to prevent bd init from creating
	// databases on the production server (port 3307).
	testutil.RequireDoltContainer(t)
	doltPort, _ := strconv.Atoi(testutil.DoltContainerPort())

	// Create isolated beads environment for testing
	tmpDir := t.TempDir()
	townRoot := tmpDir

	// Create .beads directory and initialize
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("creating beads dir: %v", err)
	}

	// Use beads.NewIsolatedWithPort with a unique random prefix to avoid Dolt
	// primary key collisions with production beads (e.g., gt-mayor).
	// NewIsolatedWithPort directs bd init to the ephemeral server via
	// --server-port and GT_DOLT_PORT, and uses --db flag for subsequent
	// commands (bypassing Dolt). We set BEADS_DB so that the Router's
	// external bd calls also use the same isolated SQLite database.
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	prefix := "vr" + hex.EncodeToString(buf[:])
	b := beads.NewIsolatedWithPort(townRoot, doltPort)
	if err := b.Init(prefix); err != nil {
		t.Fatalf("bd init: %v", err)
	}

	// Point BEADS_DB at the isolated SQLite file so the Router's
	// runBdCommand (which inherits process env) uses it too.
	beadsDB := filepath.Join(beadsDir, "beads.db")
	t.Setenv("BEADS_DB", beadsDB)

	// Register custom types required for agent beads.
	if _, err := b.Run("config", "set", "types.custom", "agent,role,rig,convoy,slot,queue,event,message,molecule,gate,merge-request"); err != nil {
		t.Fatalf("config set types.custom: %v", err)
	}

	// Create test agent beads with gt:agent label.
	// Safe to use "gt-" prefixed IDs since both NewIsolated (--db) and the
	// Router (BEADS_DB env) point to the same local SQLite database.
	createAgent := func(id, title string) {
		if _, err := b.Run("create", title, "--labels=gt:agent", "--id="+id, "--force"); err != nil {
			t.Fatalf("creating agent %s: %v", id, err)
		}
	}

	createAgent("gt-mayor", "Mayor agent")
	createAgent("gt-deacon", "Deacon agent")
	createAgent("gt-testrig-witness", "Test witness")
	createAgent("gt-testrig-crew-alice", "Test crew alice")
	createAgent("gt-testrig-polecat-bob", "Test polecat bob")

	// Create dog directory for workspace fallback validation (deacon/dogs/fido).
	// The workspace fallback handles cases where agent beads are missing or
	// the bead DB is unavailable (e.g., after Dolt reset).
	dogDir := filepath.Join(townRoot, "deacon", "dogs", "fido")
	if err := os.MkdirAll(dogDir, 0755); err != nil {
		t.Fatalf("creating dog dir: %v", err)
	}

	r := NewRouterWithTownRoot(townRoot, townRoot)

	tests := []struct {
		name     string
		identity string
		wantErr  bool
		errMsg   string
	}{
		// Overseer is always valid (human operator, no agent bead)
		{"overseer", "overseer", false, ""},

		// Town-level agents (validated against beads)
		{"mayor", "mayor/", false, ""},
		{"deacon", "deacon/", false, ""},

		// Rig-level agents (validated against beads)
		{"witness", "testrig/witness", false, ""},
		{"crew member", "testrig/alice", false, ""},
		{"polecat", "testrig/bob", false, ""},

		// Dog agents (validated via workspace fallback: deacon/dogs/<name> directory)
		{"dog agent", "deacon/dogs/fido", false, ""},

		// Invalid addresses - should fail
		{"bare name", "ruby", true, "no agent found"},
		{"nonexistent rig agent", "testrig/nonexistent", true, "no agent found"},
		{"wrong rig", "wrongrig/alice", true, "no agent found"},
		{"misrouted town agent", "testrig/mayor", true, "no agent found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := r.validateRecipient(tt.identity)
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateRecipient(%q) expected error, got nil", tt.identity)
				} else if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("validateRecipient(%q) error = %v, want containing %q", tt.identity, err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateRecipient(%q) unexpected error: %v", tt.identity, err)
				}
			}
		})
	}
}

func TestValidateAgentWorkspaceDog(t *testing.T) {
	tmpDir := t.TempDir()

	// Create dog directory structure: deacon/dogs/fido
	dogDir := filepath.Join(tmpDir, "deacon", "dogs", "fido")
	if err := os.MkdirAll(dogDir, 0755); err != nil {
		t.Fatalf("creating dog dir: %v", err)
	}

	r := &Router{townRoot: tmpDir}

	tests := []struct {
		name     string
		identity string
		want     bool
	}{
		{"dog exists", "deacon/dogs/fido", true},
		{"dog not exists", "deacon/dogs/ghost", false},
		{"not a dog path", "deacon/cats/fido", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.validateAgentWorkspace(tt.identity)
			if got != tt.want {
				t.Errorf("validateAgentWorkspace(%q) = %v, want %v", tt.identity, got, tt.want)
			}
		})
	}
}

func setupTestRegistryForAddressTest(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

func TestAddressToAgentBeadID(t *testing.T) {
	setupTestRegistryForAddressTest(t)

	tests := []struct {
		name     string
		address  string
		expected string
	}{
		{
			name:     "overseer returns empty",
			address:  "overseer",
			expected: "",
		},
		{
			name:     "mayor",
			address:  "mayor/",
			expected: "hq-mayor",
		},
		{
			name:     "mayor without slash",
			address:  "mayor",
			expected: "hq-mayor",
		},
		{
			name:     "deacon",
			address:  "deacon/",
			expected: "hq-deacon",
		},
		{
			name:     "witness",
			address:  "gastown/witness",
			expected: "gt-witness",
		},
		{
			name:     "refinery",
			address:  "gastown/refinery",
			expected: "gt-refinery",
		},
		{
			name:     "crew member",
			address:  "gastown/crew/max",
			expected: "gt-crew-max",
		},
		{
			name:     "polecat (default)",
			address:  "gastown/alpha",
			expected: "gt-alpha",
		},
		{
			name:     "explicit polecat with polecats/ prefix",
			address:  "gastown/polecats/alpha",
			expected: "gt-alpha",
		},
		{
			name:     "empty address",
			address:  "",
			expected: "",
		},
		{
			name:     "no slash non-special",
			address:  "unknown",
			expected: "",
		},
		{
			name:     "rig with empty target",
			address:  "gastown/",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addressToAgentBeadID(tt.address)
			if got != tt.expected {
				t.Errorf("addressToAgentBeadID(%q) = %q, want %q", tt.address, got, tt.expected)
			}
		})
	}
}

// ============ Crew Shorthand Resolution Tests ============

func TestResolveCrewShorthand(t *testing.T) {
	// Create a realistic town directory structure
	tmpDir := t.TempDir()

	// Create pata rig with crew members
	for _, name := range []string{"alice", "bob"} {
		dir := filepath.Join(tmpDir, "pata", "crew", name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Create pata polecat
	if err := os.MkdirAll(filepath.Join(tmpDir, "pata", "polecats", "rust"), 0755); err != nil {
		t.Fatal(err)
	}
	// Create another rig with crew
	if err := os.MkdirAll(filepath.Join(tmpDir, "beads", "crew", "alice"), 0755); err != nil {
		t.Fatal(err)
	}

	r := NewRouterWithTownRoot(tmpDir, tmpDir)

	tests := []struct {
		name     string
		identity string
		want     string
	}{
		// crew/name shorthand: unambiguous single rig match
		{
			name:     "crew/bob unambiguous",
			identity: "crew/bob",
			want:     "pata/bob", // bob only in pata
		},
		// crew/name shorthand: ambiguous (in multiple rigs) - leave unchanged
		{
			name:     "crew/alice ambiguous",
			identity: "crew/alice",
			want:     "crew/alice", // alice in both pata and beads
		},
		// Already fully-qualified rig/name - leave unchanged
		{
			name:     "pata/bob already canonical",
			identity: "pata/bob",
			want:     "pata/bob",
		},
		// polecats shorthand
		{
			name:     "polecats/rust shorthand",
			identity: "polecats/rust",
			want:     "pata/rust", // rust only in pata polecats
		},
		// Non-crew address - leave unchanged
		{
			name:     "mayor/ unchanged",
			identity: "mayor/",
			want:     "mayor/",
		},
		// No town root - no resolution attempted
		{
			name:     "no town root",
			identity: "crew/bob",
			want:     "crew/bob", // tested with empty townRoot below
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := r
			if tt.name == "no town root" {
				router = NewRouterWithTownRoot(tmpDir, "") // empty townRoot
			}
			got := router.resolveCrewShorthand(tt.identity)
			if got != tt.want {
				t.Errorf("resolveCrewShorthand(%q) = %q, want %q", tt.identity, got, tt.want)
			}
		})
	}
}

func TestValidateRecipientFilesystemFallback(t *testing.T) {
	// Create a realistic town directory structure without any agent beads
	tmpDir := t.TempDir()

	// Create pata rig structure
	for _, subpath := range []string{
		"pata/crew/bob",
		"pata/crew/alice",
		"pata/polecats/rust",
		"pata/witness",
		"pata/refinery",
	} {
		if err := os.MkdirAll(filepath.Join(tmpDir, subpath), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Create mayor/town.json marker
	if err := os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	r := NewRouterWithTownRoot(tmpDir, tmpDir)

	tests := []struct {
		name     string
		identity string
		wantErr  bool
	}{
		// Crew members found via filesystem (no agent beads needed)
		{"crew bob", "pata/bob", false},
		{"crew alice", "pata/alice", false},
		{"explicit crew bob", "pata/crew/bob", false},
		// Polecat found via filesystem
		{"polecat rust", "pata/rust", false},
		{"explicit polecat rust", "pata/polecats/rust", false},
		// Singleton roles found via filesystem
		{"witness", "pata/witness", false},
		{"refinery", "pata/refinery", false},
		// Overseer always valid
		{"overseer", "overseer", false},
		// Non-existent agent should fail
		{"nonexistent", "pata/nobody", true},
		{"wrong rig", "notarig/bob", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := r.validateRecipient(tt.identity)
			if tt.wantErr && err == nil {
				t.Errorf("validateRecipient(%q) expected error, got nil", tt.identity)
			} else if !tt.wantErr && err != nil {
				t.Errorf("validateRecipient(%q) unexpected error: %v", tt.identity, err)
			}
		})
	}
}

func TestValidateRecipientFilesystemFallbackWithRouteErrors(t *testing.T) {
	tmpDir := t.TempDir()

	for _, subpath := range []string{
		".beads",
		"mayor",
		"sfn1_fast/crew/arch",
	} {
		if err := os.MkdirAll(filepath.Join(tmpDir, subpath), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	routes := []byte("{\"prefix\":\"sf-\",\"path\":\"missing/mayor/rig\"}\n")
	if err := os.WriteFile(filepath.Join(tmpDir, ".beads", "routes.jsonl"), routes, 0644); err != nil {
		t.Fatal(err)
	}

	r := NewRouterWithTownRoot(tmpDir, tmpDir)

	for _, identity := range []string{"sfn1_fast/arch", "sfn1_fast/crew/arch"} {
		t.Run(identity, func(t *testing.T) {
			if err := r.validateRecipient(identity); err != nil {
				t.Fatalf("validateRecipient(%q) unexpected error: %v", identity, err)
			}
		})
	}
}

// requireNotifyTestSocket returns a per-test tmux socket and skips if tmux
// is unavailable. The socket server is killed on test cleanup.
func requireNotifyTestSocket(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	// Use test name for unique socket per test to prevent cleanup interference.
	// Sanitize: tmux socket names cannot contain slashes or dots.
	safe := strings.NewReplacer("/", "-", ".", "-").Replace(t.Name())
	socket := fmt.Sprintf("gt-test-%s-%d", safe, os.Getpid())
	// Pre-kill any stale server on this socket (e.g., from a crashed prior run).
	_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})
	return socket
}

// createNotifyTestSession creates a tmux session on the given socket and waits
// for it to be ready.
func createNotifyTestSession(t *testing.T, socket, sessionName, command string) {
	t.Helper()
	args := []string{"-L", socket, "new-session", "-d", "-s", sessionName, command}
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("failed to create test session %q: %v\n%s", sessionName, err, out)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("tmux", "-L", socket, "has-session", "-t", sessionName).Run() == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %q never appeared on socket %q", sessionName, socket)
}

// TestNotifyRecipient_IdleAgent verifies that an idle agent (prompt visible)
// receives a direct nudge instead of a queued one.
func TestNotifyRecipient_IdleAgent(t *testing.T) {
	socket := requireNotifyTestSocket(t)
	sessionName := "gt-crew-idletest"

	// Create a session that displays the Claude Code prompt prefix, simulating idle.
	// "printf" prints the prompt, then "cat" blocks keeping the session alive.
	createNotifyTestSession(t, socket, sessionName, `sh -c 'printf "❯ \n" && cat'`)

	// Wait briefly for printf output to appear in the pane.
	time.Sleep(500 * time.Millisecond)

	townRoot := t.TempDir()
	r := &Router{
		workDir:           t.TempDir(),
		townRoot:          townRoot,
		tmux:              tmux.NewTmuxWithSocket(socket),
		IdleNotifyTimeout: 3 * time.Second,
	}

	msg := &Message{
		From:    "gastown/crew/sender",
		To:      "gastown/crew/idletest",
		Subject: "test idle delivery",
	}

	err := r.notifyRecipient(msg)
	if err != nil {
		t.Fatalf("notifyRecipient returned error: %v", err)
	}

	// The main notification was delivered directly (no immediate queue).
	// But the reply-reminder is deferred — it should be in the queue with a
	// future DeliverAfter, waiting for the configured delay to elapse.
	pending, _ := nudge.Pending(townRoot, sessionName)
	if pending != 1 {
		t.Errorf("expected 1 queued nudge (deferred reply-reminder) for idle agent, got %d", pending)
	}

	// Confirm the queued nudge is deferred, not a missed immediate notification.
	nudges, err := nudge.Drain(townRoot, sessionName)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 0 {
		t.Errorf("expected 0 immediately-deliverable nudges (reminder should be deferred), got %d", len(nudges))
	}
}

// TestNotifyRecipient_BusyAgent verifies that a busy agent (no prompt visible)
// gets a queued nudge instead of an immediate one.
func TestNotifyRecipient_BusyAgent(t *testing.T) {
	socket := requireNotifyTestSocket(t)
	sessionName := "gt-crew-busytest"

	// Create a session running sleep — no prompt visible, simulating busy agent.
	createNotifyTestSession(t, socket, sessionName, "sleep 300")

	townRoot := t.TempDir()
	r := &Router{
		workDir:           t.TempDir(),
		townRoot:          townRoot,
		tmux:              tmux.NewTmuxWithSocket(socket),
		IdleNotifyTimeout: 1 * time.Second, // short timeout for test speed
	}

	msg := &Message{
		From:    "gastown/crew/sender",
		To:      "gastown/crew/busytest",
		Subject: "test busy delivery",
	}

	err := r.notifyRecipient(msg)
	if err != nil {
		t.Fatalf("notifyRecipient returned error: %v", err)
	}

	// Two nudges should be queued:
	//   1. The immediate "you have mail" notification (deliverable now).
	//   2. The deferred reply-reminder (not ready until configured delay elapses).
	pending, _ := nudge.Pending(townRoot, sessionName)
	if pending != 2 {
		t.Errorf("expected 2 queued nudges (notification + reply-reminder) for busy agent, got %d", pending)
	}

	// Exactly 1 should be immediately deliverable (the main notification).
	nudges, err := nudge.Drain(townRoot, sessionName)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 1 {
		t.Errorf("expected 1 immediately-deliverable nudge, got %d", len(nudges))
	}
	if nudges[0].Priority != nudge.PriorityNormal {
		t.Errorf("queued mail notification priority = %q, want %q", nudges[0].Priority, nudge.PriorityNormal)
	}

	// The reply-reminder should still be in queue (deferred).
	remaining, _ := nudge.Pending(townRoot, sessionName)
	if remaining != 1 {
		t.Errorf("expected 1 deferred reply-reminder still in queue, got %d", remaining)
	}
}

func TestNotifyRecipient_CanonicalAliasFansOutToBusyCandidates(t *testing.T) {
	socket := requireNotifyTestSocket(t)
	crewSession := "gt-crew-aliasfanout"
	polecatSession := "gt-aliasfanout"
	createNotifyTestSession(t, socket, crewSession, "sleep 300")
	createNotifyTestSession(t, socket, polecatSession, "sleep 300")

	townRoot := t.TempDir()
	r := &Router{
		workDir:           t.TempDir(),
		townRoot:          townRoot,
		tmux:              tmux.NewTmuxWithSocket(socket),
		IdleNotifyTimeout: 10 * time.Millisecond,
	}

	msg := &Message{
		From:     "gastown/witness",
		To:       "gastown/aliasfanout",
		Subject:  "fanout delivery",
		ThreadID: "thread-fanout",
	}

	if err := r.notifyRecipient(msg); err != nil {
		t.Fatalf("notifyRecipient returned error: %v", err)
	}

	for _, sessionID := range []string{crewSession, polecatSession} {
		pending, err := nudge.Pending(townRoot, sessionID)
		if err != nil {
			t.Fatalf("Pending(%s): %v", sessionID, err)
		}
		if pending != 2 {
			t.Fatalf("Pending(%s) = %d, want 2 queued nudges (mail + reminder)", sessionID, pending)
		}

		nudges, err := nudge.Drain(townRoot, sessionID)
		if err != nil {
			t.Fatalf("Drain(%s): %v", sessionID, err)
		}
		if len(nudges) != 1 {
			t.Fatalf("Drain(%s) returned %d immediately deliverable nudges, want 1", sessionID, len(nudges))
		}
		if nudges[0].Kind != "mail" {
			t.Fatalf("Drain(%s)[0].Kind = %q, want mail", sessionID, nudges[0].Kind)
		}
	}
}

func TestNotifyRecipient_CanonicalAliasQueuesAllHeadlessCandidates(t *testing.T) {
	townRoot := t.TempDir()
	r := &Router{
		workDir:  t.TempDir(),
		townRoot: townRoot,
		tmux:     tmux.NewTmuxWithSocket("gt-test-missing-socket"),
	}

	msg := &Message{
		From:     "mayor/",
		To:       "gastown/headless",
		Subject:  "headless fanout",
		ThreadID: "thread-headless",
	}

	if err := r.notifyRecipient(msg); err != nil {
		t.Fatalf("notifyRecipient returned error: %v", err)
	}

	for _, sessionID := range []string{"gt-crew-headless", "gt-headless"} {
		nudges, err := nudge.Drain(townRoot, sessionID)
		if err != nil {
			t.Fatalf("Drain(%s): %v", sessionID, err)
		}
		if len(nudges) != 1 {
			t.Fatalf("Drain(%s) returned %d nudges, want 1", sessionID, len(nudges))
		}
		if nudges[0].ThreadID != msg.ThreadID {
			t.Fatalf("Drain(%s)[0].ThreadID = %q, want %q", sessionID, nudges[0].ThreadID, msg.ThreadID)
		}
	}
}

func TestNotifyRecipient_BusyAgentEscalationUsesUrgentQueuedNudge(t *testing.T) {
	socket := requireNotifyTestSocket(t)
	sessionName := "gt-crew-busy-escalation"
	createNotifyTestSession(t, socket, sessionName, "sleep 300")

	townRoot := t.TempDir()
	r := &Router{
		workDir:           t.TempDir(),
		townRoot:          townRoot,
		tmux:              tmux.NewTmuxWithSocket(socket),
		IdleNotifyTimeout: 1 * time.Second,
	}

	msg := &Message{
		From:     "gastown/witness",
		To:       "gastown/crew/busy-escalation",
		Subject:  "[CRITICAL] Database identity mismatch",
		Type:     TypeEscalation,
		Priority: PriorityUrgent,
		ThreadID: "hq-esc123",
	}

	if err := r.notifyRecipient(msg); err != nil {
		t.Fatalf("notifyRecipient returned error: %v", err)
	}

	nudges, err := nudge.Drain(townRoot, sessionName)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 1 {
		t.Fatalf("expected 1 immediately-deliverable escalation nudge, got %d", len(nudges))
	}
	if nudges[0].Priority != nudge.PriorityUrgent {
		t.Fatalf("queued escalation priority = %q, want %q", nudges[0].Priority, nudge.PriorityUrgent)
	}
	for _, want := range []string{"Escalation mail from gastown/witness", "ID: hq-esc123", "Severity: critical", "gt mail read hq-esc123", "gt escalate ack hq-esc123"} {
		if !strings.Contains(nudges[0].Message, want) {
			t.Fatalf("queued escalation message missing %q: %s", want, nudges[0].Message)
		}
	}

	remaining, _ := nudge.Pending(townRoot, sessionName)
	if remaining != 1 {
		t.Fatalf("expected 1 deferred reply-reminder after draining escalation nudge, got %d", remaining)
	}
}

func TestFormatNotificationMessageForEscalation(t *testing.T) {
	msg := &Message{
		From:     "gastown/witness",
		Subject:  "[HIGH] Polecat stuck",
		Type:     TypeEscalation,
		Priority: PriorityHigh,
		ThreadID: "hq-esc456",
	}

	notification := formatNotificationMessage(msg)
	for _, want := range []string{"Escalation mail from gastown/witness", "ID: hq-esc456", "Severity: high", "gt mail read hq-esc456", "gt escalate ack hq-esc456"} {
		if !strings.Contains(notification, want) {
			t.Fatalf("escalation notification missing %q: %s", want, notification)
		}
	}
}

func TestRouterSendEscalationAddsStructuredLabels(t *testing.T) {
	r := &Router{}
	msg := &Message{From: "deacon/", Type: TypeEscalation, ThreadID: "hq-abc123"}
	labels := r.buildLabels(msg)
	for _, want := range []string{"gt:message", "gt:escalation", "msg-type:escalation", "from:deacon/", "thread:hq-abc123"} {
		if !containsLabel(labels, want) {
			t.Fatalf("labels %v missing %q", labels, want)
		}
	}
}

func containsLabel(labels []string, want string) bool {
	for _, label := range labels {
		if label == want {
			return true
		}
	}
	return false
}

// --- enqueueReplyReminder tests ---

// TestEnqueueReplyReminder_Basic verifies that a deferred reply-reminder nudge is
// enqueued with the correct sender, message content, and DeliverAfter timestamp.
func TestEnqueueReplyReminder_Basic(t *testing.T) {
	townRoot := t.TempDir()
	r := &Router{
		workDir:  t.TempDir(),
		townRoot: townRoot,
	}
	msg := &Message{
		From:    "gastown/witness",
		To:      "gastown/crew/alice",
		Subject: "status check",
		Type:    TypeNotification,
	}
	sessionID := "gt-gastown-crew-alice"

	before := time.Now()
	r.enqueueReplyReminder(msg, sessionID)
	after := time.Now()

	// Exactly one nudge should be queued.
	pending, err := nudge.Pending(townRoot, sessionID)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if pending != 1 {
		t.Fatalf("expected 1 queued reminder, got %d", pending)
	}

	// Nudge should not be immediately deliverable (DeliverAfter in future).
	nudges, err := nudge.Drain(townRoot, sessionID)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 0 {
		t.Errorf("reminder should be deferred, but Drain returned %d nudges", len(nudges))
	}

	// File still in queue — confirm DeliverAfter is ~30s ahead.
	dir := filepath.Join(townRoot, ".runtime", "nudge_queue", sessionID)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in queue dir, got %d", len(entries))
	}

	// Read the raw JSON to inspect DeliverAfter.
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var q nudge.QueuedNudge
	if err := json.Unmarshal(data, &q); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if q.DeliverAfter.IsZero() {
		t.Error("DeliverAfter should be set")
	}
	minDelay := before.Add(29 * time.Second)
	maxDelay := after.Add(31 * time.Second)
	if q.DeliverAfter.Before(minDelay) || q.DeliverAfter.After(maxDelay) {
		t.Errorf("DeliverAfter = %v, want ~30s from [%v, %v]", q.DeliverAfter, before, after)
	}
	if !strings.Contains(q.Message, msg.From) {
		t.Errorf("reminder message should mention sender %q, got %q", msg.From, q.Message)
	}
	if !strings.Contains(q.Message, "gt mail send") {
		t.Errorf("reminder message should mention 'gt mail send', got %q", q.Message)
	}
	if q.Kind != "reply-reminder" {
		t.Errorf("Kind = %q, want %q", q.Kind, "reply-reminder")
	}
	if q.ThreadID != msg.ThreadID {
		t.Errorf("ThreadID = %q, want %q", q.ThreadID, msg.ThreadID)
	}
}

func TestClearReplyReminders(t *testing.T) {
	townRoot := t.TempDir()
	r := &Router{workDir: t.TempDir(), townRoot: townRoot}
	sessionID := session.CrewSessionName(session.PrefixFor("gastown"), "bob")

	for _, n := range []nudge.QueuedNudge{
		{Sender: "system", Message: "reply-1", Kind: "reply-reminder", ThreadID: "thread-1"},
		{Sender: "system", Message: "reply-2", Kind: "reply-reminder", ThreadID: "thread-1"},
		{Sender: "system", Message: "keep-mail", Kind: "mail", ThreadID: "thread-1"},
		{Sender: "system", Message: "keep-other-thread", Kind: "reply-reminder", ThreadID: "thread-2"},
	} {
		if err := nudge.Enqueue(townRoot, sessionID, n); err != nil {
			t.Fatalf("Enqueue(%q): %v", n.Message, err)
		}
	}

	if err := r.ClearReplyReminders("gastown/crew/bob", "thread-1"); err != nil {
		t.Fatalf("ClearReplyReminders: %v", err)
	}

	nudges, err := nudge.Drain(townRoot, sessionID)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 2 {
		t.Fatalf("Drain returned %d nudges, want 2", len(nudges))
	}
	if nudges[0].Message != "keep-mail" {
		t.Fatalf("nudges[0].Message = %q, want %q", nudges[0].Message, "keep-mail")
	}
	if nudges[1].Message != "keep-other-thread" {
		t.Fatalf("nudges[1].Message = %q, want %q", nudges[1].Message, "keep-other-thread")
	}
}

// TestEnqueueReplyReminder_SkipsReply verifies that reply-type messages do not
// trigger a reply reminder (would be redundant noise).
func TestEnqueueReplyReminder_SkipsReply(t *testing.T) {
	townRoot := t.TempDir()
	r := &Router{workDir: t.TempDir(), townRoot: townRoot}
	msg := &Message{
		From:    "gastown/witness",
		To:      "gastown/crew/alice",
		Subject: "re: status",
		Type:    TypeReply,
	}
	r.enqueueReplyReminder(msg, "gt-gastown-crew-alice")

	pending, _ := nudge.Pending(townRoot, "gt-gastown-crew-alice")
	if pending != 0 {
		t.Errorf("TypeReply should not enqueue a reminder, got %d", pending)
	}
}

// TestEnqueueReplyReminder_NoTownRoot verifies that the function is a no-op
// when no town root is set (nudge queue requires a town root).
func TestEnqueueReplyReminder_NoTownRoot(t *testing.T) {
	r := &Router{workDir: t.TempDir(), townRoot: ""}
	msg := &Message{From: "mayor/", To: "gastown/crew/bob", Subject: "task"}
	// Should not panic or error — just silently skip.
	r.enqueueReplyReminder(msg, "gt-gastown-crew-bob")
}

// TestEnqueueReplyReminder_DisabledByConfig verifies that setting
// reply_reminder_delay = "0s" suppresses all reply reminders.
func TestEnqueueReplyReminder_DisabledByConfig(t *testing.T) {
	townRoot := t.TempDir()

	// Write a settings/config.json with reply_reminder_delay disabled.
	// LoadOperationalConfig reads from {townRoot}/settings/config.json and
	// expects the operational block nested under the "operational" key.
	settingsDir := filepath.Join(townRoot, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{"operational":{"mail":{"reply_reminder_delay":"0s"}}}`
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	r := &Router{workDir: t.TempDir(), townRoot: townRoot}
	msg := &Message{
		From:    "mayor/",
		To:      "gastown/crew/bob",
		Subject: "task",
		Type:    TypeTask,
	}
	r.enqueueReplyReminder(msg, "gt-gastown-crew-bob")

	pending, _ := nudge.Pending(townRoot, "gt-gastown-crew-bob")
	if pending != 0 {
		t.Errorf("reply_reminder_delay=0s should disable reminders, got %d pending", pending)
	}
}
