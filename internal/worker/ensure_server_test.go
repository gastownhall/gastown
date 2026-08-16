package worker

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestIsTestExecutable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		bin  string
		want bool
	}{
		{"/tmp/go-build/cmd.test", true},
		{`C:\Users\x\cmd.test.exe`, true},
		{"/usr/local/bin/gt", false},
		{"/tmp/gt-worker", false},
	}
	for _, tc := range cases {
		if got := isTestExecutable(tc.bin); got != tc.want {
			t.Errorf("isTestExecutable(%q) = %v, want %v", tc.bin, got, tc.want)
		}
	}
}

func TestEnsureServerDoesNotReexecTestBinary(t *testing.T) {
	townRoot, err := os.MkdirTemp("/tmp", "gt-worker-ensure-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(townRoot) })

	start := time.Now()
	err = EnsureServer(townRoot)
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("EnsureServer took %s; spawned the test binary as worker serve", elapsed)
	}
	if err == nil {
		t.Fatal("expected EnsureServer to refuse starting a test binary")
	}
	if !errors.Is(err, ErrServerDown) {
		t.Fatalf("EnsureServer() err = %v, want ErrServerDown", err)
	}
}
