package cmd

import (
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/doltserver"
)

// TestStampLoop_EndToEnd exercises the full pilot stamp loop:
// post → claim → done → stamp → query → verify passbook chain.
func TestStampLoop_EndToEnd(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()

	// Step 1: Post a wanted item
	item := &doltserver.WantedItem{
		ID:       "w-e2e-001",
		Title:    "Fix auth bug",
		Type:     "bug",
		Priority: 1,
		PostedBy: "test-poster",
	}
	if err := postWanted(store, item); err != nil {
		t.Fatalf("postWanted() error: %v", err)
	}

	// Step 2: Claim it
	_, err := claimWanted(store, "w-e2e-001", "test-worker")
	if err != nil {
		t.Fatalf("claimWanted() error: %v", err)
	}

	// Step 3: Complete it
	if err := submitDone(store, "w-e2e-001", "test-worker", "https://github.com/example/pr/1", "c-e2e-001"); err != nil {
		t.Fatalf("submitDone() error: %v", err)
	}

	// Step 4: Stamp it (validator stamps worker)
	stamp1 := &doltserver.StampRecord{
		ID:          "s-e2e-001",
		Author:      "test-validator",
		Subject:     "test-worker",
		Valence:     `{"quality":4,"reliability":5,"creativity":3}`,
		Confidence:  0.7,
		Severity:    "leaf",
		ContextID:   "c-e2e-001",
		ContextType: "completion",
		StampType:   "work",
		SkillTags:   `["go","auth"]`,
		Message:     "Good fix for auth bug",
		StampIndex:  -1,
	}
	if err := insertStamp(store, stamp1); err != nil {
		t.Fatalf("insertStamp() error: %v", err)
	}

	// Step 5: Verify first stamp — genesis (index 0, no prev hash)
	if stamp1.StampIndex != 0 {
		t.Errorf("first stamp index = %d, want 0", stamp1.StampIndex)
	}
	if stamp1.PrevStampHash != "" {
		t.Errorf("first stamp prev hash = %q, want empty", stamp1.PrevStampHash)
	}

	// Step 6: Second stamp — chain linkage
	stamp2 := &doltserver.StampRecord{
		ID:          "s-e2e-002",
		Author:      "test-validator",
		Subject:     "test-worker",
		Valence:     `{"quality":5,"reliability":4}`,
		Confidence:  0.7,
		Severity:    "leaf",
		ContextType: "endorsement",
		StampType:   "peer_review",
		SkillTags:   `["go"]`,
		Message:     "Excellent follow-up",
		StampIndex:  -1,
	}
	if err := insertStamp(store, stamp2); err != nil {
		t.Fatalf("insertStamp(second) error: %v", err)
	}

	if stamp2.StampIndex != 1 {
		t.Errorf("second stamp index = %d, want 1", stamp2.StampIndex)
	}
	if stamp2.PrevStampHash == "" {
		t.Error("second stamp should have prev_stamp_hash linking to first stamp")
	}

	// Step 7: Query stamps for subject
	stamps, err := store.QueryStampsForSubject("test-worker")
	if err != nil {
		t.Fatalf("QueryStampsForSubject() error: %v", err)
	}
	if len(stamps) != 2 {
		t.Fatalf("QueryStampsForSubject() returned %d stamps, want 2", len(stamps))
	}

	// Step 8: Assemble character sheet
	sheet, err := doltserver.AssembleCharacterSheet(store, "test-worker")
	if err != nil {
		t.Fatalf("AssembleCharacterSheet() error: %v", err)
	}
	if sheet.StampCount != 2 {
		t.Errorf("sheet StampCount = %d, want 2", sheet.StampCount)
	}
	if len(sheet.StampGeometry) == 0 {
		t.Error("sheet StampGeometry should not be empty")
	}
	if g, ok := sheet.StampGeometry["quality"]; !ok {
		t.Error("missing quality geometry")
	} else if g.Count != 2 {
		t.Errorf("quality stamp count = %d, want 2", g.Count)
	}

	// Verify skill coverage
	if len(sheet.SkillCoverage) == 0 {
		t.Error("sheet SkillCoverage should not be empty")
	}

	// Top stamps should include both (quality >= 4, completion/endorsement)
	if len(sheet.TopStamps) != 2 {
		t.Errorf("top stamps = %d, want 2", len(sheet.TopStamps))
	}
}

