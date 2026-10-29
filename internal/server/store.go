// Package server implements the gtb server (spec §8). This file holds its
// SQLite store: machines, tokens, rigs, item projections, artifacts, events,
// requests and results.
package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"

	_ "modernc.org/sqlite" // pure-Go SQLite driver ("sqlite")
)

// ErrNotFound is returned when a looked-up row does not exist (or, for
// MachineByToken, is revoked).
var ErrNotFound = errors.New("server: not found")

// ErrUnknownRequestType is returned by EnqueueRequest for a type outside
// agentlink.RequestTypes.
var ErrUnknownRequestType = errors.New("server: unknown request type")

// ErrInvalidExpiry is returned by EnqueueRequest for a zero ExpiresAt or one
// at or before now.
var ErrInvalidExpiry = errors.New("server: invalid request expiry")

// ErrInvalidEvent is returned by AppendEvents for an event with an empty ID,
// an unknown type or undecodable data.
var ErrInvalidEvent = errors.New("server: invalid event")

// ErrCursorMismatch is returned by AppendEventsAfter when the caller's after
// cursor is not the machine's stored cursor.
var ErrCursorMismatch = errors.New("server: cursor mismatch")

// Request statuses.
const (
	RequestPending   = "pending"
	RequestDelivered = "delivered"
	RequestDone      = "done"
	RequestExpired   = "expired"
)

const schemaVersion = 3

// Store is the server's SQLite database. All writes go through a single
// connection, so they are serialized.
type Store struct {
	db       *sql.DB
	notifier *requestNotifier
	// board wakes dashboard SSE streams when items, artifacts, sessions or
	// events change (ApplySnapshot, AppendEvents). It is separate from
	// notifier, which wakes machine long-polls.
	board *broadcaster
}

// Machine is a row of the machines table.
type Machine struct {
	ID         string
	Hostname   string
	OS         string
	Arch       string
	GtbVersion string
	Protocol   string
	CreatedAt  time.Time
	LastSeenAt time.Time
	Cursor     string
	RevokedAt  *time.Time
	// HasSnapshot is true once the server has applied a snapshot from the
	// machine. Until then the machine must resync.
	HasSnapshot bool
}

// ItemFilter selects items for Items. Empty fields match everything.
type ItemFilter struct {
	MachineID, Rig, Kind string
	WaitingOnly          bool
}

