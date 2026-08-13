package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/dog"
)

// TestIsDogTarget verifies the dog target pattern matching.
// Dogs can be targeted via:
//   - "deacon/dogs" -> pool dispatch (any idle dog)
//   - "deacon/dogs/alpha" -> specific dog
//   - "dog:" -> pool dispatch (shorthand)
//   - "dog:alpha" -> specific dog (shorthand)
func TestIsDogTarget(t *testing.T) {
	tests := []struct {
		target  string
		wantDog string
		wantIs  bool
	}{
		// Pool dispatch patterns
		{"deacon/dogs", "", true},
		{"dog:", "", true},
		{"DEACON/DOGS", "", true}, // case insensitive
		{"DOG:", "", true},

		// Specific dog patterns
		{"deacon/dogs/alpha", "alpha", true},
		{"deacon/dogs/bravo", "bravo", true},
		{"dog:alpha", "alpha", true},
		{"dog:bravo", "bravo", true},
		{"DOG:ALPHA", "alpha", true}, // case insensitive, name lowercased

		// Invalid patterns - not dog targets
		{"deacon", "", false},
		{"deacon/", "", false},
		{"deacon/dogs/", "", false},            // trailing slash, empty name
		{"deacon/dogs/alpha/extra", "", false}, // too many segments
		{"dog", "", false},                     // missing colon
		{"dogs:alpha", "", false},              // wrong prefix
		{"polecat:alpha", "", false},
		{"gastown/polecats/alpha", "", false},
		{"mayor", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			gotDog, gotIs := IsDogTarget(tt.target)
			if gotIs != tt.wantIs {
				t.Errorf("IsDogTarget(%q) isDog = %v, want %v", tt.target, gotIs, tt.wantIs)
			}
			if gotDog != tt.wantDog {
				t.Errorf("IsDogTarget(%q) dogName = %q, want %q", tt.target, gotDog, tt.wantDog)
			}
		})
	}
}

// TestDogDispatchInfoDelayedSession verifies the delayed session start pattern.
// When DelaySessionStart is true:
//   - DispatchToDog returns with Pane="" and sessionDelayed=true
//   - StartDelayedSession() must be called to actually start the session
//
// This prevents the race condition where dogs start before their hook is set.
func TestDogDispatchInfoDelayedSession(t *testing.T) {
	// Test that DogDispatchInfo correctly tracks delayed state
	info := &DogDispatchInfo{
		DogName:        "alpha",
		AgentID:        "deacon/dogs/alpha",
		Pane:           "", // Empty when delayed
		Spawned:        false,
		sessionDelayed: true,
		townRoot:       "/tmp/test",
		workDesc:       "test-work",
	}

	// Verify initial state
	if info.Pane != "" {
		t.Errorf("delayed dispatch should have empty Pane, got %q", info.Pane)
	}
	if !info.sessionDelayed {
		t.Error("sessionDelayed should be true for delayed dispatch")
	}

	// Note: We can't test StartDelayedSession without mocking tmux,
	// but we verify the struct correctly holds the delayed state.
}

// TestDogDispatchOptionsStruct verifies the DogDispatchOptions fields.
func TestDogDispatchOptionsStruct(t *testing.T) {
	opts := DogDispatchOptions{
		Create:            true,
		WorkDesc:          constants.MolConvoyFeed,
		WorkKind:          dog.WorkKindFormula,
		DelaySessionStart: true,
	}

	if !opts.Create {
		t.Error("Create should be true")
	}
	if opts.WorkDesc != constants.MolConvoyFeed {
		t.Errorf("WorkDesc = %q, want %q", opts.WorkDesc, constants.MolConvoyFeed)
	}
	if !opts.DelaySessionStart {
		t.Error("DelaySessionStart should be true")
	}
}

// TestMaxDogPoolSize verifies the pool size constant matches the documented limit.
func TestMaxDogPoolSize(t *testing.T) {
	if maxDogPoolSize != 4 {
		t.Errorf("maxDogPoolSize = %d, want 4 (matches mol-deacon-patrol pool sizing guideline)", maxDogPoolSize)
	}
}

// TestDogTargetsAreNotMistakenForRigs is a regression guard for bead aa-4yf2.
// The deferred sling path (active when scheduler.max_polecats > 0) rejects
// targets that are neither rigs nor dogs. When dispatchFeedDog calls
//
//	gt sling mol-convoy-feed deacon/dogs --var convoy=<id>
//
// the target "deacon/dogs" must be classified as a dog pool target, not
// fall through to rig-name resolution. Otherwise the deferred path bails
// with "deferred dispatch requires a rig target" and stranded-convoy
// auto-feeding breaks.
//
// This test locks in the classification invariant that dog pool targets
// satisfy IsDogTarget (so sling.go can fall them through to direct dispatch).
func TestDogTargetsAreNotMistakenForRigs(t *testing.T) {
	// Any classifier-level change that makes one of these stop being a dog
	// target will break feed-stranded auto-feeding in deferred mode.
	dogPoolTargets := []string{
		"deacon/dogs",       // canonical pool target used by dispatchFeedDog
		"deacon/dogs/alpha", // specific-dog target
		"dog:",              // shorthand pool target
		"dog:alpha",         // shorthand specific-dog target
	}

	for _, target := range dogPoolTargets {
		t.Run(target, func(t *testing.T) {
			if _, isDog := IsDogTarget(target); !isDog {
				t.Fatalf("IsDogTarget(%q) = false — dog pool targets must be "+
					"recognized so the deferred sling path can fall through "+
					"to direct dispatch (aa-4yf2 regression)", target)
			}
		})
	}
}
