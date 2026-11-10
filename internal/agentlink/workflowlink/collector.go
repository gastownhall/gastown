package workflowlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"
	"github.com/steveyegge/gastown/internal/workflow"
)

// seenFile records what the server was last sent, so a daemon restart does
// not resend every artifact.
const seenFile = "sent.json"

// WorkflowCollector builds snapshots of every gated rig in the town and, on
// each Scan, queues events for changes made outside the Observer (another
// process editing an artifact, the refinery moving a step, sling binding a
// branch). Changes are detected by comparing artifact shas and view hashes
// with what was last sent; there are no file watchers.
type WorkflowCollector struct {
	TownRoot string
	Rigs     func() ([]agentlink.Rig, error)
	Env      func(rig string) (workflow.GateEnv, error)
	Queue    *agentlink.Queue
	// Sessions lists worker sessions; nil means none.
	Sessions func() ([]agentlink.SessionView, error)
	// DiffStat computes per-item diffstats; nil leaves them empty.
	DiffStat DiffStatFunc
	Logf     func(format string, args ...any)

	mu   sync.Mutex
	seen map[string]string // "a:<rig>/<id>/<name>" -> sha, "i:<rig>/<id>" -> view hash
}

type rigItems struct {
	rig   string
	views []agentlink.ItemView // with artifacts
}

// gather lists every gated rig's items with artifacts and diffstats. Any
// rig that cannot be read fails the whole call: a snapshot replaces the
// server's state, so a partial one would drop that rig's items.
func (c *WorkflowCollector) gather() ([]agentlink.Rig, []rigItems, error) {
	rigs, err := c.Rigs()
	if err != nil {
		return nil, nil, fmt.Errorf("list rigs: %w", err)
	}
	var out []rigItems
	for _, r := range rigs {
		if !r.Gated {
			continue
		}
		env, err := c.Env(r.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("rig %s: %w", r.Name, err)
		}
		items, err := env.Items.List(r.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("rig %s: %w", r.Name, err)
		}
		ri := rigItems{rig: r.Name}
		for _, it := range items {
			iv := itemView(env, it, c.DiffStat)
			iv.Artifacts = itemArtifacts(c.TownRoot, iv.Rig, it.ID)
			ri.views = append(ri.views, iv)
		}
		out = append(out, ri)
	}
	return rigs, out, nil
}

// Collect builds the snapshot: every rig, the items of gated rigs with their
// artifacts (markdown only) and diffstats, and worker sessions. Cursor is the
// queue's acked cursor (the agent loop replaces it with the ID the snapshot
// supersedes). The sent state is reset to what the snapshot holds.
func (c *WorkflowCollector) Collect() (agentlink.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rigs, all, err := c.gather()
	if err != nil {
		return agentlink.Snapshot{}, err
	}
	snap := agentlink.Snapshot{Rigs: rigs}
	seen := map[string]string{}
	for _, ri := range all {
		for _, iv := range ri.views {
			snap.Items = append(snap.Items, iv)
			seen["i:"+iv.Rig+"/"+iv.ID] = viewHash(iv)
			for _, a := range iv.Artifacts {
				seen["a:"+iv.Rig+"/"+iv.ID+"/"+a.Name] = a.SHA
			}
		}
	}
	if c.Sessions != nil {
		ss, err := c.Sessions()
		if err != nil {
			c.log("agentlink: list sessions: %v", err)
		}
		snap.Sessions = ss
	}
	if c.Queue != nil {
		if snap.Cursor, err = c.Queue.Cursor(); err != nil {
			return agentlink.Snapshot{}, err
		}
	}
	c.seen = seen
	c.saveSeen()
	return snap, nil
}

// Scan queues an item.upsert for every item whose view changed and an
// artifact event for every artifact whose sha changed since they were last
// sent. A rig that cannot be read is skipped this round.
func (c *WorkflowCollector) Scan() error {
	if c.Queue == nil {
		return errors.New("workflowlink: Scan needs a Queue")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadSeen()
	rigs, err := c.Rigs()
	if err != nil {
		return err
	}
	changed := false
	defer func() {
		if changed {
			c.saveSeen()
		}
	}()
	for _, r := range rigs {
		if !r.Gated {
			continue
		}
		env, err := c.Env(r.Name)
		if err != nil {
			c.log("agentlink: scan rig %s: %v", r.Name, err)
			continue
		}
		items, err := env.Items.List(r.Name)
		if err != nil {
			c.log("agentlink: scan rig %s: %v", r.Name, err)
			continue
		}
		for _, it := range items {
			iv := itemView(env, it, c.DiffStat)
			ik := "i:" + iv.Rig + "/" + iv.ID
			if h := viewHash(iv); c.seen[ik] != h {
				if err := c.Queue.Append(agentlink.Event{At: time.Now().UTC(), Type: agentlink.EventItemUpsert, ItemID: iv.ID, Data: mustJSON(iv)}); err != nil {
					return err
				}
				c.seen[ik], changed = h, true
			}
			for _, a := range itemArtifacts(c.TownRoot, iv.Rig, iv.ID) {
				ak := "a:" + iv.Rig + "/" + iv.ID + "/" + a.Name
				if c.seen[ak] == a.SHA {
					continue
				}
				if err := c.Queue.Append(agentlink.Event{At: time.Now().UTC(), Type: agentlink.EventArtifact, ItemID: iv.ID, Data: mustJSON(a)}); err != nil {
					return err
				}
				c.seen[ak], changed = a.SHA, true
			}
		}
	}
	return nil
}

func (c *WorkflowCollector) seenPath() string {
	return filepath.Join(agentlink.AgentlinkDir(c.TownRoot), seenFile)
}

func (c *WorkflowCollector) loadSeen() {
	if c.seen != nil {
		return
	}
	c.seen = map[string]string{}
	if b, err := os.ReadFile(c.seenPath()); err == nil {
		_ = json.Unmarshal(b, &c.seen)
	}
}

func (c *WorkflowCollector) saveSeen() {
	b, err := json.Marshal(c.seen)
	if err == nil {
		err = writeAtomic(c.seenPath(), b)
	}
	if err != nil {
		c.log("agentlink: save sent state: %v", err)
	}
}

func (c *WorkflowCollector) log(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// writeAtomic writes data to path through a 0600 temp file and a rename.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
