package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jonbaldie/gastown/internal/constants"
)

const (
	dirWorker   = "worker"
	fileSocket  = "worker.sock"
	filePort    = "worker.port"
	fileEvents  = "events.jsonl"
	fileCosts   = "costs.jsonl"
	fileRuns    = "runs.json"
	fileQueue   = "queue.json"
	dirSessions = "sessions"
	socketMode  = 0o600
	runtimeMode = 0o700
	fileMode    = 0o600
)

// Store persists runs, lifecycle events, and costs under the town runtime dir.
type Store struct {
	townRoot string
	mu       sync.Mutex
}

func newStore(townRoot string) *Store {
	return &Store{townRoot: townRoot}
}

func (s *Store) root() string {
	return filepath.Join(s.townRoot, constants.DirRuntime, dirWorker)
}

func SocketPath(townRoot string) string {
	return filepath.Join(townRoot, constants.DirRuntime, dirWorker, fileSocket)
}

func PortPath(townRoot string) string {
	return filepath.Join(townRoot, constants.DirRuntime, dirWorker, filePort)
}

func SessionPortPath(townRoot, sessionID string) string {
	safe := strings.ReplaceAll(sessionID, "/", "_")
	return filepath.Join(townRoot, constants.DirRuntime, dirWorker, dirSessions, safe+".port")
}

func (s *Store) eventsPath() string { return filepath.Join(s.root(), fileEvents) }
func (s *Store) costsPath() string  { return filepath.Join(s.root(), fileCosts) }
func (s *Store) runsPath() string   { return filepath.Join(s.root(), fileRuns) }
func (s *Store) queuePath() string  { return filepath.Join(s.root(), fileQueue) }

func (s *Store) ensure() error {
	if err := os.MkdirAll(filepath.Join(s.root(), dirSessions), runtimeMode); err != nil {
		return fmt.Errorf("creating worker runtime dir: %w", err)
	}
	return nil
}

type runIndex struct {
	Runs map[string]*Run `json:"runs"`
}

func (s *Store) loadIndex() (*runIndex, error) {
	idx := &runIndex{Runs: map[string]*Run{}}
	data, err := os.ReadFile(s.runsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return idx, nil
		}
		return nil, fmt.Errorf("reading worker runs: %w", err)
	}
	if len(data) == 0 {
		return idx, nil
	}
	if err := json.Unmarshal(data, idx); err != nil {
		return nil, fmt.Errorf("parsing worker runs: %w", err)
	}
	if idx.Runs == nil {
		idx.Runs = map[string]*Run{}
	}
	return idx, nil
}

func (s *Store) saveIndex(idx *runIndex) error {
	if err := s.ensure(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling worker runs: %w", err)
	}
	if err := writeFileAtomic(s.runsPath(), data); err != nil {
		return fmt.Errorf("writing worker runs: %w", err)
	}
	return nil
}

// writeFileAtomic replaces path with data, staging it in a uniquely named file
// in the same directory. A fixed staging name would collide: every accessor
// builds its own Store, so the mutex guards nothing between callers, and the
// daemon writes the same files from another process. Concurrent writers would
// then share one staging path and the loser's rename would find it already
// consumed by the winner.
func writeFileAtomic(path string, data []byte) error {
	staged, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating staging file: %w", err)
	}
	name := staged.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := staged.Write(data); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, fileMode); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (s *Store) putRun(run *Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putRunLocked(run)
}

func (s *Store) putRunLocked(run *Run) error {
	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	cp := *run
	idx.Runs[run.RunID] = &cp
	return s.saveIndex(idx)
}

func (s *Store) getRun(runID string) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	run, ok := idx.Runs[runID]
	if !ok {
		return nil, ErrRunNotFound
	}
	cp := *run
	return &cp, nil
}

func (s *Store) getRunBySession(sessionID string) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	var found *Run
	for _, run := range idx.Runs {
		if run.SessionID == sessionID && run.State.live() {
			cp := *run
			found = &cp
			break
		}
	}
	if found == nil {
		return nil, ErrRunNotFound
	}
	return found, nil
}

func (s *Store) latestRunForBead(beadID string) (*Run, error) {
	if beadID == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	var found *Run
	for _, run := range idx.Runs {
		if run.BeadID != beadID {
			continue
		}
		if found == nil || run.UpdatedAt.After(found.UpdatedAt) {
			cp := *run
			found = &cp
		}
	}
	return found, nil
}

