package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"
)

func TestPrune_90Days(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	now := time.Now()
	old := now.Add(-91 * 24 * time.Hour)
	recent := now.Add(-89 * 24 * time.Hour)
	iv := agentlink.ItemView{ID: "gt-1", Rig: "r"}
	evs := []agentlink.Event{
		{ID: agentlink.NewEventID(old), At: old, Type: "item.upsert", ItemID: "gt-1", Data: mustJSON(t, iv)},
		{ID: agentlink.NewEventID(old), At: old, Type: "artifact", ItemID: "gt-1", Data: mustJSON(t, agentlink.ArtifactView{Name: "spec", SHA: "s", Content: "c"})},
		{ID: agentlink.NewEventID(recent), At: recent, Type: "evidence", ItemID: "gt-1", Data: json.RawMessage(`{}`)},
	}
	if _, err := s.AppendEvents("m1", evs); err != nil {
		t.Fatal(err)
	}
	n, err := s.Prune(now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pruned = %d, want 2", n)
	}
	var left []string
	rows, _ := s.db.Query(`SELECT id FROM events`)
	for rows.Next() {
		var id string
		rows.Scan(&id)
		left = append(left, id)
	}
	rows.Close()
	if len(left) != 1 || left[0] != evs[2].ID {
		t.Fatalf("left = %v", left)
	}
	row, arts, err := s.Item("m1", "gt-1")
	if err != nil || row == nil || len(arts) != 1 {
		t.Fatalf("item/artifacts pruned: %v %v %v", row, arts, err)
	}
}

func TestPrune_RequestsAndResults(t *testing.T) {
	s := openTest(t)
	addMachine(t, s, "m1")
	now := time.Now()
	old := now.Add(-91 * 24 * time.Hour)
	s.enqueueRequest("m1", agentlink.Request{Key: "done-old", Type: "inspect", ExpiresAt: old.Add(time.Hour)}, old)
	s.addResult("m1", agentlink.Result{Key: "done-old", OK: true}, old)
	s.enqueueRequest("m1", agentlink.Request{Key: "exp-old", Type: "inspect", ExpiresAt: old.Add(time.Hour)}, old)
	s.ExpireRequests(old.Add(2 * time.Hour))
	s.enqueueRequest("m1", agentlink.Request{Key: "pend-old", Type: "inspect", ExpiresAt: now.Add(time.Hour)}, old)
	s.EnqueueRequest("m1", agentlink.Request{Key: "new", Type: "inspect", ExpiresAt: now.Add(time.Hour)})
	n, err := s.Prune(now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 { // 2 requests + 1 result
		t.Fatalf("pruned = %d, want 3", n)
	}
	for _, k := range []string{"pend-old", "new"} {
		if st, _, err := s.RequestStatus("m1", k, now); err != nil || st != "pending" {
			t.Fatalf("%s: %q %v", k, st, err)
		}
	}
	var m int
	s.db.QueryRow(`SELECT count(*) FROM machines`).Scan(&m)
	if m != 1 {
		t.Fatal("machine pruned")
	}
}
