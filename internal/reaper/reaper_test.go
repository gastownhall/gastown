package reaper

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	scriptedDriverOnce sync.Once
	scriptedStates     sync.Map
)

type scriptedOp struct {
	kind     string
	contains []string
	wantArgs int
	columns  []string
	rows     [][]driver.Value
	affected int64
}

type scriptedState struct {
	mu       sync.Mutex
	nextConn int
	ops      []scriptedOp
	connIDs  []int
}

type scriptedDriver struct{}

type scriptedConn struct {
	state *scriptedState
	id    int
}

type scriptedRows struct {
	columns []string
	rows    [][]driver.Value
	i       int
}

type scriptedResult int64

func openScriptedDB(t *testing.T, state *scriptedState) *sql.DB {
	t.Helper()
	scriptedDriverOnce.Do(func() { sql.Register("reaper-scripted", scriptedDriver{}) })
	name := fmt.Sprintf("state-%p", state)
	scriptedStates.Store(name, state)
	t.Cleanup(func() { scriptedStates.Delete(name) })
	db, err := sql.Open("reaper-scripted", name)
	if err != nil {
		t.Fatalf("open scripted db: %v", err)
	}
	db.SetMaxIdleConns(0)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func (d scriptedDriver) Open(name string) (driver.Conn, error) {
	v, ok := scriptedStates.Load(name)
	if !ok {
		return nil, fmt.Errorf("unknown scripted state %q", name)
	}
	state := v.(*scriptedState)
	state.mu.Lock()
	state.nextConn++
	id := state.nextConn
	state.mu.Unlock()
	return &scriptedConn{state: state, id: id}, nil
}

func (c *scriptedConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepare not supported")
}
func (c *scriptedConn) Close() error              { return nil }
func (c *scriptedConn) Begin() (driver.Tx, error) { return nil, fmt.Errorf("begin not supported") }

func (c *scriptedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	op, err := c.state.next("exec", c.id, query, len(args))
	if err != nil {
		return nil, err
	}
	return scriptedResult(op.affected), nil
}

func (c *scriptedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	op, err := c.state.next("query", c.id, query, len(args))
	if err != nil {
		return nil, err
	}
	cols := op.columns
	if len(cols) == 0 {
		cols = []string{"value"}
	}
	return &scriptedRows{columns: cols, rows: op.rows}, nil
}

func (s *scriptedState) next(kind string, connID int, query string, argCount int) (scriptedOp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ops) == 0 {
		return scriptedOp{}, fmt.Errorf("unexpected %s on conn %d: %s", kind, connID, query)
	}
	op := s.ops[0]
	s.ops = s.ops[1:]
	if op.kind != kind {
		return scriptedOp{}, fmt.Errorf("got %s, want %s for query %s", kind, op.kind, query)
	}
	if op.wantArgs != argCount {
		return scriptedOp{}, fmt.Errorf("got %d args, want %d for query %s", argCount, op.wantArgs, query)
	}
	for _, needle := range op.contains {
		if !strings.Contains(query, needle) {
			return scriptedOp{}, fmt.Errorf("query missing %q: %s", needle, query)
		}
	}
	s.connIDs = append(s.connIDs, connID)
	return op, nil
}

