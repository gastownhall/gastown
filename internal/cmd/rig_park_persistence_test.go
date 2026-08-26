package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/testutil"
)

const parkedLabel = "status:parked"

func requireBdCLI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd CLI not installed, skipping test")
	}
}

func randomTestPrefix(t *testing.T) string {
	t.Helper()
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return "pk" + hex.EncodeToString(buf[:])
}

func setupRigBeadsDB(t *testing.T, rigPath, prefix string) *beads.Beads {
	t.Helper()
	requireBdCLI(t)

	portString := testutil.RequireManagedDoltEndpoint(t)
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("parse isolated Dolt port %q: %v", portString, err)
	}
	b := beads.NewIsolatedWithPort(rigPath, port)
	if err := b.Init(prefix); err != nil {
		t.Fatalf("bd init failed: %v", err)
	}

	return b
}

// Regression test for gt-6ju:
// Parked state should survive wisp cleanup when persisted in bead layer.
func TestIsRigParked_WhenOnlyBeadLabelPresent(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	rigPath := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatalf("mkdir rig path: %v", err)
	}

	prefix := randomTestPrefix(t)
	rigCfg := &config.RigConfig{
		Type:      "rig",
		Version:   config.CurrentRigConfigVersion,
		Name:      rigName,
		GitURL:    "git@github.com:example/repo.git",
		CreatedAt: time.Now(),
		Beads:     &config.BeadsConfig{Prefix: prefix},
	}
	if err := config.SaveRigConfig(filepath.Join(rigPath, "config.json"), rigCfg); err != nil {
		t.Fatalf("save rig config: %v", err)
	}

	b := setupRigBeadsDB(t, rigPath, prefix)
	rigBead, err := b.EnsureRigBead(rigName, &beads.RigFields{
		Repo:   rigCfg.GitURL,
		Prefix: prefix,
		State:  beads.RigStateActive,
	})
	if err != nil {
		t.Fatalf("ensure rig bead: %v", err)
	}
	if err := b.Update(rigBead.ID, beads.UpdateOptions{
		AddLabels: []string{parkedLabel},
	}); err != nil {
		t.Fatalf("set parked label: %v", err)
	}

	// No wisp status set: this simulates wisp cleanup removing ephemeral state.
	// Expected behavior: rig remains parked because bead layer says parked.
	if !IsRigParked(townRoot, rigName) {
		t.Fatalf("expected rig to be parked from bead label %q when wisp state is absent", parkedLabel)
	}
}
