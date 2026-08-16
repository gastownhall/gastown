package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/tmux"
)

// TestInitRegistry_SocketFromTownName verifies GT_TMUX_SOCKET socket selection:
//   - unset / "default" / "auto" → per-town socket derived from town directory path
//   - explicit value              → that value verbatim
func TestInitRegistry_SocketFromTownName(t *testing.T) {
	origTMUX := os.Getenv("TMUX")
	origSocket := tmux.GetDefaultSocket()
	origGTSocket := os.Getenv("GT_TMUX_SOCKET")
	t.Cleanup(func() {
		os.Setenv("TMUX", origTMUX)
		os.Setenv("GT_TMUX_SOCKET", origGTSocket)
		tmux.SetDefaultSocket(origSocket)
	})

	tests := []struct {
		name         string
		gtTmuxSocket string // GT_TMUX_SOCKET value ("" = unset)
		tmuxEnv      string // $TMUX value
		townDir      string // basename of the town root directory
	}{
		{
			name:         "unset → derived from town path",
			gtTmuxSocket: "",
			townDir:      "gt",
		},
		{
			name:         "explicit default → derived from town path",
			gtTmuxSocket: "default",
			townDir:      "gt",
		},
		{
			name:         "auto → derived from town path",
			gtTmuxSocket: "auto",
			townDir:      "gt",
		},
		{
			name:         "auto → sanitized town name with spaces",
			gtTmuxSocket: "auto",
			townDir:      "My Town",
		},
		{
			name:         "auto → sanitized town name with caps",
			gtTmuxSocket: "auto",
			townDir:      "GasTown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmux.SetDefaultSocket("")

			if tt.gtTmuxSocket != "" {
				os.Setenv("GT_TMUX_SOCKET", tt.gtTmuxSocket)
			} else {
				os.Unsetenv("GT_TMUX_SOCKET")
			}
			if tt.tmuxEnv != "" {
				os.Setenv("TMUX", tt.tmuxEnv)
			} else {
				os.Unsetenv("TMUX")
			}

			townRoot := filepath.Join(t.TempDir(), tt.townDir)
			os.MkdirAll(townRoot, 0o755)
			_ = InitRegistry(townRoot)

			got := tmux.GetDefaultSocket()
			want := townSocketName(townRoot)
			if got != want {
				t.Errorf("after InitRegistry(%q) with GT_TMUX_SOCKET=%q:\n  socket = %q, want %q",
					townRoot, tt.gtTmuxSocket, got, want)
			}

			tmux.SetDefaultSocket("")
			_ = InitRegistry(townRoot)
			got2 := tmux.GetDefaultSocket()
			if got != got2 {
				t.Errorf("socket not deterministic: first=%q, second=%q", got, got2)
			}
		})
	}

	// Explicit custom socket name bypasses path hashing
	t.Run("explicit custom socket name", func(t *testing.T) {
		tmux.SetDefaultSocket("")
		os.Setenv("GT_TMUX_SOCKET", "mysocket")
		townRoot := filepath.Join(t.TempDir(), "gt")
		os.MkdirAll(townRoot, 0o755)
		_ = InitRegistry(townRoot)
		got := tmux.GetDefaultSocket()
		if got != "mysocket" {
			t.Errorf("explicit socket: got %q, want %q", got, "mysocket")
		}
	})

	// Same basename, different parent paths → different sockets
	t.Run("same basename different paths get unique sockets", func(t *testing.T) {
		tmux.SetDefaultSocket("")
		os.Unsetenv("GT_TMUX_SOCKET")

		tmpDir := t.TempDir()
		townA := filepath.Join(tmpDir, "a", "gt")
		townB := filepath.Join(tmpDir, "b", "gt")
		os.MkdirAll(townA, 0o755)
		os.MkdirAll(townB, 0o755)

		_ = InitRegistry(townA)
		socketA := tmux.GetDefaultSocket()

		tmux.SetDefaultSocket("")
		_ = InitRegistry(townB)
		socketB := tmux.GetDefaultSocket()

		if socketA == "" || socketB == "" {
			t.Errorf("sockets should be non-empty: A=%q, B=%q", socketA, socketB)
		}
		if socketA == socketB {
			t.Errorf("different town paths should get different sockets: A=%q, B=%q", socketA, socketB)
		}
		if !strings.HasPrefix(socketA, "gt-") || !strings.HasPrefix(socketB, "gt-") {
			t.Errorf("sockets should start with 'gt-': A=%q, B=%q", socketA, socketB)
		}
	})
}