// ItemRow is an item projection as stored on the server.
type ItemRow struct {
	agentlink.ItemView
	MachineID string
	UpdatedAt time.Time
	// MachineLastSeenAt is the owning machine's last_seen_at, joined in so
	// callers can compute freshness.
	MachineLastSeenAt time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS schema_version (
	version INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS machines (
	id           TEXT PRIMARY KEY,
	hostname     TEXT NOT NULL DEFAULT '',
	os           TEXT NOT NULL DEFAULT '',
	arch         TEXT NOT NULL DEFAULT '',
	gtb_version  TEXT NOT NULL DEFAULT '',
	protocol     TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL,
	cursor       TEXT NOT NULL DEFAULT '',
	revoked_at   INTEGER,
	has_snapshot INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS tokens (
	sha        TEXT PRIMARY KEY,
	machine_id TEXT NOT NULL REFERENCES machines(id),
	created_at INTEGER NOT NULL,
	revoked_at INTEGER
);
CREATE INDEX IF NOT EXISTS tokens_machine ON tokens(machine_id);
CREATE TABLE IF NOT EXISTS enroll_codes (
	sha        TEXT PRIMARY KEY,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	used_at    INTEGER,
	machine_id TEXT
);
CREATE TABLE IF NOT EXISTS rigs (
	machine_id TEXT NOT NULL REFERENCES machines(id),
	name       TEXT NOT NULL,
	remote     TEXT NOT NULL DEFAULT '',
	gated      INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (machine_id, name)
);
CREATE TABLE IF NOT EXISTS items (
	machine_id        TEXT NOT NULL REFERENCES machines(id),
	id                TEXT NOT NULL,
	rig               TEXT NOT NULL DEFAULT '',
	kind              TEXT NOT NULL DEFAULT '',
	step              TEXT NOT NULL DEFAULT '',
	waiting_for_human TEXT NOT NULL DEFAULT '',
	updated_at        INTEGER NOT NULL,
	view              TEXT NOT NULL,
	PRIMARY KEY (machine_id, id)
);
CREATE INDEX IF NOT EXISTS items_rig ON items(rig);
CREATE INDEX IF NOT EXISTS items_kind ON items(kind);
CREATE INDEX IF NOT EXISTS items_step ON items(step);
CREATE INDEX IF NOT EXISTS items_waiting ON items(waiting_for_human);
CREATE INDEX IF NOT EXISTS items_updated ON items(updated_at);
CREATE TABLE IF NOT EXISTS artifacts (
	machine_id TEXT NOT NULL REFERENCES machines(id),
	item_id    TEXT NOT NULL,
	name       TEXT NOT NULL,
	sha        TEXT NOT NULL DEFAULT '',
	content    TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (machine_id, item_id, name)
);
CREATE TABLE IF NOT EXISTS sessions (
	machine_id    TEXT NOT NULL REFERENCES machines(id),
	name          TEXT NOT NULL,
	role          TEXT NOT NULL DEFAULT '',
	rig           TEXT NOT NULL DEFAULT '',
	item_id       TEXT NOT NULL DEFAULT '',
	state         TEXT NOT NULL DEFAULT '',
	last_activity INTEGER NOT NULL DEFAULT 0,
	updated_at    INTEGER NOT NULL,
	PRIMARY KEY (machine_id, name)
);
CREATE TABLE IF NOT EXISTS events (
	machine_id TEXT NOT NULL REFERENCES machines(id),
	id         TEXT NOT NULL,
	at         INTEGER NOT NULL,
	type       TEXT NOT NULL,
	item_id    TEXT NOT NULL DEFAULT '',
	data       TEXT,
	PRIMARY KEY (machine_id, id)
);
CREATE INDEX IF NOT EXISTS events_at ON events(at);
CREATE INDEX IF NOT EXISTS events_item ON events(machine_id, item_id);
CREATE TABLE IF NOT EXISTS requests (
	machine_id TEXT NOT NULL REFERENCES machines(id),
	key        TEXT NOT NULL,
	type       TEXT NOT NULL,
	item_id    TEXT NOT NULL DEFAULT '',
	params     TEXT,
	expires_at INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	status     TEXT NOT NULL DEFAULT 'pending',
	PRIMARY KEY (machine_id, key)
);
CREATE INDEX IF NOT EXISTS requests_status ON requests(status, expires_at);
CREATE TABLE IF NOT EXISTS results (
	machine_id TEXT NOT NULL REFERENCES machines(id),
	key        TEXT NOT NULL,
	ok         INTEGER NOT NULL,
	error      TEXT NOT NULL DEFAULT '',
	data       TEXT,
	at         INTEGER NOT NULL,
	PRIMARY KEY (machine_id, key)
);
CREATE INDEX IF NOT EXISTS results_at ON results(at);
CREATE TABLE IF NOT EXISTS web_sessions (
	sha        TEXT PRIMARY KEY,
	login      TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
` + adminSchema

// adminSchema is the v3 admin table: the sha256 of the dashboard admin token
// and the key that derives per-session CSRF tokens. There is at most one row.
const adminSchema = `
CREATE TABLE IF NOT EXISTS admin (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	token_sha  TEXT NOT NULL,
	csrf_key   BLOB NOT NULL,
	created_at INTEGER
);
`

// OpenStore opens (creating if needed) the SQLite store at path. The parent
// directory is created with mode 0700.
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("server: create store dir: %w", err)
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("server: open store: %w", err)
	}
	db.SetMaxOpenConns(1) // serialize all access, writes included
	s := &Store{db: db, notifier: newRequestNotifier(), board: newBroadcaster()}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("server: migrate: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("server: create schema: %w", err)
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM schema_version`).Scan(&n); err != nil {
		return fmt.Errorf("server: read schema version: %w", err)
	}
	if n == 0 {
		// Fresh database: the schema above is already current.
		if _, err := tx.Exec(`INSERT INTO schema_version(version) VALUES (?)`, schemaVersion); err != nil {
			return fmt.Errorf("server: write schema version: %w", err)
		}
		return tx.Commit()
	}
	var v int
	if err := tx.QueryRow(`SELECT max(version) FROM schema_version`).Scan(&v); err != nil {
		return fmt.Errorf("server: read schema version: %w", err)
	}
	if v < 2 {
		// v2: machines.has_snapshot.
		if _, err := tx.Exec(`ALTER TABLE machines ADD COLUMN has_snapshot INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("server: migrate to v2: %w", err)
		}
	}
	if v < 3 {
		// v3: admin (dashboard admin token and CSRF key).
		if _, err := tx.Exec(adminSchema); err != nil {
			return fmt.Errorf("server: migrate to v3: %w", err)
		}
	}
	if v < schemaVersion {
		if _, err := tx.Exec(`UPDATE schema_version SET version = ?`, schemaVersion); err != nil {
			return fmt.Errorf("server: write schema version: %w", err)
		}
	}
	return tx.Commit()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func ms(t time.Time) int64 { return t.UTC().UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

func nullMS(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

// UpsertMachine records a machine's Hello under the server-assigned id. It
// updates identity fields and last_seen_at, and never changes revoked_at or
// the cursor.
func (s *Store) UpsertMachine(id string, h agentlink.Hello) error {
	now := ms(time.Now())
	_, err := s.db.Exec(`
		INSERT INTO machines(id, hostname, os, arch, gtb_version, protocol, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hostname = excluded.hostname, os = excluded.os, arch = excluded.arch,
			gtb_version = excluded.gtb_version, protocol = excluded.protocol,
			last_seen_at = excluded.last_seen_at`,
		id, h.Hostname, h.OS, h.Arch, h.GtbVersion, h.Protocol, now, now)
	if err != nil {
		return fmt.Errorf("server: upsert machine: %w", err)
	}
	return nil
}

// AddToken stores a machine token by its sha256 hex. The raw token is never
// passed to the store.
func (s *Store) AddToken(machineID, tokenSHA string) error {
	_, err := s.db.Exec(`INSERT INTO tokens(sha, machine_id, created_at) VALUES (?, ?, ?)`,
		tokenSHA, machineID, ms(time.Now()))
	if err != nil {
		return fmt.Errorf("server: add token: %w", err)
	}
	return nil
}

// RevokeToken revokes one token.
func (s *Store) RevokeToken(tokenSHA string) error {
	_, err := s.db.Exec(`UPDATE tokens SET revoked_at = ? WHERE sha = ? AND revoked_at IS NULL`,
		ms(time.Now()), tokenSHA)
	if err != nil {
		return fmt.Errorf("server: revoke token: %w", err)
	}
	return nil
}

// RevokeMachine revokes a machine and all of its tokens.
func (s *Store) RevokeMachine(id string) error {
	now := ms(time.Now())
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("server: revoke machine: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE machines SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`, now, id)
	if err != nil {
		return fmt.Errorf("server: revoke machine: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`UPDATE tokens SET revoked_at = ? WHERE machine_id = ? AND revoked_at IS NULL`, now, id); err != nil {
		return fmt.Errorf("server: revoke machine tokens: %w", err)
	}
	return tx.Commit()
}

const machineCols = `m.id, m.hostname, m.os, m.arch, m.gtb_version, m.protocol, m.created_at, m.last_seen_at, m.cursor, m.revoked_at, m.has_snapshot`

func scanMachine(row interface{ Scan(...any) error }) (*Machine, error) {
	var m Machine
	var created, seen int64
	var revoked sql.NullInt64
	if err := row.Scan(&m.ID, &m.Hostname, &m.OS, &m.Arch, &m.GtbVersion, &m.Protocol, &created, &seen, &m.Cursor, &revoked, &m.HasSnapshot); err != nil {
		return nil, err
	}
	m.CreatedAt, m.LastSeenAt, m.RevokedAt = fromMS(created), fromMS(seen), nullMS(revoked)
	return &m, nil
}

// MachineByToken returns the machine owning tokenSHA, only if neither the
// token nor the machine is revoked. Otherwise it returns ErrNotFound.
func (s *Store) MachineByToken(tokenSHA string) (*Machine, error) {
	m, err := scanMachine(s.db.QueryRow(`
		SELECT `+machineCols+` FROM tokens t JOIN machines m ON m.id = t.machine_id
		WHERE t.sha = ? AND t.revoked_at IS NULL AND m.revoked_at IS NULL`, tokenSHA))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("server: machine by token: %w", err)
	}
	return m, nil
}

// TouchMachine sets the machine's last_seen_at to now.
func (s *Store) TouchMachine(id string, now time.Time) error {
	res, err := s.db.Exec(`UPDATE machines SET last_seen_at = ? WHERE id = ?`, ms(now), id)
	if err != nil {
		return fmt.Errorf("server: touch machine: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Machine returns a machine by id, revoked or not.
func (s *Store) Machine(id string) (*Machine, error) {
	m, err := scanMachine(s.db.QueryRow(`SELECT `+machineCols+` FROM machines m WHERE m.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("server: machine: %w", err)
	}
	return m, nil
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// upsertItem stores an item projection. Artifacts carried in the view are
// stored in the artifacts table, not in the item JSON.
func upsertItem(tx execer, machineID string, iv agentlink.ItemView, now int64) error {
	arts := iv.Artifacts
	iv.Artifacts = nil
	view, err := json.Marshal(iv)
	if err != nil {
		return fmt.Errorf("encode item %s: %w", iv.ID, err)
	}
	if _, err := tx.Exec(`
		INSERT INTO items(machine_id, id, rig, kind, step, waiting_for_human, updated_at, view)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(machine_id, id) DO UPDATE SET
			rig = excluded.rig, kind = excluded.kind, step = excluded.step,
			waiting_for_human = excluded.waiting_for_human,
			updated_at = excluded.updated_at, view = excluded.view`,
		machineID, iv.ID, iv.Rig, iv.Kind, iv.Step, iv.WaitingForHuman, now, string(view)); err != nil {
		return fmt.Errorf("upsert item %s: %w", iv.ID, err)
	}
	for _, a := range arts {
		if err := upsertArtifact(tx, machineID, iv.ID, a, now); err != nil {
			return err
		}
	}
	return nil
}

func upsertArtifact(tx execer, machineID, itemID string, a agentlink.ArtifactView, now int64) error {
	if _, err := tx.Exec(`
		INSERT INTO artifacts(machine_id, item_id, name, sha, content, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(machine_id, item_id, name) DO UPDATE SET
			sha = excluded.sha, content = excluded.content, updated_at = excluded.updated_at`,
		machineID, itemID, a.Name, a.SHA, a.Content, now); err != nil {
		return fmt.Errorf("upsert artifact %s/%s: %w", itemID, a.Name, err)
	}
	return nil
}

func upsertSession(tx execer, machineID string, sv agentlink.SessionView, now int64) error {
	if _, err := tx.Exec(`
		INSERT INTO sessions(machine_id, name, role, rig, item_id, state, last_activity, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(machine_id, name) DO UPDATE SET
			role = excluded.role, rig = excluded.rig, item_id = excluded.item_id,
			state = excluded.state, last_activity = excluded.last_activity,
			updated_at = excluded.updated_at`,
		machineID, sv.Name, sv.Role, sv.Rig, sv.ItemID, sv.State, ms(sv.LastActivity), now); err != nil {
		return fmt.Errorf("upsert session %s: %w", sv.Name, err)
	}
	return nil
}

// ApplySnapshot replaces the machine's rigs, items, artifacts and sessions
// with s, sets the machine's cursor to s.Cursor and marks it as having a
// snapshot, in one transaction.
// Events, requests and results are kept.
func (s *Store) ApplySnapshot(machineID string, snap agentlink.Snapshot) error {
	now := ms(time.Now())
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("server: apply snapshot: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE machines SET cursor = ?, last_seen_at = ?, has_snapshot = 1 WHERE id = ?`, snap.Cursor, now, machineID)
	if err != nil {
		return fmt.Errorf("server: apply snapshot: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	for _, tbl := range []string{"rigs", "items", "artifacts", "sessions"} {
		if _, err := tx.Exec(`DELETE FROM `+tbl+` WHERE machine_id = ?`, machineID); err != nil {
			return fmt.Errorf("server: apply snapshot: clear %s: %w", tbl, err)
		}
	}
	for _, r := range snap.Rigs {
		if _, err := tx.Exec(`INSERT INTO rigs(machine_id, name, remote, gated) VALUES (?, ?, ?, ?)`,
			machineID, r.Name, r.Remote, r.Gated); err != nil {
			return fmt.Errorf("server: apply snapshot: rig %s: %w", r.Name, err)
		}
	}
	for _, iv := range snap.Items {
		if err := upsertItem(tx, machineID, iv, now); err != nil {
			return fmt.Errorf("server: apply snapshot: %w", err)
		}
	}
	for _, sv := range snap.Sessions {
		if err := upsertSession(tx, machineID, sv, now); err != nil {
			return fmt.Errorf("server: apply snapshot: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("server: apply snapshot: %w", err)
	}
	s.board.notify()
	return nil
}

func validEventType(t string) bool {
	for _, e := range agentlink.EventTypes {
		if e == t {
			return true
		}
	}
	return false
}

// AppendEvents stores evs for the machine, idempotent on (machine_id, id),
// and projects newly stored events onto items, artifacts and sessions. It
// returns the greatest event ID in the batch and advances the machine's
// cursor to it (never backwards), all in one transaction. A batch with an
// unknown event type or undecodable data is rejected as a whole with
// ErrInvalidEvent.
func (s *Store) AppendEvents(machineID string, evs []agentlink.Event) (string, error) {
	return s.appendEvents(machineID, nil, evs)
}

// AppendEventsAfter is AppendEvents guarded by the machine's cursor: in the
// same transaction, it fails with ErrCursorMismatch unless the stored cursor
// equals after. An empty batch returns after.
func (s *Store) AppendEventsAfter(machineID, after string, evs []agentlink.Event) (string, error) {
	return s.appendEvents(machineID, &after, evs)
}

func (s *Store) appendEvents(machineID string, after *string, evs []agentlink.Event) (string, error) {
	now := ms(time.Now())
	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("server: append events: %w", err)
	}
	defer tx.Rollback()
	var cursor string
	if err := tx.QueryRow(`SELECT cursor FROM machines WHERE id = ?`, machineID).Scan(&cursor); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("server: append events: %w", err)
	}
	if after != nil {
		if cursor != *after {
			return "", ErrCursorMismatch
		}
		if len(evs) == 0 {
			return cursor, nil
		}
	}
	var last string
	stored := 0
	for _, ev := range evs {
		if ev.ID == "" || !validEventType(ev.Type) {
			return "", fmt.Errorf("%w: id=%q type=%q", ErrInvalidEvent, ev.ID, ev.Type)
		}
		var data any
		if len(ev.Data) > 0 {
			data = string(ev.Data)
		}
		res, err := tx.Exec(`INSERT OR IGNORE INTO events(machine_id, id, at, type, item_id, data) VALUES (?, ?, ?, ?, ?, ?)`,
			machineID, ev.ID, ms(ev.At), ev.Type, ev.ItemID, data)
		if err != nil {
			return "", fmt.Errorf("server: append event %s: %w", ev.ID, err)
		}
		if ev.ID > last {
			last = ev.ID
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // already stored and projected
		}
		stored++
		if err := project(tx, machineID, ev, now); err != nil {
			return "", fmt.Errorf("server: project event %s: %w", ev.ID, err)
		}
	}
	if last > cursor {
		if _, err := tx.Exec(`UPDATE machines SET cursor = ?, last_seen_at = ? WHERE id = ?`, last, now, machineID); err != nil {
			return "", fmt.Errorf("server: advance cursor: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("server: append events: %w", err)
	}
	if stored > 0 {
		s.board.notify()
	}
	return last, nil
}

func project(tx *sql.Tx, machineID string, ev agentlink.Event, now int64) error {
	switch ev.Type {
	case agentlink.EventItemUpsert:
		var iv agentlink.ItemView
		if err := json.Unmarshal(ev.Data, &iv); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidEvent, err)
		}
		if iv.ID == "" {
			iv.ID = ev.ItemID
		}
		return upsertItem(tx, machineID, iv, now)
	case agentlink.EventItemDeleted:
		if _, err := tx.Exec(`DELETE FROM items WHERE machine_id = ? AND id = ?`, machineID, ev.ItemID); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM artifacts WHERE machine_id = ? AND item_id = ?`, machineID, ev.ItemID)
		return err
	case agentlink.EventArtifact:
		var a agentlink.ArtifactView
		if err := json.Unmarshal(ev.Data, &a); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidEvent, err)
		}
		return upsertArtifact(tx, machineID, ev.ItemID, a, now)
	case agentlink.EventSession:
		var sv agentlink.SessionView
		if err := json.Unmarshal(ev.Data, &sv); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidEvent, err)
		}
		return upsertSession(tx, machineID, sv, now)
	}
	return nil // evidence, heartbeat: stored only
}

// Items returns item projections matching f, without artifacts, ordered by
// machine, rig and id.
func (s *Store) Items(f ItemFilter) ([]ItemRow, error) {
	q := `SELECT i.machine_id, i.updated_at, i.view, m.last_seen_at FROM items i JOIN machines m ON m.id = i.machine_id WHERE 1=1`
	var args []any
	if f.MachineID != "" {
		q += ` AND i.machine_id = ?`
		args = append(args, f.MachineID)
	}
	if f.Rig != "" {
		q += ` AND i.rig = ?`
		args = append(args, f.Rig)
	}
	if f.Kind != "" {
		q += ` AND i.kind = ?`
		args = append(args, f.Kind)
	}
	if f.WaitingOnly {
		q += ` AND i.waiting_for_human <> ''`
	}
	q += ` ORDER BY i.machine_id, i.rig, i.id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("server: items: %w", err)
	}
	defer rows.Close()
	var out []ItemRow
	for rows.Next() {
		r, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("server: items: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func scanItem(row interface{ Scan(...any) error }) (*ItemRow, error) {
	var r ItemRow
	var updated, seen int64
	var view string
	if err := row.Scan(&r.MachineID, &updated, &view, &seen); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(view), &r.ItemView); err != nil {
		return nil, err
	}
	r.UpdatedAt = fromMS(updated)
	r.MachineLastSeenAt = fromMS(seen)
	return &r, nil
}

// Item returns one item and its artifacts (ordered by name), or ErrNotFound.
func (s *Store) Item(machineID, id string) (*ItemRow, []agentlink.ArtifactView, error) {
	r, err := scanItem(s.db.QueryRow(`SELECT i.machine_id, i.updated_at, i.view, m.last_seen_at FROM items i JOIN machines m ON m.id = i.machine_id
		WHERE i.machine_id = ? AND i.id = ?`, machineID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("server: item: %w", err)
	}
	rows, err := s.db.Query(`SELECT name, sha, content FROM artifacts WHERE machine_id = ? AND item_id = ? ORDER BY name`, machineID, id)
	if err != nil {
		return nil, nil, fmt.Errorf("server: item artifacts: %w", err)
	}
	defer rows.Close()
	var arts []agentlink.ArtifactView
	for rows.Next() {
		var a agentlink.ArtifactView
		if err := rows.Scan(&a.Name, &a.SHA, &a.Content); err != nil {
			return nil, nil, fmt.Errorf("server: item artifacts: %w", err)
		}
		arts = append(arts, a)
	}
	return r, arts, rows.Err()
}

// EnqueueRequest queues r for the machine. Types outside
// agentlink.RequestTypes fail with ErrUnknownRequestType, and a zero or
// non-future ExpiresAt fails with ErrInvalidExpiry. A duplicate key is a
// no-op.
func (s *Store) EnqueueRequest(machineID string, r agentlink.Request) error {
	return s.enqueueRequest(machineID, r, time.Now())
}

func (s *Store) enqueueRequest(machineID string, r agentlink.Request, now time.Time) error {
	if !agentlink.IsRequestType(r.Type) {
		return fmt.Errorf("%w: %q", ErrUnknownRequestType, r.Type)
	}
	if r.ExpiresAt.IsZero() || !r.ExpiresAt.After(now) {
		return fmt.Errorf("%w: %v", ErrInvalidExpiry, r.ExpiresAt)
	}
	if r.Key == "" {
		return errors.New("server: enqueue request: empty key")
	}
	var params any
	if len(r.Params) > 0 {
		params = string(r.Params)
	}
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO requests(machine_id, key, type, item_id, params, expires_at, created_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		machineID, r.Key, r.Type, r.ItemID, params, ms(r.ExpiresAt), ms(now), RequestPending)
	if err != nil {
		return fmt.Errorf("server: enqueue request: %w", err)
	}
	s.notifier.notify(machineID) // wake the machine's long-poll, if any
	return nil
}

// MarkDelivered marks the machine's pending requests with the given keys as
// delivered. Requests in any other status are left alone.
func (s *Store) MarkDelivered(machineID string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("server: mark delivered: %w", err)
	}
	defer tx.Rollback()
	for _, k := range keys {
		if _, err := tx.Exec(`UPDATE requests SET status = ? WHERE machine_id = ? AND key = ? AND status = ?`,
			RequestDelivered, machineID, k, RequestPending); err != nil {
			return fmt.Errorf("server: mark delivered: %w", err)
		}
	}
	return tx.Commit()
}

// PendingRequests returns the machine's pending or delivered requests that
// have not expired at now and have no result, oldest first.
func (s *Store) PendingRequests(machineID string, now time.Time) ([]agentlink.Request, error) {
	rows, err := s.pendingRequestRows(machineID, now)
	if err != nil {
		return nil, err
	}
	var out []agentlink.Request
	for _, r := range rows {
		out = append(out, r.Request)
	}
	return out, nil
}

// pendingRequestRow is a PendingRequests entry with its status (pending or
// delivered), which the wire type does not carry.
type pendingRequestRow struct {
	agentlink.Request
	Status string
}

func (s *Store) pendingRequestRows(machineID string, now time.Time) ([]pendingRequestRow, error) {
	rows, err := s.db.Query(`
		SELECT r.key, r.type, r.item_id, r.params, r.expires_at, r.status FROM requests r
		WHERE r.machine_id = ? AND r.status IN (?, ?) AND r.expires_at > ?
		  AND NOT EXISTS (SELECT 1 FROM results x WHERE x.machine_id = r.machine_id AND x.key = r.key)
		ORDER BY r.created_at, r.key`,
		machineID, RequestPending, RequestDelivered, ms(now))
	if err != nil {
		return nil, fmt.Errorf("server: pending requests: %w", err)
	}
	defer rows.Close()
	var out []pendingRequestRow
	for rows.Next() {
		var r pendingRequestRow
		var params sql.NullString
		var exp int64
		if err := rows.Scan(&r.Key, &r.Type, &r.ItemID, &params, &exp, &r.Status); err != nil {
			return nil, fmt.Errorf("server: pending requests: %w", err)
		}
		if params.Valid {
			r.Params = json.RawMessage(params.String)
		}
		r.ExpiresAt = fromMS(exp)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddResult records a machine's result. It is idempotent: the first result
// for a key wins. A result is on time when the request is not already expired
// and its expires_at is after the time the result is recorded; an on-time
// result marks the request done. A late result is still stored, for audit,
// but the request becomes (or stays) expired (R32). It returns ErrNotFound if
// the machine has no request with that key.
func (s *Store) AddResult(machineID string, r agentlink.Result) error {
	return s.addResult(machineID, r, time.Now())
}

func (s *Store) addResult(machineID string, r agentlink.Result, now time.Time) error {
	at := ms(now)
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("server: add result: %w", err)
	}
	defer tx.Rollback()
	var status string
	var expiresAt int64
	err = tx.QueryRow(`SELECT status, expires_at FROM requests WHERE machine_id = ? AND key = ?`, machineID, r.Key).Scan(&status, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("server: add result: %w", err)
	}
	var data any
	if len(r.Data) > 0 {
		data = string(r.Data)
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO results(machine_id, key, ok, error, data, at) VALUES (?, ?, ?, ?, ?, ?)`,
		machineID, r.Key, r.OK, r.Error, data, at)
	if err != nil {
		return fmt.Errorf("server: add result: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return tx.Commit() // duplicate: the first result already decided the status
	}
	next := RequestDone
	if status == RequestExpired || expiresAt <= at {
		next = RequestExpired
	}
	if _, err := tx.Exec(`UPDATE requests SET status = ? WHERE machine_id = ? AND key = ?`, next, machineID, r.Key); err != nil {
		return fmt.Errorf("server: add result: %w", err)
	}
	return tx.Commit()
}

// ExpireRequests marks requests past expires_at with no result as expired and
// returns how many it marked.
func (s *Store) ExpireRequests(now time.Time) (int, error) {
	res, err := s.db.Exec(`
		UPDATE requests SET status = ?
		WHERE status IN (?, ?) AND expires_at <= ?
		  AND NOT EXISTS (SELECT 1 FROM results x WHERE x.machine_id = requests.machine_id AND x.key = requests.key)`,
		RequestExpired, RequestPending, RequestDelivered, ms(now))
	if err != nil {
		return 0, fmt.Errorf("server: expire requests: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RequestStatus returns a request's status at now and, when one exists, its
// result (late results included). A request with no result whose expires_at
// is at or before now reads as expired even before ExpireRequests has run.
func (s *Store) RequestStatus(machineID, key string, now time.Time) (string, *agentlink.Result, error) {
	var status string
	var expiresAt int64
	var ok sql.NullBool
	var errStr, data sql.NullString
	err := s.db.QueryRow(`
		SELECT r.status, r.expires_at, x.ok, x.error, x.data FROM requests r
		LEFT JOIN results x ON x.machine_id = r.machine_id AND x.key = r.key
		WHERE r.machine_id = ? AND r.key = ?`, machineID, key).Scan(&status, &expiresAt, &ok, &errStr, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, fmt.Errorf("server: request status: %w", err)
	}
	if !ok.Valid {
		if (status == RequestPending || status == RequestDelivered) && expiresAt <= ms(now) {
			status = RequestExpired
		}
		return status, nil, nil
	}
	res := &agentlink.Result{Key: key, OK: ok.Bool, Error: errStr.String}
	if data.Valid {
		res.Data = json.RawMessage(data.String)
	}
	return status, res, nil
}

// Machines returns every machine, revoked ones included, ordered by id.
func (s *Store) Machines() ([]Machine, error) {
	rows, err := s.db.Query(`SELECT ` + machineCols + ` FROM machines m ORDER BY m.id`)
	if err != nil {
		return nil, fmt.Errorf("server: machines: %w", err)
	}
	defer rows.Close()
	var out []Machine
	for rows.Next() {
		m, err := scanMachine(rows)
		if err != nil {
			return nil, fmt.Errorf("server: machines: %w", err)
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// Rigs returns the machine's rigs ordered by name.
func (s *Store) Rigs(machineID string) ([]agentlink.Rig, error) {
	rows, err := s.db.Query(`SELECT name, remote, gated FROM rigs WHERE machine_id = ? ORDER BY name`, machineID)
	if err != nil {
		return nil, fmt.Errorf("server: rigs: %w", err)
	}
	defer rows.Close()
	out := []agentlink.Rig{}
	for rows.Next() {
		var r agentlink.Rig
		if err := rows.Scan(&r.Name, &r.Remote, &r.Gated); err != nil {
			return nil, fmt.Errorf("server: rigs: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ItemEvents returns up to limit of the item's events, newest (greatest ID)
// first.
func (s *Store) ItemEvents(machineID, itemID string, limit int) ([]agentlink.Event, error) {
	rows, err := s.db.Query(`SELECT id, at, type, item_id, data FROM events
		WHERE machine_id = ? AND item_id = ? ORDER BY id DESC LIMIT ?`, machineID, itemID, limit)
	if err != nil {
		return nil, fmt.Errorf("server: item events: %w", err)
	}
	defer rows.Close()
	out := []agentlink.Event{}
	for rows.Next() {
		var ev agentlink.Event
		var at int64
		var data sql.NullString
		if err := rows.Scan(&ev.ID, &at, &ev.Type, &ev.ItemID, &data); err != nil {
			return nil, fmt.Errorf("server: item events: %w", err)
		}
		ev.At = fromMS(at)
		if data.Valid {
			ev.Data = json.RawMessage(data.String)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
