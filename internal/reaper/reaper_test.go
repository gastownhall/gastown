package reaper

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

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

func TestDogReaperFormulaAlertThresholdMatchesDefault(t *testing.T) {
	data, err := os.ReadFile("../formula/formulas/mol-dog-reaper.formula.toml")
	if err != nil {
		t.Fatalf("read mol-dog-reaper formula: %v", err)
	}

	threshold := fmt.Sprintf("%d", DefaultAlertThreshold)
	source := string(data)
	alertThresholdVars := sourceBetween(t, source, "[vars.alert_threshold]", "[vars.dry_run]")
	if !strings.Contains(alertThresholdVars, fmt.Sprintf("default = %q", threshold)) {
		t.Fatalf("mol-dog-reaper alert_threshold default should match DefaultAlertThreshold %s", threshold)
	}
	if !strings.Contains(source, fmt.Sprintf("default %s", threshold)) {
		t.Fatalf("mol-dog-reaper alert_threshold prose should document default %s", threshold)
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

	if joinClause == "" {
		t.Error("parentExcludeJoin joinClause should not be empty")
	}
	if !strings.Contains(joinClause, "wisp_dependencies") {
		t.Error("parentExcludeJoin should query wisp_dependencies")
	}
	if !strings.Contains(joinClause, "wd.depends_on_wisp_id") {
		t.Error("parentExcludeJoin should join wisp parents through depends_on_wisp_id")
	}
	if !strings.Contains(joinClause, "wd.depends_on_issue_id") {
		t.Error("parentExcludeJoin should join issue parents through depends_on_issue_id")
	}
	if strings.Contains(joinClause, "wd.depends_on_id") {
		t.Error("parentExcludeJoin should not use legacy depends_on_id")
	}
	if !strings.Contains(joinClause, "parent-child") {
		t.Error("parentExcludeJoin should filter on parent-child type")
	}
	if !strings.Contains(joinClause, "'open', 'hooked', 'in_progress'") {
		t.Error("parentExcludeJoin should check for open parent statuses")
	}
	if whereCondition == "" {
		t.Error("parentExcludeJoin whereCondition should not be empty")
	}
	if !strings.Contains(whereCondition, "IS NULL") {
		t.Error("parentExcludeJoin whereCondition should use IS NULL for anti-join")
	}
}

func TestReapQueryUsesParentExcludeJoin(t *testing.T) {
	dbName := "gt"
	parentJoin, parentWhere := parentExcludeJoin(dbName)
	whereClause := fmt.Sprintf(
		"w.status IN ('open', 'hooked', 'in_progress') AND w.created_at < ? AND %s", parentWhere)
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w %s WHERE %s LIMIT %d",
		parentJoin, whereClause, DefaultBatchSize)

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

func sourceBetween(t *testing.T, source, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(source, startMarker)
	if start == -1 {
		t.Fatalf("could not find %q", startMarker)
	}
	end := strings.Index(source[start:], endMarker)
	if end == -1 {
		t.Fatalf("could not find %q after %q", endMarker, startMarker)
	}
	return source[start : start+end]
}

func TestClosedMoleculeStepReapBehavior(t *testing.T) {
	now := time.Now().UTC()
	state := &fakeReaperState{
		wisps: map[string]*fakeWisp{
			"mol-closed":               {id: "mol-closed", status: "closed", issueType: "molecule", createdAt: now},
			"mol-open":                 {id: "mol-open", status: "open", issueType: "molecule", createdAt: now},
			"closed-epic":              {id: "closed-epic", status: "closed", issueType: "epic", createdAt: now},
			"step-closed-mol-recent":   {id: "step-closed-mol-recent", status: "open", issueType: "task", createdAt: now.Add(-1 * time.Hour)},
			"step-closed-mol-old":      {id: "step-closed-mol-old", status: "open", issueType: "task", createdAt: now.Add(-48 * time.Hour)},
			"step-mixed-parent-old":    {id: "step-mixed-parent-old", status: "open", issueType: "task", createdAt: now.Add(-48 * time.Hour)},
			"step-external-parent-old": {id: "step-external-parent-old", status: "open", issueType: "task", createdAt: now.Add(-48 * time.Hour)},
			"step-open-parent-old":     {id: "step-open-parent-old", status: "open", issueType: "task", createdAt: now.Add(-48 * time.Hour)},
			"step-non-molecule-parent": {id: "step-non-molecule-parent", status: "open", issueType: "task", createdAt: now.Add(-48 * time.Hour)},
			"agent-step":               {id: "agent-step", status: "open", issueType: "agent", createdAt: now.Add(-48 * time.Hour)},
			"stale-orphan":             {id: "stale-orphan", status: "open", issueType: "task", createdAt: now.Add(-48 * time.Hour)},
			"fresh-orphan":             {id: "fresh-orphan", status: "open", issueType: "task", createdAt: now.Add(-1 * time.Hour)},
		},
		deps: []fakeDep{
			{issueID: "step-closed-mol-recent", dependsOnID: "mol-closed", depType: "parent-child"},
			{issueID: "step-closed-mol-old", dependsOnID: "mol-closed", depType: "parent-child"},
			{issueID: "step-mixed-parent-old", dependsOnID: "mol-closed", depType: "parent-child"},
			{issueID: "step-mixed-parent-old", dependsOnID: "mol-open", depType: "parent-child"},
			{issueID: "step-external-parent-old", dependsOnID: "mol-closed", depType: "parent-child"},
			{issueID: "step-external-parent-old", dependsOnExternal: "external:other", depType: "parent-child"},
			{issueID: "step-open-parent-old", dependsOnID: "mol-open", depType: "parent-child"},
			{issueID: "step-non-molecule-parent", dependsOnID: "closed-epic", depType: "parent-child"},
			{issueID: "agent-step", dependsOnID: "mol-closed", depType: "parent-child"},
		},
		ops: map[int][]string{},
	}
	db := openFakeReaperDB(t, state)
	t.Cleanup(func() { _ = db.Close() })

	maxAge := 24 * time.Hour
	scan, err := Scan(db, "testdb", maxAge, 7*24*time.Hour, 7*24*time.Hour, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if scan.MoleculeStepCandidates != 2 {
		t.Fatalf("Scan MoleculeStepCandidates = %d, want 2", scan.MoleculeStepCandidates)
	}
	if scan.ReapCandidates != 2 {
		t.Fatalf("Scan ReapCandidates = %d, want 2", scan.ReapCandidates)
	}

	beforeDryRun := state.statuses()
	dryRun, err := Reap(db, "testdb", maxAge, true)
	if err != nil {
		t.Fatalf("dry-run Reap: %v", err)
	}
	if dryRun.MoleculeStepsClosed != 2 {
		t.Fatalf("dry-run MoleculeStepsClosed = %d, want 2", dryRun.MoleculeStepsClosed)
	}
	if dryRun.Reaped != 2 {
		t.Fatalf("dry-run Reaped = %d, want 2", dryRun.Reaped)
	}
	if dryRun.OpenRemain != 10 {
		t.Fatalf("dry-run OpenRemain = %d, want 10", dryRun.OpenRemain)
	}
	if afterDryRun := state.statuses(); !reflect.DeepEqual(afterDryRun, beforeDryRun) {
		t.Fatalf("dry-run mutated statuses: before=%v after=%v", beforeDryRun, afterDryRun)
	}

	preRealOps := state.opCounts()
	realRun, err := Reap(db, "testdb", maxAge, false)
	if err != nil {
		t.Fatalf("real Reap: %v", err)
	}
	if realRun.MoleculeStepsClosed != 2 {
		t.Fatalf("real MoleculeStepsClosed = %d, want 2", realRun.MoleculeStepsClosed)
	}
	if realRun.Reaped != 2 {
		t.Fatalf("real Reaped = %d, want 2", realRun.Reaped)
	}
	if realRun.OpenRemain != 6 {
		t.Fatalf("real OpenRemain = %d, want 6", realRun.OpenRemain)
	}

	for _, id := range []string{"step-closed-mol-recent", "step-closed-mol-old", "step-non-molecule-parent", "stale-orphan"} {
		if got := state.status(id); got != "closed" {
			t.Fatalf("%s status = %q, want closed", id, got)
		}
	}
	for _, id := range []string{"step-mixed-parent-old", "step-external-parent-old", "step-open-parent-old", "agent-step", "fresh-orphan", "mol-open"} {
		if got := state.status(id); got != "open" {
			t.Fatalf("%s status = %q, want open", id, got)
		}
	}
	realOps := state.opsSince(preRealOps)
	if len(realOps) != 1 {
		t.Fatalf("real Reap used %d connections, want 1: %#v", len(realOps), realOps)
	}
	for connID, ops := range realOps {
		assertOpsContainInOrder(t, ops,
			"EXEC SET @@autocommit = 0",
			"QUERY SELECT w.id FROM wisps w INNER JOIN",
			"EXEC UPDATE wisps SET status='closed'",
			"QUERY SELECT w.id FROM wisps w LEFT JOIN",
			"EXEC UPDATE wisps SET status='closed'",
			"EXEC COMMIT",
			"EXEC CALL DOLT_COMMIT",
			"QUERY SELECT COUNT(*) FROM wisps WHERE status IN",
			"EXEC SET @@autocommit = 1",
		)
		t.Logf("real Reap used pinned connection %d", connID)
	}
}

var fakeReaperDriverID uint64

func openFakeReaperDB(t *testing.T, state *fakeReaperState) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:reaper_%d?mode=memory&cache=shared", atomic.AddUint64(&fakeReaperDriverID, 1))
	inner, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	if err := seedReaperSQL(inner, state); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}
	state.inner = inner

	driverName := fmt.Sprintf("fake_reaper_%d", atomic.AddUint64(&fakeReaperDriverID, 1))
	sql.Register(driverName, &fakeReaperDriver{state: state})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("open fake db: %v", err)
	}
	return db
}

