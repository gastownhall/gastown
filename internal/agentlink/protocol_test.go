package agentlink

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRequestTypes_Closed(t *testing.T) {
	want := []string{"approve", "request_changes", "item.create", "item.sling", "item.cancel", "snapshot", "inspect", "terminal.peek"}
	if !reflect.DeepEqual(RequestTypes, want) {
		t.Fatalf("RequestTypes = %v, want %v", RequestTypes, want)
	}
	if !IsRequestType("approve") || IsRequestType("shell") {
		t.Fatal("IsRequestType wrong")
	}
}

func TestProtocolVersion(t *testing.T) {
	if ProtocolVersion != "1.0.0" {
		t.Fatalf("ProtocolVersion = %q", ProtocolVersion)
	}
}

func TestItemView_JSONSnakeCaseAndOmitEmpty(t *testing.T) {
	b, err := json.Marshal(ItemView{ID: "gt-1", WaitingForHuman: "spec", DependsOn: []string{"gt-0"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, k := range []string{`"id"`, `"rig"`, `"kind"`, `"step"`, `"title"`, `"parent"`, `"branch"`, `"waiting_for_human"`, `"depends_on"`, `"step_entered_at"`} {
		if !strings.Contains(s, k) {
			t.Errorf("missing %s in %s", k, s)
		}
	}
	for _, k := range []string{`"usage"`, `"artifacts"`, `"diffstat"`} {
		if strings.Contains(s, k) {
			t.Errorf("%s should be omitted when nil: %s", k, s)
		}
	}
	b, _ = json.Marshal(Usage{InputTokens: 1, OutputTokens: 2, CostUSD: 0.5})
	if string(b) != `{"input_tokens":1,"output_tokens":2,"cost_usd":0.5}` {
		t.Errorf("usage json = %s", b)
	}
	b, _ = json.Marshal(Hello{MachineID: "m", GtbVersion: "v"})
	if !strings.Contains(string(b), `"machine_id":"m"`) || !strings.Contains(string(b), `"gtb_version":"v"`) {
		t.Errorf("hello json = %s", b)
	}
	b, _ = json.Marshal(Request{Key: "k", ExpiresAt: time.Unix(0, 0).UTC()})
	if !strings.Contains(string(b), `"expires_at"`) || !strings.Contains(string(b), `"item_id"`) {
		t.Errorf("request json = %s", b)
	}
	b, _ = json.Marshal(FileStat{Path: "a", Added: 1, Deleted: 2})
	if string(b) != `{"path":"a","added":1,"deleted":2}` {
		t.Errorf("filestat json = %s", b)
	}
}

func TestNewEventID_SortsByTime(t *testing.T) {
	base := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 50; i++ {
		ids = append(ids, NewEventID(base.Add(time.Duration(i)*time.Millisecond)))
	}
	// Same millisecond: still strictly increasing (monotonic).
	for i := 0; i < 50; i++ {
		ids = append(ids, NewEventID(base.Add(time.Hour)))
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("ids not sorted by time")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if len(id) != 26 {
			t.Fatalf("id %q len %d", id, len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	if NewEventID(base.Add(-time.Hour)) >= ids[0] {
		t.Fatal("earlier time should sort before")
	}
}
