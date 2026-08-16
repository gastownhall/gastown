package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jonbaldie/gastown/internal/formula"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOverlayHealthCheck(t *testing.T) {
	check := NewOverlayHealthCheck()
	assert.Equal(t, "overlay-health", check.Name())
	assert.True(t, check.CanFix())
	assert.Equal(t, CategoryConfig, check.Category())
}

func TestOverlayHealthCheck_NoOverlays(t *testing.T) {
	tmpDir := t.TempDir()
	setupRigsJSON(t, tmpDir, []string{"testrig"})

	check := NewOverlayHealthCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	assert.Equal(t, StatusOK, result.Status)
	assert.Contains(t, result.Message, "no overlay files")
}

func TestOverlayHealthCheck_HealthyOverlay(t *testing.T) {
	tmpDir := t.TempDir()
	setupRigsJSON(t, tmpDir, []string{"testrig"})

	// Create a town-level overlay referencing real step IDs from mol-polecat-work.
	overlayDir := filepath.Join(tmpDir, "formula-overlays")
	require.NoError(t, os.MkdirAll(overlayDir, 0o755))

	// Get valid step IDs from the embedded formula.
	validIDs := getEmbeddedFormulaStepIDs(t, "mol-polecat-work")
	require.NotEmpty(t, validIDs, "embedded formula must have step IDs")

	content := "[[step-overrides]]\nstep_id = " + quote(validIDs[0]) + "\nmode = \"append\"\ndescription = \"Extra instructions\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(overlayDir, "mol-polecat-work.toml"), []byte(content), 0o644))

	check := NewOverlayHealthCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	assert.Equal(t, StatusOK, result.Status)
	assert.Contains(t, result.Message, "healthy")
}

func TestOverlayHealthCheck_StaleStepID(t *testing.T) {
	tmpDir := t.TempDir()
	setupRigsJSON(t, tmpDir, []string{"testrig"})

	overlayDir := filepath.Join(tmpDir, "formula-overlays")
	require.NoError(t, os.MkdirAll(overlayDir, 0o755))

	content := `[[step-overrides]]
step_id = "nonexistent-step-from-old-binary"
mode = "replace"
description = "This won't match anything"
`
	require.NoError(t, os.WriteFile(filepath.Join(overlayDir, "mol-polecat-work.toml"), []byte(content), 0o644))

	check := NewOverlayHealthCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	assert.Equal(t, StatusWarning, result.Status)
	assert.Contains(t, result.Message, "stale")
	require.NotEmpty(t, result.Details)
	assert.Contains(t, result.Details[0], "nonexistent-step-from-old-binary")
	assert.NotEmpty(t, result.FixHint)
}

func TestOverlayHealthCheck_MalformedTOML(t *testing.T) {
	tmpDir := t.TempDir()
	setupRigsJSON(t, tmpDir, []string{"testrig"})

	overlayDir := filepath.Join(tmpDir, "formula-overlays")
	require.NoError(t, os.MkdirAll(overlayDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(overlayDir, "mol-polecat-work.toml"), []byte("[[invalid"), 0o644))

	check := NewOverlayHealthCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	assert.Equal(t, StatusError, result.Status)
	assert.Contains(t, result.Message, "malformed")
}

func TestOverlayHealthCheck_RigLevel(t *testing.T) {
	tmpDir := t.TempDir()
	setupRigsJSON(t, tmpDir, []string{"testrig"})

	// Create rig-level overlay with stale step.
	rigDir := filepath.Join(tmpDir, "testrig", "formula-overlays")
	require.NoError(t, os.MkdirAll(rigDir, 0o755))

	content := `[[step-overrides]]
step_id = "old-removed-step"
mode = "skip"
`
	require.NoError(t, os.WriteFile(filepath.Join(rigDir, "mol-polecat-work.toml"), []byte(content), 0o644))

	check := NewOverlayHealthCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	assert.Equal(t, StatusWarning, result.Status)
	assert.Contains(t, result.Details[0], "old-removed-step")
}

