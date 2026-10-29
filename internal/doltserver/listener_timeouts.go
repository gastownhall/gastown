package doltserver

import "fmt"

// ListenerTimeouts holds listener limits in milliseconds; zero selects Dolt's default.
type ListenerTimeouts struct {
	ReadMs  int
	WriteMs int
}

// ResolveListenerTimeouts applies the shared override policy to caller-specific
// defaults. See docs/design/dolt-storage.md#listener-timeouts for operator guidance.
func ResolveListenerTimeouts(townRoot string, defaultReadMs, defaultWriteMs int) ListenerTimeouts {
	return ListenerTimeouts{
		ReadMs:  resolveListenerTimeoutMs(townRoot, "GT_DOLT_READ_TIMEOUT_MS", defaultReadMs),
		WriteMs: resolveListenerTimeoutMs(townRoot, "GT_DOLT_WRITE_TIMEOUT_MS", defaultWriteMs),
	}
}

// YAML returns newline-prefixed fields indented for a listener mapping, omitting
// nonpositive limits. Callers must validate limits before rendering.
func (t ListenerTimeouts) YAML() string {
	lines := ""
	if t.ReadMs > 0 {
		lines += fmt.Sprintf("\n  read_timeout_millis: %d", t.ReadMs)
	}
	if t.WriteMs > 0 {
		lines += fmt.Sprintf("\n  write_timeout_millis: %d", t.WriteMs)
	}
	return lines
}
