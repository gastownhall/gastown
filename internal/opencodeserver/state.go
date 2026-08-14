package opencodeserver

import (
	"context"

	"github.com/steveyegge/gastown/internal/opencodestate"
)

type State = opencodestate.State

func StatePath(townRoot, gasTownSession string) string {
	return opencodestate.Path(townRoot, gasTownSession)
}

func SaveState(townRoot string, state State) error {
	return opencodestate.Save(townRoot, state)
}

func LoadState(townRoot, gasTownSession string) (State, error) {
	return opencodestate.Load(townRoot, gasTownSession)
}

func RemoveState(townRoot, gasTownSession, openCodeSession string) error {
	return opencodestate.Remove(townRoot, gasTownSession, openCodeSession)
}

func ActiveState(ctx context.Context, townRoot, gasTownSession string) (State, bool) {
	return opencodestate.Active(ctx, townRoot, gasTownSession)
}

func AcquireSessionLock(townRoot, gasTownSession string) (func(), error) {
	return opencodestate.AcquireSessionLock(townRoot, gasTownSession)
}

func SessionLockHeld(townRoot, gasTownSession string) (bool, error) {
	return opencodestate.SessionLockHeld(townRoot, gasTownSession)
}
