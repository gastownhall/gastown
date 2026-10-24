package agentlink

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func testEvent(item string) Event {
	return Event{At: time.Now().UTC(), Type: EventItemUpsert, ItemID: item, Data: json.RawMessage(`{"id":"` + item + `"}`)}
}

func TestQueue_AppendPendingAck(t *testing.T) {
	root := t.TempDir()
	q := OpenQueue(root)
	for i := 0; i < 3; i++ {
		if err := q.Append(testEvent(fmt.Sprintf("it-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, ".runtime", "agentlink")
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode: %v %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "outbox.jsonl")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("outbox mode: %v %v", fi, err)
	}
	pend, err := q.Pending(10)
	if err != nil || len(pend) != 3 {
		t.Fatalf("pending = %d, %v", len(pend), err)
	}
	for i := 1; i < len(pend); i++ {
		if pend[i].ID <= pend[i-1].ID {
			t.Fatalf("ids not ascending: %s <= %s", pend[i].ID, pend[i-1].ID)
		}
	}
	if err := q.Ack(pend[1].ID); err != nil {
		t.Fatal(err)
	}
	if c, _ := q.Cursor(); c != pend[1].ID {
		t.Fatalf("cursor = %q", c)
	}
	rest, _ := q.Pending(10)
	if len(rest) != 1 || rest[0].ID != pend[2].ID {
		t.Fatalf("after ack pending = %+v", rest)
	}
	// A new queue over the same town sees the same state.
	rest2, _ := OpenQueue(root).Pending(10)
	if len(rest2) != 1 || rest2[0].ID != pend[2].ID {
		t.Fatalf("reopened pending = %+v", rest2)
	}
	if lim, _ := q.Pending(0); len(lim) != 0 {
		t.Fatalf("Pending(0) = %d", len(lim))
	}
}

// Append stamps every event under the queue lock with an ID greater than the
// last queued and the acked ID, whatever ID the caller (or another process)
// generated, so the outbox is strictly ascending.
func TestQueue_OutOfOrderAppendIsRestamped(t *testing.T) {
	q := OpenQueue(t.TempDir())
	late := NewEventID(time.Now().Add(time.Hour))
	early := NewEventID(time.Now().Add(-time.Hour))
	a, b := testEvent("a"), testEvent("b")
	a.ID, b.ID = late, early
	if err := q.Append(a); err != nil {
		t.Fatal(err)
	}
	if err := q.Append(b); err != nil {
		t.Fatal(err)
	}
	pend, _ := q.Pending(10)
	if len(pend) != 2 || pend[0].ItemID != "a" || pend[1].ItemID != "b" || pend[1].ID <= pend[0].ID {
		t.Fatalf("pending = %+v", pend)
	}
	// A cursor far in the future (another clock) still bounds new IDs.
	if err := q.Ack(late); err != nil {
		t.Fatal(err)
	}
	c := testEvent("c")
	c.ID = early
	if err := q.Append(c); err != nil {
		t.Fatal(err)
	}
	p2, _ := q.Pending(10)
	if len(p2) != 1 || p2[0].ItemID != "c" || p2[0].ID <= late {
		t.Fatalf("pending after ack = %+v (cursor %s)", p2, late)
	}
}

func TestNextULID(t *testing.T) {
	for _, tc := range [][2]string{
		{"00000000000000000000000000", "00000000000000000000000001"},
		{"0000000000000000000000000Z", "00000000000000000000000010"},
		{"01HZZZZZZZZZZZZZZZZZZZZZZZ", "01J00000000000000000000000"},
	} {
		if got := nextULID(tc[0]); got != tc[1] || got <= tc[0] {
			t.Errorf("nextULID(%s) = %s, want %s", tc[0], got, tc[1])
		}
	}
}

func TestQueue_TornLineIsSkipped(t *testing.T) {
	root := t.TempDir()
	q := OpenQueue(root)
	if err := q.Append(testEvent("a")); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-write: a partial line with no newline.
	f, err := os.OpenFile(q.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"id":"01ZZZ","ty`)
	f.Close()
	if err := q.Append(testEvent("b")); err != nil {
		t.Fatal(err)
	}
	pend, err := q.Pending(10)
	if err != nil || len(pend) != 2 || pend[0].ItemID != "a" || pend[1].ItemID != "b" {
		t.Fatalf("pending = %+v, %v", pend, err)
	}
}

func TestQueue_CompactionKeepsUnacked(t *testing.T) {
	root := t.TempDir()
	q := OpenQueue(root)
	big := strings.Repeat("x", 4096)
	var ids []string
	for i := 0; i < 400; i++ { // ~1.6 MiB
		e := testEvent(fmt.Sprintf("it-%d", i))
		e.Data = json.RawMessage(`{"pad":"` + big + `"}`)
		if err := q.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	pend, _ := q.Pending(1000)
	for _, e := range pend {
		ids = append(ids, e.ID)
	}
	before, _ := os.Stat(q.path)
	if err := q.Ack(ids[299]); err != nil { // 75% acked, file > 1 MiB
		t.Fatal(err)
	}
	after, _ := os.Stat(q.path)
	if after.Size() >= before.Size()/2 {
		t.Fatalf("not compacted: %d -> %d", before.Size(), after.Size())
	}
	if after.Mode().Perm() != 0o600 {
		t.Fatalf("compacted mode %v", after.Mode().Perm())
	}
	rest, _ := q.Pending(1000)
	if len(rest) != 100 || rest[0].ID != ids[300] || rest[99].ID != ids[399] {
		t.Fatalf("after compaction pending = %d first=%v", len(rest), rest)
	}
	// Appends after compaction stay above the cursor and the last ID.
	if err := q.Append(testEvent("new")); err != nil {
		t.Fatal(err)
	}
	rest, _ = q.Pending(1000)
	if len(rest) != 101 || rest[100].ID <= ids[399] {
		t.Fatalf("post-compaction append: %d", len(rest))
	}
}

func TestQueue_NoCompactionBelowThreshold(t *testing.T) {
	q := OpenQueue(t.TempDir())
	for i := 0; i < 10; i++ {
		q.Append(testEvent("x"))
	}
	pend, _ := q.Pending(10)
	before, _ := os.Stat(q.path)
	q.Ack(pend[8].ID)
	after, _ := os.Stat(q.path)
	if after.Size() != before.Size() {
		t.Fatalf("small file compacted: %d -> %d", before.Size(), after.Size())
	}
}

// helperAppends is run in a child process by TestQueue_ConcurrentAppends.
func TestQueueHelperProcess(t *testing.T) {
	root := os.Getenv("GTB_QUEUE_HELPER_ROOT")
	if root == "" {
		t.Skip("helper process only")
	}
	n, _ := strconv.Atoi(os.Getenv("GTB_QUEUE_HELPER_N"))
	tag := os.Getenv("GTB_QUEUE_HELPER_TAG")
	q := OpenQueue(root)
	for i := 0; i < n; i++ {
		e := testEvent(fmt.Sprintf("%s-%d", tag, i))
		e.Data = json.RawMessage(`{"pad":"` + strings.Repeat(tag, 512) + `"}`)
		if err := q.Append(e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestQueue_ConcurrentAppends(t *testing.T) {
	root := t.TempDir()
	const n = 150
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for _, tag := range []string{"g1", "g2"} {
		wg.Add(1)
		go func(tag string) {
			defer wg.Done()
			q := OpenQueue(root) // own lock fd: flock serialises across fds
			for i := 0; i < n; i++ {
				e := testEvent(fmt.Sprintf("%s-%d", tag, i))
				e.Data = json.RawMessage(`{"pad":"` + strings.Repeat(tag, 512) + `"}`)
				if err := q.Append(e); err != nil {
					errs <- err
					return
				}
			}
		}(tag)
	}
	for _, tag := range []string{"p1", "p2"} {
		wg.Add(1)
		go func(tag string) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestQueueHelperProcess$", "-test.count=1")
			cmd.Env = append(os.Environ(), "GTB_QUEUE_HELPER_ROOT="+root, "GTB_QUEUE_HELPER_N="+strconv.Itoa(n), "GTB_QUEUE_HELPER_TAG="+tag)
			if out, err := cmd.CombinedOutput(); err != nil {
				errs <- fmt.Errorf("helper %s: %v: %s", tag, err, out)
			}
		}(tag)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(root, ".runtime", "agentlink", "outbox.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	seen := map[string]bool{}
	prev := ""
	lines := 0
	for sc.Scan() {
		lines++
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("corrupt line %d: %v", lines, err)
		}
		if e.ID <= prev {
			t.Fatalf("line %d id %s not > %s", lines, e.ID, prev)
		}
		prev = e.ID
		if seen[e.ItemID] {
			t.Fatalf("duplicate %s", e.ItemID)
		}
		seen[e.ItemID] = true
	}
	if lines != 4*n {
		t.Fatalf("lines = %d, want %d", lines, 4*n)
	}
}

func TestAppliedSet_StoresAndTrims(t *testing.T) {
	root := t.TempDir()
	s, err := OpenAppliedSet(root, 5)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if err := s.Put(Result{Key: fmt.Sprintf("k%d", i), OK: true, Data: json.RawMessage(`{"n":` + strconv.Itoa(i) + `}`)}); err != nil {
			t.Fatal(err)
		}
	}
	s2, err := OpenAppliedSet(root, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Get("k0"); ok {
		t.Fatal("k0 should have been trimmed")
	}
	r, ok := s2.Get("k7")
	if !ok || !r.OK || string(r.Data) != `{"n":7}` {
		t.Fatalf("k7 = %+v %v", r, ok)
	}
	if n := s2.Len(); n > 5 {
		t.Fatalf("len = %d, want <= 5", n)
	}
	fi, _ := os.Stat(filepath.Join(root, ".runtime", "agentlink", "applied.jsonl"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("applied mode %v", fi.Mode().Perm())
	}
}
