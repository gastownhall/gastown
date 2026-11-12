package beads

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	beadsdk "github.com/steveyegge/beads"
)

func TestPromoteWispUsesSDKPromotion(t *testing.T) {
	data, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	source := string(data)
	body := sourceBetween(t, source, "func (b *Beads) PromoteWisp(", "// storeAddLabel")

	for _, want := range []string{
		"type wispPromoter interface",
		"PromoteFromEphemeral(context.Context, string, string) error",
		"type commentEventStore interface",
		"AddComment(context.Context, string, string, string) error",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("store.go missing %q", want)
		}
	}

	for _, want := range []string{
		"OpenStore(ctx)",
		"store.(wispPromoter)",
		"promoter.PromoteFromEphemeral(ctx, id, actor)",
		"store.(commentEventStore)",
		"commenter.AddComment(ctx, id, actor, comment)",
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
		`RunInTransaction`,
		`ImportIssueComment`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("PromoteWisp should not use %q:\n%s", forbidden, body)
		}
	}
}

func TestPromoteWispCallsSDKPromotionAndComments(t *testing.T) {
	store := &promoteWispStore{}
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
	if store.commentID != "gt-wisp-alpha" || store.commentActor != "tester" {
		t.Fatalf("comment target = %q/%q, want gt-wisp-alpha/tester", store.commentID, store.commentActor)
	}
	if store.commentText != "Promoted from Level 0: has comments" {
		t.Fatalf("comment text = %q", store.commentText)
	}
}

func TestPromoteWispTreatsCommentFailureAsBestEffort(t *testing.T) {
	wantErr := errors.New("comment failed")
	store := &promoteWispStore{commentErr: wantErr}
	bd := NewWithStore(t.TempDir(), store)
	t.Setenv("BD_ACTOR", "tester")

	if err := bd.PromoteWisp("gt-wisp-beta", "comment may fail"); err != nil {
		t.Fatalf("PromoteWisp should not fail when promotion comment fails: %v", err)
	}
	if store.promotedID != "gt-wisp-beta" {
		t.Fatalf("promoted ID = %q, want gt-wisp-beta", store.promotedID)
	}
}

func TestPromoteWispRequiresPromotionCapability(t *testing.T) {
	bd := NewWithStore(t.TempDir(), &commentOnlyStore{})

	err := bd.PromoteWisp("gt-wisp-gamma", "no promoter")
	if err == nil || !strings.Contains(err.Error(), "does not support wisp promotion") {
		t.Fatalf("PromoteWisp error = %v, want unsupported promotion", err)
	}
}

type promoteWispStore struct {
	beadsdk.Storage
	promotedID    string
	promotedActor string
	commentID     string
	commentActor  string
	commentText   string
	commentErr    error
}

func (s *promoteWispStore) PromoteFromEphemeral(_ context.Context, id, actor string) error {
	s.promotedID = id
	s.promotedActor = actor
	return nil
}

func (s *promoteWispStore) AddComment(_ context.Context, issueID, actor, comment string) error {
	s.commentID = issueID
	s.commentActor = actor
	s.commentText = comment
	return s.commentErr
}

type commentOnlyStore struct {
	beadsdk.Storage
}

func (s *commentOnlyStore) AddComment(context.Context, string, string, string) error {
	return nil
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