func TestOverlayHealthCheck_UnknownFormula(t *testing.T) {
	tmpDir := t.TempDir()
	setupRigsJSON(t, tmpDir, []string{"testrig"})

	overlayDir := filepath.Join(tmpDir, "formula-overlays")
	require.NoError(t, os.MkdirAll(overlayDir, 0o755))

	content := `[[step-overrides]]
step_id = "some-step"
mode = "replace"
description = "Override for non-existent formula"
`
	require.NoError(t, os.WriteFile(filepath.Join(overlayDir, "nonexistent-formula.toml"), []byte(content), 0o644))

	check := NewOverlayHealthCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	// All step IDs should be reported as stale since the formula doesn't exist.
	assert.Equal(t, StatusWarning, result.Status)
	assert.Contains(t, result.Details[0], "some-step")
}

func TestOverlayHealthCheck_Fix_RemovesStaleEntries(t *testing.T) {
	tmpDir := t.TempDir()
	setupRigsJSON(t, tmpDir, []string{"testrig"})

	overlayDir := filepath.Join(tmpDir, "formula-overlays")
	require.NoError(t, os.MkdirAll(overlayDir, 0o755))

	// Get a valid step ID.
	validIDs := getEmbeddedFormulaStepIDs(t, "mol-polecat-work")
	require.NotEmpty(t, validIDs)

	// Create overlay with one valid and one stale override.
	content := "[[step-overrides]]\nstep_id = " + quote(validIDs[0]) + "\nmode = \"append\"\ndescription = \"Keep this\"\n\n" +
		"[[step-overrides]]\nstep_id = \"ghost-step\"\nmode = \"skip\"\n"
	overlayPath := filepath.Join(overlayDir, "mol-polecat-work.toml")
	require.NoError(t, os.WriteFile(overlayPath, []byte(content), 0o644))

	check := NewOverlayHealthCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Verify it's warning before fix.
	result := check.Run(ctx)
	assert.Equal(t, StatusWarning, result.Status)

	// Run fix.
	require.NoError(t, check.Fix(ctx))

	// Re-check — should be healthy now.
	result = check.Run(ctx)
	assert.Equal(t, StatusOK, result.Status)

	// File should still exist (has valid entries).
	_, err := os.Stat(overlayPath)
	assert.NoError(t, err)
}

func TestOverlayHealthCheck_Fix_RemovesEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	setupRigsJSON(t, tmpDir, []string{"testrig"})

	overlayDir := filepath.Join(tmpDir, "formula-overlays")
	require.NoError(t, os.MkdirAll(overlayDir, 0o755))

	// All overrides are stale.
	content := `[[step-overrides]]
step_id = "ghost-step-1"
mode = "skip"

[[step-overrides]]
step_id = "ghost-step-2"
mode = "replace"
description = "Also stale"
`
	overlayPath := filepath.Join(overlayDir, "mol-polecat-work.toml")
	require.NoError(t, os.WriteFile(overlayPath, []byte(content), 0o644))

	check := NewOverlayHealthCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	require.NoError(t, check.Fix(ctx))

	// File should be removed entirely.
	_, err := os.Stat(overlayPath)
	assert.True(t, os.IsNotExist(err), "overlay file should be removed when all entries are stale")

	// Re-check — should be OK (no overlays).
	result := check.Run(ctx)
	assert.Equal(t, StatusOK, result.Status)
}

func TestOverlayHealthCheck_Fix_SkipsMalformed(t *testing.T) {
	tmpDir := t.TempDir()
	setupRigsJSON(t, tmpDir, []string{"testrig"})

	overlayDir := filepath.Join(tmpDir, "formula-overlays")
	require.NoError(t, os.MkdirAll(overlayDir, 0o755))

	overlayPath := filepath.Join(overlayDir, "mol-polecat-work.toml")
	require.NoError(t, os.WriteFile(overlayPath, []byte("[[invalid"), 0o644))

	check := NewOverlayHealthCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Fix should not error — just skips malformed files.
	require.NoError(t, check.Fix(ctx))

	// File should still exist (untouched).
	data, err := os.ReadFile(overlayPath)
	require.NoError(t, err)
	assert.Equal(t, "[[invalid", string(data))
}

// --- helpers ---

func getEmbeddedFormulaStepIDs(t *testing.T, name string) []string {
	t.Helper()
	data, err := formula.GetEmbeddedFormulaContent(name)
	require.NoError(t, err)
	f, err := formula.Parse(data)
	require.NoError(t, err)
	return f.GetAllIDs()
}

func quote(s string) string {
	return `"` + s + `"`
}
