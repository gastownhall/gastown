package doltserver

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestSummarizeSamples(t *testing.T) {
	tests := []struct {
		name       string
		samples    []time.Duration
		wantN      int
		wantMedian time.Duration
		wantMax    time.Duration
		wantErr    bool
	}{
		{
			// The shape this probe exists to catch: a tight floor with one
			// excursion. The median must stay on the floor and the max must
			// carry the spike, because that difference is the whole signal.
			name:       "bimodal, one spike",
			samples:    []time.Duration{ms(196), ms(190), ms(1939), ms(205), ms(198)},
			wantN:      5,
			wantMedian: ms(198),
			wantMax:    ms(1939),
		},
		{
			name:       "input order does not matter",
			samples:    []time.Duration{ms(1939), ms(205), ms(198), ms(196), ms(190)},
			wantN:      5,
			wantMedian: ms(198),
			wantMax:    ms(1939),
		},
		{
			name:       "all fast",
			samples:    []time.Duration{ms(190), ms(196), ms(198)},
			wantN:      3,
			wantMedian: ms(196),
			wantMax:    ms(198),
		},
		{
			name:       "single sample",
			samples:    []time.Duration{ms(204)},
			wantN:      1,
			wantMedian: ms(204),
			wantMax:    ms(204),
		},
		{
			// Zero samples must be an ERROR, never a zero-valued reading. A
			// zero here would render as a very fast store, which is the exact
			// confusion this file exists to remove.
			name:    "no samples is an error, not a zero",
			samples: nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeSamples(tt.samples)
			if tt.wantErr {
				if got.Err == nil {
					t.Fatalf("summarizeSamples(empty) = %+v, want an error", got)
				}
				if got.Median != 0 || got.Max != 0 || got.Samples != 0 {
					t.Errorf("an errored reading must carry no numbers, got %+v", got)
				}
				return
			}
			if got.Err != nil {
				t.Fatalf("unexpected error: %v", got.Err)
			}
			if got.Samples != tt.wantN {
				t.Errorf("Samples = %d, want %d", got.Samples, tt.wantN)
			}
			if got.Median != tt.wantMedian {
				t.Errorf("Median = %v, want %v", got.Median, tt.wantMedian)
			}
			if got.Max != tt.wantMax {
				t.Errorf("Max = %v, want %v", got.Max, tt.wantMax)
			}
		})
	}
}

// TestSummarizeSamplesDoesNotMutateInput guards the caller's slice, since the
// samples may be reused for other reporting later.
func TestSummarizeSamplesDoesNotMutateInput(t *testing.T) {
	in := []time.Duration{ms(1939), ms(190), ms(205)}
	summarizeSamples(in)
	if in[0] != ms(1939) || in[1] != ms(190) || in[2] != ms(205) {
		t.Errorf("input slice was reordered: %v", in)
	}
}

func TestBeadsLatencyWarning(t *testing.T) {
	threshold := time.Second

	t.Run("quiet when the max is under threshold", func(t *testing.T) {
		got := BeadsLatencyWarning(BeadsReadLatency{Samples: 5, Median: ms(196), Max: ms(662)}, threshold)
		if got != "" {
			t.Errorf("want no warning for a 662ms max, got %q", got)
		}
	})

	t.Run("fires on the MAX even when the median is on the floor", func(t *testing.T) {
		// The load-bearing case. A median-based threshold would never fire
		// here, and this is precisely the reading a suffering town produces.
		got := BeadsLatencyWarning(BeadsReadLatency{Samples: 5, Median: ms(190), Max: ms(1939)}, threshold)
		if got == "" {
			t.Fatal("want a warning when max is 1.939s, got none")
		}
		if !strings.Contains(got, "1.939s") {
			t.Errorf("warning must name the max: %q", got)
		}
		if !strings.Contains(got, "190ms") {
			t.Errorf("warning must also give the median for context: %q", got)
		}
	})

	t.Run("an unmeasurable probe warns rather than reading healthy", func(t *testing.T) {
		got := BeadsLatencyWarning(BeadsReadLatency{Err: errors.New("bd not on PATH")}, threshold)
		if got == "" {
			t.Fatal("an errored probe must warn, not stay silent")
		}
		if !strings.Contains(got, "UNMEASURED") {
			t.Errorf("warning must say the reading is absent, not fine: %q", got)
		}
	})

	t.Run("a zeroed reading with an error never reads as fast", func(t *testing.T) {
		// Defence against the regression this whole file addresses: a zero
		// that renders as a healthy store.
		got := BeadsLatencyWarning(BeadsReadLatency{Err: errors.New("no samples completed")}, threshold)
		if strings.Contains(got, "0s") {
			t.Errorf("an unmeasured probe must not report a duration: %q", got)
		}
	})
}

// TestMeasureBeadsReadLatencyFailsLoudly checks the no-bd path end to end.
// A probe that cannot run must return an error rather than a zero reading.
func TestMeasureBeadsReadLatencyFailsLoudly(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	got := MeasureBeadsReadLatency(t.TempDir())
	if got.Err == nil {
		t.Fatalf("want an error when bd is not on PATH, got %+v", got)
	}
	if got.Samples != 0 || got.Median != 0 || got.Max != 0 {
		t.Errorf("an errored probe must carry no numbers, got %+v", got)
	}
}