func seedReaperSQL(db *sql.DB, state *fakeReaperState) error {
	schema := []string{
		`CREATE TABLE wisps (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			issue_type TEXT,
			created_at DATETIME NOT NULL,
			closed_at DATETIME,
			wisp_type TEXT
		)`,
		`CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			status TEXT,
			closed_at DATETIME,
			updated_at DATETIME,
			priority INTEGER,
			issue_type TEXT
		)`,
		`CREATE TABLE labels (
			issue_id TEXT NOT NULL,
			label TEXT NOT NULL
		)`,
		`CREATE TABLE dependencies (
			issue_id TEXT NOT NULL,
			depends_on_issue_id TEXT
		)`,
		`CREATE TABLE wisp_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_wisp_id TEXT,
			depends_on_issue_id TEXT,
			depends_on_external TEXT,
			type TEXT NOT NULL
		)`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	for _, w := range state.wisps {
		if _, err := db.Exec(
			`INSERT INTO wisps (id, status, issue_type, created_at) VALUES (?, ?, ?, ?)`,
			w.id, w.status, w.issueType, w.createdAt,
		); err != nil {
			return err
		}
	}
	for _, dep := range state.deps {
		var wispParent, issueParent, external any
		if dep.dependsOnID != "" {
			wispParent = dep.dependsOnID
		}
		if dep.dependsOnExternal != "" {
			external = dep.dependsOnExternal
		}
		if _, err := db.Exec(
			`INSERT INTO wisp_dependencies (issue_id, depends_on_wisp_id, depends_on_issue_id, depends_on_external, type) VALUES (?, ?, ?, ?, ?)`,
			dep.issueID, wispParent, issueParent, external, dep.depType,
		); err != nil {
			return err
		}
	}
	return nil
}

type fakeWisp struct {
	id        string
	status    string
	issueType string
	createdAt time.Time
}

type fakeDep struct {
	issueID           string
	dependsOnID       string
	dependsOnExternal string
	depType           string
}

type fakeReaperState struct {
	mu       sync.Mutex
	inner    *sql.DB
	wisps    map[string]*fakeWisp
	deps     []fakeDep
	nextConn int
	ops      map[int][]string
}

func (s *fakeReaperState) status(id string) string {
	var status string
	if err := s.inner.QueryRow(`SELECT status FROM wisps WHERE id = ?`, id).Scan(&status); err != nil {
		return ""
	}
	return status
}

func (s *fakeReaperState) statuses() map[string]string {
	rows, err := s.inner.Query(`SELECT id, status FROM wisps`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	statuses := map[string]string{}
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil
		}
		statuses[id] = status
	}
	return statuses
}

