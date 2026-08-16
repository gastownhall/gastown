package cmd

import (
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/doltserver"
)

func TestValidateStampInputs_Valid(t *testing.T) {
	// Not parallel: mutates package-level flag vars
	// Save and restore globals
	origQ, origR, origC := wlStampQuality, wlStampReliability, wlStampCreativity
	origSev, origType, origCtx := wlStampSeverity, wlStampType, wlStampContextType
	origConf := wlStampConfidence
	defer func() {
		wlStampQuality, wlStampReliability, wlStampCreativity = origQ, origR, origC
		wlStampSeverity, wlStampType, wlStampContextType = origSev, origType, origCtx
		wlStampConfidence = origConf
	}()

	wlStampQuality = 4
	wlStampReliability = 3
	wlStampCreativity = 2
	wlStampConfidence = 0.7
	wlStampSeverity = "leaf"
	wlStampType = "work"
	wlStampContextType = "completion"

	if err := validateStampInputs(); err != nil {
		t.Errorf("validateStampInputs() = %v, want nil", err)
	}
}

func TestValidateStampInputs_QualityOutOfRange(t *testing.T) {
	// Not parallel: mutates package-level flag vars
	origQ, origR, origC := wlStampQuality, wlStampReliability, wlStampCreativity
	origSev, origType, origCtx := wlStampSeverity, wlStampType, wlStampContextType
	defer func() {
		wlStampQuality, wlStampReliability, wlStampCreativity = origQ, origR, origC
		wlStampSeverity, wlStampType, wlStampContextType = origSev, origType, origCtx
	}()

	wlStampQuality = 6
	wlStampReliability = -1
	wlStampCreativity = -1
	wlStampSeverity = "leaf"
	wlStampType = "work"
	wlStampContextType = "completion"

	err := validateStampInputs()
	if err == nil || !strings.Contains(err.Error(), "quality") {
		t.Errorf("validateStampInputs() = %v, want quality error", err)
	}
}

func TestValidateStampInputs_BadSeverity(t *testing.T) {
	// Not parallel: mutates package-level flag vars
	origQ, origR, origC := wlStampQuality, wlStampReliability, wlStampCreativity
	origSev, origType, origCtx := wlStampSeverity, wlStampType, wlStampContextType
	defer func() {
		wlStampQuality, wlStampReliability, wlStampCreativity = origQ, origR, origC
		wlStampSeverity, wlStampType, wlStampContextType = origSev, origType, origCtx
	}()

	wlStampQuality = 3
	wlStampReliability = -1
	wlStampCreativity = -1
	wlStampSeverity = "invalid"
	wlStampType = "work"
	wlStampContextType = "completion"

	err := validateStampInputs()
	if err == nil || !strings.Contains(err.Error(), "severity") {
		t.Errorf("validateStampInputs() = %v, want severity error", err)
	}
}

func TestValidateStampInputs_BadStampType(t *testing.T) {
	// Not parallel: mutates package-level flag vars
	origQ, origR, origC := wlStampQuality, wlStampReliability, wlStampCreativity
	origSev, origType, origCtx := wlStampSeverity, wlStampType, wlStampContextType
	defer func() {
		wlStampQuality, wlStampReliability, wlStampCreativity = origQ, origR, origC
		wlStampSeverity, wlStampType, wlStampContextType = origSev, origType, origCtx
	}()

	wlStampQuality = 3
	wlStampReliability = -1
	wlStampCreativity = -1
	wlStampSeverity = "leaf"
	wlStampType = "invalid"
	wlStampContextType = "completion"

	err := validateStampInputs()
	if err == nil || !strings.Contains(err.Error(), "stamp-type") {
		t.Errorf("validateStampInputs() = %v, want stamp-type error", err)
	}
}

func TestBuildValenceJSON_AllScores(t *testing.T) {
	t.Parallel()
	got := buildValenceJSON(4, 3, 2)
	if got != `{"quality":4,"reliability":3,"creativity":2}` {
		t.Errorf("buildValenceJSON(4,3,2) = %q", got)
	}
}

func TestBuildValenceJSON_QualityOnly(t *testing.T) {
	t.Parallel()
	got := buildValenceJSON(5, -1, -1)
	if got != `{"quality":5}` {
		t.Errorf("buildValenceJSON(5,-1,-1) = %q", got)
	}
}

func TestBuildSkillTagsJSON(t *testing.T) {
	t.Parallel()
	got := buildSkillTagsJSON([]string{"go", "federation"})
	if got != `["go","federation"]` {
		t.Errorf("buildSkillTagsJSON = %q", got)
	}
}

func TestBuildSkillTagsJSON_Single(t *testing.T) {
	t.Parallel()
	got := buildSkillTagsJSON([]string{"rust"})
	if got != `["rust"]` {
		t.Errorf("buildSkillTagsJSON(single) = %q", got)
	}
}

func TestGenerateStampID_Format(t *testing.T) {
	t.Parallel()
	id := generateStampID("alice", "bob", `{"quality":4}`, "c-abc")
	if !strings.HasPrefix(id, "s-") {
		t.Errorf("stamp ID should start with s-, got %q", id)
	}
	if len(id) != 14 { // "s-" + 12 hex chars
		t.Errorf("stamp ID should be 14 chars, got %d: %q", len(id), id)
	}
}

func TestGenerateStampID_Unique(t *testing.T) {
	t.Parallel()
	id1 := generateStampID("alice", "bob", `{"quality":4}`, "c-abc")
	id2 := generateStampID("alice", "bob", `{"quality":4}`, "c-abc")
	if id1 == id2 {
		t.Error("sequential stamp IDs should be unique (includes nanosecond timestamp)")
	}
}

func TestComputeStampHash(t *testing.T) {
	t.Parallel()
	hash := computeStampHash("s-abc123def456")
	if len(hash) != 64 { // SHA-256 hex
		t.Errorf("stamp hash should be 64 hex chars, got %d", len(hash))
	}
}

func TestInsertStamp_GenesisStamp(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()
	stamp := &doltserver.StampRecord{
		ID:          "s-test001",
		Author:      "alice",
		Subject:     "bob",
		Valence:     `{"quality":4}`,
		Confidence:  0.7,
		Severity:    "leaf",
		ContextType: "completion",
		StampType:   "work",
		StampIndex:  -1,
	}

	err := insertStamp(store, stamp)
	if err != nil {
		t.Fatalf("insertStamp() = %v", err)
	}
	if stamp.StampIndex != 0 {
		t.Errorf("genesis stamp should have index 0, got %d", stamp.StampIndex)
	}
	if stamp.PrevStampHash != "" {
		t.Errorf("genesis stamp should have empty prev hash, got %q", stamp.PrevStampHash)
	}
}
