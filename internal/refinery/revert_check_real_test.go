package refinery

import (
	"os"
	"testing"
)

// Validation against the ACTUAL ap-8iyu objects rather than a constructed
// scenario. The synthetic tests prove the rule; this proves it fires on the
// incident it was written for, with the file and superseded commit the witness
// and refinery independently identified:
//
//	8ee8f142 pre-dust     blob e789af3b8cac
//	7fbe7ae3 dust landed  blob 922240d06478
//	485be74f guzzle       blob e789af3b8cac  <- byte-identical to pre-dust
//
// Skipped unless the alphaprime repo is present, so it never breaks CI elsewhere.
func TestCheckForReverts_TheRealAp8iyuIncident(t *testing.T) {
	const repo = "/home/gastown/gt/alphaprime/.repo.git"
	if _, err := os.Stat(repo); err != nil {
		t.Skip("alphaprime repo not present on this host")
	}

	findings, err := CheckForReverts(repo, "7fbe7ae3", "485be74f", 0)
	if err != nil {
		t.Skipf("objects not available: %v", err)
	}

	var hit *RevertFinding
	for i := range findings {
		if findings[i].Path == "python/tests/test_tv_native_ledger.py" {
			hit = &findings[i]
		}
	}
	if hit == nil {
		t.Fatalf("did not flag python/tests/test_tv_native_ledger.py; got %d findings: %v", len(findings), findings)
	}
	if got := hit.SupersededCommit[:8]; got != "8ee8f142" {
		t.Errorf("named superseded commit %s, expected 8ee8f142", got)
	}
	t.Logf("flagged correctly: %s", hit)
}
