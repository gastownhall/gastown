package doltserver

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"time"
)

// beadsProbeSamples is how many reads a single latency measurement takes.
//
// One sample is not enough, and that is the whole point of this file rather
// than a detail of it. Measured on this town 2026-09-03, the slow tail is
// roughly 28% of invocations, so a single-shot probe reads fast about seven
// times in ten BY CONSTRUCTION -- which is exactly how the pre-existing
// server probe managed to report "0s" through a real outage. Five samples
// miss the tail entirely only about 1 time in 5.
const beadsProbeSamples = 5

// beadsProbeTimeout bounds a single sample. The worst honestly-measured read
// on this town was 6.7 seconds, so this must sit well above that or the probe
// would truncate the very tail it exists to report.
const beadsProbeTimeout = 30 * time.Second

// BeadsReadLatency is a DISTRIBUTION of read latencies against the store bd
// actually uses, not a single reading.
//
// It reports Median and Max because they answer different questions and the
// Max is the one that matters. Latency here is bimodal, not noisy: a tight
// floor around 200ms with independent excursions to several seconds. The
// median tracks the floor and is nearly constant even while the town is
// suffering; the max tracks the contention that actually costs agents time.
type BeadsReadLatency struct {
	// Samples is how many reads completed. Fewer than beadsProbeSamples means
	// some failed; zero means the probe could not run at all.
	Samples int `json:"samples"`

	// Median is the typical read: the floor.
	Median time.Duration `json:"median_ns"`

	// Max is the worst read observed: the symptom.
	Max time.Duration `json:"max_ns"`

	// Err is non-nil when the probe could not run. A probe that cannot run
	// must say so rather than report a zero, because a zero here is
	// indistinguishable from a healthy store and that confusion is the
	// defect this whole file addresses.
	Err error `json:"-"`
}

// MeasureBeadsReadLatency times reads against the store bd ACTUALLY USES.
//
// WHY THIS EXISTS ALONGSIDE MeasureQueryLatency. MeasureQueryLatency times
// SELECT active_branch() against the Dolt server on :3307 and is correct for
// what it measures. But bd does not use that server. bd runs Dolt EMBEDDED out
// of .beads/embeddeddolt (see the non-server path in beads' own
// store_factory.go, which calls embeddeddolt.Open). So no query against :3307,
// however expensive, can track how slow bd is -- the two are different
// processes against different files. Making the server query heavier would not
// have fixed it; only measuring a different thing does.
//
// HOW THE PROBE COMMAND WAS CHOSEN, because the obvious candidates are wrong
// and the next person will otherwise repeat this. Measured against `bd show`
// on 2026-09-03:
//
//	bd stats            FLAT at 117-187ms while bd show swung to 2736ms.
//	                    Touches the store, never sees the contention. Shipping
//	                    it would have been a second probe that reads green
//	                    during the outage it exists to detect.
//	bd list --limit 1   DOES spike (186ms to 1632ms). Chosen.
//	bd ready --limit 1  Also spikes; equivalent, arbitrarily not chosen.
//
// A NOTE THAT LOOKS LIKE A CONTRADICTION AND IS NOT. `bd list` run from the
// town root returns a PARTIAL row set -- it can omit a rig bead that `bd show`
// resolves (gt-irl). That is irrelevant here because this code TIMES the
// command and never reads a single row of its output. It is used as a
// stopwatch, not as a query.
//
// The returned durations are RAW WALL TIME and include roughly 27ms of bd
// process startup (measured: `bd --version` is 27ms). That is deliberately not
// subtracted: it is a real cost every agent pays, it is small against a 200ms
// floor, and it is irrelevant against a multi-second tail. Subtracting a
// constant nobody can re-verify later would make the number less honest, not
// more precise.
func MeasureBeadsReadLatency(townRoot string) BeadsReadLatency {
	bd, err := exec.LookPath("bd")
	if err != nil {
		return BeadsReadLatency{Err: fmt.Errorf("bd not on PATH: %w", err)}
	}

	samples := make([]time.Duration, 0, beadsProbeSamples)
	var lastErr error
	for i := 0; i < beadsProbeSamples; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), beadsProbeTimeout)
		cmd := exec.CommandContext(ctx, bd, "list", "--limit", "1")
		cmd.Dir = townRoot
		start := time.Now()
		runErr := cmd.Run()
		elapsed := time.Since(start)
		cancel()

		if runErr != nil {
			lastErr = runErr
			continue
		}
		samples = append(samples, elapsed)
	}

	if len(samples) == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("no samples completed")
		}
		return BeadsReadLatency{Err: fmt.Errorf("probing bd read latency: %w", lastErr)}
	}

	return summarizeSamples(samples)
}

// summarizeSamples reduces raw sample durations to the reported distribution.
// Split out from MeasureBeadsReadLatency so the reduction can be tested
// without spawning bd.
func summarizeSamples(samples []time.Duration) BeadsReadLatency {
	if len(samples) == 0 {
		return BeadsReadLatency{Err: fmt.Errorf("no samples completed")}
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	return BeadsReadLatency{
		Samples: len(sorted),
		Median:  sorted[len(sorted)/2],
		Max:     sorted[len(sorted)-1],
	}
}

// BeadsLatencyWarning returns the warning an operator should see for this
// reading, or "" when there is nothing to say.
//
// It fires on the MAX, never the median. Latency here is bimodal: a tight
// floor around 200ms with independent excursions to several seconds. The
// median sits on the floor and barely moves while the town is suffering, so a
// median threshold is blind to the tail by construction.
//
// A probe that could not run returns a warning rather than silence, because a
// zero reading is indistinguishable from a fast store and that confusion is
// the entire defect this file exists to correct.
func BeadsLatencyWarning(br BeadsReadLatency, threshold time.Duration) string {
	switch {
	case br.Err != nil:
		return fmt.Sprintf("bd read latency UNMEASURED (%v) — this is not a healthy reading, it is no reading", br.Err)
	case br.Max > threshold:
		return fmt.Sprintf("bd read latency spiked to %v (median %v over %d samples) — agents are waiting on store contention",
			br.Max.Round(time.Millisecond), br.Median.Round(time.Millisecond), br.Samples)
	default:
		return ""
	}
}
