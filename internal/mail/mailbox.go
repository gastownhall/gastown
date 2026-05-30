package mail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/telemetry"
)

// timeNow is a function that returns the current time. It can be overridden in tests.
var timeNow = time.Now

// Common errors
var (
	ErrMessageNotFound = errors.New("message not found")
	ErrEmptyInbox      = errors.New("inbox is empty")
)

// Mailbox manages messages for an identity via beads.
// When store is non-nil, beads-mode methods use the in-process beadsdk.Storage
// directly instead of shelling out to the bd CLI.
type Mailbox struct {
	identity string // beads identity (e.g., "gastown/polecats/Toast")
	workDir  string // directory to run bd commands in
	beadsDir string // explicit .beads directory path (set via BEADS_DIR)
	path     string // for legacy JSONL mode (crew workers)
	legacy   bool   // true = use JSONL files, false = use beads

	// store is an optional in-process beadsdk.Storage. When set, beads-mode
	// methods bypass the bd subprocess and use the store directly.
	// Callers are responsible for closing the store.
	store beadsdk.Storage
}

// NewMailbox creates a mailbox for the given JSONL path (legacy mode).
// Used by crew workers that have local JSONL inboxes.
func NewMailbox(path string) *Mailbox {
	return &Mailbox{
		path:   filepath.Join(path, "inbox.jsonl"),
		legacy: true,
	}
}

// NewMailboxBeads creates a mailbox backed by beads.
func NewMailboxBeads(identity, workDir string) *Mailbox {
	return &Mailbox{
		identity: identity,
		workDir:  workDir,
		legacy:   false,
	}
}

// NewMailboxFromAddress creates a beads-backed mailbox from a GGT address.
// Follows .beads/redirect for crew workers and polecats using shared beads.
func NewMailboxFromAddress(address, workDir string) *Mailbox {
	beadsDir := beads.ResolveBeadsDir(workDir)
	return &Mailbox{
		identity: AddressToIdentity(address),
		workDir:  workDir,
		beadsDir: beadsDir,
		legacy:   false,
	}
}

// NewMailboxWithBeadsDir creates a mailbox with an explicit beads directory.
func NewMailboxWithBeadsDir(address, workDir, beadsDir string) *Mailbox {
	return &Mailbox{
		identity: AddressToIdentity(address),
		workDir:  workDir,
		beadsDir: beadsDir,
		legacy:   false,
	}
}

// Identity returns the beads identity for this mailbox.
func (m *Mailbox) Identity() string {
	return m.identity
}

// Path returns the JSONL path for legacy mailboxes.
func (m *Mailbox) Path() string {
	return m.path
}

// lockLegacy acquires an exclusive flock for legacy mailbox operations.
// Callers must defer Unlock on the returned flock. The lock file is
// separate from the data file to avoid interfering with reads.
func (m *Mailbox) lockLegacy() (*flock.Flock, error) {
	fl := flock.New(m.path + ".lock")
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("acquiring mailbox lock: %w", err)
	}
	return fl, nil
}

// List returns all open messages in the mailbox.
func (m *Mailbox) List() ([]*Message, error) {
	if m.legacy {
		return m.listLegacy()
	}
	return m.listBeads()
}

func (m *Mailbox) listBeads() ([]*Message, error) {
	// Single query to beads - returns both persistent and wisp messages
	// Wisps are stored in same DB with wisp=true flag, not synced to git
	messages, err := m.listFromDir(m.beadsDir)
	if err != nil {
		return nil, err
	}

	// Sort by priority (higher first), then timestamp (newest first).
	sort.Slice(messages, func(i, j int) bool {
		pi, pj := PriorityToBeads(messages[i].Priority), PriorityToBeads(messages[j].Priority)
		if pi != pj {
			return pi < pj // lower beads int = higher priority
		}
		return messages[i].Timestamp.After(messages[j].Timestamp)
	})

	return messages, nil
}