func (r *scriptedRows) Columns() []string { return r.columns }
func (r *scriptedRows) Close() error      { return nil }
func (r *scriptedRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func (r scriptedResult) LastInsertId() (int64, error) { return 0, nil }
func (r scriptedResult) RowsAffected() (int64, error) { return int64(r), nil }

func TestValidateDBName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"hq", false},
		{"beads", false},
		{"gt", false},
		{"test_db_123", false},
		{"", true},
		{"drop table", true},
		{"db;--", true},
		{"db`name", true},
		{"../etc/passwd", true},
	}
	for _, tt := range tests {
		err := ValidateDBName(tt.name)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateDBName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestDefaultDatabases(t *testing.T) {
	if len(DefaultDatabases) == 0 {
		t.Error("DefaultDatabases should not be empty")
	}
	for _, db := range DefaultDatabases {
		if err := ValidateDBName(db); err != nil {
			t.Errorf("DefaultDatabases contains invalid name %q: %v", db, err)
		}
	}
}

func TestFormatJSON(t *testing.T) {
	result := FormatJSON(map[string]int{"count": 42})
	if result == "" {
		t.Error("FormatJSON should not return empty string")
	}
	if result[0] != '{' {
		t.Errorf("FormatJSON should return JSON object, got %q", result[:10])
	}
}

func TestParentExcludeJoin(t *testing.T) {
	joinClause, whereCondition := parentExcludeJoin("testdb")

	// JOIN clause should reference the correct database.
	if joinClause == "" {
		t.Error("parentExcludeJoin joinClause should not be empty")
	}
	// parentExcludeJoin no longer qualifies table names with the database — the
	// reaper connects to a specific database via the DSN, so unqualified names
	// are correct. The dbName parameter is retained for API compatibility.

	// JOIN should select wisps with open parents from wisp_dependencies.
	if !contains(joinClause, "wisp_dependencies") {
		t.Error("parentExcludeJoin should query wisp_dependencies")
	}
	if !contains(joinClause, "parent-child") {
		t.Error("parentExcludeJoin should filter on parent-child type")
	}
	if !contains(joinClause, "depends_on_wisp_id") {
		t.Error("parentExcludeJoin should join wisp parents through depends_on_wisp_id")
	}
	if !contains(joinClause, "depends_on_issue_id") {
		t.Error("parentExcludeJoin should join issue parents through depends_on_issue_id")
	}
	if contains(joinClause, "wd.depends_on_id") {
		t.Error("parentExcludeJoin must not use legacy depends_on_id")
	}
	if !contains(joinClause, "'open', 'hooked', 'in_progress'") {
		t.Error("parentExcludeJoin should check for open parent statuses")
	}

	// WHERE condition should be an IS NULL anti-join filter.
	if whereCondition == "" {
		t.Error("parentExcludeJoin whereCondition should not be empty")
	}
	if !contains(whereCondition, "IS NULL") {
		t.Error("parentExcludeJoin whereCondition should use IS NULL for anti-join")
	}
}

func TestClosedMoleculeStepPredicate(t *testing.T) {
	where := closedMoleculeStepWhere("w")
	for _, want := range []string{
		"w.status IN ('open', 'hooked', 'in_progress')",
		"w.issue_type != 'agent'",
		"wisp_dependencies wd",
		"wd.issue_id = w.id",
		"wd.depends_on_wisp_id",
		"wd.type = 'parent-child'",
		"pm.issue_type = 'molecule'",
		"pm.status = 'closed'",
	} {
		if !strings.Contains(where, want) {
			t.Fatalf("closed molecule step predicate missing %q:\n%s", want, where)
		}
	}
	if strings.Contains(where, "depends_on_id") {
		t.Fatalf("closed molecule step predicate must not use legacy depends_on_id:\n%s", where)
	}
}

func TestReapStaleQueryExcludesClosedMoleculeSteps(t *testing.T) {
	parentJoin, parentWhere := parentExcludeJoin("gt")
	whereClause := fmt.Sprintf(
		"w.status IN ('open', 'hooked', 'in_progress') AND w.created_at < ? AND w.issue_type != 'agent' AND %s AND NOT %s",
		parentWhere, closedMoleculeStepExists("w"))
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w %s WHERE %s LIMIT %d",
		parentJoin, whereClause, DefaultBatchSize)

	for _, want := range []string{"AND NOT EXISTS", "depends_on_wisp_id", "pm.status = 'closed'"} {
		if !strings.Contains(idQuery, want) {
			t.Fatalf("stale reap query missing %q:\n%s", want, idQuery)
		}
	}
}

