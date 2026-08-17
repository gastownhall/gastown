package worker

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/process"
)

// FindServePIDs returns PIDs of `gt worker serve --town <townRoot>` processes
// for this town only. Other towns' worker servers are left alone.
func FindServePIDs(townRoot string) []int {
	if townRoot == "" {
		return nil
	}
	table, err := process.Capture()
	if err != nil {
		return nil
	}
	townAbs := canonicalTownRoot(townRoot)
	self := os.Getpid()
	seen := map[int]bool{}
	var pids []int
	for _, p := range table.All() {
		if p.PID == self || p.PID <= 0 || seen[p.PID] || !process.Alive(p.PID) {
			continue
		}
		line := p.Args
		if line == "" {
			line = process.CommandLine(p.PID)
		}
		if !isTownWorkerServeArgs(line, townRoot) && !isTownWorkerServeArgs(line, townAbs) {
			fallback := process.CommandLine(p.PID)
			if !isTownWorkerServeArgs(fallback, townRoot) && !isTownWorkerServeArgs(fallback, townAbs) {
				continue
			}
		}
		seen[p.PID] = true
		pids = append(pids, p.PID)
	}
	return pids
}

func canonicalTownRoot(townRoot string) string {
	abs, err := filepath.Abs(townRoot)
	if err != nil {
		return filepath.Clean(townRoot)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// StopServe terminates this town's `gt worker serve` processes and removes
// stale socket/port files. It does not signal processes for other towns.
func StopServe(townRoot string) int {
	pids := FindServePIDs(townRoot)
	for _, pid := range pids {
		stopServePID(pid)
	}
	_ = os.Remove(SocketPath(townRoot))
	_ = os.Remove(PortPath(townRoot))
	return len(pids)
}

func isTownWorkerServeArgs(args, townRoot string) bool {
	if args == "" || townRoot == "" {
		return false
	}
	fields := strings.Fields(args)
	sawWorker := false
	sawServe := false
	for i, field := range fields {
		base := filepath.Base(field)
		switch {
		case field == "worker" || base == "worker":
			sawWorker = true
		case sawWorker && (field == "serve" || base == "serve"):
			sawServe = true
		case field == "--town" && i+1 < len(fields) && sameTownPath(fields[i+1], townRoot):
			return sawWorker && sawServe
		case strings.HasPrefix(field, "--town=") && sameTownPath(strings.TrimPrefix(field, "--town="), townRoot):
			return sawWorker && sawServe
		}
	}
	return false
}

func sameTownPath(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	if got == want {
		return true
	}
	gotAbs, gotErr := filepath.Abs(got)
	wantAbs, wantErr := filepath.Abs(want)
	if gotErr != nil || wantErr != nil {
		return false
	}
	if gotAbs == wantAbs {
		return true
	}
	gotRes, gotResErr := filepath.EvalSymlinks(gotAbs)
	wantRes, wantResErr := filepath.EvalSymlinks(wantAbs)
	return gotResErr == nil && wantResErr == nil && gotRes == wantRes
}

func stopServePID(pid int) {
	_ = terminateServePID(pid)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !process.Alive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = killServePID(pid)
	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !process.Alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