// listFromDir queries messages from a beads directory.
// Returns messages where identity is the assignee OR a CC recipient.
// Includes both open and hooked messages (hooked = auto-assigned handoff mail).
//
// Uses per-identity --assignee queries to push filtering to Dolt, reducing
// memory footprint under concurrent agent load. A separate CC query fetches
// messages where this identity is CC'd.
func (m *Mailbox) listFromDir(beadsDir string) ([]*Message, error) {
	// Use in-process store when available
	if m.store != nil {
		return m.storeListFromDir()
	}

	identities := m.identityVariants()

	if err := beads.EnsureCustomTypes(beadsDir); err != nil {
		return nil, fmt.Errorf("ensuring custom types: %w", err)
	}

	// Deduplicate messages across queries (assignee + CC may overlap)
	seen := make(map[string]bool)
	messages := make([]*Message, 0)

	// Query 1: assignee match (per identity variant)
	for _, id := range identities {
		args := []string{"list",
			"--label", "gt:message",
			"--assignee", id,
			"--json",
			"--limit", "0",
		}

		ctx, cancel := bdReadCtx()
		stdout, err := runBdCommand(ctx, args, m.workDir, beadsDir)
		cancel()
		if err != nil {
			return nil, err
		}

		// bd v0.58.0 returns plain text (e.g. "No issues found.") for
		// empty result sets instead of JSON. Skip non-JSON output.
		if !isJSON(stdout) {
			continue
		}
		var msgs []BeadsMessage
		trimmed := bytes.TrimSpace(stdout)
		if len(trimmed) == 0 || string(trimmed) == "null" || (trimmed[0] != '[' && trimmed[0] != '{') {
			// bd v0.58.0 returns plain text (e.g. "No issues found.") for
			// empty result sets instead of JSON. Skip non-JSON output.
			continue
		}
		if err := json.Unmarshal(stdout, &msgs); err != nil {
			return nil, err
		}

		for i := range msgs {
			bm := &msgs[i]
			if seen[bm.ID] {
				continue
			}
			// Assignee match: open or hooked status
			if bm.Status == "open" || bm.Status == "hooked" {
				seen[bm.ID] = true
				messages = append(messages, bm.ToMessage())
			}
		}
	}

	// Query 2: CC match — fetch messages with cc:<identity> label
	for _, id := range identities {
		ccLabel := "cc:" + id
		args := []string{"list",
			"--label", "gt:message",
			"--label", ccLabel,
			"--json",
			"--limit", "0",
		}

		ctx, cancel := bdReadCtx()
		stdout, err := runBdCommand(ctx, args, m.workDir, beadsDir)
		cancel()
		if err != nil {
			// CC query failure is non-fatal — assignee messages are primary
			continue
		}

		if !isJSON(stdout) {
			continue
		}
		var msgs []BeadsMessage
		trimmedCC := bytes.TrimSpace(stdout)
		if len(trimmedCC) == 0 || string(trimmedCC) == "null" || (trimmedCC[0] != '[' && trimmedCC[0] != '{') {
			continue
		}
		if err := json.Unmarshal(stdout, &msgs); err != nil {
			continue // Non-fatal for CC
		}

		for i := range msgs {
			bm := &msgs[i]
			if seen[bm.ID] {
				continue
			}
			// CC match: open status only
			if bm.Status == "open" {
				seen[bm.ID] = true
				messages = append(messages, bm.ToMessage())
			}
		}
	}

	// Query 3: Wisps table — ephemeral messages (protocol/lifecycle) are stored
	// as wisps by shouldBeWisp(), but bd list only queries the issues table.
	wispMessages := m.listWispMessages(beadsDir, identities, seen)
	messages = append(messages, wispMessages...)

	return messages, nil
}

// listWispMessages queries the wisps table for ephemeral messages matching the identity.
// Protocol/lifecycle messages are stored as wisps by shouldBeWisp(), but bd list only
// queries the issues table. Uses bd sql --json for full wisp data.
func (m *Mailbox) listWispMessages(beadsDir string, identities []string, seen map[string]bool) []*Message {
	var messages []*Message

	// Query 3a: assignee match via SQL on wisps table
	for _, id := range identities {
		wispMsgs := m.queryWispMessagesByAssignee(beadsDir, id)
		for _, bm := range wispMsgs {
			if seen[bm.ID] {
				continue
			}
			if bm.Status == "open" || bm.Status == "hooked" {
				seen[bm.ID] = true
				messages = append(messages, bm.ToMessage())
			}
		}
	}

	// Query 3b: CC match via SQL on wisps table
	for _, id := range identities {
		wispMsgs := m.queryWispMessagesByCC(beadsDir, id)
		for _, bm := range wispMsgs {
			if seen[bm.ID] {
				continue
			}
			if bm.Status == "open" {
				seen[bm.ID] = true
				messages = append(messages, bm.ToMessage())
			}
		}
	}

	return messages
}

