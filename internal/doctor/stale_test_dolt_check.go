package doctor

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/doltserver"
)

const staleTestDoltErrorThreshold = 3

var staleOwnedTestServersFn = doltserver.StaleOwnedTestServers

// StaleTestDoltCheck reports native Dolt test servers whose durable ownership
// metadata proves that their test parent has exited.
type StaleTestDoltCheck struct {
	BaseCheck
}

func NewStaleTestDoltCheck() *StaleTestDoltCheck {
	return &StaleTestDoltCheck{
		BaseCheck: BaseCheck{
			CheckName:        "stale-test-dolt",
			CheckDescription: "Detect test-owned Dolt servers that outlived their test process",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

func (c *StaleTestDoltCheck) Run(_ *CheckContext) *CheckResult {
	stale := staleOwnedTestServersFn()
	if len(stale) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No test-owned Dolt servers outlived their tests",
		}
	}

	details := make([]string, 0, len(stale))
	for _, process := range stale {
		details = append(details, fmt.Sprintf(
			"PID %d owner=%s parent=%d root=%s started=%s",
			process.PID, process.Owner, process.ParentPID, process.TownRoot,
			process.StartedAt.Format("2006-01-02T15:04:05Z07:00"),
		))
	}
	status := StatusWarning
	if len(stale) > staleTestDoltErrorThreshold {
		status = StatusError
	}
	return &CheckResult{
		Name:    c.Name(),
		Status:  status,
		Message: fmt.Sprintf("%d test-owned Dolt server(s) outlived their tests", len(stale)),
		Details: details,
		FixHint: "Reap only the listed ownership-proven test roots; never kill Dolt by name or port",
	}
}
