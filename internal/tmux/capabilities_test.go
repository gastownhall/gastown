package tmux

import "testing"

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in    string
		major int
		minor int
		ok    bool
	}{
		{in: "tmux 3.7b", major: 3, minor: 7, ok: true},
		{in: "tmux 3.7a", major: 3, minor: 7, ok: true},
		{in: "tmux 3.4", major: 3, minor: 4, ok: true},
		{in: "tmux 2.9a", major: 2, minor: 9, ok: true},
		{in: "not-tmux", ok: false},
		{in: "", ok: false},
	}
	for _, tt := range tests {
		got, ok := ParseVersion(tt.in)
		if ok != tt.ok {
			t.Fatalf("ParseVersion(%q) ok=%v want %v", tt.in, ok, tt.ok)
		}
		if !ok {
			continue
		}
		if got.Major != tt.major || got.Minor != tt.minor {
			t.Fatalf("ParseVersion(%q) = %+v, want %d.%d", tt.in, got, tt.major, tt.minor)
		}
	}
}

func TestCapabilitiesForVersion_LiteralCR(t *testing.T) {
	if !CapabilitiesForVersion(Version{Major: 3, Minor: 7}).LiteralCR {
		t.Fatal("tmux 3.7 must use a literal CR")
	}
	if !CapabilitiesForVersion(Version{Major: 3, Minor: 8}).LiteralCR {
		t.Fatal("tmux 3.8 must use a literal CR")
	}
	if CapabilitiesForVersion(Version{Major: 3, Minor: 4}).LiteralCR {
		t.Fatal("tmux 3.4 named Enter is sufficient")
	}
	if !CapabilitiesForVersion(Version{}).LiteralCR {
		t.Fatal("unknown version must fail safe to literal CR")
	}
}

func TestSubmitEnterArgs(t *testing.T) {
	got := submitEnterArgs(Capabilities{LiteralCR: true}, "gt-crew")
	want := []string{"send-keys", "-t", "gt-crew", "-l", "\r"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}

	got = submitEnterArgs(Capabilities{}, "pane")
	if got[len(got)-1] != "Enter" {
		t.Fatalf("named Enter path = %v", got)
	}
}

func TestSetCapabilities_IsPerInstance(t *testing.T) {
	withCR := NewTmuxWithSocketAndBinary("sock-a", "tmux")
	withoutCR := NewTmuxWithSocketAndBinary("sock-b", "tmux")
	withCR.SetCapabilities(Capabilities{LiteralCR: true})
	withoutCR.SetCapabilities(Capabilities{})

	if !withCR.capabilities().LiteralCR {
		t.Fatal("override LiteralCR=true did not stick on this Tmux")
	}
	if withoutCR.capabilities().LiteralCR {
		t.Fatal("sibling Tmux inherited LiteralCR from another instance")
	}
}