func (s *Store) latestRunForSession(sessionID string) (*Run, error) {
	if sessionID == "" {
		return nil, ErrRunNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	var found *Run
	for _, run := range idx.Runs {
		if run.SessionID != sessionID {
			continue
		}
		if found == nil || run.UpdatedAt.After(found.UpdatedAt) {
			cp := *run
			found = &cp
		}
	}
	if found == nil {
		return nil, ErrRunNotFound
	}
	return found, nil
}

func (s *Store) liveRunForBead(beadID string) (*Run, error) {
	if beadID == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	for _, run := range idx.Runs {
		if run.BeadID == beadID && run.State.live() {
			cp := *run
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *Store) listRuns() ([]*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	out := make([]*Run, 0, len(idx.Runs))
	for _, run := range idx.Runs {
		cp := *run
		out = append(out, &cp)
	}
	return out, nil
}

func (s *Store) appendEvent(ev Event) error {
	if err := s.ensure(); err != nil {
		return err
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshaling worker event: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.eventsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, fileMode)
	if err != nil {
		return fmt.Errorf("opening worker events: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing worker event: %w", err)
	}
	return nil
}

func (s *Store) readEvents() ([]Event, error) {
	data, err := os.ReadFile(s.eventsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading worker events: %w", err)
	}
	var out []Event
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

func (s *Store) appendCost(rec CostRecord) error {
	if err := s.ensure(); err != nil {
		return err
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshaling worker cost: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.costsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, fileMode)
	if err != nil {
		return fmt.Errorf("opening worker costs: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing worker cost: %w", err)
	}
	return nil
}

func (s *Store) readCosts() ([]CostRecord, error) {
	data, err := os.ReadFile(s.costsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading worker costs: %w", err)
	}
	var out []CostRecord
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec CostRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

type queueFile struct {
	Items []QueuedPrompt `json:"items"`
}

func (s *Store) loadQueue() (*queueFile, error) {
	q := &queueFile{}
	data, err := os.ReadFile(s.queuePath())
	if err != nil {
		if os.IsNotExist(err) {
			return q, nil
		}
		return nil, fmt.Errorf("reading worker queue: %w", err)
	}
	if len(data) == 0 {
		return q, nil
	}
	if err := json.Unmarshal(data, q); err != nil {
		return nil, fmt.Errorf("parsing worker queue: %w", err)
	}
	return q, nil
}

func (s *Store) saveQueue(q *queueFile) error {
	if err := s.ensure(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling worker queue: %w", err)
	}
	if err := writeFileAtomic(s.queuePath(), data); err != nil {
		return fmt.Errorf("writing worker queue: %w", err)
	}
	return nil
}

func (s *Store) enqueue(item QueuedPrompt) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := s.loadQueue()
	if err != nil {
		return 0, err
	}
	q.Items = append(q.Items, item)
	if err := s.saveQueue(q); err != nil {
		return 0, err
	}
	return len(q.Items), nil
}

func (s *Store) pendingFor(runID string) []QueuedPrompt {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := s.loadQueue()
	if err != nil {
		return nil
	}
	var out []QueuedPrompt
	now := time.Now()
	for _, item := range q.Items {
		if item.Prompt.RunID != runID {
			continue
		}
		if !item.ExpiresAt.IsZero() && now.After(item.ExpiresAt) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (s *Store) drainDue(runID string) ([]QueuedPrompt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := s.loadQueue()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var due, keep []QueuedPrompt
	for _, item := range q.Items {
		if item.Prompt.RunID != runID {
			keep = append(keep, item)
			continue
		}
		if !item.ExpiresAt.IsZero() && now.After(item.ExpiresAt) {
			continue
		}
		due = append(due, item)
	}
	q.Items = keep
	if err := s.saveQueue(q); err != nil {
		return nil, err
	}
	return due, nil
}

func (s *Store) expireStale(now time.Time) ([]QueuedPrompt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := s.loadQueue()
	if err != nil {
		return nil, err
	}
	var expired, keep []QueuedPrompt
	for _, item := range q.Items {
		if !item.ExpiresAt.IsZero() && now.After(item.ExpiresAt) {
			item.Reason = "expired"
			expired = append(expired, item)
			continue
		}
		keep = append(keep, item)
	}
	q.Items = keep
	if err := s.saveQueue(q); err != nil {
		return nil, err
	}
	return expired, nil
}
