package workflowlink

import (
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"
	"github.com/steveyegge/gastown/internal/workflow"
)

// ObserveOptions are the optional parts of Observe.
type ObserveOptions struct {
	// DiffStat fills Diffstat on queued item views; nil leaves it empty.
	// The server stores each upserted view whole, so a view without a
	// diffstat clears the one it had until the next scan.
	DiffStat DiffStatFunc
	// Logf receives queue errors; nil discards them.
	Logf func(format string, args ...any)
}

// Observe returns env with an ObserverAdapter as Observer and with Items
// wrapped so Create, SetStep, SetWaiting and SetBranch queue an item.upsert.
// A nil queue returns env unchanged. Queue errors are logged, never returned:
// the workflow action has already happened, and the next snapshot or scan
// reconciles the server.
func Observe(env workflow.GateEnv, q *agentlink.Queue, opts *ObserveOptions) workflow.GateEnv {
	if q == nil {
		return env
	}
	o := &ObserverAdapter{env: env, queue: q}
	if opts != nil {
		o.diff, o.logf = opts.DiffStat, opts.Logf
	}
	env.Items = observedItems{Items: env.Items, o: o}
	env.Observer = o
	return env
}

// ObserverAdapter implements workflow.Observer by queueing agentlink events:
// "transition" becomes an item.upsert at the new step, "evidence" an
// evidence event.
type ObserverAdapter struct {
	env   workflow.GateEnv // Items is the unwrapped store
	queue *agentlink.Queue
	diff  DiffStatFunc
	logf  func(format string, args ...any)
}

// OnEvent implements workflow.Observer.
func (o *ObserverAdapter) OnEvent(kind, itemID string, data any) {
	switch kind {
	case workflow.EventTransition:
		tr, ok := data.(workflow.Transition)
		if !ok {
			return
		}
		it, err := o.env.Items.Get(itemID)
		if err != nil {
			o.log("agentlink: observe %s: %v", itemID, err)
			return
		}
		iv := itemView(o.env, it, o.diff)
		// Advance clears the waiting reason right after; SetWaiting then
		// queues another upsert, so the server never keeps a stale wait.
		iv.Step, iv.StepEnteredAt, iv.WaitingForHuman = string(tr.To), tr.At.UTC(), ""
		o.append(agentlink.EventItemUpsert, itemID, mustJSON(iv))
	case workflow.EventEvidence:
		if b, ok := evidenceData(data); ok {
			o.append(agentlink.EventEvidence, itemID, b)
		}
	}
}

func (o *ObserverAdapter) upsert(itemID string) {
	it, err := o.env.Items.Get(itemID)
	if err != nil {
		o.log("agentlink: observe %s: %v", itemID, err)
		return
	}
	o.append(agentlink.EventItemUpsert, itemID, mustJSON(itemView(o.env, it, o.diff)))
}

func (o *ObserverAdapter) append(typ, itemID string, data []byte) {
	err := o.queue.Append(agentlink.Event{At: time.Now().UTC(), Type: typ, ItemID: itemID, Data: data})
	if err != nil {
		o.log("agentlink: queue %s for %s: %v", typ, itemID, err)
	}
}

func (o *ObserverAdapter) log(format string, args ...any) {
	if o.logf != nil {
		o.logf(format, args...)
	}
}

// observedItems queues an item.upsert after every successful state write.
type observedItems struct {
	workflow.Items
	o *ObserverAdapter
}

func (w observedItems) Create(rig string, k workflow.Kind, title, parent string) (*workflow.Item, error) {
	it, err := w.Items.Create(rig, k, title, parent)
	if err == nil {
		w.o.append(agentlink.EventItemUpsert, it.ID, mustJSON(itemView(w.o.env, it, nil)))
	}
	return it, err
}

func (w observedItems) SetStep(id string, s workflow.Step, at time.Time) error {
	err := w.Items.SetStep(id, s, at)
	if err == nil {
		w.o.upsert(id)
	}
	return err
}

func (w observedItems) SetWaiting(id, reason string) error {
	err := w.Items.SetWaiting(id, reason)
	if err == nil {
		w.o.upsert(id)
	}
	return err
}

func (w observedItems) SetBranch(id, branch string) error {
	err := w.Items.SetBranch(id, branch)
	if err == nil {
		w.o.upsert(id)
	}
	return err
}
