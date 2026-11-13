package channelevents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmitToTown(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	path, err := EmitToTown(townRoot, "refinery", "MERGE_READY", []string{
		"source=witness",
		"rig=dashboard",
	})
	if err != nil {
		t.Fatalf("EmitToTown failed: %v", err)
	}

	if !strings.HasSuffix(path, ".event") {
		t.Errorf("expected .event suffix, got %q", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading event file: %v", err)
	}

	var event map[string]interface{}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("unmarshaling event: %v", err)
	}

	if event["type"] != "MERGE_READY" {
		t.Errorf("type = %v, want MERGE_READY", event["type"])
	}
	if event["channel"] != "refinery" {
		t.Errorf("channel = %v, want refinery", event["channel"])
	}

	payload, ok := event["payload"].(map[string]interface{})
	if !ok {
		t.Fatal("payload is not a map")
	}
	if payload["source"] != "witness" {
		t.Errorf("payload.source = %v, want witness", payload["source"])
	}
	if payload["rig"] != "dashboard" {
		t.Errorf("payload.rig = %v, want dashboard", payload["rig"])
	}
}

func TestEmitToTown_InvalidChannel(t *testing.T) {
	t.Parallel()
	_, err := EmitToTown(t.TempDir(), "../escape", "TEST", nil)
	if err == nil {
		t.Error("expected error for invalid channel name")
	}
}

func TestEmitToTown_UniqueFilenames(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	seen := make(map[string]bool)

	for i := 0; i < 10; i++ {
		path, err := EmitToTown(townRoot, "test", "EVENT", nil)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if seen[path] {
			t.Errorf("duplicate filename: %s", path)
		}
		seen[path] = true
	}
}

func TestValidChannelName(t *testing.T) {
	t.Parallel()
	valid := []string{
		"refinery", "witness", "my-channel", "test_chan", "abc123",
		"gastown/refinery", "llmprobe/refinery", "rig-a/channel-b",
	}
	for _, name := range valid {
		if !ValidChannelName.MatchString(name) {
			t.Errorf("%q should be valid", name)
		}
	}

	invalid := []string{
		"../escape", "has space", "has.dot", "", "a/../b",
		"/leading", "trailing/", "double//slash", "a//b", "a/",
	}
	for _, name := range invalid {
		if ValidChannelName.MatchString(name) {
			t.Errorf("%q should be invalid", name)
		}
	}
}

func TestEmitToTown_CreatesDirectory(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	channelDir := filepath.Join(townRoot, "events", "newchannel")

	if _, err := os.Stat(channelDir); !os.IsNotExist(err) {
		t.Fatal("channel dir should not exist yet")
	}

	_, err := EmitToTown(townRoot, "newchannel", "TEST", nil)
	if err != nil {
		t.Fatalf("EmitToTown failed: %v", err)
	}

	if _, err := os.Stat(channelDir); err != nil {
		t.Errorf("channel dir should exist after emit: %v", err)
	}
}

func TestScoped(t *testing.T) {
	t.Parallel()
	cases := []struct {
		rig     string
		channel string
		want    string
	}{
		{"gastown", "refinery", "gastown/refinery"},
		{"", "refinery", "refinery"},
		{"UNSET_RIG", "refinery", "refinery"},
		{"gastown", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := Scoped(c.rig, c.channel); got != c.want {
			t.Errorf("Scoped(%q, %q) = %q, want %q", c.rig, c.channel, got, c.want)
		}
	}
}

func TestScopedChannelEmitToRigSubdirectory(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	path, err := EmitToTown(townRoot, Scoped("dashboard", "refinery"), "MERGE_READY", nil)
	if err != nil {
		t.Fatalf("scoped EmitToTown failed: %v", err)
	}

	wantDir := filepath.Join(townRoot, "events", "dashboard", "refinery")
	if !strings.HasPrefix(path, wantDir) {
		t.Errorf("event file %q should live under rig-scoped dir %q", path, wantDir)
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Errorf("rig-scoped event dir should exist: %v", err)
	}

	// Bare channel must remain in the top-level events dir.
	barePath, err := EmitToTown(townRoot, "refinery", "MERGE_READY", nil)
	if err != nil {
		t.Fatalf("bare EmitToTown failed: %v", err)
	}
	bareDir := filepath.Join(townRoot, "events", "refinery")
	if !strings.HasPrefix(barePath, bareDir) {
		t.Errorf("bare event file %q should live in %q", barePath, bareDir)
	}
}

func TestResolveRigFromContext(t *testing.T) {
	// GT_RIG is authoritative when set and not the unset placeholder.
	t.Setenv("GT_RIG", "llmprobe")
	if got := ResolveRigFromContext(""); got != "llmprobe" {
		t.Errorf("ResolveRigFromContext with GT_RIG = %q, want llmprobe", got)
	}

	// UNSET_RIG placeholder is treated as no rig.
	t.Setenv("GT_RIG", UnsetRig)
	if got := ResolveRigFromContext(""); got != "" {
		t.Errorf("ResolveRigFromContext with GT_RIG=UNSET_RIG = %q, want empty", got)
	}

	// No GT_RIG but inside a rig directory -> resolves via path.
	t.Setenv("GT_RIG", "")
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "polecats"), 0755); err != nil {
		t.Fatal(err)
	}
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	if got := ResolveRigFromContext(townRoot); got != "gastown" {
		t.Errorf("ResolveRigFromContext inside rig = %q, want gastown", got)
	}

	// Inside the town root itself -> no rig.
	if err := os.Chdir(townRoot); err != nil {
		t.Fatal(err)
	}
	if got := ResolveRigFromContext(townRoot); got != "" {
		t.Errorf("ResolveRigFromContext at town root = %q, want empty", got)
	}
}