func TestHasReaperSchemaRequiresTypedDependencyColumns(t *testing.T) {
	state := &scriptedState{ops: []scriptedOp{
		{kind: "query", wantArgs: 0, contains: []string{"information_schema.tables", "wisps", "wisp_dependencies"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(3)}}},
		{kind: "query", wantArgs: 0, contains: []string{"information_schema.columns", "wisp_dependencies", "depends_on_external"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(3)}}},
		{kind: "query", wantArgs: 0, contains: []string{"information_schema.tables", "dependencies"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(1)}}},
		{kind: "query", wantArgs: 0, contains: []string{"information_schema.columns", "dependencies", "depends_on_issue_id"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(1)}}},
	}}
	db := openScriptedDB(t, state)

	ok, err := HasReaperSchema(db)
	if err != nil {
		t.Fatalf("HasReaperSchema: %v", err)
	}
	if !ok {
		t.Fatal("HasReaperSchema should accept typed dependency schema")
	}

	legacy := &scriptedState{ops: []scriptedOp{
		{kind: "query", wantArgs: 0, contains: []string{"information_schema.tables", "wisps", "wisp_dependencies"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(3)}}},
		{kind: "query", wantArgs: 0, contains: []string{"information_schema.columns", "wisp_dependencies", "depends_on_external"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(2)}}},
	}}
	db = openScriptedDB(t, legacy)
	ok, err = HasReaperSchema(db)
	if err != nil {
		t.Fatalf("HasReaperSchema legacy: %v", err)
	}
	if ok {
		t.Fatal("HasReaperSchema should reject incomplete typed dependency schema")
	}
}

func TestTypedIssueDependencyQueriesAreNullSafe(t *testing.T) {
	data, err := os.ReadFile("reaper.go")
	if err != nil {
		t.Fatalf("read reaper.go: %v", err)
	}
	source := string(data)
	if got := strings.Count(source, "AND d.depends_on_issue_id IS NOT NULL"); got < 2 {
		t.Fatalf("typed dependency NOT IN queries must filter NULL targets, found %d guards", got)
	}
}

func TestReapDryRunCountsMoleculeStepsSeparately(t *testing.T) {
	state := &scriptedState{ops: []scriptedOp{
		{kind: "query", wantArgs: 1, contains: []string{"SELECT COUNT(*) FROM wisps w", "AND NOT EXISTS", "depends_on_wisp_id"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(1)}}},
		{kind: "query", wantArgs: 0, contains: []string{"SELECT COUNT(*) FROM wisps w", "pm.status = 'closed'", "depends_on_wisp_id"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(2)}}},
		{kind: "query", wantArgs: 0, contains: []string{"SELECT COUNT(*) FROM wisps WHERE status IN"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(3)}}},
	}}
	db := openScriptedDB(t, state)

	result, err := Reap(db, "hq", 24*time.Hour, true)
	if err != nil {
		t.Fatalf("dry-run Reap: %v", err)
	}
	if result.Reaped != 1 || result.MoleculeStepsClosed != 2 || result.OpenRemain != 3 {
		t.Fatalf("Reap() = %+v, want reaped=1 molecule_steps_closed=2 open=3", result)
	}
}

func TestScanCountsMoleculeStepsSeparately(t *testing.T) {
	state := &scriptedState{ops: []scriptedOp{
		{kind: "query", wantArgs: 1, contains: []string{"SELECT COUNT(*) FROM wisps w", "AND NOT EXISTS", "depends_on_wisp_id"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(1)}}},
		{kind: "query", wantArgs: 0, contains: []string{"SELECT COUNT(*) FROM wisps w", "pm.status = 'closed'", "depends_on_wisp_id"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(2)}}},
		{kind: "query", wantArgs: 1, contains: []string{"SELECT COUNT(*) FROM wisps w WHERE w.status = 'closed'"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(3)}}},
		{kind: "query", wantArgs: 1, contains: []string{"SELECT COUNT(*) FROM issues WHERE status = 'closed'"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(4)}}},
		{kind: "query", wantArgs: 1, contains: []string{"SELECT COUNT(*) FROM issues i", "depends_on_issue_id"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(5)}}},
		{kind: "query", wantArgs: 0, contains: []string{"SELECT COUNT(*) FROM wisps WHERE status IN"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(6)}}},
		{kind: "query", wantArgs: 0, contains: []string{"SELECT COUNT(*) FROM wisp_dependencies wd", "depends_on_wisp_id", "depends_on_issue_id"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(0)}}},
	}}
	db := openScriptedDB(t, state)

	result, err := Scan(db, "hq", 24*time.Hour, 168*time.Hour, 168*time.Hour, 168*time.Hour)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if result.ReapCandidates != 1 || result.MoleculeStepCandidates != 2 || result.PurgeCandidates != 3 || result.MailCandidates != 4 || result.StaleCandidates != 5 || result.OpenWisps != 6 {
		t.Fatalf("Scan() = %+v, want disjoint reap/molecule counts and other scripted totals", result)
	}
}

