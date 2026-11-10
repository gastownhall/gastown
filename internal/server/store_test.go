package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "sub", "gtb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addMachine(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.UpsertMachine(id, agentlink.Hello{Hostname: "h-" + id, OS: "linux", Arch: "amd64", GtbVersion: "0.1.0", Protocol: agentlink.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestOpenStore_Tables(t *testing.T) {
	s := openTest(t)
	for _, tbl := range []string{"machines", "tokens", "enroll_codes", "rigs", "items", "artifacts", "events", "requests", "results", "web_sessions", "sessions", "schema_version", "admin"} {
		var n int
		if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing (n=%d err=%v)", tbl, n, err)
		}
	}
	var v int
	if err := s.db.QueryRow(`SELECT version FROM schema_version`).Scan(&v); err != nil || v != schemaVersion {
		t.Errorf("schema_version = %d, %v", v, err)
	}
}

func TestAppendEvents_Idempotent(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	now := time.Now()
	evs := []agentlink.Event{
		{ID: agentlink.NewEventID(now), At: now, Type: "heartbeat"},
		{ID: agentlink.NewEventID(now), At: now, Type: "evidence", ItemID: "gt-1", Data: json.RawMessage(`{"x":1}`)},
	}
	for i := 0; i < 2; i++ {
		last, err := s.AppendEvents("m1", evs)
		if err != nil {
			t.Fatal(err)
		}
		if last != evs[1].ID {
			t.Fatalf("last = %q, want %q", last, evs[1].ID)
		}
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM events WHERE machine_id='m1'`).Scan(&n)
	if n != 2 {
		t.Fatalf("events stored = %d, want 2", n)
	}
	var cur string
	s.db.QueryRow(`SELECT cursor FROM machines WHERE id='m1'`).Scan(&cur)
	if cur != evs[1].ID {
		t.Fatalf("cursor = %q", cur)
	}
}

func TestAppendEvents_ProjectsItems(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	now := time.Now()
	iv := agentlink.ItemView{ID: "gt-1", Rig: "r", Kind: "feature", Step: "spec", Title: "T", WaitingForHuman: "spec approval"}
	_, err := s.AppendEvents("m1", []agentlink.Event{
		{ID: agentlink.NewEventID(now), At: now, Type: "item.upsert", ItemID: "gt-1", Data: mustJSON(t, iv)},
		{ID: agentlink.NewEventID(now), At: now, Type: "artifact", ItemID: "gt-1", Data: mustJSON(t, agentlink.ArtifactView{Name: "spec", SHA: "abc", Content: "# s"})},
		{ID: agentlink.NewEventID(now), At: now, Type: "session", Data: mustJSON(t, agentlink.SessionView{Name: "p1", Role: "polecat", Rig: "r", ItemID: "gt-1", State: "working", LastActivity: now})},
	})
	if err != nil {
		t.Fatal(err)
	}
	row, arts, err := s.Item("m1", "gt-1")
	if err != nil {
		t.Fatal(err)
	}
	if row.Title != "T" || row.MachineID != "m1" || row.WaitingForHuman != "spec approval" {
		t.Fatalf("row = %+v", row)
	}
	if len(arts) != 1 || arts[0].SHA != "abc" || arts[0].Content != "# s" {
		t.Fatalf("arts = %+v", arts)
	}
	waiting, _ := s.Items(ItemFilter{WaitingOnly: true})
	if len(waiting) != 1 {
		t.Fatalf("waiting = %d", len(waiting))
	}
	var nsess int
	s.db.QueryRow(`SELECT count(*) FROM sessions WHERE machine_id='m1'`).Scan(&nsess)
	if nsess != 1 {
		t.Fatalf("sessions = %d", nsess)
	}

	// Update, then delete.
	iv.Step = "plan"
	iv.WaitingForHuman = ""
	if _, err := s.AppendEvents("m1", []agentlink.Event{{ID: agentlink.NewEventID(now), At: now, Type: "item.upsert", ItemID: "gt-1", Data: mustJSON(t, iv)}}); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.Items(ItemFilter{MachineID: "m1", Kind: "feature"})
	if len(rows) != 1 || rows[0].Step != "plan" {
		t.Fatalf("rows = %+v", rows)
	}
	if waiting, _ := s.Items(ItemFilter{WaitingOnly: true}); len(waiting) != 0 {
		t.Fatalf("waiting after update = %d", len(waiting))
	}
	if _, err := s.AppendEvents("m1", []agentlink.Event{{ID: agentlink.NewEventID(now), At: now, Type: "item.deleted", ItemID: "gt-1"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Item("m1", "gt-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete err = %v", err)
	}
	var na int
	s.db.QueryRow(`SELECT count(*) FROM artifacts`).Scan(&na)
	if na != 0 {
		t.Fatalf("artifacts after delete = %d", na)
	}
}

func TestApplySnapshot_ReplacesPriorState(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	addMachine(t, s, "m2")
	snap1 := agentlink.Snapshot{
		Rigs:     []agentlink.Rig{{Name: "a", Remote: "git@x:a", Gated: true}, {Name: "b"}},
		Items:    []agentlink.ItemView{{ID: "gt-1", Rig: "a", Artifacts: []agentlink.ArtifactView{{Name: "spec", SHA: "1"}}}, {ID: "gt-2", Rig: "b"}},
		Sessions: []agentlink.SessionView{{Name: "p1"}},
		Cursor:   "C1",
	}
	if err := s.ApplySnapshot("m1", snap1); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplySnapshot("m2", agentlink.Snapshot{Items: []agentlink.ItemView{{ID: "gt-9"}}, Cursor: "Z"}); err != nil {
		t.Fatal(err)
	}
	snap2 := agentlink.Snapshot{
		Rigs:   []agentlink.Rig{{Name: "c"}},
		Items:  []agentlink.ItemView{{ID: "gt-3", Rig: "c", Artifacts: []agentlink.ArtifactView{{Name: "plan", SHA: "2", Content: "p"}}}},
		Cursor: "C2",
	}
	if err := s.ApplySnapshot("m1", snap2); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Items(ItemFilter{MachineID: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "gt-3" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Artifacts != nil {
		t.Fatalf("Items should not carry artifact content: %+v", rows[0].Artifacts)
	}
	_, arts, _ := s.Item("m1", "gt-3")
	if len(arts) != 1 || arts[0].Name != "plan" {
		t.Fatalf("arts = %+v", arts)
	}
	count := func(q string) int {
		var n int
		if err := s.db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM rigs WHERE machine_id='m1'`); n != 1 {
		t.Fatalf("rigs = %d", n)
	}
	if n := count(`SELECT count(*) FROM artifacts WHERE machine_id='m1'`); n != 1 {
		t.Fatalf("artifacts = %d", n)
	}
	if n := count(`SELECT count(*) FROM sessions WHERE machine_id='m1'`); n != 0 {
		t.Fatalf("sessions = %d", n)
	}
	if n := count(`SELECT count(*) FROM items WHERE machine_id='m2'`); n != 1 {
		t.Fatalf("other machine touched: %d", n)
	}
	var cur string
	s.db.QueryRow(`SELECT cursor FROM machines WHERE id='m1'`).Scan(&cur)
	if cur != "C2" {
		t.Fatalf("cursor = %q", cur)
	}
}

