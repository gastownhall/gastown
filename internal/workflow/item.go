package workflow

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// ErrItemNotFound is returned when an item does not exist.
var ErrItemNotFound = errors.New("workflow: item not found")

// Item is a gated work item's workflow state.
type Item struct {
	ID, Rig, Title, Parent string
	Kind                   Kind
	Step                   Step
	StepEnteredAt          time.Time
	WaitingForHuman        string
	// Branch is the git branch of the worktree the item was slung to
	// (gtb:branch:<branch>); empty until sling binds one.
	Branch string
}

// Items reads and writes work item workflow state.
type Items interface {
	Get(id string) (*Item, error)
	Create(rig string, k Kind, title, parent string) (*Item, error)
	SetStep(id string, s Step, at time.Time) error
	SetWaiting(id, reason string) error
	// SetBranch binds the item to the worktree branch it was slung to,
	// replacing any previous binding.
	SetBranch(id, branch string) error
	List(rig string) ([]*Item, error)
}

// LabelWaitingPrefix prefixes the bead label recording that an item waits on
// a human ("gtb:waiting:<reason>"). Dispatchers (convoy stranded scan and
// feeder) use it to leave waiting items alone.
const LabelWaitingPrefix = "gtb:waiting:"

// HasWaitingLabel reports whether labels include a gtb:waiting: label.
func HasWaitingLabel(labels []string) bool {
	for _, l := range labels {
		if strings.HasPrefix(l, LabelWaitingPrefix) {
			return true
		}
	}
	return false
}

// Bead label prefixes holding item state.
const (
	labelKind    = "gtb:wf:"
	labelStep    = "gtb:step:"
	labelEntered = "gtb:entered:"
	labelWaiting = LabelWaitingPrefix
	labelRig     = "gtb:rig:"
	labelBranch  = "gtb:branch:"
)

type beadsItems struct{ b *beads.Beads }

// NewBeadsItems returns Items stored on beads as gtb: labels: gtb:wf:<kind>,
// gtb:step:<step> and gtb:entered:<RFC3339> (each replaced on change),
// gtb:waiting:<reason> (removed when empty), gtb:branch:<branch> (replaced
// on change), and gtb:rig:<rig> (used to recover Item.Rig and to filter List).
func NewBeadsItems(b *beads.Beads) Items { return &beadsItems{b: b} }

func (bi *beadsItems) Get(id string) (*Item, error) {
	issue, err := bi.b.Show(id)
	if errors.Is(err, beads.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s", ErrItemNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("workflow: show %s: %w", id, err)
	}
	it := itemFromIssue(issue)
	if it.Kind == "" {
		return nil, fmt.Errorf("%w: %s has no %s label", ErrItemNotFound, id, labelKind)
	}
	return it, nil
}

func (bi *beadsItems) Create(rig string, k Kind, title, parent string) (*Item, error) {
	steps := Steps(k)
	if len(steps) == 0 {
		return nil, fmt.Errorf("workflow: unknown kind %q", k)
	}
	entered := time.Now().UTC().Truncate(time.Second)
	labels := []string{labelKind + string(k), labelStep + string(steps[0]), labelEntered + entered.Format(time.RFC3339)}
	if rig != "" {
		labels = append(labels, labelRig+rig)
	}
	issue, err := bi.b.Create(beads.CreateOptions{Title: title, Parent: parent, Labels: labels, Rig: rig, Priority: 2})
	if err != nil {
		return nil, fmt.Errorf("workflow: create item: %w", err)
	}
	it := itemFromIssue(issue)
	// bd create output may omit labels or parent; fill from what we sent.
	it.Rig, it.Kind, it.Step, it.StepEnteredAt = rig, k, steps[0], entered
	if it.Title == "" {
		it.Title = title
	}
	if it.Parent == "" {
		it.Parent = parent
	}
	return it, nil
}

func (bi *beadsItems) SetStep(id string, s Step, at time.Time) error {
	issue, err := bi.b.Show(id)
	if err != nil {
		return fmt.Errorf("workflow: show %s: %w", id, err)
	}
	stepL := labelStep + string(s)
	enteredL := labelEntered + at.UTC().Format(time.RFC3339)
	var remove []string
	for _, l := range issue.Labels {
		if (strings.HasPrefix(l, labelStep) && l != stepL) || (strings.HasPrefix(l, labelEntered) && l != enteredL) {
			remove = append(remove, l)
		}
	}
	if err := bi.b.Update(id, beads.UpdateOptions{AddLabels: []string{stepL, enteredL}, RemoveLabels: remove}); err != nil {
		return fmt.Errorf("workflow: set step %s: %w", id, err)
	}
	return nil
}

