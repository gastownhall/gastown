// Package instructions provisions Gas Town instruction files.
//
// The canonical file is AGENTS.md. CLAUDE.md is a symlink to that file.
// When a constitution file is already present, the local pair is used
// instead: AGENTS.local.md and CLAUDE.local.md.
package instructions

import "strings"

const (
	// CanonicalFile is the default Gas Town instruction file.
	CanonicalFile = "AGENTS.md"
	// AliasFile is the Claude Code alias for CanonicalFile.
	AliasFile = "CLAUDE.md"
	// LocalCanonicalFile is the overlay file used when a constitution file exists.
	LocalCanonicalFile = "AGENTS.local.md"
	// LocalAliasFile is the Claude Code alias for LocalCanonicalFile.
	LocalAliasFile = "CLAUDE.local.md"
	// GeminiAliasFile is the Gemini CLI alias for the canonical file of the pair.
	GeminiAliasFile = "GEMINI.md"
	// LifecycleMarker is the polecat overlay fingerprint.
	LifecycleMarker = "IDLE POLECAT HERESY"
)

// IsIdentityText reports whether content is a Gas Town town-root identity anchor.
func IsIdentityText(content string) bool {
	return strings.HasPrefix(content, "# Gas Town") && strings.Contains(content, "prime")
}

// IsGasTownOverlay reports whether content is a Gas Town identity or polecat overlay.
func IsGasTownOverlay(content string) bool {
	return IsIdentityText(content) || strings.Contains(content, LifecycleMarker)
}