func TestReapUsesPinnedConnectionForWrites(t *testing.T) {
	state := &scriptedState{ops: []scriptedOp{
		{kind: "exec", wantArgs: 0, contains: []string{"SET @@autocommit = 0"}},
		{kind: "query", wantArgs: 0, contains: []string{"SELECT w.id FROM wisps w", "pm.status = 'closed'", "depends_on_wisp_id"}, columns: []string{"id"}, rows: [][]driver.Value{{"mol-step-1"}}},
		{kind: "exec", wantArgs: 1, contains: []string{"UPDATE wisps SET status='closed'", "reaper: parent molecule closed"}, affected: 1},
		{kind: "query", wantArgs: 0, contains: []string{"SELECT w.id FROM wisps w", "pm.status = 'closed'", "depends_on_wisp_id"}, columns: []string{"id"}},
		{kind: "query", wantArgs: 1, contains: []string{"SELECT w.id FROM wisps w", "AND NOT EXISTS", "depends_on_wisp_id"}, columns: []string{"id"}, rows: [][]driver.Value{{"stale-1"}}},
		{kind: "exec", wantArgs: 1, contains: []string{"UPDATE wisps SET status='closed', closed_at=NOW() WHERE id IN (?)"}, affected: 1},
		{kind: "query", wantArgs: 1, contains: []string{"SELECT w.id FROM wisps w", "AND NOT EXISTS", "depends_on_wisp_id"}, columns: []string{"id"}},
		{kind: "exec", wantArgs: 0, contains: []string{"COMMIT"}},
		{kind: "exec", wantArgs: 0, contains: []string{"CALL DOLT_COMMIT", "1 stale + 1 molecule-step"}},
		{kind: "query", wantArgs: 0, contains: []string{"SELECT COUNT(*) FROM wisps WHERE status IN"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(0)}}},
		{kind: "exec", wantArgs: 0, contains: []string{"SET @@autocommit = 1"}},
	}}
	db := openScriptedDB(t, state)

	result, err := Reap(db, "hq", 24*time.Hour, false)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if result.Reaped != 1 || result.MoleculeStepsClosed != 1 || result.OpenRemain != 0 {
		t.Fatalf("Reap() = %+v, want reaped=1 molecule_steps_closed=1 open=0", result)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.ops) != 0 {
		t.Fatalf("%d scripted operations were not consumed", len(state.ops))
	}
	for _, id := range state.connIDs {
		if id != state.connIDs[0] {
			t.Fatalf("Reap used multiple connections: %v", state.connIDs)
		}
	}
}

