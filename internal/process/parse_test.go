package process

import (
	"reflect"
	"testing"
	"time"
)

func TestParse_BuildsChildrenAndNames(t *testing.T) {
	table := Parse([]byte(`
1 0 bash
2 1 /usr/local/bin/node
3 2 /Users/peter/bin/claude
4 1 tmux: client
bad header row
8 bad nope
`))

	got := table.Children(1)
	want := []int{2, 4}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Children(1) = %v, want %v", got, want)
	}
	if table.Name(2) != "/usr/local/bin/node" {
		t.Fatalf("Name(2) = %q", table.Name(2))
	}
	if table.CommandLine(4) != "tmux: client" {
		t.Fatalf("CommandLine(4) = %q", table.CommandLine(4))
	}
}

func TestParse_DescendantsDeepestFirst(t *testing.T) {
	table := Parse([]byte(`
1 0 bash
2 1 /usr/local/bin/node
3 2 /Users/peter/bin/claude
4 2 helper
5 1 tmux: client
`))
	got := table.Descendants(1)
	want := []int{3, 4, 2, 5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Descendants(1) = %v, want %v", got, want)
	}
	if got := table.Descendants(42); len(got) != 0 {
		t.Fatalf("missing root = %v", got)
	}
}

func TestParse_RichTTYFormat(t *testing.T) {
	table := Parse([]byte("10 1 ? 01:23 claude --dangerously-skip-permissions\n"))
	p, ok := table.Lookup(10)
	if !ok {
		t.Fatal("missing pid 10")
	}
	if p.PPID != 1 || p.TTY != "?" || p.Elapsed != 83*time.Second || p.Name != "claude" {
		t.Fatalf("proc = %+v", p)
	}
	if p.Args != "claude --dangerously-skip-permissions" {
		t.Fatalf("args = %q", p.Args)
	}
}

func TestIsKnownAgent_UsesRegistry(t *testing.T) {
	if !IsKnownAgent("claude") {
		t.Fatal("claude should be a known agent")
	}
	if !IsKnownAgent("/usr/local/bin/codex") {
		t.Fatal("codex basename should be a known agent")
	}
	if IsKnownAgent("make") {
		t.Fatal("make is not a known agent")
	}
}
