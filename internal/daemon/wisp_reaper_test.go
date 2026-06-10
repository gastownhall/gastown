package daemon

import (
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/dog"
)

func TestAnyDogWorkingOnReaper(t *testing.T) {
	tests := []struct {
		name string
		dogs []*dog.Dog
		want bool
	}{
		{
			name: "no dogs",
			dogs: nil,
			want: false,
		},
		{
			name: "reaper dog in flight",
			dogs: []*dog.Dog{
				{Name: "bravo", State: dog.StateWorking, Work: constants.MolDogReaper},
			},
			want: true,
		},
		{
			name: "dog working other formula",
			dogs: []*dog.Dog{
				{Name: "bravo", State: dog.StateWorking, Work: "plugin:stuck-agent-dog"},
			},
			want: false,
		},
		{
			name: "idle dog that previously reaped does not count",
			dogs: []*dog.Dog{
				{Name: "bravo", State: dog.StateIdle, Work: constants.MolDogReaper},
			},
			want: false,
		},
		{
			name: "mixed pool with one reaper in flight",
			dogs: []*dog.Dog{
				{Name: "bravo", State: dog.StateIdle},
				{Name: "charlie", State: dog.StateWorking, Work: constants.MolDogReaper},
			},
			want: true,
		},
		{
			name: "nil entry is skipped",
			dogs: []*dog.Dog{nil, {Name: "bravo", State: dog.StateIdle}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := anyDogWorkingOn(tt.dogs, constants.MolDogReaper); got != tt.want {
				t.Errorf("anyDogWorkingOn = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWispReaperInterval(t *testing.T) {
	// Default (now 1h after Dog-driven refactor)
	if got := wispReaperInterval(nil); got != defaultWispReaperInterval {
		t.Errorf("expected default %v, got %v", defaultWispReaperInterval, got)
	}

	// Custom
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:     true,
				IntervalStr: "2h",
			},
		},
	}
	if got := wispReaperInterval(config); got != 2*time.Hour {
		t.Errorf("expected 2h, got %v", got)
	}

	// Invalid falls back to default
	config.Patrols.WispReaper.IntervalStr = "nope"
	if got := wispReaperInterval(config); got != defaultWispReaperInterval {
		t.Errorf("expected default for invalid, got %v", got)
	}
}

func TestWispReaperMaxAge(t *testing.T) {
	if got := wispReaperMaxAge(nil); got != defaultWispMaxAge {
		t.Errorf("expected default %v, got %v", defaultWispMaxAge, got)
	}

	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:   true,
				MaxAgeStr: "48h",
			},
		},
	}
	if got := wispReaperMaxAge(config); got != 48*time.Hour {
		t.Errorf("expected 48h, got %v", got)
	}
}

func TestWispDeleteAge(t *testing.T) {
	if got := wispDeleteAge(nil); got != defaultWispDeleteAge {
		t.Errorf("expected default %v, got %v", defaultWispDeleteAge, got)
	}

	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:      true,
				DeleteAgeStr: "336h",
			},
		},
	}
	if got := wispDeleteAge(config); got != 14*24*time.Hour {
		t.Errorf("expected 336h, got %v", got)
	}
}

func TestDefaultReaperIntervalIsOneHour(t *testing.T) {
	// Verify the default changed from 30m to 1h per issue gt-caf7.
	if defaultWispReaperInterval != 1*time.Hour {
		t.Errorf("expected default interval 1h, got %v", defaultWispReaperInterval)
	}
}
