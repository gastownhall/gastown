package server

import (
	"fmt"
	"time"
)

// Retention is how long the server keeps events, results and finished
// requests (spec §7.2).
const Retention = 90 * 24 * time.Hour

// Prune deletes events and results older than Retention, and done or expired
// requests created more than Retention ago. It never deletes items,
// artifacts, sessions, machines or tokens. It returns the number of rows
// deleted.
func (s *Store) Prune(now time.Time) (int, error) {
	cutoff := ms(now.Add(-Retention))
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("server: prune: %w", err)
	}
	defer tx.Rollback()
	total := 0
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM events WHERE at < ?`, []any{cutoff}},
		{`DELETE FROM results WHERE at < ?`, []any{cutoff}},
		{`DELETE FROM requests WHERE status IN (?, ?) AND created_at < ?`, []any{RequestDone, RequestExpired, cutoff}},
	} {
		res, err := tx.Exec(q.sql, q.args...)
		if err != nil {
			return 0, fmt.Errorf("server: prune: %w", err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("server: prune: %w", err)
	}
	return total, nil
}
