package cmd

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestVersionOutputNamesGtb(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	versionCmd.Run(versionCmd, nil)
	os.Stdout = old
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if !strings.HasPrefix(string(out), "gtb version ") {
		t.Fatalf("version output = %q, want prefix %q", out, "gtb version ")
	}
}
