package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestConvoyTracksBeadExactMatch verifies that convoyTracksBead finds a bead
// when the dep query returns the raw beadID.
func TestConvoyTracksBeadExactMatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	beadsDir := t.TempDir()

	// Stub bd sql to return a tracked dep with raw beadID
	bdScript := `#!/bin/sh
echo '[{"depends_on_id":"gt-abc123"}]'
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	if !convoyTracksBead(beadsDir, "hq-cv-test1", "gt-abc123") {
		t.Error("convoyTracksBead should return true for exact match")
	}
}

// TestConvoyTracksBeadExternalRef verifies that convoyTracksBead finds a bead
// when the dep query returns an external-formatted reference.
func TestConvoyTracksBeadExternalRef(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	beadsDir := t.TempDir()

	// Stub bd sql to return a tracked dep with external:prefix:beadID format
	bdScript := `#!/bin/sh
echo '[{"depends_on_id":"external:gt-abc:gt-abc123"}]'
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	if !convoyTracksBead(beadsDir, "hq-cv-test2", "gt-abc123") {
		t.Error("convoyTracksBead should return true for external ref match")
	}
}

// TestConvoyTracksBeadNoMatch verifies that convoyTracksBead returns false
// when the convoy tracks a different bead.
func TestConvoyTracksBeadNoMatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	beadsDir := t.TempDir()

	// Stub bd sql to return a tracked dep with a different beadID
	bdScript := `#!/bin/sh
echo '[{"depends_on_id":"gt-other456"}]'
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	if convoyTracksBead(beadsDir, "hq-cv-test3", "gt-abc123") {
		t.Error("convoyTracksBead should return false when bead not tracked")
	}
}

// TestConvoyTracksBeadEmptyDeps verifies that convoyTracksBead returns false
// when the convoy has no tracked deps.
func TestConvoyTracksBeadEmptyDeps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	beadsDir := t.TempDir()

	// Stub bd sql to return empty array
	bdScript := `#!/bin/sh
echo '[]'
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	if convoyTracksBead(beadsDir, "hq-cv-test4", "gt-abc123") {
		t.Error("convoyTracksBead should return false for empty deps")
	}
}

// TestConvoyTracksBeadMultipleDeps verifies that convoyTracksBead finds the
// target bead among multiple tracked deps.
func TestConvoyTracksBeadMultipleDeps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	beadsDir := t.TempDir()

	// Stub bd sql to return multiple tracked deps, one of which matches
	bdScript := `#!/bin/sh
echo '[{"depends_on_id":"gt-other1"},{"depends_on_id":"external:gt-abc:gt-abc123"},{"depends_on_id":"gt-other2"}]'
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	if !convoyTracksBead(beadsDir, "hq-cv-test5", "gt-abc123") {
		t.Error("convoyTracksBead should return true when bead found among multiple deps")
	}
}

// TestBdDepListRawIDsValidation verifies that bdDepListRawIDs rejects
// invalid bead IDs to prevent SQL injection.
func TestBdDepListRawIDsValidation(t *testing.T) {
	_, err := bdDepListRawIDs("/tmp", "'; DROP TABLE deps; --", "down", "tracks")
	if err == nil {
		t.Error("bdDepListRawIDs should reject SQL injection attempts")
	}

	_, err = bdDepListRawIDs("/tmp", "valid-id", "down", "'; DROP TABLE deps; --")
	if err == nil {
		t.Error("bdDepListRawIDs should reject SQL injection in depType")
	}
}

func TestBdDepListRawIDsFallsBackToSplitTargetSchema(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	bdScript := `#!/bin/sh
case "$*" in
  *"SELECT depends_on_id FROM dependencies"*)
    echo '{"error":"query error: Error 1105 (HY000): column \"depends_on_id\" could not be found in any table in scope","schema_version":1}'
    exit 1
    ;;
  *"COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS depends_on_id"*)
    echo '[{"depends_on_id":"external:gt:gt-abc123"}]'
    exit 0
    ;;
  *)
    echo "unexpected args: $*" >&2
    exit 1
    ;;
esac
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	ids, err := bdDepListRawIDs(t.TempDir(), "hq-cv-test", "down", "tracks")
	if err != nil {
		t.Fatalf("bdDepListRawIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "gt-abc123" {
		t.Fatalf("ids = %v, want [gt-abc123]", ids)
	}
}

func TestBdDepListRawIDsFallsBackToSplitTargetSchemaUpDirection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	bdScript := `#!/bin/sh
case "$*" in
  *"SELECT issue_id FROM dependencies WHERE depends_on_id = 'gt-target'"*)
    echo '{"error":"query error: Error 1105 (HY000): column \"depends_on_id\" could not be found in any table in scope","schema_version":1}'
    exit 1
    ;;
  *"SELECT issue_id FROM dependencies WHERE 'gt-target' IN (depends_on_issue_id, depends_on_wisp_id, depends_on_external)"*)
    echo '[{"issue_id":"hq-cv-parent"}]'
    exit 0
    ;;
  *)
    echo "unexpected args: $*" >&2
    exit 1
    ;;
esac
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	ids, err := bdDepListRawIDs(t.TempDir(), "gt-target", "up", "tracks")
	if err != nil {
		t.Fatalf("bdDepListRawIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "hq-cv-parent" {
		t.Fatalf("ids = %v, want [hq-cv-parent]", ids)
	}
}
