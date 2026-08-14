package dog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Guardian control-plane flags. Autonomous dog dispatch and inline daemon
// mutation must consult these before creating molecules, slinging dogs, or
// mutating beads.
type GuardianState struct {
	DispatchAllowed   bool   `json:"dispatch_allowed"`
	ActivationAllowed bool   `json:"activation_allowed"`
	Reason            string `json:"reason,omitempty"`
}

// ErrGuardianDenied is returned when dispatch or activation is not allowed.
var ErrGuardianDenied = errors.New("guardian denied")

// GuardianFile returns the path of the integrity-guardian state file.
func GuardianFile(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "deacon", "guardian.json")
}

// LoadGuardianState reads guardian flags. A missing file means no guardian is
// installed, so both flags default to allowed. A present but unreadable or
// invalid file is unavailable and fails closed.
func LoadGuardianState(townRoot string) (GuardianState, error) {
	path := GuardianFile(townRoot)
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed from trusted townRoot
	if err != nil {
		if os.IsNotExist(err) {
			return GuardianState{DispatchAllowed: true, ActivationAllowed: true, Reason: "guardian not installed"}, nil
		}
		return GuardianState{}, fmt.Errorf("%w: reading guardian state: %w", ErrGuardianDenied, err)
	}
	var state GuardianState
	if err := json.Unmarshal(data, &state); err != nil {
		return GuardianState{}, fmt.Errorf("%w: parsing guardian state: %w", ErrGuardianDenied, err)
	}
	return state, nil
}

// RequireDispatchAllowed fails closed when guardian state is unavailable or
// dispatch_allowed is false.
func RequireDispatchAllowed(townRoot string) error {
	state, err := LoadGuardianState(townRoot)
	if err != nil {
		return err
	}
	if !state.DispatchAllowed {
		reason := state.Reason
		if reason == "" {
			reason = "dispatch_allowed=false"
		}
		return fmt.Errorf("%w: %s", ErrGuardianDenied, reason)
	}
	return nil
}

// RequireActivationAllowed fails closed when guardian state is unavailable or
// activation_allowed is false. Inline fallback mutation must call this.
func RequireActivationAllowed(townRoot string) error {
	state, err := LoadGuardianState(townRoot)
	if err != nil {
		return err
	}
	if !state.ActivationAllowed {
		reason := state.Reason
		if reason == "" {
			reason = "activation_allowed=false"
		}
		return fmt.Errorf("%w: %s", ErrGuardianDenied, reason)
	}
	return nil
}
