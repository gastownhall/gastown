package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/channelevents"
)

func TestEmitEvent(t *testing.T) {
	t.Run("basic event creation", func(t *testing.T) {
		townRoot := t.TempDir()

		path, err := channelevents.EmitToTown(townRoot, "test-channel", "MERGE_READY", []string{"polecat=nux", "branch=feat/test"})
		if err != nil {
			t.Fatalf("EmitEvent failed: %v", err)
		}
		if !strings.HasSuffix(path, ".event") {
			t.Errorf("expected .event suffix, got %q", path)
		}

		// Read and verify content
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read event file: %v", err)
		}

		var event map[string]interface{}
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("failed to parse event JSON: %v", err)
		}
		if event["type"] != "MERGE_READY" {
			t.Errorf("type = %v, want MERGE_READY", event["type"])
		}
		if event["channel"] != "test-channel" {
			t.Errorf("channel = %v, want test-channel", event["channel"])
		}
		if event["timestamp"] == nil {
			t.Error("expected timestamp to be set")
		}

		payload, ok := event["payload"].(map[string]interface{})
		if !ok {
			t.Fatalf("payload is not a map: %T", event["payload"])
		}
		if payload["polecat"] != "nux" {
			t.Errorf("payload.polecat = %v, want nux", payload["polecat"])
		}
		if payload["branch"] != "feat/test" {
			t.Errorf("payload.branch = %v, want feat/test", payload["branch"])
		}
	})

	t.Run("empty payload", func(t *testing.T) {
		townRoot := t.TempDir()
		path, err := channelevents.EmitToTown(townRoot, "test-channel", "PATROL_WAKE", nil)
		if err != nil {
			t.Fatalf("EmitEvent failed: %v", err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read event file: %v", err)
		}

		var event map[string]interface{}
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("failed to parse event JSON: %v", err)
		}
		if event["type"] != "PATROL_WAKE" {
			t.Errorf("type = %v, want PATROL_WAKE", event["type"])
		}

		payload, ok := event["payload"].(map[string]interface{})
		if !ok {
			t.Fatalf("payload is not a map: %T", event["payload"])
		}
		if len(payload) != 0 {
			t.Errorf("expected empty payload, got %v", payload)
		}
	})

	t.Run("multiple events unique paths", func(t *testing.T) {
		townRoot := t.TempDir()
		paths := make(map[string]bool)
		for i := 0; i < 5; i++ {
			path, err := channelevents.EmitToTown(townRoot, "test-channel", "TEST", nil)
			if err != nil {
				t.Fatalf("EmitEvent failed on iteration %d: %v", i, err)
			}
			if paths[path] {
				t.Errorf("duplicate path on iteration %d: %s", i, path)
			}
			paths[path] = true
		}
	})

	t.Run("malformed payload pair ignored", func(t *testing.T) {
		townRoot := t.TempDir()
		path, err := channelevents.EmitToTown(townRoot, "test-channel", "TEST", []string{"valid=yes", "no-equals-sign"})
		if err != nil {
			t.Fatalf("EmitEvent failed: %v", err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read event file: %v", err)
		}

		var event map[string]interface{}
		json.Unmarshal(data, &event)
		payload := event["payload"].(map[string]interface{})
		if payload["valid"] != "yes" {
			t.Errorf("expected payload.valid=yes, got %v", payload["valid"])
		}
		// "no-equals-sign" has no = so strings.Cut returns found=false, skipped
		if _, exists := payload["no-equals-sign"]; exists {
			t.Error("malformed pair should not be in payload")
		}
	})
}

func TestEmitEventChannelValidation(t *testing.T) {
	townRoot := t.TempDir()

	// Valid channel name should succeed
	_, err := channelevents.EmitToTown(townRoot, "valid-channel", "TEST", nil)
	if err != nil {
		t.Errorf("valid channel name rejected: %v", err)
	}

	// Path traversal should be rejected
	_, err = channelevents.EmitToTown(townRoot, "../etc", "TEST", nil)
	if err == nil {
		t.Error("expected error for path traversal channel name, got nil")
	}

	// Slash in channel is now valid (rig-scoped namespace, e.g. gastown/refinery)
	_, err = channelevents.EmitToTown(townRoot, "foo/bar", "TEST", nil)
	if err != nil {
		t.Errorf("channel with slash should be accepted for rig-scoping: %v", err)
	}

	// Double-slash / trailing-slash / leading-slash segments should be rejected
	for _, bad := range []string{"a//b", "a/", "/a"} {
		if _, err := channelevents.EmitToTown(townRoot, bad, "TEST", nil); err == nil {
			t.Errorf("expected error for malformed channel %q with slash segments, got nil", bad)
		}
	}

	// Empty channel should be rejected
	_, err = channelevents.EmitToTown(townRoot, "", "TEST", nil)
	if err == nil {
		t.Error("expected error for empty channel name, got nil")
	}
}

