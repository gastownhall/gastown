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
	valid := []string{"refinery", "witness", "my-channel", "test_chan", "abc123"}
	for _, name := range valid {
		if !ValidChannelName.MatchString(name) {
			t.Errorf("%q should be valid", name)
		}
	}

	invalid := []string{"../escape", "has space", "has/slash", "", "has.dot"}
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

func TestEmitToRig_IsolatesRefineryChannels(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	alphaPath, err := EmitToRig(townRoot, "alpha", "refinery", "MQ_SUBMIT", nil)
	if err != nil {
		t.Fatalf("EmitToRig alpha failed: %v", err)
	}
	betaPath, err := EmitToRig(townRoot, "beta", "refinery", "MQ_SUBMIT", nil)
	if err != nil {
		t.Fatalf("EmitToRig beta failed: %v", err)
	}

	alphaDir := filepath.Join(townRoot, "events", "refinery", "alpha")
	betaDir := filepath.Join(townRoot, "events", "refinery", "beta")
	if filepath.Dir(alphaPath) != alphaDir {
		t.Errorf("alpha event dir = %q, want %q", filepath.Dir(alphaPath), alphaDir)
	}
	if filepath.Dir(betaPath) != betaDir {
		t.Errorf("beta event dir = %q, want %q", filepath.Dir(betaPath), betaDir)
	}
	if alphaPath == betaPath {
		t.Errorf("rig-scoped events share a path: %q", alphaPath)
	}
	alphaEvents, err := os.ReadDir(alphaDir)
	if err != nil {
		t.Fatalf("reading alpha event dir: %v", err)
	}
	betaEvents, err := os.ReadDir(betaDir)
	if err != nil {
		t.Fatalf("reading beta event dir: %v", err)
	}
	if len(alphaEvents) != 1 || len(betaEvents) != 1 {
		t.Errorf("isolated event counts = alpha:%d beta:%d, want 1 each", len(alphaEvents), len(betaEvents))
	}
	globalEntries, err := os.ReadDir(filepath.Join(townRoot, "events", "refinery"))
	if err != nil {
		t.Fatalf("reading global refinery dir: %v", err)
	}
	for _, entry := range globalEntries {
		if strings.HasSuffix(entry.Name(), ".event") {
			t.Errorf("rig-scoped emit leaked event %q into the town-wide channel", entry.Name())
		}
	}

	if _, err := EmitToRig(townRoot, "../escape", "refinery", "TEST", nil); err == nil {
		t.Error("expected invalid rig name to be rejected")
	}
}
