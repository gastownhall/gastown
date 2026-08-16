package buildgraph

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMatchingPrefixesReportsOnlyForbiddenPrefixes(t *testing.T) {
	deps := []string{
		"fmt",
		"github.com/dolthub/driver",
		"github.com/steveyegge/beads",
		"github.com/testcontainers/testcontainers-go",
		"github.com/go-rod/rod",
		"github.com/go-rod/rod/lib/launcher",
	}

	got := MatchingPrefixes(deps, ProductionForbiddenPrefixes)
	want := []string{
		"github.com/dolthub/driver",
		"github.com/testcontainers/testcontainers-go",
		"github.com/go-rod/rod",
		"github.com/go-rod/rod/lib/launcher",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("MatchingPrefixes() = %q, want %q", got, want)
	}
}

func TestMatchingPrefixesEmptyWhenGraphIsClean(t *testing.T) {
	deps := []string{
		"fmt",
		"github.com/steveyegge/beads",
		"github.com/go-sql-driver/mysql",
	}
	if hits := MatchingPrefixes(deps, ProductionForbiddenPrefixes); len(hits) != 0 {
		t.Fatalf("MatchingPrefixes() = %q, want none", hits)
	}
}

func TestMakeDefaultsCGOOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "make", "--no-print-directory", "cgo-enabled")
	cmd.Dir = moduleRoot(t)
	cmd.Env = envWithout(os.Environ(), "CGO_ENABLED")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("make cgo-enabled: %v\n%s", err, ee.Stderr)
		}
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "0" {
		t.Fatalf("make cgo-enabled = %q, want 0 (Makefile default must be CGO off)", got)
	}
}

func TestCmdGTWithoutCGOOmitsForbiddenDeps(t *testing.T) {
	deps := listDeps(t, "0", "./cmd/gt")
	if hits := MatchingPrefixes(deps, ProductionForbiddenPrefixes); len(hits) != 0 {
		t.Fatalf("CGO_ENABLED=0 ./cmd/gt still imports forbidden packages:\n%s", strings.Join(hits, "\n"))
	}
}

func TestCmdGTWithCGOPullsEmbeddedDolt(t *testing.T) {
	deps := listDeps(t, "1", "./cmd/gt")
	if MatchingPrefixes(deps, []string{"github.com/steveyegge/beads/internal/storage/embeddeddolt"}) == nil {
		t.Fatal("CGO_ENABLED=1 ./cmd/gt should import beads embeddeddolt; that is the compile tax CGO_ENABLED=0 removes")
	}
}

func TestTestutilDefaultTestGraphOmitsTestcontainers(t *testing.T) {
	deps := listTestDeps(t, "", "./internal/testutil")
	if hits := MatchingPrefixes(deps, []string{"github.com/testcontainers/"}); len(hits) != 0 {
		t.Fatalf("go test ./internal/testutil (no tags) still depends on testcontainers:\n%s", strings.Join(hits, "\n"))
	}
}

func TestConvoyDefaultTestGraphOmitsTestcontainers(t *testing.T) {
	deps := listTestDeps(t, "", "./internal/convoy")
	if hits := MatchingPrefixes(deps, []string{"github.com/testcontainers/"}); len(hits) != 0 {
		t.Fatalf("go test ./internal/convoy (no tags) still depends on testcontainers:\n%s", strings.Join(hits, "\n"))
	}
}

func TestTestutilIntegrationTestGraphKeepsTestcontainers(t *testing.T) {
	deps := listTestDeps(t, "integration", "./internal/testutil")
	if MatchingPrefixes(deps, []string{"github.com/testcontainers/testcontainers-go"}) == nil {
		t.Fatal("go test -tags=integration ./internal/testutil must still compile testcontainers")
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func listDeps(t *testing.T, cgo, pkg string) []string {
	t.Helper()
	return runGoList(t, []string{"list", "-deps", pkg}, "CGO_ENABLED="+cgo)
}

func listTestDeps(t *testing.T, tags, pkg string) []string {
	t.Helper()
	args := []string{"list", "-deps", "-test"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, pkg)
	return runGoList(t, args)
}

func envWithout(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

func runGoList(t *testing.T, args []string, extraEnv ...string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = moduleRoot(t)
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, ee.Stderr)
		}
		t.Fatal(err)
	}
	return splitNonEmpty(string(out))
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