// TestStampLoop_SelfStampFails verifies the yearbook rule (author != subject).
// Not parallel: mutates package-level wlStamp* globals.
func TestStampLoop_SelfStampFails(t *testing.T) {
	// Save/restore globals
	origQ, origR, origC := wlStampQuality, wlStampReliability, wlStampCreativity
	origSev, origType, origCtx := wlStampSeverity, wlStampType, wlStampContextType
	origConf, origSubj := wlStampConfidence, wlStampSubject
	defer func() {
		wlStampQuality, wlStampReliability, wlStampCreativity = origQ, origR, origC
		wlStampSeverity, wlStampType, wlStampContextType = origSev, origType, origCtx
		wlStampConfidence, wlStampSubject = origConf, origSubj
	}()

	wlStampQuality = 4
	wlStampReliability = -1
	wlStampCreativity = -1
	wlStampConfidence = -1
	wlStampSeverity = "leaf"
	wlStampType = "work"
	wlStampContextType = "completion"
	wlStampSubject = "self-rig"

	// The DB layer InsertStamp validates author != subject
	stamp := &doltserver.StampRecord{
		ID:         "s-self-001",
		Author:     "self-rig",
		Subject:    "self-rig",
		Valence:    `{"quality":4}`,
		Confidence: 0.7,
		Severity:   "leaf",
		StampIndex: -1,
	}

	// The insertStamp function in wl_stamp.go doesn't check author==subject
	// (that's done in runWlStamp before calling insertStamp), but the DB
	// layer InsertStamp in wl_commons.go does. Let's verify the DB validation.
	err := doltserver.InsertStamp("", stamp)
	if err == nil {
		t.Fatal("InsertStamp should fail for author == subject")
	}
	if !strings.Contains(err.Error(), "author cannot equal subject") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestStampLoop_InvalidValence verifies validation rejects out-of-range scores.
// Not parallel: mutates package-level wlStamp* globals.
func TestStampLoop_InvalidValence(t *testing.T) {
	tests := []struct {
		name    string
		quality float64
		wantErr string
	}{
		{"quality too high", 6.0, "quality"},
		{"quality negative", -0.5, "quality"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origQ, origR, origC := wlStampQuality, wlStampReliability, wlStampCreativity
			origSev, origType, origCtx := wlStampSeverity, wlStampType, wlStampContextType
			defer func() {
				wlStampQuality, wlStampReliability, wlStampCreativity = origQ, origR, origC
				wlStampSeverity, wlStampType, wlStampContextType = origSev, origType, origCtx
			}()

			wlStampQuality = tt.quality
			wlStampReliability = -1
			wlStampCreativity = -1
			wlStampSeverity = "leaf"
			wlStampType = "work"
			wlStampContextType = "completion"

			err := validateStampInputs()
			if err == nil {
				t.Fatal("validateStampInputs() should fail")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestStampLoop_PassbookChainIntegrity verifies the full chain grows correctly.
func TestStampLoop_PassbookChainIntegrity(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()

	// Create a chain of 5 stamps
	for i := 0; i < 5; i++ {
		stamp := &doltserver.StampRecord{
			ID:          "s-chain-" + string(rune('a'+i)),
			Author:      "validator",
			Subject:     "worker",
			Valence:     `{"quality":4}`,
			Confidence:  0.7,
			Severity:    "leaf",
			ContextType: "completion",
			StampIndex:  -1,
		}
		if err := insertStamp(store, stamp); err != nil {
			t.Fatalf("insertStamp(%d) error: %v", i, err)
		}

		if stamp.StampIndex != i {
			t.Errorf("stamp[%d].StampIndex = %d, want %d", i, stamp.StampIndex, i)
		}

		if i == 0 {
			if stamp.PrevStampHash != "" {
				t.Errorf("stamp[0].PrevStampHash should be empty, got %q", stamp.PrevStampHash)
			}
		} else {
			if stamp.PrevStampHash == "" {
				t.Errorf("stamp[%d].PrevStampHash should not be empty", i)
			}
		}
	}

	// Verify total chain length
	stamps, _ := store.QueryStampsForSubject("worker")
	if len(stamps) != 5 {
		t.Errorf("chain length = %d, want 5", len(stamps))
	}
}

// TestStampLoop_CharsheetWithRootWarning verifies root-severity stamps appear in warnings.
func TestStampLoop_CharsheetWithRootWarning(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()

	// Normal stamps
	store.stamps = []doltserver.StampRecord{
		{ID: "s-good", Author: "v1", Subject: "risky-rig", Valence: `{"quality":4}`, Confidence: 0.7, Severity: "leaf", ContextType: "completion"},
		{ID: "s-bad", Author: "guardian", Subject: "risky-rig", Valence: `{"quality":1}`, Confidence: 0.9, Severity: "root", ContextType: "completion", Message: "Poisoned shared infra", CreatedAt: "2026-01-20"},
	}

	sheet, err := doltserver.AssembleCharacterSheet(store, "risky-rig")
	if err != nil {
		t.Fatalf("AssembleCharacterSheet() error: %v", err)
	}

	// Root stamp should be excluded from geometry
	if g, ok := sheet.StampGeometry["quality"]; ok {
		if g.Count != 1 {
			t.Errorf("quality count = %d, want 1 (root excluded)", g.Count)
		}
	}

	// Warning should be present
	if len(sheet.Warnings) != 1 {
		t.Fatalf("warnings = %d, want 1", len(sheet.Warnings))
	}
	if sheet.Warnings[0].Author != "guardian" {
		t.Errorf("warning author = %q, want guardian", sheet.Warnings[0].Author)
	}
}
