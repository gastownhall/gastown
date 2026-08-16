// trust.go implements the trust tier escalation engine for the Wasteland
// reputation system. Rigs progress through tiers based on stamp accumulation,
// completion quality, and time-in-tier requirements.
//
// Trust tiers (from the MVR schema):
//
//	0 = Drifter    — unverified, just joined
//	1 = Registered — completed DoltHub identity verification
//	2 = Contributor — earned stamps through validated completions
//	3 = War Chief  — sustained high-quality contributions, can validate others
//
// The escalation model is deliberately conservative: promotions require
// meeting ALL criteria for a tier, and each tier adds harder requirements.
// This prevents gaming through volume alone — quality and consistency matter.
package wasteland

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/util"
)

// TrustTier represents a rig's trust level in the Wasteland.
type TrustTier int

const (
	TierDrifter     TrustTier = 0
	TierRegistered  TrustTier = 1
	TierContributor TrustTier = 2
	TierWarChief    TrustTier = 3
)

// validHandle matches safe wasteland handle names (alphanumeric, underscore, hyphen).
var validHandle = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// String returns the human-readable name for a trust tier.
func (t TrustTier) String() string {
	switch t {
	case TierDrifter:
		return "Drifter"
	case TierRegistered:
		return "Registered"
	case TierContributor:
		return "Contributor"
	case TierWarChief:
		return "War Chief"
	default:
		return fmt.Sprintf("Unknown(%d)", int(t))
	}
}

// TierRequirements defines the criteria a rig must meet to reach a given tier.
// All fields must be satisfied — partial matches don't count. This prevents
// gaming through any single dimension (e.g., spamming low-quality completions).
type TierRequirements struct {
	Tier TrustTier

	// MinCompletions is the minimum number of validated completions (with stamps).
	MinCompletions int

	// MinStamps is the minimum total stamps received across all completions.
	MinStamps int

	// MinAvgQuality is the minimum average quality score from stamps.
	// Quality is extracted from the valence JSON's "quality" field (1-5 scale).
	// Zero means no quality requirement (for lower tiers).
	MinAvgQuality float64

	// MinDistinctValidators ensures stamps come from multiple validators,
	// not just one ally. This is the Spider Protocol's first line of defense
	// against collusion — if all your stamps come from one rig, you can't
	// escalate.
	MinDistinctValidators int

	// MinTimeInCurrentTier is how long the rig must have held their current
	// tier before being eligible for promotion. Prevents rapid trust farming.
	MinTimeInCurrentTier time.Duration
}

// DefaultTierRequirements returns the production escalation rules.
// These are tuned for a wasteland with 50-200 active rigs.
//
// The progression is:
//   - Drifter → Registered: automatic on join (handled by gt wl join)
//   - Registered → Contributor: 3 completions, 3 stamps, 2 validators, 7 days
//   - Contributor → War Chief: 10 completions, 15 stamps, quality ≥3.5, 5 validators, 30 days
func DefaultTierRequirements() []TierRequirements {
	return []TierRequirements{
		{
			Tier:                  TierRegistered,
			MinCompletions:        0,
			MinStamps:             0,
			MinDistinctValidators: 0,
			MinTimeInCurrentTier:  0,
		},
		{
			Tier:                  TierContributor,
			MinCompletions:        3,
			MinStamps:             3,
			MinDistinctValidators: 2,
			MinTimeInCurrentTier:  7 * 24 * time.Hour,
		},
		{
			Tier:                  TierWarChief,
			MinCompletions:        10,
			MinStamps:             15,
			MinAvgQuality:         3.5,
			MinDistinctValidators: 5,
			MinTimeInCurrentTier:  30 * 24 * time.Hour,
		},
	}
}

// RigTrustProfile captures a rig's current reputation metrics, gathered
// from the stamps and completions tables. These are the inputs to the
// escalation decision.
type RigTrustProfile struct {
	Handle             string
	CurrentTier        TrustTier
	TierSince          time.Time // When they reached their current tier
	CompletionCount    int
	StampCount         int
	AvgQuality         float64
	DistinctValidators int
}

// EscalationResult describes the outcome of evaluating a rig for promotion.
type EscalationResult struct {
	Eligible bool
	NextTier TrustTier
	// Reasons explains why promotion was granted or denied. For denied
	// promotions, each unmet criterion is listed so the rig knows what
	// to work toward.
	Reasons []string
}