func (s *fakeReaperState) opCounts() map[int]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := make(map[int]int, len(s.ops))
	for connID, ops := range s.ops {
		counts[connID] = len(ops)
	}
	return counts
}

func (s *fakeReaperState) opsSince(counts map[int]int) map[int][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	opsSince := map[int][]string{}
	for connID, ops := range s.ops {
		start := counts[connID]
		if start < len(ops) {
			opsSince[connID] = append([]string(nil), ops[start:]...)
		}
	}
	return opsSince
}

func (s *fakeReaperState) record(connID int, op string) {
	s.ops[connID] = append(s.ops[connID], normalizeSQL(op))
}

type fakeReaperDriver struct {
	state *fakeReaperState
}

func (d *fakeReaperDriver) Open(string) (driver.Conn, error) {
	d.state.mu.Lock()
	defer d.state.mu.Unlock()
	d.state.nextConn++
	connID := d.state.nextConn
	d.state.ops[connID] = nil
	return &fakeReaperConn{state: d.state, id: connID}, nil
}

type fakeReaperConn struct {
	state *fakeReaperState
	id    int
}

func (c *fakeReaperConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepare not implemented")
}

func (c *fakeReaperConn) Close() error { return nil }

func (c *fakeReaperConn) Begin() (driver.Tx, error) { return fakeReaperTx{}, nil }