func TestMachineByToken_HashOnly(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	raw := "gtbm_supersecret_token_value"
	if err := s.AddToken("m1", sha(raw)); err != nil {
		t.Fatal(err)
	}
	m, err := s.MachineByToken(sha(raw))
	if err != nil || m.ID != "m1" || m.Hostname != "h-m1" {
		t.Fatalf("m=%+v err=%v", m, err)
	}
	if _, err := s.MachineByToken(raw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("raw token lookup err = %v", err)
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM tokens WHERE sha = ? OR machine_id = ? OR instr(sha, ?) > 0`, raw, raw, raw).Scan(&n)
	if n != 0 {
		t.Fatal("raw token stored")
	}
}

func TestMachineByToken_Revoked(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	addMachine(t, s, "m2")
	s.AddToken("m1", sha("a"))
	s.AddToken("m1", sha("b"))
	s.AddToken("m2", sha("c"))
	if err := s.RevokeToken(sha("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MachineByToken(sha("a")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token err = %v", err)
	}
	if _, err := s.MachineByToken(sha("b")); err != nil {
		t.Fatalf("other token err = %v", err)
	}
	if err := s.RevokeMachine("m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MachineByToken(sha("b")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked machine err = %v", err)
	}
	if _, err := s.MachineByToken(sha("c")); err != nil {
		t.Fatalf("m2 err = %v", err)
	}
	// A revoked machine stays revoked even if a token row were un-revoked.
	s.db.Exec(`UPDATE tokens SET revoked_at = NULL WHERE machine_id='m1'`)
	if _, err := s.MachineByToken(sha("b")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("machine revocation not enforced: %v", err)
	}
	// Re-hello does not un-revoke.
	addMachine(t, s, "m1")
	if _, err := s.MachineByToken(sha("b")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("upsert un-revoked machine: %v", err)
	}
}

func TestEnqueueRequest_UnknownType(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	err := s.EnqueueRequest("m1", agentlink.Request{Key: "k", Type: "shell", ExpiresAt: time.Now().Add(time.Hour)})
	if !errors.Is(err, ErrUnknownRequestType) {
		t.Fatalf("err = %v", err)
	}
}

func TestRequests_ExpiredNotDelivered(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	now := time.Now()
	// Enqueued 2 s ago with a 1 s lifetime: expired 1 s ago.
	if err := s.enqueueRequest("m1", agentlink.Request{Key: "old", Type: "snapshot", ExpiresAt: now.Add(-time.Second)}, now.Add(-2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueRequest("m1", agentlink.Request{Key: "new", Type: "approve", ItemID: "gt-1", Params: mustJSON(t, agentlink.ApproveParams{Artifact: "spec", SHA: "abc", Actor: "baovo15"}), ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// Duplicate key is a no-op.
	if err := s.EnqueueRequest("m1", agentlink.Request{Key: "new", Type: "item.cancel", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	pend, err := s.PendingRequests("m1", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 1 || pend[0].Key != "new" || pend[0].Type != "approve" || pend[0].ItemID != "gt-1" {
		t.Fatalf("pending = %+v", pend)
	}
	var ap agentlink.ApproveParams
	if err := json.Unmarshal(pend[0].Params, &ap); err != nil || ap.SHA != "abc" {
		t.Fatalf("params = %s %v", pend[0].Params, err)
	}
	n, err := s.ExpireRequests(now)
	if err != nil || n != 1 {
		t.Fatalf("expired n=%d err=%v", n, err)
	}
	st, res, err := s.RequestStatus("m1", "old", now)
	if err != nil || st != "expired" || res != nil {
		t.Fatalf("status=%q res=%v err=%v", st, res, err)
	}
	if n, _ := s.ExpireRequests(now); n != 0 {
		t.Fatalf("second expire n=%d", n)
	}
}

func TestResults_DoneAndStatus(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	now := time.Now()
	s.EnqueueRequest("m1", agentlink.Request{Key: "k1", Type: "inspect", ExpiresAt: now.Add(time.Hour)})
	st, res, err := s.RequestStatus("m1", "k1", now)
	if err != nil || st != "pending" || res != nil {
		t.Fatalf("status=%q res=%v err=%v", st, res, err)
	}
	r := agentlink.Result{Key: "k1", OK: false, Error: "sha changed", Data: json.RawMessage(`{"a":1}`)}
	if err := s.AddResult("m1", r); err != nil {
		t.Fatal(err)
	}
	if err := s.AddResult("m1", agentlink.Result{Key: "k1", OK: true}); err != nil {
		t.Fatal(err)
	}
	st, res, err = s.RequestStatus("m1", "k1", now)
	if err != nil || st != "done" || res == nil || res.OK || res.Error != "sha changed" || string(res.Data) != `{"a":1}` {
		t.Fatalf("status=%q res=%+v err=%v", st, res, err)
	}
	if pend, _ := s.PendingRequests("m1", now); len(pend) != 0 {
		t.Fatalf("pending after result = %+v", pend)
	}
	// An on-time result keeps the request done, even after expires_at passes.
	if n, _ := s.ExpireRequests(now.Add(2 * time.Hour)); n != 0 {
		t.Fatalf("expired done request: %d", n)
	}
	if st, _, _ := s.RequestStatus("m1", "k1", now.Add(2*time.Hour)); st != "done" {
		t.Fatalf("on-time result later read as %q", st)
	}
	if _, _, err := s.RequestStatus("m1", "nope", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestOpenStore_Pragmas(t *testing.T) {
	s := openTest(t)
	var mode string
	var fk, bt int
	s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&bt)
	if mode != "wal" || fk != 1 || bt != 5000 {
		t.Fatalf("journal_mode=%q foreign_keys=%d busy_timeout=%d", mode, fk, bt)
	}
}

func TestAppendEvents_UnknownTypeRejected(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	now := time.Now()
	_, err := s.AppendEvents("m1", []agentlink.Event{
		{ID: agentlink.NewEventID(now), At: now, Type: "heartbeat"},
		{ID: agentlink.NewEventID(now), At: now, Type: "bogus"},
	})
	if err == nil {
		t.Fatal("want error")
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM events`).Scan(&n)
	if n != 0 {
		t.Fatalf("partial batch stored: %d", n)
	}
}

