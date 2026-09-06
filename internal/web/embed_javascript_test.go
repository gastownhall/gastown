package web

import (
	"os/exec"
	"testing"
)

func TestEmbedJavaScript(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required for JavaScript contract tests")
	}
	if out, err := exec.Command("node", "--test", "testdata/embed.test.cjs").CombinedOutput(); err != nil {
		t.Fatalf("JavaScript contract: %v\n%s", err, out)
	}
}
