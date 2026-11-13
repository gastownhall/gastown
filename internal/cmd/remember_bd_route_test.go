package cmd

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The memory write/clear argv is checked against the REAL bd on PATH, not a
// mock: the defect being guarded is a disagreement with bd about who owns the
// "memory." kv namespace, and a mock would only restate our own side of it.
//
// Every probe runs against a throwaway --db under t.TempDir(). The fixed argv
// is ACCEPTED by bd, so it does store -- isolating the database is what keeps
// the probe from writing into whatever store the test's working directory
// happens to resolve to.

func bdOnPath(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("bd")
	if err != nil {
		t.Skipf("bd not on PATH: %v", err)
	}
	return path
}

// runBdIsolated runs bd against a scratch database private to this test.
func runBdIsolated(t *testing.T, args ...string) string {
	t.Helper()
	full := append([]string{"--db", filepath.Join(t.TempDir(), "probe.db")}, args...)
	out, _ := exec.Command(bdOnPath(t), full...).CombinedOutput()
	return string(out)
}

const reservedPrefixRejection = "reserved for persistent memories"

// TestMemoryWriteArgsNotRejectedAsReservedPrefix fails while gt builds
// `bd kv set memory.<type>.<key>`, which bd refuses outright.
func TestMemoryWriteArgsNotRejectedAsReservedPrefix(t *testing.T) {
	args := memoryWriteArgs(memoryKeyPrefix+"general.route-probe", "probe content")

	if out := runBdIsolated(t, args...); strings.Contains(out, reservedPrefixRejection) {
		t.Errorf("bd refuses the memory-write argv as a reserved-prefix key.\nargv: bd %s\nbd said: %s",
			strings.Join(args, " "), strings.TrimSpace(out))
	}
}

// TestMemoryClearArgsNotRejectedAsReservedPrefix covers gt forget, which hits
// the same reserved namespace.
func TestMemoryClearArgsNotRejectedAsReservedPrefix(t *testing.T) {
	args := memoryClearArgs(memoryKeyPrefix + "general.route-probe")

	if out := runBdIsolated(t, args...); strings.Contains(out, reservedPrefixRejection) {
		t.Errorf("bd refuses the memory-clear argv as a reserved-prefix key.\nargv: bd %s\nbd said: %s",
			strings.Join(args, " "), strings.TrimSpace(out))
	}
}

// TestMemoryArgsStripPrefixBdManages pins the half of the contract the
// rejection probes cannot see: bd prepends "memory." itself, so a key that
// still carries the prefix stores memory.memory.<type>.<key> -- accepted by
// bd, invisible to gt memories, and silent.
func TestMemoryArgsStripPrefixBdManages(t *testing.T) {
	fullKey := memoryKeyPrefix + "feedback.some-key"

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"write", memoryWriteArgs(fullKey, "content")},
		{"clear", memoryClearArgs(fullKey)},
	} {
		for _, arg := range tc.args {
			if strings.HasPrefix(arg, memoryKeyPrefix) {
				t.Errorf("%s argv passes a key bd would double-prefix: %q in bd %s",
					tc.name, arg, strings.Join(tc.args, " "))
			}
		}
		if !containsArg(tc.args, "feedback.some-key") {
			t.Errorf("%s argv does not carry the de-prefixed key %q: bd %s",
				tc.name, "feedback.some-key", strings.Join(tc.args, " "))
		}
	}
}

// TestMemoryRoundTripsThroughBd is the end-to-end check: a memory written with
// memoryWriteArgs must come back from `bd recall` under the de-prefixed key,
// and memoryClearArgs must remove it. This is what actually fails if bd's
// prefix ownership changes again.
func TestMemoryRoundTripsThroughBd(t *testing.T) {
	db := filepath.Join(t.TempDir(), "probe.db")
	bd := bdOnPath(t)
	run := func(args ...string) string {
		out, _ := exec.Command(bd, append([]string{"--db", db}, args...)...).CombinedOutput()
		return string(out)
	}

	const content = "route probe content"
	fullKey := memoryKeyPrefix + "general.route-probe"

	run(memoryWriteArgs(fullKey, content)...)

	stored := run("kv", "list")
	if !strings.Contains(stored, fullKey+" = "+content) {
		t.Errorf("memory did not land at %q.\nbd kv list said: %s", fullKey, strings.TrimSpace(stored))
	}

	run(memoryClearArgs(fullKey)...)

	if after := run("kv", "list"); strings.Contains(after, fullKey) {
		t.Errorf("memory survived the clear argv.\nbd kv list said: %s", strings.TrimSpace(after))
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