func TestEmitEventPIDInFilename(t *testing.T) {
	townRoot := t.TempDir()
	path, err := channelevents.EmitToTown(townRoot, "test-channel", "TEST", nil)
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}

	// Filename should contain PID for uniqueness: <nanoseconds>-<seq>-<pid>.event
	base := filepath.Base(path)
	if !strings.Contains(base, "-") {
		t.Errorf("filename %q should contain separator '-'", base)
	}
	parts := strings.Split(strings.TrimSuffix(base, ".event"), "-")
	if len(parts) != 3 {
		t.Errorf("filename %q should be <nanos>-<seq>-<pid>.event, got %d parts", base, len(parts))
	}
}

func TestEmitEventResult(t *testing.T) {
	result := EmitEventResult{
		Path:    "/home/gt/events/refinery/12345.event",
		Channel: "refinery",
		Type:    "MERGE_READY",
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded EmitEventResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if decoded.Path != result.Path {
		t.Errorf("path = %q, want %q", decoded.Path, result.Path)
	}
	if decoded.Channel != result.Channel {
		t.Errorf("channel = %q, want %q", decoded.Channel, result.Channel)
	}
	if decoded.Type != result.Type {
		t.Errorf("type = %q, want %q", decoded.Type, result.Type)
	}
}

func TestEmitEventCommandRigScopesChannel(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	rigDir := filepath.Join(townRoot, "gastown", "polecats", "nux")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}

	oldCwd, _ := os.Getwd()
	oldChannel, oldType, oldPayload, oldJSON := emitEventChannel, emitEventType, emitEventPayload, moleculeJSON
	t.Cleanup(func() {
		_ = os.Chdir(oldCwd)
		emitEventChannel, emitEventType, emitEventPayload, moleculeJSON = oldChannel, oldType, oldPayload, oldJSON
	})
	if err := os.Chdir(rigDir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_RIG", "gastown")

	emitEventChannel = "refinery"
	emitEventType = "MQ_SUBMIT"
	emitEventPayload = []string{"branch=feat/x"}
	moleculeJSON = false

	var out bytes.Buffer
	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w
	runErr := runMoleculeEmitEvent(nil, nil)
	w.Close()
	os.Stdout = oldStdout
	_, _ = io.Copy(&out, r)

	if runErr != nil {
		t.Fatalf("runMoleculeEmitEvent: %v", runErr)
	}

	// Rig-scoped dir must contain the event; bare dir must NOT exist.
	scopedDir := filepath.Join(townRoot, "events", "gastown", "refinery")
	entries, err := os.ReadDir(scopedDir)
	if err != nil {
		t.Fatalf("rig-scoped event dir not created: %v", err)
	}
	found := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".event") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no .event file in %s", scopedDir)
	}
	if _, err := os.Stat(filepath.Join(townRoot, "events", "refinery")); !os.IsNotExist(err) {
		t.Errorf("bare refinery dir should not exist for rig-scoped emit")
	}
}

func TestEmitEventCommandBareChannelBackwardCompat(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	oldCwd, _ := os.Getwd()
	oldChannel, oldType, oldPayload, oldJSON := emitEventChannel, emitEventType, emitEventPayload, moleculeJSON
	t.Cleanup(func() {
		_ = os.Chdir(oldCwd)
		emitEventChannel, emitEventType, emitEventPayload, moleculeJSON = oldChannel, oldType, oldPayload, oldJSON
	})
	if err := os.Chdir(townRoot); err != nil {
		t.Fatal(err)
	}
	// No GT_RIG and cwd is the town root -> no rig context resolves.
	t.Setenv("GT_RIG", "")

	emitEventChannel = "refinery"
	emitEventType = "MQ_SUBMIT"
	emitEventPayload = nil
	moleculeJSON = false

	var out bytes.Buffer
	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w
	runErr := runMoleculeEmitEvent(nil, nil)
	w.Close()
	os.Stdout = oldStdout
	_, _ = io.Copy(&out, r)

	if runErr != nil {
		t.Fatalf("runMoleculeEmitEvent: %v", runErr)
	}

	// Bare channel dir must be used (backward compat).
	bareDir := filepath.Join(townRoot, "events", "refinery")
	entries, err := os.ReadDir(bareDir)
	if err != nil {
		t.Fatalf("bare event dir not created for backward compat: %v", err)
	}
	found := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".event") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no .event file in %s", bareDir)
	}
}
