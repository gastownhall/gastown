// This file holds the machine's offline event queue (spec §7.4): an
// append-only JSONL outbox of Events with an acknowledged cursor next to it,
// and the applied-request set used to make requests idempotent by key.
package agentlink

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Queue file names under <townRoot>/.runtime/agentlink/.
const (
	outboxName       = "outbox.jsonl"
	outboxCursorName = "outbox.cursor"
	outboxLockName   = "outbox.lock"
	appliedName      = "applied.jsonl"

	// compactMinSize: the outbox is compacted only when it is larger than
	// this and the acknowledged prefix is more than half of it.
	compactMinSize = 1 << 20

	// DefaultAppliedKeys is how many applied request keys are remembered.
	DefaultAppliedKeys = 10000
)

// AgentlinkDir returns <townRoot>/.runtime/agentlink.
func AgentlinkDir(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "agentlink")
}

// Queue is the machine's outbox: <townRoot>/.runtime/agentlink/outbox.jsonl
// holds one JSON Event per line in strictly ascending ID order, and
// outbox.cursor holds the ID of the last event the server acknowledged.
//
// Several processes append concurrently (the daemon, and CLI commands such as
// gtb advance through the workflow Observer). Every operation takes an
// exclusive flock on outbox.lock, so lines never interleave.
//
// ID assignment: Append always stamps the event with a fresh ID while holding
// the lock, greater than both the last queued ID and the acked cursor. IDs
// generated in different processes are not monotonic relative to each other,
// so a caller-supplied ID is discarded; this keeps the file sorted and every
// pending ID above the cursor, which is what POST /events requires.
type Queue struct {
	dir    string
	path   string
	cursor string
	lock   string
	now    func() time.Time
}

// OpenQueue returns the queue of the town at townRoot. Files are created on
// first use (directory 0700, files 0600).
func OpenQueue(townRoot string) *Queue {
	dir := AgentlinkDir(townRoot)
	return &Queue{
		dir:    dir,
		path:   filepath.Join(dir, outboxName),
		cursor: filepath.Join(dir, outboxCursorName),
		lock:   filepath.Join(dir, outboxLockName),
		now:    time.Now,
	}
}

func (q *Queue) withLock(fn func() error) error {
	if err := ensureDir(q.dir); err != nil {
		return err
	}
	unlock, err := flockAcquire(q.lock)
	if err != nil {
		return fmt.Errorf("agentlink: queue: %w", err)
	}
	defer unlock()
	return fn()
}

func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("agentlink: create %s: %w", dir, err)
	}
	return nil
}

// Append stamps e with a new ID (see Queue), sets At when it is zero, and
// appends it as one line with O_APPEND, then fsyncs.
func (q *Queue) Append(e Event) error {
	return q.withLock(func() error { return q.appendLocked(e) })
}

