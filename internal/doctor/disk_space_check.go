package doctor

import (
	"fmt"

	"github.com/jonbaldie/gastown/internal/util"
)

// DiskSpaceCheck verifies that the filesystem has sufficient free space.
// Low disk space is the root cause of cascading failures: Dolt data loss,
// polecat session death, lost commits, and broken mail delivery.
type DiskSpaceCheck struct {
	BaseCheck
}

// NewDiskSpaceCheck creates a new disk space check.
func NewDiskSpaceCheck() *DiskSpaceCheck {
	return &DiskSpaceCheck{
		BaseCheck: BaseCheck{
			CheckName:        "disk-space",
			CheckDescription: "Check filesystem has sufficient free space",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// Run checks disk space at the town root.
func (c *DiskSpaceCheck) Run(ctx *CheckContext) *CheckResult {
	info, err := util.GetDiskSpace(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not check disk space: %v", err),
		}
	}

	level, msg, _ := util.CheckDiskSpace(ctx.TownRoot)

	switch level {
	case util.DiskSpaceCritical:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: msg,
			Details: []string{
				fmt.Sprintf("Available: %s of %s (%.1f%% used)",
					info.AvailableHuman(),
					util.FormatBytesHuman(info.TotalBytes),
					info.UsedPercent),
				"Disk space exhaustion causes: Dolt data loss, polecat session death, lost commits",
				"Free up space immediately, then run 'gt doctor --fix' to recover",
			},
			FixHint: "Free disk space, then run 'gt doctor --fix'",
		}

	case util.DiskSpaceWarning:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: msg,
			Details: []string{
				fmt.Sprintf("Available: %s of %s (%.1f%% used)",
					info.AvailableHuman(),
					util.FormatBytesHuman(info.TotalBytes),
					info.UsedPercent),
				"Consider freeing space or reducing active polecats to prevent failures",
			},
		}

	default:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("%s free (%.1f%% used)", info.AvailableHuman(), info.UsedPercent),
		}
	}
}