func (c *fakeReaperConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *fakeReaperConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	normalized := normalizeSQL(query)
	c.state.mu.Lock()
	c.state.record(c.id, "QUERY "+normalized)
	c.state.mu.Unlock()

	if strings.Contains(normalized, "created_at <") {
		if err := validateStaleWispQuery(normalized); err != nil {
			return nil, err
		}
	}
	if strings.Contains(normalized, "pm.issue_type = 'molecule'") {
		if err := validateMoleculeStepQuery(normalized); err != nil {
			return nil, err
		}
	}

	rows, err := c.state.inner.QueryContext(ctx, rewriteReaperSQL(query), namedValues(args)...)
	if err != nil {
		return nil, err
	}
	return collectDriverRows(rows)
}

func (c *fakeReaperConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	normalized := normalizeSQL(query)
	c.state.mu.Lock()
	c.state.record(c.id, "EXEC "+normalized)
	c.state.mu.Unlock()

	if isSessionSQL(normalized) {
		return fakeReaperResult(0), nil
	}

	result, err := c.state.inner.ExecContext(ctx, rewriteReaperSQL(query), namedValues(args)...)
	if err != nil {
		return nil, err
	}
	affected, _ := result.RowsAffected()
	return fakeReaperResult(affected), nil
}

func rewriteReaperSQL(query string) string {
	return strings.ReplaceAll(query, "NOW()", "datetime('now')")
}

func isSessionSQL(query string) bool {
	return query == "SET @@autocommit = 0" ||
		query == "SET @@autocommit = 1" ||
		query == "ROLLBACK" ||
		query == "COMMIT" ||
		strings.HasPrefix(query, "CALL DOLT_COMMIT")
}

func namedValues(args []driver.NamedValue) []any {
	out := make([]any, len(args))
	for i, arg := range args {
		out[i] = arg.Value
	}
	return out
}

func collectDriverRows(rows *sql.Rows) (driver.Rows, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var collected [][]driver.Value
	for rows.Next() {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		dest := make([]driver.Value, len(cols))
		for i, v := range raw {
			dest[i] = v
		}
		collected = append(collected, dest)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &fakeReaperRows{cols: cols, rows: collected}, nil
}

type fakeReaperTx struct{}

func (fakeReaperTx) Commit() error   { return nil }
func (fakeReaperTx) Rollback() error { return nil }

type fakeReaperResult int64

func (r fakeReaperResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeReaperResult) RowsAffected() (int64, error) { return int64(r), nil }

type fakeReaperRows struct {
	cols []string
	rows [][]driver.Value
	next int
}

func (r *fakeReaperRows) Columns() []string { return r.cols }
func (r *fakeReaperRows) Close() error      { return nil }

func (r *fakeReaperRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

func normalizeSQL(query string) string {
	return strings.Join(strings.Fields(query), " ")
}

func validateMoleculeStepQuery(query string) error {
	return requireSQL(query,
		"wd.issue_id",
		"pm.id = wd.depends_on_wisp_id",
		"wd.type = 'parent-child'",
		"pm.issue_type = 'molecule'",
		"pm.status = 'closed'",
		"NOT EXISTS",
		"open_dep.depends_on_external IS NOT NULL",
		"w.issue_type != 'agent'",
		"w.status IN ('open', 'hooked', 'in_progress')",
	)
}

func validateStaleWispQuery(query string) error {
	return requireSQL(query,
		"wd.issue_id",
		"pw.id = wd.depends_on_wisp_id",
		"pi.id = wd.depends_on_issue_id",
		"pi.status IN ('open', 'hooked', 'in_progress')",
		"depends_on_external IS NOT NULL",
		"wd.type = 'parent-child'",
		"w.issue_type != 'agent'",
		"w.created_at < ?",
		"open_parent.issue_id IS NULL",
		"closed_molecule_step.issue_id IS NULL",
	)
}

func requireSQL(query string, required ...string) error {
	if strings.Contains(query, "depends_on_id") {
		return fmt.Errorf("query uses legacy depends_on_id column: %s", query)
	}
	for _, want := range required {
		if !strings.Contains(query, want) {
			return fmt.Errorf("query missing %q: %s", want, query)
		}
	}
	return nil
}

func assertOpsContainInOrder(t *testing.T, ops []string, want ...string) {
	t.Helper()
	next := 0
	for _, op := range ops {
		if strings.Contains(op, want[next]) {
			next++
			if next == len(want) {
				return
			}
		}
	}
	t.Fatalf("ops missing ordered sequence %v in %v", want[next:], ops)
}
