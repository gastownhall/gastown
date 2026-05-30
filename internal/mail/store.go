// Package mail: in-process beadsdk.Storage integration for mail operations.
//
// When a beadsdk.Storage is set on a Mailbox (via SetStore), methods bypass
// the bd subprocess and use the store directly. This eliminates ~600ms per
// operation for mail queries (inbox, get, mark-read).
package mail

import (
	"context"
	"fmt"
	"strings"
	"time"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/telemetry"
)

// SetStore configures an in-process beadsdk.Storage for this Mailbox.
// When set, beads-mode methods use the store directly instead of shelling
// out to the bd CLI. Legacy JSONL mode is unaffected.
//
// Callers are responsible for closing the store when done.
func (m *Mailbox) SetStore(store beadsdk.Storage) {
	m.store = store
}

// Store returns the in-process beadsdk.Storage, or nil if not set.
func (m *Mailbox) Store() beadsdk.Storage {
	return m.store
}

// NewMailboxBeadsWithStore creates a mailbox backed by an in-process beads store.
func NewMailboxBeadsWithStore(identity, workDir string, store beadsdk.Storage) *Mailbox {
	return &Mailbox{
		identity: identity,
		workDir:  workDir,
		legacy:   false,
		store:    store,
	}
}

// NewMailboxWithBeadsDirAndStore creates a mailbox with an explicit beads
// directory and an in-process store.
func NewMailboxWithBeadsDirAndStore(address, workDir, beadsDir string, store beadsdk.Storage) *Mailbox {
	return &Mailbox{
		identity: AddressToIdentity(address),
		workDir:  workDir,
		beadsDir: beadsDir,
		legacy:   false,
		store:    store,
	}
}

// mailStoreCtx returns a context with a standard timeout for mail store operations.
func mailStoreCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// storeListFromDir queries messages using the in-process store.
// Returns messages where identity is the assignee.
func (m *Mailbox) storeListFromDir() ([]*Message, error) {
	ctx, cancel := mailStoreCtx()
	defer cancel()

	identities := m.identityVariants()

	seen := make(map[string]bool)
	messages := make([]*Message, 0)

	// Query by assignee for each identity variant. We omit the status filter
	// so that a single query returns both "open" and "hooked" messages,
	// avoiding a redundant second round-trip per identity variant.
	for _, id := range identities {
		filter := beadsdk.IssueFilter{
			Labels:   []string{"gt:message"},
			Assignee: &id,
			Limit:    0, // No limit
		}

		sdkIssues, err := m.store.SearchIssues(ctx, "", filter)
		if err != nil {
			return nil, fmt.Errorf("store list messages: %w", err)
		}

		for _, si := range sdkIssues {
			if seen[si.ID] {
				continue
			}
			if si.Status == beadsdk.StatusOpen || string(si.Status) == "hooked" {
				seen[si.ID] = true
				messages = append(messages, sdkIssueToMessage(si))
			}
		}
	}

	return messages, nil
}

// storeGetFromDir retrieves a message using the in-process store.
func (m *Mailbox) storeGetFromDir(id string) (*Message, error) {
	ctx, cancel := mailStoreCtx()
	defer cancel()

	si, err := m.store.GetIssue(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, ErrMessageNotFound
		}
		return nil, fmt.Errorf("store get message: %w", err)
	}

	return sdkIssueToMessage(si), nil
}

// storeCloseInDir closes a message using the in-process store.
func (m *Mailbox) storeCloseInDir(id string) error {
	ctx, cancel := mailStoreCtx()
	defer cancel()

	sessionID := runtime.SessionIDFromEnv()
	err := m.store.CloseIssue(ctx, id, "", "", sessionID)
	telemetry.RecordMailMessage(context.Background(), "read", telemetry.MailMessageInfo{
		ID: id,
		To: m.identity,
	}, err)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return ErrMessageNotFound
		}
		return fmt.Errorf("store close message: %w", err)
	}
	return nil
}

// storeMarkReadOnly adds a "read" label using the in-process store.
func (m *Mailbox) storeMarkReadOnly(id string) error {
	ctx, cancel := mailStoreCtx()
	defer cancel()

	err := m.store.AddLabel(ctx, id, "read", "")
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return ErrMessageNotFound
		}
		return fmt.Errorf("store mark read: %w", err)
	}
	return nil
}

func (m *Mailbox) storeAcknowledgeDeliveryForPrimary(id string) error {
	ctx, cancel := mailStoreCtx()
	defer cancel()

	si, err := m.store.GetIssue(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return ErrMessageNotFound
		}
		return fmt.Errorf("store get message for delivery ack: %w", err)
	}
	if AddressToIdentity(si.Assignee) != m.identity {
		return nil
	}
	state, _, _ := ParseDeliveryLabels(si.Labels)
	if state == "" {
		return nil
	}

	toWrite := deliveryAckLabelsToWrite(m.identity, timeNow().UTC(), si.Labels)
	for _, label := range toWrite {
		if err := m.store.AddLabel(ctx, id, label, ""); err != nil {
			if strings.Contains(err.Error(), "not found") {
				return ErrMessageNotFound
			}
			return fmt.Errorf("store delivery ack: %w", err)
		}
	}

	labelsAfterAck := append(append([]string{}, si.Labels...), toWrite...)
	if !deliveryPendingRemovalNeeded(labelsAfterAck) {
		return nil
	}
	if err := m.store.RemoveLabel(ctx, id, DeliveryLabelPending, ""); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return ErrMessageNotFound
		}
		if strings.Contains(err.Error(), "does not have label") {
			return nil
		}
		return fmt.Errorf("store delivery convergence: %w", err)
	}
	return nil
}

// storeMarkUnreadOnly removes a "read" label using the in-process store.
func (m *Mailbox) storeMarkUnreadOnly(id string) error {
	ctx, cancel := mailStoreCtx()
	defer cancel()

	err := m.store.RemoveLabel(ctx, id, "read", "")
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return ErrMessageNotFound
		}
		// Ignore error if label doesn't exist
		if strings.Contains(err.Error(), "does not have label") {
			return nil
		}
		return fmt.Errorf("store mark unread: %w", err)
	}
	return nil
}

// storeMarkUnread reopens a message using the in-process store.
func (m *Mailbox) storeMarkUnread(id string) error {
	ctx, cancel := mailStoreCtx()
	defer cancel()

	updates := map[string]interface{}{
		"status": "open",
	}
	err := m.store.UpdateIssue(ctx, id, updates, "")
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return ErrMessageNotFound
		}
		return fmt.Errorf("store reopen message: %w", err)
	}
	return nil
}

// sdkIssueToMessage converts a beadsdk Issue to a mail Message by routing
// through BeadsMessage for correct label parsing and type conversion.
func sdkIssueToMessage(si *beadsdk.Issue) *Message {
	if si == nil {
		return nil
	}

	// Build a BeadsMessage from SDK issue fields, then use its ToMessage()
	// method for correct label parsing (from:, thread:, cc:, etc.).
	bm := &BeadsMessage{
		ID:          si.ID,
		Title:       si.Title,
		Description: si.Description,
		Assignee:    si.Assignee,
		Priority:    si.Priority,
		Status:      string(si.Status),
		CreatedAt:   si.CreatedAt,
		Labels:      si.Labels,
		Pinned:      si.Pinned,
		Wisp:        si.Ephemeral,
	}

	return bm.ToMessage()
}