// EvaluateEscalation checks whether a rig's profile meets the requirements
// for the next trust tier. Returns the result with detailed reasons.
//
// This function is pure — it takes a profile and requirements, with no
// database access. Callers are responsible for gathering the profile data.
func EvaluateEscalation(profile RigTrustProfile, requirements []TierRequirements) EscalationResult {
	nextTier := profile.CurrentTier + 1

	// Find requirements for the next tier
	var req *TierRequirements
	for i := range requirements {
		if requirements[i].Tier == nextTier {
			req = &requirements[i]
			break
		}
	}

	if req == nil {
		return EscalationResult{
			Eligible: false,
			NextTier: nextTier,
			Reasons:  []string{"already at maximum tier"},
		}
	}

	var failures []string
	eligible := true

	if profile.CompletionCount < req.MinCompletions {
		eligible = false
		failures = append(failures, fmt.Sprintf(
			"completions: %d/%d required", profile.CompletionCount, req.MinCompletions))
	}

	if profile.StampCount < req.MinStamps {
		eligible = false
		failures = append(failures, fmt.Sprintf(
			"stamps: %d/%d required", profile.StampCount, req.MinStamps))
	}

	if req.MinAvgQuality > 0 && profile.AvgQuality < req.MinAvgQuality {
		eligible = false
		failures = append(failures, fmt.Sprintf(
			"avg quality: %.1f/%.1f required", profile.AvgQuality, req.MinAvgQuality))
	}

	if profile.DistinctValidators < req.MinDistinctValidators {
		eligible = false
		failures = append(failures, fmt.Sprintf(
			"distinct validators: %d/%d required", profile.DistinctValidators, req.MinDistinctValidators))
	}

	if req.MinTimeInCurrentTier > 0 {
		timeInTier := time.Since(profile.TierSince)
		if timeInTier < req.MinTimeInCurrentTier {
			eligible = false
			remaining := req.MinTimeInCurrentTier - timeInTier
			failures = append(failures, fmt.Sprintf(
				"time in tier: %s remaining", remaining.Round(time.Hour)))
		}
	}

	if eligible {
		return EscalationResult{
			Eligible: true,
			NextTier: nextTier,
			Reasons:  []string{fmt.Sprintf("all criteria met for %s", nextTier)},
		}
	}

	return EscalationResult{
		Eligible: false,
		NextTier: nextTier,
		Reasons:  failures,
	}
}

// LoadRigTrustProfile queries the local dolt fork for a rig's reputation
// metrics. Returns a populated RigTrustProfile for use with EvaluateEscalation.
//
// The query aggregates across completions and stamps tables. If the rig
// has no completions or stamps, the profile will have zero values (which
// is correct — they haven't earned any reputation yet).
func LoadRigTrustProfile(doltPath, forkDir, handle string) (RigTrustProfile, error) {
	profile := RigTrustProfile{Handle: handle}

	// Validate handle to prevent SQL injection — handles are alphanumeric with
	// hyphens and underscores only.
	if !validHandle.MatchString(handle) {
		return profile, fmt.Errorf("invalid handle: %q", handle)
	}

	// Get current tier and registration time
	tierQuery := fmt.Sprintf(
		`SELECT trust_level, registered_at FROM rigs WHERE handle = '%s'`, handle)
	tierRows, err := runTrustQuery(doltPath, forkDir, tierQuery)
	if err != nil {
		return profile, fmt.Errorf("querying rig tier: %w", err)
	}
	if len(tierRows) > 0 && len(tierRows[0]) >= 2 {
		tier, _ := strconv.Atoi(tierRows[0][0])
		profile.CurrentTier = TrustTier(tier)
		// Parse registration time as tier-since (approximation — ideally we'd
		// track when each tier was reached, but the schema doesn't have that yet)
		if t, err := time.Parse("2006-01-02 15:04:05", tierRows[0][1]); err == nil {
			profile.TierSince = t
		}
	}

	// Count validated completions (those with stamps)
	compQuery := fmt.Sprintf(`
		SELECT COUNT(DISTINCT c.id)
		FROM completions c
		INNER JOIN stamps s ON s.context_id = c.id
		WHERE c.completed_by = '%s'`, handle)
	compRows, err := runTrustQuery(doltPath, forkDir, compQuery)
	if err == nil && len(compRows) > 0 && len(compRows[0]) > 0 {
		profile.CompletionCount, _ = strconv.Atoi(compRows[0][0])
	}

	// Count total stamps received and compute average quality
	stampQuery := fmt.Sprintf(`
		SELECT
			COUNT(*) AS stamp_count,
			COALESCE(AVG(JSON_EXTRACT(valence, '$.quality')), 0) AS avg_quality,
			COUNT(DISTINCT author) AS distinct_validators
		FROM stamps
		WHERE subject = '%s'`, handle)
	stampRows, err := runTrustQuery(doltPath, forkDir, stampQuery)
	if err == nil && len(stampRows) > 0 && len(stampRows[0]) >= 3 {
		profile.StampCount, _ = strconv.Atoi(stampRows[0][0])
		profile.AvgQuality, _ = strconv.ParseFloat(stampRows[0][1], 64)
		profile.DistinctValidators, _ = strconv.Atoi(stampRows[0][2])
	}

	return profile, nil
}

// runTrustQuery executes a SQL query against a local dolt database. Shared
// helper between trust and spider modules. Returns parsed CSV rows
// (excluding header).
func runTrustQuery(doltPath, forkDir, query string) ([][]string, error) {
	cmd := exec.Command(doltPath, "sql", "-r", "csv", "-q", query)
	cmd.Dir = forkDir
	util.SetDetachedProcessGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("dolt sql: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return nil, nil
	}

	var rows [][]string
	for _, line := range lines[1:] {
		fields := strings.Split(line, ",")
		rows = append(rows, fields)
	}
	return rows, nil
}