func TestInitRegistry_SocketFormat(t *testing.T) {
	origSocket := tmux.GetDefaultSocket()
	origGTSocket := os.Getenv("GT_TMUX_SOCKET")
	t.Cleanup(func() {
		os.Setenv("GT_TMUX_SOCKET", origGTSocket)
		tmux.SetDefaultSocket(origSocket)
	})

	os.Unsetenv("GT_TMUX_SOCKET")
	tmux.SetDefaultSocket("")

	townRoot := filepath.Join(t.TempDir(), "myproject")
	os.MkdirAll(townRoot, 0o755)
	_ = InitRegistry(townRoot)

	got := tmux.GetDefaultSocket()

	if !strings.HasPrefix(got, "myproject-") {
		t.Fatalf("socket %q should start with 'myproject-'", got)
	}
	hash := strings.TrimPrefix(got, "myproject-")
	if len(hash) != 6 {
		t.Errorf("socket hash suffix %q should be 6 hex chars, got %d", hash, len(hash))
	}
	for _, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("socket hash suffix %q contains non-hex char %c", hash, c)
			break
		}
	}
}

func TestSanitizeTownName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"mytown", "mytown"},
		{"MyTown", "mytown"},
		{"my town", "my-town"},
		{"my_town!", "my-town"},
		{"  spaces  ", "spaces"},
		{"My-Town-123", "my-town-123"},
		{"café", "caf"},
		{"", "default"},
		{"!!!!", "default"},
		{"a/b/c", "a-b-c"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeTownName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeTownName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTownSocketName(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("includes basename and hash suffix", func(t *testing.T) {
		townRoot := filepath.Join(tmpDir, "gt")
		os.MkdirAll(townRoot, 0o755)
		got := townSocketName(townRoot)
		if !strings.HasPrefix(got, "gt-") {
			t.Errorf("townSocketName(%q) = %q, want prefix 'gt-'", townRoot, got)
		}
		// Should be "gt-" + 6 hex chars = 9 chars total
		parts := strings.SplitN(got, "-", 2)
		if len(parts) != 2 || len(parts[1]) != 6 {
			t.Errorf("townSocketName(%q) = %q, want 'gt-XXXXXX' format", townRoot, got)
		}
	})

	t.Run("deterministic for same path", func(t *testing.T) {
		townRoot := filepath.Join(tmpDir, "stable")
		os.MkdirAll(townRoot, 0o755)
		a := townSocketName(townRoot)
		b := townSocketName(townRoot)
		if a != b {
			t.Errorf("not deterministic: %q != %q", a, b)
		}
	})

	t.Run("different for same basename at different paths", func(t *testing.T) {
		pathA := filepath.Join(tmpDir, "a", "mytown")
		pathB := filepath.Join(tmpDir, "b", "mytown")
		os.MkdirAll(pathA, 0o755)
		os.MkdirAll(pathB, 0o755)
		socketA := townSocketName(pathA)
		socketB := townSocketName(pathB)
		if socketA == socketB {
			t.Errorf("same-basename dirs got same socket: %q", socketA)
		}
	})
}

func TestLegacySocketName(t *testing.T) {
	got := LegacySocketName("/Users/hal/gt")
	if got != "gt" {
		t.Errorf("LegacySocketName = %q, want %q", got, "gt")
	}
	got = LegacySocketName("/home/user/My Town")
	if got != "my-town" {
		t.Errorf("LegacySocketName = %q, want %q", got, "my-town")
	}
}
