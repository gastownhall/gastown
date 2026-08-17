package now

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonbaldie/gastown/internal/constants"
)

func TestEnsureRigCorruptRigsJSONDoesNotOverwrite(t *testing.T) {
	town := t.TempDir()
	mayorDir := filepath.Join(town, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	rigsPath := constants.MayorRigsPath(town)
	corrupt := []byte("{not-json")
	if err := os.WriteFile(rigsPath, corrupt, 0644); err != nil {
		t.Fatalf("write corrupt rigs.json: %v", err)
	}

	repo := t.TempDir()
	_, err := ensureRig(context.Background(), town, repo, "proj")
	if err == nil {
		t.Fatal("ensureRig succeeded with corrupt rigs.json")
	}

	got, readErr := os.ReadFile(rigsPath)
	if readErr != nil {
		t.Fatalf("reading rigs.json: %v", readErr)
	}
	if string(got) != string(corrupt) {
		t.Fatalf("corrupt rigs.json was overwritten:\n%s", got)
	}
}

func TestEnsureRigMissingRigsJSONDoesNotCreateEmpty(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	_, err := ensureRig(context.Background(), town, t.TempDir(), "proj")
	if err == nil {
		t.Fatal("ensureRig succeeded with missing rigs.json")
	}
	if _, statErr := os.Stat(constants.MayorRigsPath(town)); !os.IsNotExist(statErr) {
		t.Fatal("missing rigs.json was replaced")
	}
}

func TestEnsureRigRespectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ensureRig(ctx, t.TempDir(), t.TempDir(), "proj"); err == nil {
		t.Fatal("ensureRig ignored canceled context")
	}
}
