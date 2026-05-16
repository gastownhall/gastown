package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReadFanoutTitles(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "simple titles",
			input: "Fix auth bug\nAdd rate limit\nAudit storage\n",
			want:  []string{"Fix auth bug", "Add rate limit", "Audit storage"},
		},
		{
			name:  "skip blank lines",
			input: "Title A\n\nTitle B\n\n",
			want:  []string{"Title A", "Title B"},
		},
		{
			name:  "skip comment lines",
			input: "# This is a comment\nTitle A\n# Another comment\nTitle B\n",
			want:  []string{"Title A", "Title B"},
		},
		{
			name:  "trim whitespace",
			input: "  Title A  \n  Title B  \n",
			want:  []string{"Title A", "Title B"},
		},
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
		{
			name:  "only comments and blanks",
			input: "# comment\n\n# another\n",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Redirect stdin for the test.
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			origStdin := os.Stdin
			os.Stdin = r
			t.Cleanup(func() { os.Stdin = origStdin })

			_, _ = w.WriteString(tt.input)
			w.Close()

			got, err := readFanoutTitles()
			if err != nil {
				t.Fatalf("readFanoutTitles() error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("readFanoutTitles() = %v (len %d), want %v (len %d)", got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("readFanoutTitles()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestLoadFanoutState(t *testing.T) {
	t.Run("nonexistent file returns empty map", func(t *testing.T) {
		done, err := loadFanoutState(filepath.Join(t.TempDir(), "no-such-file.jsonl"))
		if err != nil {
			t.Fatalf("loadFanoutState() error: %v", err)
		}
		if len(done) != 0 {
			t.Errorf("expected empty map, got %v", done)
		}
	})

	t.Run("reads existing entries", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "state.jsonl")

		entries := []fanoutStateEntry{
			{Title: "Fix auth bug", BeadID: "gt-aaa", CreatedAt: time.Now().UTC().Format(time.RFC3339)},
			{Title: "Add rate limit", BeadID: "gt-bbb", CreatedAt: time.Now().UTC().Format(time.RFC3339)},
		}
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, _ := json.Marshal(e)
			_, _ = f.WriteString(string(b) + "\n")
		}
		f.Close()

		done, err := loadFanoutState(path)
		if err != nil {
			t.Fatalf("loadFanoutState() error: %v", err)
		}
		if len(done) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(done))
		}
		if done["Fix auth bug"] != "gt-aaa" {
			t.Errorf("expected gt-aaa, got %q", done["Fix auth bug"])
		}
		if done["Add rate limit"] != "gt-bbb" {
			t.Errorf("expected gt-bbb, got %q", done["Add rate limit"])
		}
	})

	t.Run("skips malformed lines", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "state.jsonl")

		content := `{"title":"Good","bead_id":"gt-aaa","created_at":"2026-01-01T00:00:00Z"}
not-valid-json
{"title":"Also Good","bead_id":"gt-bbb","created_at":"2026-01-01T00:00:00Z"}
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		done, err := loadFanoutState(path)
		if err != nil {
			t.Fatalf("loadFanoutState() error: %v", err)
		}
		if len(done) != 2 {
			t.Fatalf("expected 2 valid entries, got %d", len(done))
		}
	})
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"gastown", "gastown"},
		{"gt-epic123", "gt-epic123"},
		{"some/rig:name", "some-rig-name"},
		{"spaces here", "spaces-here"},
		{"gt.abc", "gt-abc"},
	}
	for _, tt := range tests {
		got := sanitizeFilename(tt.in)
		if got != tt.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRunFanout_DryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	townRoot := t.TempDir()

	// Minimal workspace structure.
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	// Rig directory.
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", ".beads"), 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	// Change cwd to town root so workspace.FindFromCwdOrError() works.
	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Redirect stdin.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })
	_, _ = w.WriteString("Fix auth bug\nAdd rate limit\n")
	w.Close()

	// Set flags.
	fanoutRig = "gastown"
	fanoutParent = ""
	fanoutType = "task"
	fanoutPriority = "2"
	fanoutLabels = nil
	fanoutRate = 0
	fanoutStateFile = filepath.Join(t.TempDir(), "state.jsonl")
	fanoutDryRun = true
	t.Cleanup(func() { fanoutDryRun = false })

	if err := runFanout(fanoutCmd, nil); err != nil {
		t.Errorf("runFanout(dry-run) error: %v", err)
	}
}

func TestRunFanout_SkipsAlreadyCreated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	townRoot := t.TempDir()
	binDir := filepath.Join(townRoot, "bin")

	// Workspace structure.
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Count bd create calls.
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
echo "CMD:$*" >> "` + logPath + `"
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then shift || true; cmd="$1"; fi
shift || true
case "$cmd" in
  create) echo "gt-new001" ;;
  dep) exit 0 ;;
esac
exit 0
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	// Pre-populate state file: "Fix auth bug" already created.
	stateFile := filepath.Join(t.TempDir(), "state.jsonl")
	existing := fanoutStateEntry{Title: "Fix auth bug", BeadID: "gt-old001", CreatedAt: "2026-01-01T00:00:00Z"}
	b, _ := json.Marshal(existing)
	if err := os.WriteFile(stateFile, append(b, '\n'), 0644); err != nil {
		t.Fatal(err)
	}

	// stdin: two titles, one already in state.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })
	_, _ = w.WriteString("Fix auth bug\nAdd rate limit\n")
	w.Close()

	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatal(err)
	}

	fanoutRig = "gastown"
	fanoutParent = ""
	fanoutType = "task"
	fanoutPriority = "2"
	fanoutLabels = nil
	fanoutRate = 0
	fanoutStateFile = stateFile
	fanoutDryRun = false

	if err := runFanout(fanoutCmd, nil); err != nil {
		t.Errorf("runFanout() error: %v", err)
	}

	// Only one bd create call expected (the already-created title was skipped).
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading bd.log: %v", err)
	}
	log := string(logData)
	createCalls := strings.Count(log, "create")
	if createCalls != 1 {
		t.Errorf("expected 1 bd create call (skipped 1), got %d\nlog:\n%s", createCalls, log)
	}
}

func TestRunFanout_LinksToParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	townRoot := t.TempDir()
	binDir := filepath.Join(townRoot, "bin")

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
echo "CMD:$*" >> "` + logPath + `"
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then shift || true; cmd="$1"; fi
shift || true
case "$cmd" in
  create) echo "gt-child001" ;;
  dep) exit 0 ;;
esac
exit 0
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })
	_, _ = w.WriteString("Task one\n")
	w.Close()

	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatal(err)
	}

	fanoutRig = "gastown"
	fanoutParent = "gt-epic001"
	fanoutType = "task"
	fanoutPriority = "2"
	fanoutLabels = nil
	fanoutRate = 0
	fanoutStateFile = filepath.Join(t.TempDir(), "state.jsonl")
	fanoutDryRun = false
	t.Cleanup(func() { fanoutParent = "" })

	if err := runFanout(fanoutCmd, nil); err != nil {
		t.Errorf("runFanout() error: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading bd.log: %v", err)
	}
	log := string(logData)

	if !strings.Contains(log, "dep") {
		t.Errorf("expected dep add call in log, got:\n%s", log)
	}
	if !strings.Contains(log, "gt-epic001") {
		t.Errorf("expected parent ID gt-epic001 in dep call, got:\n%s", log)
	}
	if !strings.Contains(log, "parent-child") {
		t.Errorf("expected --type=parent-child in dep call, got:\n%s", log)
	}
}

func TestRunFanout_PartialFailureReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	townRoot := t.TempDir()
	binDir := filepath.Join(townRoot, "bin")

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Stub: fail on "Bad title", succeed otherwise.
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
echo "CMD:$*" >> "` + logPath + `"
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then shift || true; cmd="$1"; fi
shift || true
case "$cmd" in
  create)
    # fail if title contains "Bad"
    if echo "$*" | grep -q "Bad"; then
      echo "error: rejected" >&2
      exit 1
    fi
    echo "gt-good001"
    ;;
  dep) exit 0 ;;
esac
exit 0
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })
	_, _ = w.WriteString("Good title\nBad title\n")
	w.Close()

	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatal(err)
	}

	fanoutRig = "gastown"
	fanoutParent = ""
	fanoutType = "task"
	fanoutPriority = "2"
	fanoutLabels = nil
	fanoutRate = 0
	fanoutStateFile = filepath.Join(t.TempDir(), "state.jsonl")
	fanoutDryRun = false

	err = runFanout(fanoutCmd, nil)
	if err == nil {
		t.Error("runFanout() expected error for partial failures, got nil")
	}
	if !strings.Contains(err.Error(), "1 bead(s) failed") {
		t.Errorf("unexpected error: %v", err)
	}

	// Verify good bead was persisted to state file.
	done, loadErr := loadFanoutState(fanoutStateFile)
	if loadErr != nil {
		t.Fatalf("loadFanoutState() error: %v", loadErr)
	}
	if done["Good title"] != "gt-good001" {
		t.Errorf("state file: expected gt-good001 for 'Good title', got %q", done["Good title"])
	}
	if _, ok := done["Bad title"]; ok {
		t.Error("state file: 'Bad title' should not be persisted after failure")
	}
}
