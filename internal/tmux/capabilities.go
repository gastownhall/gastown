package tmux

import (
	"os/exec"
	"strconv"
	"strings"
)

// Version is a parsed tmux -V result.
type Version struct {
	Major int
	Minor int
}

// Capabilities is one field per tmux quirk. Send paths read this value;
// no other code needs to know the version string.
type Capabilities struct {
	// LiteralCR is true when named-key Enter/C-m/KPEnter do not submit
	// (tmux 3.7 and later, including 3.7b).
	LiteralCR bool
}

func (t *Tmux) capabilities() Capabilities {
	if t == nil {
		return CapabilitiesForVersion(Version{})
	}
	if t.capsOverride != nil {
		return *t.capsOverride
	}
	t.capsOnce.Do(func() {
		t.caps = probeCapabilities(t.binary)
	})
	return t.caps
}

// ParseVersion reads a `tmux -V` line such as "tmux 3.7b".
func ParseVersion(output string) (Version, bool) {
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) < 2 {
		return Version{}, false
	}
	raw := fields[len(fields)-1]
	raw = strings.TrimLeft(raw, "vV")
	end := 0
	for end < len(raw) && (raw[end] == '.' || (raw[end] >= '0' && raw[end] <= '9')) {
		end++
	}
	raw = raw[:end]
	parts := strings.Split(raw, ".")
	if len(parts) < 2 {
		return Version{}, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return Version{}, false
	}
	return Version{Major: major, Minor: minor}, true
}

// CapabilitiesForVersion maps a version to quirks. An unknown version
// fails safe to literal CR so a Nudge still submits.
func CapabilitiesForVersion(v Version) Capabilities {
	if v.Major == 0 && v.Minor == 0 {
		return Capabilities{LiteralCR: true}
	}
	if v.Major > 3 || (v.Major == 3 && v.Minor >= 7) {
		return Capabilities{LiteralCR: true}
	}
	return Capabilities{}
}

func probeCapabilities(binary string) Capabilities {
	if binary == "" {
		binary = "tmux"
	}
	cmd := exec.Command(binary, "-V")
	hideConsoleWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return CapabilitiesForVersion(Version{})
	}
	v, ok := ParseVersion(string(out))
	if !ok {
		return CapabilitiesForVersion(Version{})
	}
	return CapabilitiesForVersion(v)
}

// SetCapabilities is the test seam: tests set the version-derived quirks
// without a real tmux binary.
func (t *Tmux) SetCapabilities(c Capabilities) {
	cp := c
	t.capsOverride = &cp
}

func submitEnterArgs(caps Capabilities, target string) []string {
	if caps.LiteralCR {
		return []string{"send-keys", "-t", target, "-l", "\r"}
	}
	return []string{"send-keys", "-t", target, "Enter"}
}

func (t *Tmux) submitEnter(target string) error {
	_, err := t.run(submitEnterArgs(t.capabilities(), target)...)
	return err
}

func (t *Tmux) sendCommandAndSubmit(target, command string) error {
	if _, err := t.run("send-keys", "-t", target, command); err != nil {
		return err
	}
	return t.submitEnter(target)
}