func (q *Queue) appendLocked(e Event) error {
	cur, err := q.readCursor()
	if err != nil {
		return err
	}
	last, endsNL, err := q.lastID()
	if err != nil {
		return err
	}
	floor := max(cur, last)
	if e.At.IsZero() {
		e.At = q.now().UTC()
	}
	e.ID = NewEventID(q.now())
	if e.ID <= floor {
		e.ID = nextULID(floor)
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("agentlink: encode event: %w", err)
	}
	if !endsNL {
		// A torn last line (crash mid-write) is terminated so it stays a
		// separate, skipped line instead of corrupting this one.
		line = append([]byte{'\n'}, line...)
	}
	line = append(line, '\n')
	f, err := os.OpenFile(q.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("agentlink: open outbox: %w", err)
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		return fmt.Errorf("agentlink: append outbox: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("agentlink: sync outbox: %w", err)
	}
	return f.Close()
}

// Pending returns up to max queued events whose ID is after the acked
// cursor, in ID order. Undecodable (torn) lines are skipped.
func (q *Queue) Pending(max int) ([]Event, error) {
	var out []Event
	err := q.withLock(func() error {
		cur, err := q.readCursor()
		if err != nil {
			return err
		}
		out, _, err = q.scan(cur, max)
		return err
	})
	return out, err
}

// Cursor returns the acked cursor ("" before the first ack).
func (q *Queue) Cursor() (string, error) {
	var c string
	err := q.withLock(func() (err error) { c, err = q.readCursor(); return err })
	return c, err
}

// LastID returns the greatest ID the queue knows: the last queued event's ID
// or, when nothing is queued after it, the acked cursor.
func (q *Queue) LastID() (string, error) {
	var id string
	err := q.withLock(func() error {
		cur, err := q.readCursor()
		if err != nil {
			return err
		}
		last, _, err := q.lastID()
		id = max(cur, last)
		return err
	})
	return id, err
}

// Ack records lastID as acknowledged (via a temp file and rename). It never
// moves the cursor backwards. Afterwards the outbox is compacted when it is
// larger than 1 MiB and the acked prefix is more than half of it.
func (q *Queue) Ack(lastID string) error {
	return q.withLock(func() error {
		cur, err := q.readCursor()
		if err != nil {
			return err
		}
		if lastID <= cur {
			return nil
		}
		if err := writeFileAtomic(q.cursor, []byte(lastID+"\n")); err != nil {
			return fmt.Errorf("agentlink: write cursor: %w", err)
		}
		return q.maybeCompact(lastID)
	})
}

func (q *Queue) readCursor() (string, error) {
	b, err := os.ReadFile(q.cursor)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("agentlink: read cursor: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// readLine reads one line of any length (without the newline). eof is true
// when the line was not newline-terminated.
func readLine(r *bufio.Reader) (line []byte, eof bool, err error) {
	line, err = r.ReadBytes('\n')
	if errors.Is(err, io.EOF) {
		return line, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return line[:len(line)-1], false, nil
}

// scan reads the outbox and returns up to max events with ID > after, plus
// the byte offset of the first line holding such an event (the size of the
// acked prefix; the file size when there is none).
func (q *Queue) scan(after string, max int) ([]Event, int64, error) {
	f, err := os.Open(q.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("agentlink: open outbox: %w", err)
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	var out []Event
	var off int64
	first := int64(-1)
	for {
		line, eof, err := readLine(r)
		if err != nil {
			return nil, 0, fmt.Errorf("agentlink: read outbox: %w", err)
		}
		start := off
		off += int64(len(line))
		if !eof {
			off++
		}
		if len(bytes.TrimSpace(line)) > 0 {
			var e Event
			if json.Unmarshal(line, &e) == nil && e.ID > after {
				if first < 0 {
					first = start
				}
				if len(out) < max {
					out = append(out, e)
				} else if first >= 0 {
					break
				}
			}
		}
		if eof {
			break
		}
	}
	if first < 0 {
		first = off
	}
	return out, first, nil
}

// lastID returns the ID of the last decodable line and whether the file
// ends with a newline (true for a missing or empty file). It reads the file
// backwards in chunks, so it is cheap for large outboxes.
func (q *Queue) lastID() (string, bool, error) {
	f, err := os.Open(q.path)
	if errors.Is(err, os.ErrNotExist) {
		return "", true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("agentlink: open outbox: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	size := fi.Size()
	if size == 0 {
		return "", true, nil
	}
	const chunk = 64 << 10
	var tail []byte // bytes from pos to end of file
	pos := size
	endsNL := false
	for pos > 0 {
		n := int64(chunk)
		if pos < n {
			n = pos
		}
		pos -= n
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, pos); err != nil && !errors.Is(err, io.EOF) {
			return "", false, err
		}
		tail = append(buf, tail...)
		if pos+n == size {
			endsNL = tail[len(tail)-1] == '\n'
		}
		// Try complete lines from the end; the first line in tail is only
		// complete when we have reached the start of the file.
		lines := bytes.Split(tail, []byte{'\n'})
		startIdx := 1
		if pos == 0 {
			startIdx = 0
		}
		for i := len(lines) - 1; i >= startIdx; i-- {
			var e Event
			if l := bytes.TrimSpace(lines[i]); len(l) > 0 && json.Unmarshal(l, &e) == nil && e.ID != "" {
				return e.ID, endsNL, nil
			}
		}
		if len(lines) > 1 {
			tail = lines[0] // keep only the possibly incomplete first line
		}
	}
	return "", endsNL, nil
}

// maybeCompact rewrites the outbox with only the lines after cursor when the
// file is larger than compactMinSize and the acked prefix is more than half
// of it. The rewrite goes through a temp file and a rename.
func (q *Queue) maybeCompact(cursor string) error {
	fi, err := os.Stat(q.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Size() <= compactMinSize {
		return nil
	}
	_, acked, err := q.scan(cursor, 0)
	if err != nil {
		return err
	}
	if acked*2 <= fi.Size() {
		return nil
	}
	src, err := os.Open(q.path)
	if err != nil {
		return err
	}
	defer src.Close()
	if _, err := src.Seek(acked, io.SeekStart); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(q.dir, "."+outboxName+".tmp-*")
	if err != nil {
		return fmt.Errorf("agentlink: compact: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		return fmt.Errorf("agentlink: compact: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), q.path); err != nil {
		return fmt.Errorf("agentlink: compact: %w", err)
	}
	ok = true
	return nil
}

// nextULID returns the ULID string that follows id (id + 1 in Crockford
// base32). It is used to stamp an event above a floor ID that came from a
// faster clock or another process.
func nextULID(id string) string {
	b := []byte(id)
	for i := len(b) - 1; i >= 0; i-- {
		j := strings.IndexByte(crockford, b[i])
		if j < 0 {
			break // not a ULID; fall through to the suffix below
		}
		if j < len(crockford)-1 {
			b[i] = crockford[j+1]
			return string(b)
		}
		b[i] = crockford[0]
	}
	// Overflow (or a non-ULID floor): a longer string sorts after id.
	return id + "0"
}

// AppliedSet remembers the results of the last N requests the machine
// applied, keyed by request key, in <townRoot>/.runtime/agentlink/applied.jsonl
// (one {"key","result"} line per entry, mode 0600). A re-delivered key gets
// its stored result re-posted instead of being applied again. Only the daemon
// loop uses it, so it is guarded by a mutex rather than a file lock.
type AppliedSet struct {
	mu    sync.Mutex
	path  string
	max   int
	order []string
	byKey map[string]Result
	lines int // lines in the file; rewritten when it exceeds 2*max
}

type appliedLine struct {
	Key    string `json:"key"`
	Result Result `json:"result"`
}

// OpenAppliedSet loads the applied set of the town at townRoot, keeping the
// last max keys (DefaultAppliedKeys when max <= 0).
func OpenAppliedSet(townRoot string, max int) (*AppliedSet, error) {
	if max <= 0 {
		max = DefaultAppliedKeys
	}
	dir := AgentlinkDir(townRoot)
	if err := ensureDir(dir); err != nil {
		return nil, err
	}
	s := &AppliedSet{path: filepath.Join(dir, appliedName), max: max, byKey: map[string]Result{}}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agentlink: open applied set: %w", err)
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		line, eof, err := readLine(r)
		if err != nil {
			return nil, fmt.Errorf("agentlink: read applied set: %w", err)
		}
		if len(bytes.TrimSpace(line)) > 0 {
			s.lines++
			var al appliedLine
			if json.Unmarshal(line, &al) == nil && al.Key != "" {
				s.remember(al.Key, al.Result)
			}
		}
		if eof {
			break
		}
	}
	return s, nil
}

func (s *AppliedSet) remember(key string, r Result) {
	if _, ok := s.byKey[key]; !ok {
		s.order = append(s.order, key)
	}
	s.byKey[key] = r
	for len(s.order) > s.max {
		delete(s.byKey, s.order[0])
		s.order = s.order[1:]
	}
}

// Get returns the stored result for key.
func (s *AppliedSet) Get(key string) (Result, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byKey[key]
	return r, ok
}

// Len returns how many keys are remembered.
func (s *AppliedSet) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.order)
}

// Put records r under r.Key, appending one fsynced line. When the file holds
// more than twice max lines it is rewritten with the remembered keys only.
func (s *AppliedSet) Put(r Result) error {
	if r.Key == "" {
		return errors.New("agentlink: applied set: empty key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remember(r.Key, r)
	line, err := json.Marshal(appliedLine{Key: r.Key, Result: r})
	if err != nil {
		return err
	}
	if s.lines+1 > 2*s.max {
		return s.rewrite()
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("agentlink: open applied set: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("agentlink: write applied set: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	s.lines++
	return f.Close()
}

func (s *AppliedSet) rewrite() error {
	var buf bytes.Buffer
	for _, k := range s.order {
		line, err := json.Marshal(appliedLine{Key: k, Result: s.byKey[k]})
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := writeFileAtomic(s.path, buf.Bytes()); err != nil {
		return fmt.Errorf("agentlink: rewrite applied set: %w", err)
	}
	s.lines = len(s.order)
	return nil
}
