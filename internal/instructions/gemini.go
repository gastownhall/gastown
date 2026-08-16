package instructions

import (
	"fmt"
	"os"
	"path/filepath"
)

type geminiOp int

const (
	geminiNone geminiOp = iota
	geminiWrite
)

// EnsureGeminiAlias makes GEMINI.md a symlink to the canonical instruction
// file when Gas Town is allowed to create that alias (Gemini provider).
// A regular GEMINI.md is left unchanged. Missing GEMINI.md is created only
// when the canonical file already exists.
func EnsureGeminiAlias(dir string) error {
	if dir == "" {
		return nil
	}
	snap := snapshot(dir)
	canonical := CanonicalName(dir)
	return applyGemini(dir, canonical, geminiAction(snap, canonical, true))
}

func geminiAction(snap dirSnap, canonical string, createIfMissing bool) geminiOp {
	if snap.gemini.regular {
		return geminiNone
	}
	if snap.gemini.symlink {
		if filepath.Clean(snap.gemini.target) == canonical {
			return geminiNone
		}
		if isGasTownAliasTarget(snap.gemini.target) {
			return geminiWrite
		}
		return geminiNone
	}
	if createIfMissing && canonicalPresent(snap, canonical) {
		return geminiWrite
	}
	return geminiNone
}

func canonicalPresent(snap dirSnap, canonical string) bool {
	entry := snap.entry(canonical)
	return entry.regular || entry.symlink
}

func applyGemini(dir, canonical string, op geminiOp) error {
	if op != geminiWrite {
		return nil
	}
	path := filepath.Join(dir, GeminiAliasFile)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", GeminiAliasFile, err)
	}
	if err := os.Symlink(canonical, path); err != nil {
		return fmt.Errorf("linking %s: %w", GeminiAliasFile, err)
	}
	return nil
}
