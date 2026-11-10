package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/agentlink"
	"github.com/steveyegge/gastown/internal/workflow"
)

func TestObserveIfJoined_UnjoinedTownNoObserver(t *testing.T) {
	town := t.TempDir()
	items := workflow.NewMemItems()
	env := observeIfJoined(workflow.GateEnv{TownRoot: town, Rig: "r", Items: items})
	if env.Observer != nil || env.Items != items {
		t.Fatalf("unjoined town got an observer: %+v", env)
	}
	if _, err := os.Stat(filepath.Join(town, ".runtime", "agentlink")); !os.IsNotExist(err) {
		t.Fatalf("unjoined town touched .runtime/agentlink: %v", err)
	}
}

func TestObserveIfJoined_JoinedTownQueuesEvents(t *testing.T) {
	town := t.TempDir()
	if err := agentlink.SaveToken(town, "https://gtb.example.com", "m1", "tok"); err != nil {
		t.Fatal(err)
	}
	env := observeIfJoined(workflow.GateEnv{TownRoot: town, Rig: "r", Items: workflow.NewMemItems(), Store: workflow.NewMemStore()})
	if env.Observer == nil {
		t.Fatal("joined town has no observer")
	}
	it, err := env.Items.Create("r", workflow.KindTask, "t", "")
	if err != nil {
		t.Fatal(err)
	}
	pend, err := agentlink.OpenQueue(town).Pending(10)
	if err != nil || len(pend) != 1 || pend[0].ItemID != it.ID || pend[0].Type != agentlink.EventItemUpsert {
		t.Fatalf("pending = %+v, %v", pend, err)
	}
}

func TestObserveIfJoined_DisabledByEnv(t *testing.T) {
	town := t.TempDir()
	if err := agentlink.SaveToken(town, "https://gtb.example.com", "m1", "tok"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GTB_AGENTLINK", "0")
	if env := observeIfJoined(workflow.GateEnv{TownRoot: town, Items: workflow.NewMemItems()}); env.Observer != nil {
		t.Fatal("GTB_AGENTLINK=0 still observed")
	}
}
