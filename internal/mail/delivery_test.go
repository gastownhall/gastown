package mail

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

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

func TestDeliveryAckLabelSequence(t *testing.T) {
	t.Run("no existing labels uses new timestamp", func(t *testing.T) {
		at := time.Date(2026, 2, 17, 14, 0, 0, 0, time.UTC)
		got := DeliveryAckLabelSequence("gastown/worker", at, nil)
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
		got := DeliveryAckLabelSequence("gastown/worker", at, existing)
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
		got := DeliveryAckLabelSequence("gastown/worker", at, existing)
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
		got := DeliveryAckLabelSequence("gastown/workerB", at, existing)
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
		got := DeliveryAckLabelSequence("gastown/workerB", at, existing)
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

func TestDeliveryPendingRemovalNeeded(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"no delivery labels", []string{"gt:message"}, false},
		{"pending only", []string{DeliveryLabelPending}, false},
		{"partial ack keeps pending", []string{DeliveryLabelPending, "delivery-acked-by:gastown/worker", "delivery-acked-at:2026-02-17T12:00:00Z"}, false},
		{"pending and acked converges", []string{DeliveryLabelPending, DeliveryLabelAcked}, true},
		{"acked only", []string{DeliveryLabelAcked}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deliveryPendingRemovalNeeded(tt.labels); got != tt.want {
				t.Fatalf("deliveryPendingRemovalNeeded(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}

func TestAcknowledgeDeliveryBeadConvergesPendingLabel(t *testing.T) {
	tmp := t.TempDir()
	labelsPath := filepath.Join(tmp, "labels.txt")
	logPath := filepath.Join(tmp, "bd.log")
	initialLabels := strings.Join([]string{
		DeliveryLabelPending,
		"delivery-acked-by:gastown/worker",
		"delivery-acked-at:2026-02-17T12:00:00Z",
		DeliveryLabelAcked,
	}, "\n") + "\n"
	if err := os.WriteFile(labelsPath, []byte(initialLabels), 0644); err != nil {
		t.Fatalf("write labels: %v", err)
	}

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	bdStub := filepath.Join(binDir, "bd")
	script := `#!/usr/bin/env bash
set -euo pipefail
labels_file="$BD_STUB_LABELS"
log_file="$BD_STUB_LOG"
printf '%s\n' "$*" >> "$log_file"

if [[ "${1:-}" == "show" ]]; then
  id="${2:-}"
  printf '[{"id":"%s","labels":[' "$id"
  first=1
  while IFS= read -r label; do
    [[ -z "$label" ]] && continue
    if [[ $first -eq 0 ]]; then printf ','; fi
    first=0
    printf '"%s"' "$label"
  done < "$labels_file"
  printf ']}]'
  exit 0
fi

if [[ "${1:-}" == "label" && "${2:-}" == "add" ]]; then
  label="${4:-}"
  found=0
  while IFS= read -r existing; do
    if [[ "$existing" == "$label" ]]; then
      found=1
      break
    fi
  done < "$labels_file"
  if [[ $found -eq 0 ]]; then
    printf '%s\n' "$label" >> "$labels_file"
  fi
  exit 0
fi

if [[ "${1:-}" == "label" && "${2:-}" == "remove" ]]; then
  label="${4:-}"
  tmp_file="$labels_file.tmp"
  removed=0
  : > "$tmp_file"
  while IFS= read -r existing; do
    if [[ "$existing" == "$label" ]]; then
      removed=1
      continue
    fi
    printf '%s\n' "$existing" >> "$tmp_file"
  done < "$labels_file"
  mv "$tmp_file" "$labels_file"
  if [[ $removed -eq 0 ]]; then
    echo "does not have label" >&2
    exit 1
  fi
  exit 0
fi

echo "unsupported bd args: $*" >&2
exit 1
`
	if err := os.WriteFile(bdStub, []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_STUB_LABELS", labelsPath)
	t.Setenv("BD_STUB_LOG", logPath)

	if err := AcknowledgeDeliveryBead(tmp, "", "msg-1", "gastown/worker"); err != nil {
		t.Fatalf("AcknowledgeDeliveryBead: %v", err)
	}

	data, err := os.ReadFile(labelsPath)
	if err != nil {
		t.Fatalf("read labels: %v", err)
	}
	labels := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, label := range labels {
		if label == DeliveryLabelPending {
			t.Fatalf("%s should have been removed after ack convergence; labels=%v", DeliveryLabelPending, labels)
		}
	}
	if !containsDeliveryTestLabel(labels, DeliveryLabelAcked) {
		t.Fatalf("%s should remain; labels=%v", DeliveryLabelAcked, labels)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(logData), "label remove msg-1 "+DeliveryLabelPending) {
		t.Fatalf("expected pending label removal command, log:\n%s", logData)
	}
}

func containsDeliveryTestLabel(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
