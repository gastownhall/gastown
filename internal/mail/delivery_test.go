package mail

import (
	"reflect"
	"testing"
	"time"
)

func TestDeliveryAckLabelSequenceOrder(t *testing.T) {
	at := time.Date(2026, 2, 17, 12, 0, 0, 0, time.UTC)
	got := DeliveryAckLabelSequence("gastown/worker", at)
	want := []string{
		"delivery-acked-by:gastown/worker",
		"delivery-acked-at:2026-02-17T12:00:00Z",
		"delivery:acked",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeliveryAckLabelSequence() = %v, want %v", got, want)
	}
}

func TestParseDeliveryLabels_CrashAndRetryStates(t *testing.T) {
	t.Run("pending only", func(t *testing.T) {
		state, by, at := ParseDeliveryLabels([]string{
			DeliveryLabelPending,
		})
		if state != DeliveryStatePending {
			t.Fatalf("state = %q, want %q", state, DeliveryStatePending)
		}
		if by != "" || at != nil {
			t.Fatalf("pending state should not include ack metadata, got by=%q at=%v", by, at)
		}
	})

	t.Run("partial ack write keeps pending", func(t *testing.T) {
		state, by, at := ParseDeliveryLabels([]string{
			DeliveryLabelPending,
			"delivery-acked-by:gastown/worker",
			"delivery-acked-at:2026-02-17T12:00:00Z",
		})
		if state != DeliveryStatePending {
			t.Fatalf("state = %q, want %q", state, DeliveryStatePending)
		}
		if by != "" || at != nil {
			t.Fatalf("partial ack should not flip state, got by=%q at=%v", by, at)
		}
	})

	t.Run("acked label flips state", func(t *testing.T) {
		state, by, at := ParseDeliveryLabels([]string{
			DeliveryLabelPending,
			"delivery-acked-by:gastown/worker",
			"delivery-acked-at:2026-02-17T12:00:00Z",
			DeliveryLabelAcked,
		})
		if state != DeliveryStateAcked {
			t.Fatalf("state = %q, want %q", state, DeliveryStateAcked)
		}
		if by != "gastown/worker" {
			t.Fatalf("ackedBy = %q, want %q", by, "gastown/worker")
		}
		if at == nil {
			t.Fatal("ackedAt should be populated for acked state")
		}
	})

	t.Run("lexicographic label order still parses correctly", func(t *testing.T) {
		// bd show --json returns labels in lexicographic order.
		state, by, at := ParseDeliveryLabels([]string{
			"delivery-acked-at:2026-02-17T12:00:00Z",
			"delivery-acked-by:gastown/worker",
			"delivery:acked",
			"delivery:pending",
		})
		if state != DeliveryStateAcked {
			t.Fatalf("state = %q, want %q", state, DeliveryStateAcked)
		}
		if by != "gastown/worker" {
			t.Fatalf("ackedBy = %q, want %q", by, "gastown/worker")
		}
		if at == nil {
			t.Fatal("ackedAt should be populated for acked state with lex-ordered labels")
		}
	})
}

func TestDeliveryAckLabelSequenceIdempotent(t *testing.T) {
	t.Run("no existing labels uses new timestamp", func(t *testing.T) {
		at := time.Date(2026, 2, 17, 14, 0, 0, 0, time.UTC)
		got := DeliveryAckLabelSequenceIdempotent("gastown/worker", at, nil)
		want := []string{
			"delivery-acked-by:gastown/worker",
			"delivery-acked-at:2026-02-17T14:00:00Z",
			"delivery:acked",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("existing timestamp is reused on retry", func(t *testing.T) {
		existing := []string{
			"delivery:pending",
			"delivery-acked-by:gastown/worker",
			"delivery-acked-at:2026-02-17T12:00:00Z",
		}
		// Use a different time — should be ignored in favor of existing.
		at := time.Date(2026, 2, 17, 14, 0, 0, 0, time.UTC)
		got := DeliveryAckLabelSequenceIdempotent("gastown/worker", at, existing)
		want := []string{
			"delivery-acked-by:gastown/worker",
			"delivery-acked-at:2026-02-17T12:00:00Z",
			"delivery:acked",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("lexicographic label order still reuses timestamp", func(t *testing.T) {
		// bd show --json returns labels in lexicographic order, so acked-at
		// appears before acked-by. The function must be order-independent.
		existing := []string{
			"delivery-acked-at:2026-02-17T12:00:00Z",
			"delivery-acked-by:gastown/worker",
			"delivery:acked",
			"delivery:pending",
		}
		at := time.Date(2026, 2, 17, 14, 0, 0, 0, time.UTC)
		got := DeliveryAckLabelSequenceIdempotent("gastown/worker", at, existing)
		want := []string{
			"delivery-acked-by:gastown/worker",
			"delivery-acked-at:2026-02-17T12:00:00Z",
			"delivery:acked",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("different recipient gets fresh timestamp", func(t *testing.T) {
		existing := []string{
			"delivery:pending",
			"delivery-acked-by:gastown/workerA",
			"delivery-acked-at:2026-02-17T12:00:00Z",
		}
		// Different recipient — should NOT reuse workerA's timestamp.
		at := time.Date(2026, 2, 17, 14, 0, 0, 0, time.UTC)
		got := DeliveryAckLabelSequenceIdempotent("gastown/workerB", at, existing)
		want := []string{
			"delivery-acked-by:gastown/workerB",
			"delivery-acked-at:2026-02-17T14:00:00Z",
			"delivery:acked",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("mixed labels after crash: B must not reuse A's timestamp", func(t *testing.T) {
		// Scenario: A acked fully, then B started acking but crashed after
		// writing acked-by:B (before acked-at). Labels accumulated:
		existing := []string{
			"delivery:pending",
			"delivery-acked-by:gastown/workerA",
			"delivery-acked-at:2026-02-17T12:00:00Z",
			"delivery:acked",
			"delivery-acked-by:gastown/workerB",
		}
		// B retries — must generate a fresh timestamp, not reuse A's t1.
		at := time.Date(2026, 2, 17, 14, 0, 0, 0, time.UTC)
		got := DeliveryAckLabelSequenceIdempotent("gastown/workerB", at, existing)
		want := []string{
			"delivery-acked-by:gastown/workerB",
			"delivery-acked-at:2026-02-17T14:00:00Z",
			"delivery:acked",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}

func TestDeliveryAckLabelsToWriteSkipsExistingLabels(t *testing.T) {
	at := time.Date(2026, 2, 17, 14, 0, 0, 0, time.UTC)

	t.Run("partial retry only writes missing ack label", func(t *testing.T) {
		existing := []string{
			"delivery:pending",
			"delivery-acked-by:gastown/worker",
			"delivery-acked-at:2026-02-17T12:00:00Z",
		}
		got := deliveryAckLabelsToWrite("gastown/worker", at, existing)
		want := []string{"delivery:acked"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("complete retry writes nothing", func(t *testing.T) {
		existing := []string{
			"delivery:pending",
			"delivery-acked-by:gastown/worker",
			"delivery-acked-at:2026-02-17T12:00:00Z",
			"delivery:acked",
		}
		got := deliveryAckLabelsToWrite("gastown/worker", at, existing)
		if len(got) != 0 {
			t.Fatalf("got %v, want no labels", got)
		}
	})
}