// R32: a result recorded after the request expired is stored for audit but
// leaves the request expired.
func TestResults_LateAfterSweep(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	now := time.Now()
	if err := s.enqueueRequest("m1", agentlink.Request{Key: "k", Type: "approve", ExpiresAt: now.Add(time.Second)}, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	if n, _ := s.ExpireRequests(later); n != 1 {
		t.Fatalf("expired = %d", n)
	}
	if err := s.addResult("m1", agentlink.Result{Key: "k", OK: true, Data: json.RawMessage(`{"late":true}`)}, later); err != nil {
		t.Fatal(err)
	}
	st, res, err := s.RequestStatus("m1", "k", later)
	if err != nil || st != "expired" {
		t.Fatalf("status=%q err=%v", st, err)
	}
	if res == nil || !res.OK || string(res.Data) != `{"late":true}` {
		t.Fatalf("late result not retrievable: %+v", res)
	}
}

func TestResults_LateBeforeSweep(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	now := time.Now()
	s.enqueueRequest("m1", agentlink.Request{Key: "k", Type: "inspect", ExpiresAt: now.Add(time.Second)}, now)
	// Recorded exactly at expires_at: late.
	if err := s.addResult("m1", agentlink.Result{Key: "k", OK: true}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	st, res, err := s.RequestStatus("m1", "k", now.Add(time.Second))
	if err != nil || st != "expired" || res == nil {
		t.Fatalf("status=%q res=%v err=%v", st, res, err)
	}
	if pend, _ := s.PendingRequests("m1", now); len(pend) != 0 {
		t.Fatalf("pending = %+v", pend)
	}
}

func TestResults_OnTimeDone(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	now := time.Now()
	s.enqueueRequest("m1", agentlink.Request{Key: "k", Type: "inspect", ExpiresAt: now.Add(time.Second)}, now)
	if err := s.addResult("m1", agentlink.Result{Key: "k", OK: true}, now.Add(999*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if st, res, err := s.RequestStatus("m1", "k", now.Add(time.Hour)); err != nil || st != "done" || res == nil {
		t.Fatalf("status=%q res=%v err=%v", st, res, err)
	}
}

func TestRequestStatus_ExpiredWithoutSweep(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	now := time.Now()
	s.enqueueRequest("m1", agentlink.Request{Key: "k", Type: "inspect", ExpiresAt: now.Add(time.Second)}, now)
	if st, _, _ := s.RequestStatus("m1", "k", now); st != "pending" {
		t.Fatalf("before expiry = %q", st)
	}
	if st, res, err := s.RequestStatus("m1", "k", now.Add(time.Second)); err != nil || st != "expired" || res != nil {
		t.Fatalf("at expiry status=%q res=%v err=%v", st, res, err)
	}
}

func TestEnqueueRequest_InvalidExpiry(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	for name, exp := range map[string]time.Time{
		"zero": {},
		"past": time.Now().Add(-time.Second),
	} {
		err := s.EnqueueRequest("m1", agentlink.Request{Key: name, Type: "inspect", ExpiresAt: exp})
		if !errors.Is(err, ErrInvalidExpiry) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	now := time.Now()
	if err := s.enqueueRequest("m1", agentlink.Request{Key: "eq", Type: "inspect", ExpiresAt: now}, now); !errors.Is(err, ErrInvalidExpiry) {
		t.Errorf("at now: err = %v", err)
	}
}

func TestOpenStore_MigratesV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gtb.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	addMachine(t, s, "m1")
	// Turn it back into a v1 database.
	if _, err := s.db.Exec(`ALTER TABLE machines DROP COLUMN has_snapshot`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE schema_version SET version = 1`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v int
	if err := s.db.QueryRow(`SELECT version FROM schema_version`).Scan(&v); err != nil || v != schemaVersion {
		t.Fatalf("schema_version = %d, %v", v, err)
	}
	m, err := s.Machine("m1")
	if err != nil || m.HasSnapshot {
		t.Fatalf("machine = %+v, %v", m, err)
	}
	if err := s.ApplySnapshot("m1", agentlink.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Machine("m1"); !m.HasSnapshot {
		t.Fatal("has_snapshot not set after snapshot")
	}
}

func TestOpenStore_MigratesV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gtb.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE admin`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE schema_version SET version = 2`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v int
	if err := s.db.QueryRow(`SELECT version FROM schema_version`).Scan(&v); err != nil || v != 3 {
		t.Fatalf("schema_version = %d, %v", v, err)
	}
	if _, err := s.Admin(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("admin after migration: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO admin(id, token_sha, csrf_key, created_at) VALUES (2, 'x', x'00', 0)`); err == nil {
		t.Fatal("admin accepted id != 1")
	}
}
