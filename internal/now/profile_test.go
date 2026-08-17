package now

import "testing"

func TestParseProfileTwoTokenKnownEffortIsEffort(t *testing.T) {
	profile, err := ParseProfile("cursor:xhigh", DefaultMayorEffort)
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	if profile.Runtime != "cursor" || profile.Model != "" || profile.Effort != "xhigh" {
		t.Fatalf("got %+v, want runtime=cursor effort=xhigh", profile)
	}
}

func TestParseProfileTwoTokenUnknownIsModel(t *testing.T) {
	profile, err := ParseProfile("cursor:grok-4.6", DefaultMayorEffort)
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	if profile.Model != "grok-4.6" || profile.Effort != DefaultMayorEffort {
		t.Fatalf("got %+v, want model grok-4.6 and default effort", profile)
	}
}

func TestParseProfileSlashInModel(t *testing.T) {
	profile, err := ParseProfile("cursor:org/model:high", DefaultMayorEffort)
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	if profile.Runtime != "cursor" || profile.Model != "org/model" || profile.Effort != "high" {
		t.Fatalf("got %+v, want cursor org/model high", profile)
	}
}
