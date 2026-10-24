package workflow

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
)

// ErrStepConflict is returned by Store.Transition when the item is no longer
// at the step the caller expected to leave (another actor advanced it first).
var ErrStepConflict = errors.New("workflow: step conflict")

// Approval records a verdict on an artifact at a given content hash.
// Verdict is one of approve, concerns, reject, request_changes. Role is
// "human" for approvals, otherwise a reviewer role. Channel is terminal or
// dashboard.
type Approval struct {
	ItemID, Artifact, SHA, Verdict, Role, Source, Channel, Actor string
	At                                                           time.Time
}

// TDDRun records one red/green (or refused) test run for a plan step.
// Cmd is the argv as a JSON array string (never a shell string). TestFiles
// are the test file paths (relative to the run directory) whose contents
// TestFilesSHA covers.
type TDDRun struct {
	ItemID, PlanStep, Phase, Cmd           string
	Exit                                   int
	TestFilesSHA, Tail, Source, Provenance string
	TestFiles                              []string
	At                                     time.Time
}

// CheckRun records one run of the item's check command set.
type CheckRun struct {
	ItemID, CmdSetSHA        string
	Passed                   bool
	Tail, Source, Provenance string
	At                       time.Time
}

// Transition records an item moving from one step to the next.
type Transition struct {
	ItemID   string
	From, To Step
	Actor    string
	At       time.Time
}

// Store is the append-only evidence store for gated workflow items.
type Store interface {
	AddApproval(Approval) error
	Approvals(itemID string) ([]Approval, error)
	AddTDD(TDDRun) error
	TDD(itemID string) ([]TDDRun, error)
	AddCheck(CheckRun) error
	Checks(itemID string) ([]CheckRun, error)
	// Transition atomically records itemID moving from -> to. The store does
	// not know the item's kind, so an item with no transitions yet accepts
	// the first transition for any from; afterwards from must equal the
	// latest transition's To, otherwise ErrStepConflict is returned and
	// nothing is recorded.
	Transition(itemID string, from, to Step, actor string) error
	Transitions(itemID string) ([]Transition, error)
}

func utc(t time.Time) time.Time {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC()
}

// ---- in-memory store ----

type memStore struct {
	mu          sync.Mutex
	approvals   []Approval
	tdd         []TDDRun
	checks      []CheckRun
	transitions []Transition
}

// NewMemStore returns an in-memory Store for unit tests.
func NewMemStore() Store { return &memStore{} }

func filter[T any](xs []T, keep func(T) bool) []T {
	var out []T
	for _, x := range xs {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}

func (m *memStore) AddApproval(a Approval) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a.At = utc(a.At)
	m.approvals = append(m.approvals, a)
	return nil
}

func (m *memStore) Approvals(id string) ([]Approval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return filter(m.approvals, func(a Approval) bool { return a.ItemID == id }), nil
}

func (m *memStore) AddTDD(r TDDRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.At = utc(r.At)
	r.TestFiles = append([]string(nil), r.TestFiles...)
	m.tdd = append(m.tdd, r)
	return nil
}

func (m *memStore) TDD(id string) ([]TDDRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := filter(m.tdd, func(r TDDRun) bool { return r.ItemID == id })
	for i := range out {
		out[i].TestFiles = append([]string(nil), out[i].TestFiles...)
	}
	return out, nil
}

func (m *memStore) AddCheck(c CheckRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.At = utc(c.At)
	m.checks = append(m.checks, c)
	return nil
}

func (m *memStore) Checks(id string) ([]CheckRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return filter(m.checks, func(c CheckRun) bool { return c.ItemID == id }), nil
}

func (m *memStore) Transition(id string, from, to Step, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.transitions) - 1; i >= 0; i-- {
		if m.transitions[i].ItemID == id {
			if m.transitions[i].To != from {
				return fmt.Errorf("%w: item %s is at %s, not %s", ErrStepConflict, id, m.transitions[i].To, from)
			}
			break
		}
	}
	m.transitions = append(m.transitions, Transition{ItemID: id, From: from, To: to, Actor: actor, At: utc(time.Time{})})
	return nil
}

func (m *memStore) Transitions(id string) ([]Transition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return filter(m.transitions, func(t Transition) bool { return t.ItemID == id }), nil
}

// ---- Dolt store ----

type doltStore struct{ db *sql.DB }