// queryWispMessagesByAssignee queries wisps table for messages assigned to identity.
func (m *Mailbox) queryWispMessagesByAssignee(beadsDir, identity string) []BeadsMessage {
	query := fmt.Sprintf(
		"SELECT w.id, w.title, w.description, w.status, w.priority, w.assignee, w.created_at, w.updated_at, "+
			"GROUP_CONCAT(al.label) as labels_csv "+
			"FROM wisps w "+
			"JOIN wisp_labels l ON w.id = l.issue_id "+
			"JOIN wisp_labels al ON w.id = al.issue_id "+
			"WHERE l.label = 'gt:message' AND w.status IN ('open', 'hooked') AND w.assignee = '%s' "+
			"GROUP BY w.id, w.title, w.description, w.status, w.priority, w.assignee, w.created_at, w.updated_at",
		escapeSQLString(identity))
	return m.runWispSQL(beadsDir, query)
}

// queryWispMessagesByCC queries wisps table for messages where identity is CC'd.
func (m *Mailbox) queryWispMessagesByCC(beadsDir, identity string) []BeadsMessage {
	ccLabel := "cc:" + identity
	query := fmt.Sprintf(
		"SELECT w.id, w.title, w.description, w.status, w.priority, w.assignee, w.created_at, w.updated_at, "+
			"GROUP_CONCAT(al.label) as labels_csv "+
			"FROM wisps w "+
			"JOIN wisp_labels l1 ON w.id = l1.issue_id "+
			"JOIN wisp_labels l2 ON w.id = l2.issue_id "+
			"JOIN wisp_labels al ON w.id = al.issue_id "+
			"WHERE l1.label = 'gt:message' AND l2.label = '%s' AND w.status IN ('open', 'hooked') "+
			"GROUP BY w.id, w.title, w.description, w.status, w.priority, w.assignee, w.created_at, w.updated_at",
		escapeSQLString(ccLabel))
	return m.runWispSQL(beadsDir, query)
}

