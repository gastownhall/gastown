package beads

import (
	"encoding/json"
	"testing"
)

func TestMergeSlotLeaseEvidenceRoundTrips(t *testing.T) {
	data := mergeSlotData{
		Holder: "rig/refinery/push/token",
		Lease: &MergeLease{
			OwnerToken:    "rig/refinery/push/token",
			Phase:         MergeLeasePhaseMutationPossible,
			Target:        "main",
			PrePushSHA:    "before",
			IntendedTip:   "after",
			MRIDs:         []string{"mr-1", "mr-2"},
			SubmittedSHAs: []string{"submitted-1", "submitted-2"},
			BatchID:       "batch-identity",
		},
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var decoded mergeSlotData
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Lease == nil || decoded.Lease.OwnerToken != data.Holder ||
		decoded.Lease.Phase != MergeLeasePhaseMutationPossible ||
		decoded.Lease.PrePushSHA != "before" || decoded.Lease.IntendedTip != "after" ||
		decoded.Lease.BatchID != "batch-identity" || len(decoded.Lease.MRIDs) != 2 ||
		len(decoded.Lease.SubmittedSHAs) != 2 {
		t.Fatalf("decoded lease = %+v", decoded.Lease)
	}
}

func TestMergeSlotLeaseTransitionAndReleaseRequireExactToken(t *testing.T) {
	data := mergeSlotData{
		Holder: "original-token",
		Lease: &MergeLease{
			OwnerToken: "original-token",
			Phase:      MergeLeasePhaseProofEstablished,
		},
	}
	if err := transitionMergeSlotData(&data, "different-token", MergeLeasePhaseLifecycleComplete); err == nil {
		t.Fatal("transition accepted a different token")
	}
	if err := releaseMergeSlotData(&data, "different-token"); err == nil {
		t.Fatal("release accepted a different token")
	}
	if data.Holder != "original-token" || data.Lease == nil {
		t.Fatalf("token mismatch mutated data: %+v", data)
	}
	if err := transitionMergeSlotData(&data, "original-token", MergeLeasePhaseLifecycleComplete); err != nil {
		t.Fatalf("exact transition: %v", err)
	}
	if err := releaseMergeSlotData(&data, "original-token"); err != nil {
		t.Fatalf("exact release: %v", err)
	}
	if data.Holder != "" || data.Lease != nil {
		t.Fatalf("exact release left lease: %+v", data)
	}
}
