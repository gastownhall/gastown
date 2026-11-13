package cmd

import "testing"

func TestWitnessHookLabel(t *testing.T) {
	cases := []struct {
		idleSeconds int
		want        string
	}{
		{21 * 3600, "running, hook idle 21h"},
		{45 * 60, "running, hook idle 45m"},
		{3 * 86400, "running, hook idle 3d"},
		{0, "running, hook not advancing"},
		{-1, "running, hook not advancing"},
	}
	for _, tc := range cases {
		if got := witnessHookLabel(tc.idleSeconds); got != tc.want {
			t.Errorf("witnessHookLabel(%d) = %q, want %q", tc.idleSeconds, got, tc.want)
		}
	}
}

func TestRigBeadsDirFallsBackToConvention(t *testing.T) {
	// A rig with no registered directory still resolves to <townRoot>/<rig>
	// rather than to an empty path, which would silently read the wrong
	// database and report an empty hook (and therefore a healthy witness).
	got := rigBeadsDir(t.TempDir(), "gastown")
	if got == "" {
		t.Fatal("rigBeadsDir returned empty path")
	}
}