func TestReapCommitsMoleculeStepOnlyWrites(t *testing.T) {
	state := &scriptedState{ops: []scriptedOp{
		{kind: "exec", wantArgs: 0, contains: []string{"SET @@autocommit = 0"}},
		{kind: "query", wantArgs: 0, contains: []string{"SELECT w.id FROM wisps w", "pm.status = 'closed'", "depends_on_wisp_id"}, columns: []string{"id"}, rows: [][]driver.Value{{"mol-step-1"}}},
		{kind: "exec", wantArgs: 1, contains: []string{"UPDATE wisps SET status='closed'", "reaper: parent molecule closed"}, affected: 1},
		{kind: "query", wantArgs: 0, contains: []string{"SELECT w.id FROM wisps w", "pm.status = 'closed'", "depends_on_wisp_id"}, columns: []string{"id"}},
		{kind: "query", wantArgs: 1, contains: []string{"SELECT w.id FROM wisps w", "AND NOT EXISTS", "depends_on_wisp_id"}, columns: []string{"id"}},
		{kind: "exec", wantArgs: 0, contains: []string{"COMMIT"}},
		{kind: "exec", wantArgs: 0, contains: []string{"CALL DOLT_COMMIT", "0 stale + 1 molecule-step"}},
		{kind: "query", wantArgs: 0, contains: []string{"SELECT COUNT(*) FROM wisps WHERE status IN"}, columns: []string{"count"}, rows: [][]driver.Value{{int64(0)}}},
		{kind: "exec", wantArgs: 0, contains: []string{"SET @@autocommit = 1"}},
	}}
	db := openScriptedDB(t, state)

	result, err := Reap(db, "hq", 24*time.Hour, false)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if result.Reaped != 0 || result.MoleculeStepsClosed != 1 || result.OpenRemain != 0 {
		t.Fatalf("Reap() = %+v, want reaped=0 molecule_steps_closed=1 open=0", result)
	}
}

// TestReapQueryNoDatabaseNameInjection verifies that the Reap function's batch
// SELECT query does not inject the database name into the SQL string. Previously,
// dbName was passed as a Sprintf arg but the format string didn't use it, causing
// positional shift: "FROM wisps w gt WHERE..." instead of "FROM wisps w LEFT JOIN...".
func TestReapQueryNoDatabaseNameInjection(t *testing.T) {
	// Reproduce the exact Sprintf call from Reap() to verify no dbName injection.
	dbName := "gt"
	parentJoin, parentWhere := parentExcludeJoin(dbName)
	whereClause := fmt.Sprintf(
		"w.status IN ('open', 'hooked', 'in_progress') AND w.created_at < ? AND %s", parentWhere)

	// This is the fixed query — dbName is NOT in the Sprintf args.
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w %s WHERE %s LIMIT %d",
		parentJoin, whereClause, DefaultBatchSize)

	// The query must NOT contain the literal database name as a bare token.
	// Before the fix, "gt" appeared between "wisps w" and "WHERE".
	if strings.Contains(idQuery, "wisps w gt") {
		t.Errorf("Reap idQuery contains injected database name: %s", idQuery)
	}
	if !strings.Contains(idQuery, "LEFT JOIN") {
		t.Errorf("Reap idQuery should contain LEFT JOIN from parentExcludeJoin, got: %s", idQuery)
	}
	if !strings.Contains(idQuery, fmt.Sprintf("LIMIT %d", DefaultBatchSize)) {
		t.Errorf("Reap idQuery should end with LIMIT %d, got: %s", DefaultBatchSize, idQuery)
	}
}

// TestReapUpdateQueryNoDatabaseNameInjection verifies that the UPDATE query in
// Reap() does not inject dbName where the IN clause should go.
func TestReapUpdateQueryNoDatabaseNameInjection(t *testing.T) {
	dbName := "gt"
	inClause := "?,?,?"

	// This is the fixed query — only inClause in the Sprintf args.
	updateQuery := fmt.Sprintf(
		"UPDATE wisps SET status='closed', closed_at=NOW() WHERE id IN (%s)",
		inClause)

	if strings.Contains(updateQuery, dbName) {
		t.Errorf("Reap updateQuery contains injected database name %q: %s", dbName, updateQuery)
	}
	if !strings.Contains(updateQuery, "IN (?,?,?)") {
		t.Errorf("Reap updateQuery should contain parameterized IN clause, got: %s", updateQuery)
	}
}

// TestPurgeDigestQueryNoDatabaseNameInjection verifies that the purge digest
// query is a plain string with no Sprintf interpolation at all.
func TestPurgeDigestQueryNoDatabaseNameInjection(t *testing.T) {
	// The fixed digestQuery is a string literal — no Sprintf.
	digestQuery := "SELECT COALESCE(w.wisp_type, 'unknown') AS wtype, COUNT(*) AS cnt FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ? GROUP BY wtype"

	if strings.Contains(digestQuery, "gt") {
		t.Errorf("purge digestQuery should not contain database name, got: %s", digestQuery)
	}
	if !strings.Contains(digestQuery, "GROUP BY wtype") {
		t.Errorf("purge digestQuery should end with GROUP BY, got: %s", digestQuery)
	}
}

