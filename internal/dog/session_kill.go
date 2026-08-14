package dog

import "fmt"

// ShouldKillCompletedDogSession reports whether a delayed session kill from
// `gt dog done` may still run. A newer assignment or live work must be left
// alone.
func ShouldKillCompletedDogSession(current *Dog) bool {
	return current != nil && current.State == StateIdle && current.Work == ""
}

// KillCompletedDogSession re-reads dog state and kills the session only when
// the dog is still idle with no work. This closes the race where a new
// assignment starts during the post-done delay.
func KillCompletedDogSession(mgr *Manager, name, sessionID string, killer func(string) error) error {
	if mgr == nil {
		return fmt.Errorf("missing dog manager")
	}
	if killer == nil {
		return fmt.Errorf("missing session killer")
	}
	current, err := mgr.Get(name)
	if err != nil {
		return err
	}
	if !ShouldKillCompletedDogSession(current) {
		return nil
	}
	return killer(sessionID)
}