var doltSchema = []string{
	`CREATE TABLE IF NOT EXISTS gtb_approvals (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		item_id VARCHAR(255) NOT NULL,
		artifact VARCHAR(255) NOT NULL,
		sha VARCHAR(128) NOT NULL,
		verdict VARCHAR(32) NOT NULL,
		role VARCHAR(64) NOT NULL,
		source VARCHAR(255) NOT NULL,
		channel VARCHAR(32) NOT NULL,
		actor VARCHAR(255) NOT NULL,
		at DATETIME(6) NOT NULL,
		INDEX idx_gtb_approvals_item (item_id)
	)`,
	`CREATE TABLE IF NOT EXISTS gtb_tdd (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		item_id VARCHAR(255) NOT NULL,
		plan_step VARCHAR(255) NOT NULL,
		phase VARCHAR(32) NOT NULL,
		cmd TEXT NOT NULL,
		exit_code INT NOT NULL,
		test_files_sha VARCHAR(128) NOT NULL,
		test_files TEXT NOT NULL,
		tail TEXT NOT NULL,
		source VARCHAR(255) NOT NULL,
		provenance TEXT NOT NULL,
		at DATETIME(6) NOT NULL,
		INDEX idx_gtb_tdd_item (item_id)
	)`,
	`CREATE TABLE IF NOT EXISTS gtb_checks (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		item_id VARCHAR(255) NOT NULL,
		cmd_set_sha VARCHAR(128) NOT NULL,
		passed BOOLEAN NOT NULL,
		tail TEXT NOT NULL,
		source VARCHAR(255) NOT NULL,
		provenance TEXT NOT NULL,
		at DATETIME(6) NOT NULL,
		INDEX idx_gtb_checks_item (item_id)
	)`,
	`CREATE TABLE IF NOT EXISTS gtb_transitions (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		item_id VARCHAR(255) NOT NULL,
		seq INT NOT NULL,
		from_step VARCHAR(64) NOT NULL,
		to_step VARCHAR(64) NOT NULL,
		actor VARCHAR(255) NOT NULL,
		at DATETIME(6) NOT NULL,
		UNIQUE KEY uq_gtb_transitions_item_seq (item_id, seq)
	)`,
}

// NewDoltStore returns a Store backed by db, creating the gtb_approvals,
// gtb_tdd, gtb_checks and gtb_transitions tables when missing. The DSN must
// set parseTime=true. All tables are append-only; times are stored in UTC.
func NewDoltStore(db *sql.DB) (Store, error) {
	for _, stmt := range doltSchema {
		if _, err := db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("workflow: creating evidence tables: %w", err)
		}
	}
	return &doltStore{db: db}, nil
}

func (d *doltStore) AddApproval(a Approval) error {
	_, err := d.db.Exec(`INSERT INTO gtb_approvals (item_id, artifact, sha, verdict, role, source, channel, actor, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ItemID, a.Artifact, a.SHA, a.Verdict, a.Role, a.Source, a.Channel, a.Actor, utc(a.At))
	return wrap("add approval", err)
}

func (d *doltStore) Approvals(id string) ([]Approval, error) {
	rows, err := d.db.Query(`SELECT item_id, artifact, sha, verdict, role, source, channel, actor, at
		FROM gtb_approvals WHERE item_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, wrap("list approvals", err)
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		var a Approval
		if err := rows.Scan(&a.ItemID, &a.Artifact, &a.SHA, &a.Verdict, &a.Role, &a.Source, &a.Channel, &a.Actor, &a.At); err != nil {
			return nil, wrap("scan approval", err)
		}
		a.At = a.At.UTC()
		out = append(out, a)
	}
	return out, wrap("list approvals", rows.Err())
}

func (d *doltStore) AddTDD(r TDDRun) error {
	files, err := json.Marshal(r.TestFiles)
	if err != nil {
		return wrap("encode tdd test files", err)
	}
	_, err = d.db.Exec(`INSERT INTO gtb_tdd (item_id, plan_step, phase, cmd, exit_code, test_files_sha, test_files, tail, source, provenance, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ItemID, r.PlanStep, r.Phase, r.Cmd, r.Exit, r.TestFilesSHA, string(files), r.Tail, r.Source, r.Provenance, utc(r.At))
	return wrap("add tdd run", err)
}

func (d *doltStore) TDD(id string) ([]TDDRun, error) {
	rows, err := d.db.Query(`SELECT item_id, plan_step, phase, cmd, exit_code, test_files_sha, test_files, tail, source, provenance, at
		FROM gtb_tdd WHERE item_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, wrap("list tdd runs", err)
	}
	defer rows.Close()
	var out []TDDRun
	for rows.Next() {
		var r TDDRun
		var files string
		if err := rows.Scan(&r.ItemID, &r.PlanStep, &r.Phase, &r.Cmd, &r.Exit, &r.TestFilesSHA, &files, &r.Tail, &r.Source, &r.Provenance, &r.At); err != nil {
			return nil, wrap("scan tdd run", err)
		}
		if err := json.Unmarshal([]byte(files), &r.TestFiles); err != nil {
			return nil, wrap("decode tdd test files", err)
		}
		r.At = r.At.UTC()
		out = append(out, r)
	}
	return out, wrap("list tdd runs", rows.Err())
}