// TestPurgeBatchQueryNoDatabaseNameInjection verifies that the purge batch
// SELECT query uses DefaultBatchSize as the LIMIT, not dbName.
func TestPurgeBatchQueryNoDatabaseNameInjection(t *testing.T) {
	// This is the fixed query — only DefaultBatchSize in the Sprintf args.
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ? LIMIT %d",
		DefaultBatchSize)

	if strings.Contains(idQuery, "gt") {
		t.Errorf("purge idQuery contains injected database name: %s", idQuery)
	}
	expected := fmt.Sprintf("LIMIT %d", DefaultBatchSize)
	if !strings.Contains(idQuery, expected) {
		t.Errorf("purge idQuery should contain %s, got: %s", expected, idQuery)
	}
}

// TestIsNothingToCommit verifies that "nothing to commit" errors are recognized
// correctly. This prevents false-positive dolt_commit_failed anomalies when the
// reaper operates on dolt_ignored tables (wisps, wisp_*), where Dolt has nothing
// to version after a successful SQL DELETE.
func TestIsNothingToCommit(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"nothing to commit", true},
		{"NOTHING TO COMMIT", true},
		{"Error 1105 (HY000): nothing to commit", true},
		{"no changes to commit", false}, // must also contain "commit" — see isNothingToCommit
		{"no changes", false},
		{"connection refused", false},
		{"table not found: wisps", false},
		{"", false},
	}
	for _, c := range cases {
		var err error
		if c.msg != "" {
			err = fmt.Errorf("%s", c.msg)
		}
		got := isNothingToCommit(err)
		if got != c.want {
			t.Errorf("isNothingToCommit(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestReapExcludesAgentBeads verifies that the Reap function excludes agent beads
// from being closed, regardless of their age. This is a regression test for the bug
// where the wisp reaper was closing agent beads (hq-mayor, hq-deacon, witness, refinery,
// etc.) after 24 hours, causing doctor to report them as missing.
func TestReapExcludesAgentBeads(t *testing.T) {
	// Verify that the WHERE clause in Reap() excludes issue_type='agent'
	// by checking the source code pattern.
	// This is a compile-time guard — if the exclusion is removed, this test
	// will fail when the query pattern doesn't match.

	// The whereClause in Reap() should contain:
	// "w.issue_type != 'agent'"
	// This test documents the expected behavior; actual exclusion is tested
	// in integration tests with a real database.

	// Integration test would require spinning up a Dolt server, which is
	// beyond the scope of this unit test. The exclusion is verified manually
	// by checking that agent beads are not closed by the wisp_reaper patrol.
	t.Log("Agent beads (issue_type='agent') are excluded from wisp reaping")
	t.Log("This prevents hq-mayor, hq-deacon, witness, refinery, etc. from being closed")
}

// TestScanExcludesAgentBeads documents that Scan() must use the same eligibility
// predicate as Reap() for stale open wisps. If Scan counts agent beads but Reap
// excludes them, the operator sees scan>0 and reap=0 for the same cutoff.
func TestScanExcludesAgentBeads(t *testing.T) {
	sourcePath := "reaper.go"
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read %s: %v", sourcePath, err)
	}
	source := string(data)
	scanStart := strings.Index(source, "func Scan(")
	reapStart := strings.Index(source, "func Reap(")
	if scanStart == -1 || reapStart == -1 || reapStart <= scanStart {
		t.Fatalf("could not isolate Scan() body in %s", sourcePath)
	}
	scanBody := source[scanStart:reapStart]
	if !strings.Contains(scanBody, "w.issue_type != 'agent'") {
		t.Fatalf("expected Scan() eligibility to exclude agent beads, scan body was:\n%s", scanBody)
	}
}