func (bi *beadsItems) SetWaiting(id, reason string) error {
	issue, err := bi.b.Show(id)
	if err != nil {
		return fmt.Errorf("workflow: show %s: %w", id, err)
	}
	want := ""
	if reason != "" {
		want = labelWaiting + reason
	}
	var remove []string
	have := false
	for _, l := range issue.Labels {
		if strings.HasPrefix(l, labelWaiting) {
			if l == want {
				have = true
			} else {
				remove = append(remove, l)
			}
		}
	}
	opts := beads.UpdateOptions{RemoveLabels: remove}
	if want != "" && !have {
		opts.AddLabels = []string{want}
	}
	if len(opts.AddLabels) == 0 && len(opts.RemoveLabels) == 0 {
		return nil
	}
	if err := bi.b.Update(id, opts); err != nil {
		return fmt.Errorf("workflow: set waiting %s: %w", id, err)
	}
	return nil
}

func (bi *beadsItems) SetBranch(id, branch string) error {
	if branch == "" {
		return fmt.Errorf("workflow: set branch %s: empty branch", id)
	}
	issue, err := bi.b.Show(id)
	if err != nil {
		return fmt.Errorf("workflow: show %s: %w", id, err)
	}
	want := labelBranch + branch
	opts := beads.UpdateOptions{}
	have := false
	for _, l := range issue.Labels {
		if strings.HasPrefix(l, labelBranch) {
			if l == want {
				have = true
			} else {
				opts.RemoveLabels = append(opts.RemoveLabels, l)
			}
		}
	}
	if !have {
		opts.AddLabels = []string{want}
	}
	if len(opts.AddLabels) == 0 && len(opts.RemoveLabels) == 0 {
		return nil
	}
	if err := bi.b.Update(id, opts); err != nil {
		return fmt.Errorf("workflow: set branch %s: %w", id, err)
	}
	return nil
}

func (bi *beadsItems) List(rig string) ([]*Item, error) {
	issues, err := bi.b.List(beads.ListOptions{Label: labelRig + rig, Priority: -1})
	if err != nil {
		return nil, fmt.Errorf("workflow: list items: %w", err)
	}
	var out []*Item
	for _, is := range issues {
		if it := itemFromIssue(is); it.Kind != "" {
			out = append(out, it)
		}
	}
	return out, nil
}

func itemFromIssue(is *beads.Issue) *Item {
	it := &Item{ID: is.ID, Title: is.Title, Parent: is.Parent}
	for _, l := range is.Labels {
		switch {
		case strings.HasPrefix(l, labelKind):
			it.Kind = Kind(strings.TrimPrefix(l, labelKind))
		case strings.HasPrefix(l, labelStep):
			it.Step = Step(strings.TrimPrefix(l, labelStep))
		case strings.HasPrefix(l, labelEntered):
			if t, err := time.Parse(time.RFC3339, strings.TrimPrefix(l, labelEntered)); err == nil {
				it.StepEnteredAt = t.UTC()
			}
		case strings.HasPrefix(l, labelWaiting):
			it.WaitingForHuman = strings.TrimPrefix(l, labelWaiting)
		case strings.HasPrefix(l, labelRig):
			it.Rig = strings.TrimPrefix(l, labelRig)
		case strings.HasPrefix(l, labelBranch):
			it.Branch = strings.TrimPrefix(l, labelBranch)
		}
	}
	return it
}

// ---- in-memory items ----

type memItems struct {
	mu    sync.Mutex
	seq   int
	items map[string]*Item
}

// NewMemItems returns an in-memory Items for unit tests.
func NewMemItems() Items { return &memItems{items: map[string]*Item{}} }

func (m *memItems) Get(id string) (*Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrItemNotFound, id)
	}
	cp := *it
	return &cp, nil
}

func (m *memItems) Create(rig string, k Kind, title, parent string) (*Item, error) {
	steps := Steps(k)
	if len(steps) == 0 {
		return nil, fmt.Errorf("workflow: unknown kind %q", k)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	it := &Item{ID: fmt.Sprintf("mem-%d", m.seq), Rig: rig, Title: title, Parent: parent, Kind: k, Step: steps[0], StepEnteredAt: time.Now().UTC()}
	m.items[it.ID] = it
	cp := *it
	return &cp, nil
}

func (m *memItems) SetStep(id string, s Step, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrItemNotFound, id)
	}
	it.Step, it.StepEnteredAt = s, at.UTC()
	return nil
}

func (m *memItems) SetWaiting(id, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrItemNotFound, id)
	}
	it.WaitingForHuman = reason
	return nil
}

func (m *memItems) SetBranch(id, branch string) error {
	if branch == "" {
		return fmt.Errorf("workflow: set branch %s: empty branch", id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrItemNotFound, id)
	}
	it.Branch = branch
	return nil
}

func (m *memItems) List(rig string) ([]*Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Item
	for _, it := range m.items {
		if it.Rig == rig {
			cp := *it
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
