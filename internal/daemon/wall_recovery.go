package daemon

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	defaultWallRecoveryInterval = 5 * time.Minute
	// defaultWallRecoveryProbeGap is the minimum time between recovery probes
	// for one session. The probe is a nudge, which is free while an agent is
	// walled (a walled session cannot act on it), so this only limits noise.
	defaultWallRecoveryProbeGap = 10 * time.Minute
	// wallRecoveryPaneLines is how much of the pane tail to inspect. It must be
	// small: once an agent resumes, the stale limit message has to scroll out of
	// this window so the session stops being reported as walled.
	wallRecoveryPaneLines = 30
)

// WallRecoveryConfig configures the wall-recovery patrol.
type WallRecoveryConfig struct {
	// Enabled controls whether the patrol runs. Absent config means ENABLED:
	// this is a recovery mechanism, and the failure it repairs is silent.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to sweep, as a string (e.g., "5m").
	IntervalStr string `json:"interval,omitempty"`

	// ProbeGapStr is the minimum gap between probes of one session (e.g. "10m").
	ProbeGapStr string `json:"probe_gap,omitempty"`
}

func wallRecoveryInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.WallRecovery != nil {
		if s := config.Patrols.WallRecovery.IntervalStr; s != "" {
			if d, err := time.ParseDuration(s); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultWallRecoveryInterval
}

func wallRecoveryProbeGap(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.WallRecovery != nil {
		if s := config.Patrols.WallRecovery.ProbeGapStr; s != "" {
			if d, err := time.ParseDuration(s); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultWallRecoveryProbeGap
}

// wallRecoveryState remembers when each session was last probed, so a long wall
// does not produce a nudge every sweep.
type wallRecoveryState struct {
	mu         sync.Mutex
	lastProbe  map[string]time.Time
	firstSeen  map[string]time.Time
	probeCount map[string]int
}

func newWallRecoveryState() *wallRecoveryState {
	return &wallRecoveryState{
		lastProbe:  map[string]time.Time{},
		firstSeen:  map[string]time.Time{},
		probeCount: map[string]int{},
	}
}

// shouldProbe reports whether session is due for a recovery probe, and records
// the probe if so. Sessions no longer walled are forgotten by clear().
func (s *wallRecoveryState) shouldProbe(session string, now time.Time, gap time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.firstSeen[session]; !ok {
		s.firstSeen[session] = now
	}
	if last, ok := s.lastProbe[session]; ok && now.Sub(last) < gap {
		return false
	}
	s.lastProbe[session] = now
	s.probeCount[session]++
	return true
}

// clear forgets a session that is no longer showing a wall, so that if it is
// walled again later it is probed promptly rather than waiting out an old gap.
func (s *wallRecoveryState) clear(session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.lastProbe, session)
	delete(s.firstSeen, session)
	delete(s.probeCount, session)
}

func (s *wallRecoveryState) walledSince(session string, now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.firstSeen[session]; ok {
		return now.Sub(t)
	}
	return 0
}

// runWallRecovery nudges every agent session that is showing a Claude
// usage-limit wall, so that work resumes as soon as capacity returns.
//
// WHY THIS EXISTS. On 2026-09-10 the whole alphaprime rig hit a weekly usage
// limit at about 04:29. Capacity came back later the same day, but four
// polecats -- brahmin, deathclaw, enclave and vault -- sat idle for roughly
// fourteen hours holding finished work one command short of the merge queue. A
// single nudge each recovered all four in under 45 seconds, and four merges
// landed within twenty minutes.
//
// THE DAEMON WAS NOT BLIND. It detected the wall 16 minutes in and logged
// "Deacon paused - Claude usage-limit detected, skipping kill (quota_dog will
// rotate accounts)" 299 times between 04:45 and 18:41, with staleness climbing
// monotonically to 14h17m. Declining to kill a walled agent is CORRECT -- a kill
// destroys its context and the replacement hits the same limit. The defect is
// that declining to kill was the whole response: the handoff target, quota_dog,
// cannot rotate without an account roster (hq-rqcc), so the path ended nowhere
// and nothing ever re-checked. A signal that fires 299 times and produces no
// action is worse than no signal, because it reads as coverage.
//
// TWO PROPERTIES THIS SWEEP MUST HAVE, both learned from that outage:
//
//   - IT ENUMERATES SESSIONS. The existing detection keyed on the Deacon's
//     heartbeat alone, so the four walled polecats were never in scope for
//     recovery even while the wall was being logged every three minutes.
//
//   - THE NUDGE IS THE PROBE. A recovered-but-idle agent and a still-walled one
//     look IDENTICAL from outside: both sit at an empty composer with the same
//     stale limit message in scrollback. Nothing in the pane distinguishes them,
//     so the sweep cannot wait for a reliable "capacity is back" signal before
//     acting. It nudges, which is free while the agent is genuinely walled
//     (a walled session cannot act on it) and recovers the agent the moment it
//     is not.
//
// It deliberately does not depend on quota_dog or on account rotation.
func (d *Daemon) runWallRecovery() {
	if d.tmux == nil {
		return
	}
	if d.wallRecovery == nil {
		d.wallRecovery = newWallRecoveryState()
	}

	sessions, err := d.tmux.ListSessions()
	if err != nil {
		d.logger.Printf("Wall-recovery: cannot list sessions: %v", err)
		return
	}

	gap := wallRecoveryProbeGap(d.patrolConfig)
	now := time.Now()
	walled, probed := 0, 0

	for _, session := range sessions {
		pane, err := d.tmux.CapturePane(session, wallRecoveryPaneLines)
		if err != nil {
			continue
		}
		if !IsClaudeUsageLimit(pane) {
			// Either never walled, or resumed and the message has scrolled away.
			d.wallRecovery.clear(session)
			continue
		}
		walled++
		if !d.wallRecovery.shouldProbe(session, now, gap) {
			continue
		}
		probed++
		since := d.wallRecovery.walledSince(session, now).Round(time.Minute)
		msg := fmt.Sprintf(
			"WALL_RECOVERY: a Claude usage limit was detected in this session and it has been showing that wall for %s. "+
				"Capacity may have returned - this nudge is the probe, because a recovered-but-idle session and a still-walled one look identical from outside. "+
				"If you can act on this, re-check your hook with 'gt hook' and resume; if you have finished work that never reached the merge queue, submit it now. "+
				"If you are still walled this message costs nothing and will be repeated later.", since)
		if err := d.tmux.NudgeSession(session, msg); err != nil {
			d.logger.Printf("Wall-recovery: nudge to %s failed: %v", session, err)
			continue
		}
		d.logger.Printf("Wall-recovery: probed %s (showing a usage-limit wall for %s)", session, since)
	}

	if walled > 0 {
		d.logger.Printf("Wall-recovery: %d session(s) showing a usage-limit wall, %d probed this sweep", walled, probed)
	}
}

// describeWalledSessions is a helper for status output: the names of sessions
// currently believed walled. Reported so a wall is visible as a wall rather than
// rendering as ordinary idleness, which is what let fourteen hours pass.
func (s *wallRecoveryState) describeWalledSessions() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.firstSeen) == 0 {
		return ""
	}
	names := make([]string, 0, len(s.firstSeen))
	for name := range s.firstSeen {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}