func (d *doltStore) AddCheck(c CheckRun) error {
	_, err := d.db.Exec(`INSERT INTO gtb_checks (item_id, cmd_set_sha, passed, tail, source, provenance, at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.ItemID, c.CmdSetSHA, c.Passed, c.Tail, c.Source, c.Provenance, utc(c.At))
	return wrap("add check run", err)
}

func (d *doltStore) Checks(id string) ([]CheckRun, error) {
	rows, err := d.db.Query(`SELECT item_id, cmd_set_sha, passed, tail, source, provenance, at
		FROM gtb_checks WHERE item_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, wrap("list check runs", err)
	}
	defer rows.Close()
	var out []CheckRun
	for rows.Next() {
		var c CheckRun
		if err := rows.Scan(&c.ItemID, &c.CmdSetSHA, &c.Passed, &c.Tail, &c.Source, &c.Provenance, &c.At); err != nil {
			return nil, wrap("scan check run", err)
		}
		c.At = c.At.UTC()
		out = append(out, c)
	}
	return out, wrap("list check runs", rows.Err())
}

// raceErr maps the errors a lost Transition race produces to ErrStepConflict:
// a duplicate (item_id, seq) key on insert (1062), Dolt rejecting the commit
// because a concurrent transaction committed the same key first (1105 with
// "constraint violation"), or Dolt's optimistic commit failing with 1213
// (40001 "serialization failure: this transaction conflicts with a committed
// transaction from another client"). Verified against Dolt 2.0.7
// (dolthub/dolt-sql-server:2.0.7, testutil.DoltDockerImage). Other errors
// are returned wrapped as-is.
func raceErr(id, op string, err error) error {
	if err == nil {
		return nil
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) && (me.Number == 1062 || me.Number == 1213 || strings.Contains(me.Message, "constraint violation")) {
		return fmt.Errorf("%w: item %s advanced concurrently: %v", ErrStepConflict, id, err)
	}
	return wrap(op, err)
}

// Transition implements Store.Transition. Dolt accepts SELECT ... FOR UPDATE
// but its transactions are optimistic and the row lock does not serialize
// concurrent writers, so the read is backed by UNIQUE(item_id, seq): every
// transition takes seq = latest+1, and of several racing transactions only
// the first to commit wins; the rest fail and are reported as ErrStepConflict.
func (d *doltStore) Transition(id string, from, to Step, actor string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return wrap("begin transition", err)
	}
	defer func() { _ = tx.Rollback() }()

	var latest string
	var seq int
	err = tx.QueryRow(`SELECT to_step, seq FROM gtb_transitions WHERE item_id = ? ORDER BY seq DESC LIMIT 1 FOR UPDATE`, id).Scan(&latest, &seq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No transitions yet: accept the first one for any from.
	case err != nil:
		return wrap("read latest transition", err)
	case Step(latest) != from:
		return fmt.Errorf("%w: item %s is at %s, not %s", ErrStepConflict, id, latest, from)
	}
	if _, err := tx.Exec(`INSERT INTO gtb_transitions (item_id, seq, from_step, to_step, actor, at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, seq+1, string(from), string(to), actor, utc(time.Time{})); err != nil {
		return raceErr(id, "insert transition", err)
	}
	return raceErr(id, "commit transition", tx.Commit())
}

func (d *doltStore) Transitions(id string) ([]Transition, error) {
	rows, err := d.db.Query(`SELECT item_id, from_step, to_step, actor, at
		FROM gtb_transitions WHERE item_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, wrap("list transitions", err)
	}
	defer rows.Close()
	var out []Transition
	for rows.Next() {
		var t Transition
		var from, to string
		if err := rows.Scan(&t.ItemID, &from, &to, &t.Actor, &t.At); err != nil {
			return nil, wrap("scan transition", err)
		}
		t.From, t.To, t.At = Step(from), Step(to), t.At.UTC()
		out = append(out, t)
	}
	return out, wrap("list transitions", rows.Err())
}

func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("workflow: %s: %w", op, err)
}
