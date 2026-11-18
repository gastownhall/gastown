package beads

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"
)

func TestPromoteWispUsesSDKPromotion(t *testing.T) {
	data, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	body := sourceBetween(t, string(data), "func (b *Beads) PromoteWisp(", "// storeAddLabel")

	for _, want := range []string{
		"OpenStore(ctx)",
		"PromoteFromEphemeral(context.Context, string, string) error",
		"promoter.PromoteFromEphemeral(ctx, id, actor)",
		"store.RunInTransaction(ctx",
		"tx.ImportIssueComment(ctx, id, actor, comment",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("PromoteWisp missing %q:\n%s", want, body)
		}
	}

	for _, forbidden := range []string{
		`Run("update"`,
		`Run("promote"`,
		`Run("comments"`,
		`--persistent`,
		`depends_on_id`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("PromoteWisp should not use %q:\n%s", forbidden, body)
		}
	}
}

func TestPromoteWispCallsSDKPromotionAndComments(t *testing.T) {
	store := &promoteWispStore{tx: &promoteWispTx{}}
	bd := NewWithStore(t.TempDir(), store)
	t.Setenv("BD_ACTOR", "tester")

	if err := bd.PromoteWisp("gt-wisp-alpha", "has comments"); err != nil {
		t.Fatalf("PromoteWisp: %v", err)
	}

	if store.promotedID != "gt-wisp-alpha" {
		t.Fatalf("promoted ID = %q, want gt-wisp-alpha", store.promotedID)
	}
	if store.promotedActor != "tester" {
		t.Fatalf("promoted actor = %q, want tester", store.promotedActor)
	}
	if store.txMessage != "bd: comment on promoted wisp gt-wisp-alpha" {
		t.Fatalf("transaction message = %q", store.txMessage)
	}
	if store.tx.issueID != "gt-wisp-alpha" || store.tx.author != "tester" {
		t.Fatalf("comment target = %q/%q, want gt-wisp-alpha/tester", store.tx.issueID, store.tx.author)
	}
	if store.tx.text != "Promoted from Level 0: has comments" {
		t.Fatalf("comment text = %q", store.tx.text)
	}
	if store.tx.createdAt.IsZero() {
		t.Fatalf("comment timestamp was not set")
	}
}

type promoteWispStore struct {
	beadsdk.Storage
	promotedID    string
	promotedActor string
	txMessage     string
	tx            *promoteWispTx
}

func (s *promoteWispStore) PromoteFromEphemeral(_ context.Context, id, actor string) error {
	s.promotedID = id
	s.promotedActor = actor
	return nil
}

func (s *promoteWispStore) RunInTransaction(_ context.Context, msg string, fn func(beadsdk.Transaction) error) error {
	s.txMessage = msg
	return fn(s.tx)
}

type promoteWispTx struct {
	beadsdk.Transaction
	issueID   string
	author    string
	text      string
	createdAt time.Time
}

func (tx *promoteWispTx) ImportIssueComment(_ context.Context, issueID, author, text string, createdAt time.Time) (*beadsdk.Comment, error) {
	tx.issueID = issueID
	tx.author = author
	tx.text = text
	tx.createdAt = createdAt
	return &beadsdk.Comment{}, nil
}

func sourceBetween(t *testing.T, source, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(source, startMarker)
	if start == -1 {
		t.Fatalf("could not find %q", startMarker)
	}
	end := strings.Index(source[start:], endMarker)
	if end == -1 {
		t.Fatalf("could not find %q after %q", endMarker, startMarker)
	}
	return source[start : start+end]
}