// wispSQLRow represents a row from the wisps SQL query with aggregated labels.
type wispSQLRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Priority    int    `json:"priority"`
	Assignee    string `json:"assignee"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	LabelsCSV   string `json:"labels_csv"`
}

// runWispSQL executes a bd sql --json query and converts results to BeadsMessages.
func (m *Mailbox) runWispSQL(beadsDir, query string) []BeadsMessage {
	args := []string{"sql", "--json", query}
	ctx, cancel := bdReadCtx()
	stdout, err := runBdCommand(ctx, args, m.workDir, beadsDir)
	cancel()
	if err != nil {
		return nil // Wisps table may not exist yet
	}
	if !isJSON(stdout) {
		return nil
	}

	var rows []wispSQLRow
	if err := json.Unmarshal(stdout, &rows); err != nil {
		return nil
	}

	msgs := make([]BeadsMessage, 0, len(rows))
	for _, row := range rows {
		bm := BeadsMessage{
			ID:          row.ID,
			Title:       row.Title,
			Description: row.Description,
			Status:      row.Status,
			Priority:    row.Priority,
			Assignee:    row.Assignee,
			Wisp:        true,
		}
		if t, err := time.Parse(time.RFC3339, row.CreatedAt); err == nil {
			bm.CreatedAt = t
		} else if t, err := time.Parse("2006-01-02 15:04:05 +0000 UTC", row.CreatedAt); err == nil {
			bm.CreatedAt = t
		}
		if row.LabelsCSV != "" {
			bm.Labels = strings.Split(row.LabelsCSV, ",")
		}
		msgs = append(msgs, bm)
	}
	return msgs
}

// escapeSQLString escapes single quotes for SQL string literals.
func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// identityVariants returns all identity formats to query.
// For town-level agents (mayor/, deacon/), also includes the variant without
// trailing slash for backwards compatibility with legacy messages.
func (m *Mailbox) identityVariants() []string {
	variants := []string{m.identity}

	// Town-level agents may have legacy messages without trailing slash
	if m.identity == "mayor/" {
		variants = append(variants, "mayor")
	} else if m.identity == "deacon/" {
		variants = append(variants, "deacon")
	}

	return variants
}

func (m *Mailbox) listLegacy() ([]*Message, error) {
	file, err := os.Open(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return make([]*Message, 0), nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }() // non-fatal: OS will close on exit

	messages := make([]*Message, 0)
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return nil, fmt.Errorf("corrupt mailbox %s line %d: %w", m.path, lineNum, err)
		}
		messages = append(messages, &msg)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Sort by priority (higher first), then timestamp (newest first).
	sort.Slice(messages, func(i, j int) bool {
		pi, pj := PriorityToBeads(messages[i].Priority), PriorityToBeads(messages[j].Priority)
		if pi != pj {
			return pi < pj
		}
		return messages[i].Timestamp.After(messages[j].Timestamp)
	})

	return messages, nil
}

// ListUnread returns unread (open) messages.
// Filters out messages marked as read (via "read" label in beads mode).
func (m *Mailbox) ListUnread() ([]*Message, error) {
	all, err := m.List()
	if err != nil {
		return nil, err
	}
	unread := make([]*Message, 0)
	for _, msg := range all {
		if !msg.Read {
			unread = append(unread, msg)
		}
	}
	return unread, nil
}

// Get returns a message by ID.
func (m *Mailbox) Get(id string) (*Message, error) {
	if m.legacy {
		return m.getLegacy(id)
	}
	return m.getBeads(id)
}

func (m *Mailbox) getBeads(id string) (*Message, error) {
	// Resolve correct beadsDir based on bead ID prefix (GH#2423)
	primary := beads.ResolveBeadsDirForID(m.beadsDir, id)
	msg, err := m.getFromDir(id, primary)
	if errors.Is(err, ErrMessageNotFound) && primary != m.beadsDir {
		// Cross-rig bead IDs (e.g. ne-*) may live in the home DB when created
		// via the mail router (which always uses town beads). Fall back to
		// m.beadsDir before giving up. See ne-bgr.
		return m.getFromDir(id, m.beadsDir)
	}
	return msg, err
}

// getFromDir retrieves a message from a beads directory.
func (m *Mailbox) getFromDir(id, beadsDir string) (*Message, error) {
	if m.store != nil {
		return m.storeGetFromDir(id)
	}

	args := []string{"show", id, "--json"}

	ctx, cancel := bdReadCtx()
	defer cancel()
	stdout, err := runBdCommand(ctx, args, m.workDir, beadsDir)
	if err != nil {
		if bdErr, ok := err.(*bdError); ok && (bdErr.ContainsError("not found") || bdErr.ContainsError("no issue found") || bdErr.ContainsError("no issue found")) {
			return nil, ErrMessageNotFound
		}
		return nil, err
	}

	// bd show --json returns an array
	if !isJSON(stdout) {
		return nil, ErrMessageNotFound
	}
	var bms []BeadsMessage
	if err := json.Unmarshal(stdout, &bms); err != nil {
		return nil, err
	}
	if len(bms) == 0 {
		return nil, ErrMessageNotFound
	}

	// Wisp status comes from beads issue.wisp field via ToMessage()
	return bms[0].ToMessage(), nil
}

func (m *Mailbox) getLegacy(id string) (*Message, error) {
	messages, err := m.List()
	if err != nil {
		return nil, err
	}
	for _, msg := range messages {
		if msg.ID == id {
			return msg, nil
		}
	}
	return nil, ErrMessageNotFound
}

// MarkRead marks a message as read.
func (m *Mailbox) MarkRead(id string) error {
	if m.legacy {
		return m.markReadLegacy(id)
	}
	return m.markReadBeads(id)
}

func (m *Mailbox) markReadBeads(id string) error {
	if err := m.acknowledgeDeliveryForPrimary(id); err != nil {
		return err
	}

	// Resolve correct beadsDir based on bead ID prefix (GH#2423)
	primary := beads.ResolveBeadsDirForID(m.beadsDir, id)
	err := m.closeInDir(id, primary)
	if errors.Is(err, ErrMessageNotFound) && primary != m.beadsDir {
		// Cross-rig bead IDs (e.g. ne-*) may live in the home DB when created
		// via the mail router (which always uses town beads). Fall back to
		// m.beadsDir before giving up. See ne-bgr.
		return m.closeInDir(id, m.beadsDir)
	}
	return err
}

// closeInDir closes a message in a specific beads directory.
func (m *Mailbox) closeInDir(id, beadsDir string) error {
	if m.store != nil {
		return m.storeCloseInDir(id)
	}

	args := []string{"close", id}
	// Pass session ID for work attribution if available
	if sessionID := runtime.SessionIDFromEnv(); sessionID != "" {
		args = append(args, "--session="+sessionID)
	}

	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err := runBdCommand(ctx, args, m.workDir, beadsDir)
	telemetry.RecordMailMessage(context.Background(), "read", telemetry.MailMessageInfo{
		ID: id,
		To: m.identity,
	}, err)
	if err != nil {
		if isBdNotFound(err) {
			return ErrMessageNotFound
		}
		return err
	}

	return nil
}

func (m *Mailbox) markReadLegacy(id string) error {
	fl, err := m.lockLegacy()
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	messages, err := m.List()
	if err != nil {
		return err
	}

	found := false
	for _, msg := range messages {
		if msg.ID == id {
			msg.Read = true
			found = true
		}
	}

	if !found {
		return ErrMessageNotFound
	}

	return m.rewriteLegacy(messages)
}

// MarkReadOnly marks a message as read WITHOUT archiving/closing it.
// For beads mode, this adds a "read" label to the message.
// For legacy mode, this sets the Read field to true.
// The message remains in the inbox but is displayed as read.
func (m *Mailbox) MarkReadOnly(id string) error {
	if m.legacy {
		return m.markReadLegacy(id)
	}
	return m.markReadOnlyBeads(id)
}

func (m *Mailbox) markReadOnlyBeads(id string) error {
	if err := m.acknowledgeDeliveryForPrimary(id); err != nil {
		return err
	}

	if m.store != nil {
		return m.storeMarkReadOnly(id)
	}

	// Add "read" label to mark as read without closing
	args := []string{"label", "add", id, "read"}
	primary := beads.ResolveBeadsDirForID(m.beadsDir, id)

	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err := runBdCommand(ctx, args, m.workDir, primary)
	if err != nil {
		if isBdNotFound(err) {
			if primary != m.beadsDir {
				// Cross-rig bead IDs (e.g. ne-*) may live in the home DB. See ne-bgr.
				ctx2, cancel2 := bdWriteCtx()
				defer cancel2()
				_, err2 := runBdCommand(ctx2, args, m.workDir, m.beadsDir)
				if err2 != nil {
					if isBdNotFound(err2) {
						return ErrMessageNotFound
					}
					return err2
				}
				return nil
			}
			return ErrMessageNotFound
		}
		return err
	}

	return nil
}

func (m *Mailbox) acknowledgeDeliveryForPrimary(id string) error {
	if m.legacy {
		return nil
	}
	if m.store != nil {
		return m.storeAcknowledgeDeliveryForPrimary(id)
	}

	msg, err := m.Get(id)
	if err != nil {
		return err
	}
	if msg == nil || msg.DeliveryState == "" || AddressToIdentity(msg.To) != m.identity {
		return nil
	}
	return AcknowledgeDeliveryBead(m.workDir, m.beadsDir, id, m.identity)
}

func isBdNotFound(err error) bool {
	bdErr, ok := err.(*bdError)
	return ok && (bdErr.ContainsError("not found") || bdErr.ContainsError("no issue found"))
}

// MarkUnreadOnly marks a message as unread (removes "read" label).
// For beads mode, this removes the "read" label from the message.
// For legacy mode, this sets the Read field to false.
func (m *Mailbox) MarkUnreadOnly(id string) error {
	if m.legacy {
		return m.markUnreadLegacy(id)
	}
	return m.markUnreadOnlyBeads(id)
}

func (m *Mailbox) markUnreadOnlyBeads(id string) error {
	if m.store != nil {
		return m.storeMarkUnreadOnly(id)
	}

	// Remove "read" label to mark as unread
	args := []string{"label", "remove", id, "read"}
	primary := beads.ResolveBeadsDirForID(m.beadsDir, id)

	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err := runBdCommand(ctx, args, m.workDir, primary)
	if err != nil {
		if isBdNotFound(err) {
			if primary != m.beadsDir {
				// Cross-rig bead IDs (e.g. ne-*) may live in the home DB. See ne-bgr.
				ctx2, cancel2 := bdWriteCtx()
				defer cancel2()
				_, err2 := runBdCommand(ctx2, args, m.workDir, m.beadsDir)
				if err2 != nil {
					if isBdNotFound(err2) {
						return ErrMessageNotFound
					}
					if bdErr2, ok := err2.(*bdError); ok && bdErr2.ContainsError("does not have label") {
						return nil
					}
					return err2
				}
				return nil
			}
			return ErrMessageNotFound
		}
		// Ignore error if label doesn't exist
		if bdErr, ok := err.(*bdError); ok && bdErr.ContainsError("does not have label") {
			return nil
		}
		return err
	}

	return nil
}

// MarkUnread marks a message as unread (reopens in beads).
func (m *Mailbox) MarkUnread(id string) error {
	if m.legacy {
		return m.markUnreadLegacy(id)
	}
	return m.markUnreadBeads(id)
}

func (m *Mailbox) markUnreadBeads(id string) error {
	if m.store != nil {
		return m.storeMarkUnread(id)
	}

	args := []string{"reopen", id}
	primary := beads.ResolveBeadsDirForID(m.beadsDir, id)

	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err := runBdCommand(ctx, args, m.workDir, primary)
	if err != nil {
		if isBdNotFound(err) {
			if primary != m.beadsDir {
				// Cross-rig bead IDs (e.g. ne-*) may live in the home DB. See ne-bgr.
				ctx2, cancel2 := bdWriteCtx()
				defer cancel2()
				_, err2 := runBdCommand(ctx2, args, m.workDir, m.beadsDir)
				if err2 != nil {
					if isBdNotFound(err2) {
						return ErrMessageNotFound
					}
					return err2
				}
				return nil
			}
			return ErrMessageNotFound
		}
		return err
	}

	return nil
}

func (m *Mailbox) markUnreadLegacy(id string) error {
	fl, err := m.lockLegacy()
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	messages, err := m.List()
	if err != nil {
		return err
	}

	found := false
	for _, msg := range messages {
		if msg.ID == id {
			msg.Read = false
			found = true
		}
	}

	if !found {
		return ErrMessageNotFound
	}

	return m.rewriteLegacy(messages)
}

// Delete removes a message.
func (m *Mailbox) Delete(id string) error {
	if m.legacy {
		return m.deleteLegacy(id)
	}
	return m.MarkRead(id) // beads: just acknowledge/close
}

func (m *Mailbox) deleteLegacy(id string) error {
	fl, err := m.lockLegacy()
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	messages, err := m.List()
	if err != nil {
		return err
	}

	var filtered []*Message
	found := false
	for _, msg := range messages {
		if msg.ID == id {
			found = true
		} else {
			filtered = append(filtered, msg)
		}
	}

	if !found {
		return ErrMessageNotFound
	}

	return m.rewriteLegacy(filtered)
}

// Archive moves a message to the archive file and removes it from inbox.
//
// Archive is a mail cleanup operation, not a bead operation. If the
// underlying bead has been garbage collected (by `bd mol wisp gc` or
// `bd compact`), there is nothing to append to the archive and nothing
// to close — we still return nil so the caller's inbox reference is
// considered cleared. See aa-6hv.
func (m *Mailbox) Archive(id string) error {
	if m.legacy {
		return m.archiveLegacy(id)
	}
	// Beads mode: append to archive then close
	msg, err := m.Get(id)
	if err != nil {
		if errors.Is(err, ErrMessageNotFound) {
			// Underlying bead has been GC'd; nothing to archive or close.
			return nil
		}
		return err
	}
	if err := m.appendToArchive(msg); err != nil {
		return err
	}
	if err := m.Delete(id); err != nil {
		if errors.Is(err, ErrMessageNotFound) {
			// Bead was GC'd between Get and Delete; metadata is archived,
			// and there is nothing left to close.
			return nil
		}
		return err
	}
	return nil
}

// archiveLegacy moves a message to the archive file atomically.
// A single flock covers the entire read-archive-rewrite cycle so that
// a crash between appendToArchive and the inbox rewrite cannot lose the
// message (worst case: duplicate in both archive and inbox).
func (m *Mailbox) archiveLegacy(id string) error {
	fl, err := m.lockLegacy()
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	// Read inbox
	messages, err := m.listLegacy()
	if err != nil {
		return err
	}

	// Find and extract target
	var target *Message
	var remaining []*Message
	for _, msg := range messages {
		if msg.ID == id {
			target = msg
		} else {
			remaining = append(remaining, msg)
		}
	}
	if target == nil {
		return ErrMessageNotFound
	}

	// Append to archive first (safe failure mode: duplicate, not loss)
	if err := m.appendToArchive(target); err != nil {
		return err
	}

	// Rewrite inbox without the target
	return m.rewriteLegacy(remaining)
}

// ArchivePath returns the path to the archive file.
func (m *Mailbox) ArchivePath() string {
	if m.legacy {
		return m.path + ".archive"
	}
	// For beads, use archive.jsonl in the same directory as beads
	return filepath.Join(m.beadsDir, "archive.jsonl")
}

func (m *Mailbox) appendToArchive(msg *Message) error {
	archivePath := m.ArchivePath()

	// Ensure directory exists
	dir := filepath.Dir(archivePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Open for append
	file, err := os.OpenFile(archivePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) //nolint:gosec // G302: archive is non-sensitive operational data
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	_, err = file.WriteString(string(data) + "\n")
	return err
}

// ListArchived returns all messages in the archive file.
func (m *Mailbox) ListArchived() ([]*Message, error) {
	archivePath := m.ArchivePath()

	file, err := os.Open(archivePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var messages []*Message
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return nil, fmt.Errorf("corrupt archive %s line %d: %w", archivePath, lineNum, err)
		}
		messages = append(messages, &msg)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return messages, nil
}

// PurgeArchive removes messages from the archive, optionally filtering by age.
// If olderThanDays is 0, removes all archived messages.
func (m *Mailbox) PurgeArchive(olderThanDays int) (int, error) {
	if m.legacy {
		fl, err := m.lockLegacy()
		if err != nil {
			return 0, err
		}
		defer func() { _ = fl.Unlock() }()
	}

	messages, err := m.ListArchived()
	if err != nil {
		return 0, err
	}

	if len(messages) == 0 {
		return 0, nil
	}

	// If no age filter, remove all
	if olderThanDays <= 0 {
		if err := os.Remove(m.ArchivePath()); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		return len(messages), nil
	}

	// Filter by age
	cutoff := timeNow().AddDate(0, 0, -olderThanDays)
	var keep []*Message
	purged := 0

	for _, msg := range messages {
		if msg.Timestamp.Before(cutoff) {
			purged++
		} else {
			keep = append(keep, msg)
		}
	}

	// Rewrite archive with remaining messages
	if len(keep) == 0 {
		if err := os.Remove(m.ArchivePath()); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
	} else {
		if err := m.rewriteArchive(keep); err != nil {
			return 0, err
		}
	}

	return purged, nil
}

func (m *Mailbox) rewriteArchive(messages []*Message) error {
	archivePath := m.ArchivePath()
	tmpPath := archivePath + ".tmp"

	file, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	for _, msg := range messages {
		data, err := json.Marshal(msg)
		if err != nil {
			_ = file.Close()
			_ = os.Remove(tmpPath)
			return err
		}
		if _, err := file.WriteString(string(data) + "\n"); err != nil {
			_ = file.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("writing archive: %w", err)
		}
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, archivePath)
}

// SearchOptions specifies search parameters.
type SearchOptions struct {
	Query       string // Regex pattern to search for
	FromFilter  string // Optional: only match messages from this sender
	SubjectOnly bool   // Only search subject
	BodyOnly    bool   // Only search body
}

// Search finds messages matching the given criteria.
// Returns messages from both inbox and archive.
// Query and FromFilter are treated as literal strings (not regex) to prevent ReDoS.
func (m *Mailbox) Search(opts SearchOptions) ([]*Message, error) {
	// Use QuoteMeta to escape special regex chars - prevents ReDoS attacks
	// and provides intuitive literal string matching for users
	re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(opts.Query))
	if err != nil {
		return nil, fmt.Errorf("invalid search pattern: %w", err)
	}

	var fromRe *regexp.Regexp
	if opts.FromFilter != "" {
		fromRe, err = regexp.Compile("(?i)" + regexp.QuoteMeta(opts.FromFilter))
		if err != nil {
			return nil, fmt.Errorf("invalid from pattern: %w", err)
		}
	}

	// Get inbox messages
	inbox, err := m.List()
	if err != nil {
		return nil, err
	}

	// Get archived messages
	archived, err := m.ListArchived()
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	// Combine and search
	all := append(inbox, archived...)
	var matches []*Message

	for _, msg := range all {
		// Apply from filter
		if fromRe != nil && !fromRe.MatchString(msg.From) {
			continue
		}

		// Search in specified fields
		matched := false
		if opts.SubjectOnly {
			matched = re.MatchString(msg.Subject)
		} else if opts.BodyOnly {
			matched = re.MatchString(msg.Body)
		} else {
			// Search in both subject and body
			matched = re.MatchString(msg.Subject) || re.MatchString(msg.Body)
		}

		if matched {
			matches = append(matches, msg)
		}
	}

	// Sort by priority (higher first), then timestamp (newest first).
	sort.Slice(matches, func(i, j int) bool {
		pi, pj := PriorityToBeads(matches[i].Priority), PriorityToBeads(matches[j].Priority)
		if pi != pj {
			return pi < pj
		}
		return matches[i].Timestamp.After(matches[j].Timestamp)
	})

	return matches, nil
}

// Count returns the total and unread message counts.
func (m *Mailbox) Count() (total, unread int, err error) {
	messages, err := m.List()
	if err != nil {
		return 0, 0, err
	}

	total = len(messages)
	// Count messages that are NOT marked as read (including via "read" label)
	for _, msg := range messages {
		if !msg.Read {
			unread++
		}
	}

	return total, unread, nil
}

// AcknowledgeDeliveries marks delivery receipt for unread messages where this
// mailbox is the primary recipient. This is phase-2 of two-phase delivery
// tracking (phase-1 is written at send time as delivery:pending).
// Acks are run concurrently (bounded to 8) to avoid N+1 sequential subprocess
// spawns on the hot path.
func (m *Mailbox) AcknowledgeDeliveries(recipientAddress string, messages []*Message) error {
	if m.legacy || len(messages) == 0 {
		return nil
	}

	recipientIdentity := AddressToIdentity(recipientAddress)

	// Collect messages that need acking.
	var toAck []*Message
	for _, msg := range messages {
		if msg == nil || msg.ID == "" {
			continue
		}
		if AddressToIdentity(msg.To) != recipientIdentity {
			continue
		}
		if msg.DeliveryState == "" {
			continue
		}
		toAck = append(toAck, msg)
	}
	if len(toAck) == 0 {
		return nil
	}

	// Run acks concurrently with bounded parallelism.
	const maxConcurrentAckOps = 8
	sem := make(chan struct{}, maxConcurrentAckOps)
	var mu sync.Mutex
	var errs []string
	var wg sync.WaitGroup

	for _, msg := range toAck {
		wg.Add(1)
		sem <- struct{}{} // acquire
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }() // release
			if err := AcknowledgeDeliveryBead(m.workDir, m.beadsDir, id, recipientIdentity); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", id, err))
				mu.Unlock()
			}
		}(msg.ID)
	}
	wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("acknowledging deliveries failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Append adds a message to the mailbox (legacy mode only).
// For beads mode, use Router.Send() instead.
func (m *Mailbox) Append(msg *Message) error {
	if !m.legacy {
		return errors.New("use Router.Send() to send messages via beads")
	}
	return m.appendLegacy(msg)
}

func (m *Mailbox) appendLegacy(msg *Message) error {
	// Ensure directory exists before acquiring lock (lock file is in same dir)
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	fl, err := m.lockLegacy()
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	// Open for append
	file, err := os.OpenFile(m.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }() // non-fatal: OS will close on exit

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	_, err = file.WriteString(string(data) + "\n")
	return err
}

// rewriteLegacy rewrites the mailbox with the given messages.
func (m *Mailbox) rewriteLegacy(messages []*Message) error {
	// Sort by timestamp (oldest first for JSONL)
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Timestamp.Before(messages[j].Timestamp)
	})

	// Write to temp file
	tmpPath := m.path + ".tmp"
	file, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	for _, msg := range messages {
		data, err := json.Marshal(msg)
		if err != nil {
			_ = file.Close()       // best-effort cleanup
			_ = os.Remove(tmpPath) // best-effort cleanup
			return err
		}
		if _, err := file.WriteString(string(data) + "\n"); err != nil {
			_ = file.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("writing mailbox: %w", err)
		}
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath) // best-effort cleanup
		return err
	}

	// Atomic rename
	return os.Rename(tmpPath, m.path)
}

// ListByThread returns all messages in a given thread.
func (m *Mailbox) ListByThread(threadID string) ([]*Message, error) {
	if m.legacy {
		return m.listByThreadLegacy(threadID)
	}
	return m.listByThreadBeads(threadID)
}

func (m *Mailbox) listByThreadBeads(threadID string) ([]*Message, error) {
	args := []string{"message", "thread", threadID, "--json"}

	ctx, cancel := bdReadCtx()
	defer cancel()
	stdout, err := runBdCommand(ctx, args, m.workDir, m.beadsDir, "BD_IDENTITY="+m.identity)
	if err != nil {
		return nil, err
	}

	if !isJSON(stdout) {
		return nil, nil
	}
	var beadsMsgs []BeadsMessage
	if err := json.Unmarshal(stdout, &beadsMsgs); err != nil {
		return nil, err
	}

	var messages []*Message
	for _, bm := range beadsMsgs {
		messages = append(messages, bm.ToMessage())
	}

	// Sort by timestamp (oldest first for thread view)
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Timestamp.Before(messages[j].Timestamp)
	})

	return messages, nil
}

func (m *Mailbox) listByThreadLegacy(threadID string) ([]*Message, error) {
	messages, err := m.List()
	if err != nil {
		return nil, err
	}

	var thread []*Message
	for _, msg := range messages {
		if msg.ThreadID == threadID {
			thread = append(thread, msg)
		}
	}

	// Sort by timestamp (oldest first for thread view)
	sort.Slice(thread, func(i, j int) bool {
		return thread[i].Timestamp.Before(thread[j].Timestamp)
	})

	return thread, nil
}

// isJSON returns true if the byte slice looks like JSON (starts with [ or {).
// bd list --json may return plain text like "No issues found." instead of JSON
// when there are no results.
func isJSON(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '[', '{':
			return true
		default:
			return false
		}
	}
	return false
}
