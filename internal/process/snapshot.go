// Package process answers whether a process is alive and what it is running.
// Snapshot, Alive, CommandLine, and Children are the four operations. Agent
// names come from the config registry so a new runtime is detected everywhere
// after one preset edit.
package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
)

// Proc is one process table row.
type Proc struct {
	PID     int
	PPID    int
	Name    string
	Args    string
	TTY     string
	Elapsed time.Duration
}

// Table is a point-in-time process snapshot.
type Table struct {
	byPID    map[int]Proc
	children map[int][]int
	order    []int
}

// Capture reads the process table once.
func Capture() (Table, error) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,tty=,etime=,args=").Output()
	if err != nil {
		return Table{}, fmt.Errorf("snapshot processes: %w", err)
	}
	return Parse(out), nil
}

// Parse builds a Table from `ps` text. It accepts the tmux tree format
// (`pid ppid comm...`) and the rich capture format (`pid ppid tty etime args...`).
func Parse(out []byte) Table {
	table := Table{
		byPID:    make(map[int]Proc),
		children: make(map[int][]int),
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid < 0 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid < 0 {
			continue
		}
		p := Proc{PID: pid, PPID: ppid}
		if rich, ok := parseRichTail(fields[2:]); ok {
			p.TTY = rich.tty
			p.Elapsed = rich.elapsed
			p.Args = rich.args
			p.Name = commandName(rich.args)
		} else {
			p.Args = strings.Join(fields[2:], " ")
			p.Name = p.Args
		}
		table.byPID[pid] = p
		table.children[ppid] = append(table.children[ppid], pid)
		table.order = append(table.order, pid)
	}
	return table
}

type richTail struct {
	tty     string
	elapsed time.Duration
	args    string
}

func parseRichTail(fields []string) (richTail, bool) {
	if len(fields) < 3 {
		return richTail{}, false
	}
	if !looksLikeTTY(fields[0]) {
		return richTail{}, false
	}
	sec, err := parseEtime(fields[1])
	if err != nil {
		return richTail{}, false
	}
	return richTail{
		tty:     fields[0],
		elapsed: time.Duration(sec) * time.Second,
		args:    strings.Join(fields[2:], " "),
	}, true
}

func looksLikeTTY(s string) bool {
	switch s {
	case "?", "??", "-":
		return true
	}
	return strings.HasPrefix(s, "pts/") ||
		strings.HasPrefix(s, "tty") ||
		strings.HasPrefix(s, "ttys") ||
		strings.HasPrefix(s, "console")
}

func parseEtime(etime string) (int, error) {
	var days, hours, minutes, seconds int
	if idx := strings.Index(etime, "-"); idx != -1 {
		d, err := strconv.Atoi(etime[:idx])
		if err != nil {
			return 0, err
		}
		days = d
		etime = etime[idx+1:]
	}
	parts := strings.Split(etime, ":")
	switch len(parts) {
	case 2:
		m, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, err
		}
		s, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, err
		}
		minutes, seconds = m, s
	case 3:
		h, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, err
		}
		m, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, err
		}
		s, err := strconv.Atoi(parts[2])
		if err != nil {
			return 0, err
		}
		hours, minutes, seconds = h, m, s
	default:
		return 0, os.ErrInvalid
	}
	return days*86400 + hours*3600 + minutes*60 + seconds, nil
}

func commandName(args string) string {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

// Lookup returns one process by PID.
func (t Table) Lookup(pid int) (Proc, bool) {
	p, ok := t.byPID[pid]
	return p, ok
}

// All returns every captured process in ps-output order.
func (t Table) All() []Proc {
	out := make([]Proc, 0, len(t.order))
	for _, pid := range t.order {
		out = append(out, t.byPID[pid])
	}
	return out
}

// Children returns the direct child PIDs of pid.
func (t Table) Children(pid int) []int {
	return append([]int(nil), t.children[pid]...)
}

// Descendants returns all descendant PIDs, deepest first.
func (t Table) Descendants(pid int) []int {
	seen := map[int]bool{pid: true}
	var result []int
	var walk func(int)
	walk = func(parent int) {
		for _, child := range t.children[parent] {
			if seen[child] {
				continue
			}
			seen[child] = true
			walk(child)
			result = append(result, child)
		}
	}
	walk(pid)
	return result
}

// Name returns the comm/args name for pid.
func (t Table) Name(pid int) string {
	return t.byPID[pid].Name
}

// CommandLine returns the argument vector for pid, falling back to Name.
func (t Table) CommandLine(pid int) string {
	p := t.byPID[pid]
	if p.Args != "" {
		return p.Args
	}
	return p.Name
}

// CommandLine looks up a live process argument vector.
func CommandLine(pid int) string {
	if pid <= 0 {
		return ""
	}
	if line := procCommandLine(pid); line != "" {
		return line
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// IsKnownAgent reports whether name (or its basename) is a registered agent process.
func IsKnownAgent(name string) bool {
	name = strings.ToLower(strings.TrimSpace(filepath.Base(name)))
	if name == "" {
		return false
	}
	for _, known := range config.AllProcessNames() {
		if strings.ToLower(known) == name {
			return true
		}
	}
	return false
}
