package deps

import "testing"

func TestParseDoltVersion(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"dolt version 1.82.4", "1.82.4"},
		{"dolt version 1.82.4\n", "1.82.4"},
		{"dolt version 1.84.0", "1.84.0"},
		{"dolt version 2.0.3", "2.0.3"},
		{"dolt version 2.0.7", "2.0.7"},
		{"dolt version 1.84.0\nWarning: you are on an old version of Dolt. The newest version is 2.0.3.", "1.84.0"},
		{"dolt version 1.0.0", "1.0.0"},
		{"dolt version 10.20.30", "10.20.30"},
		{"some other output", ""},
		{"", ""},
	}

	for _, tt := range tests {
		result := parseDoltVersion(tt.input)
		if result != tt.expected {
			t.Errorf("parseDoltVersion(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestCheckDolt(t *testing.T) {
	status, version, _ := CheckDolt()

	if status == DoltNotFound {
		t.Skip("dolt not installed, skipping integration test")
	}

	if status == DoltOK && version == "" {
		t.Error("CheckDolt returned DoltOK but empty version")
	}

	t.Logf("CheckDolt: status=%d, version=%s", status, version)
}

func TestMinDoltVersionBoundary(t *testing.T) {
	if CompareVersions("2.0.6", MinDoltVersion) >= 0 {
		t.Fatalf("2.0.6 should be below MinDoltVersion %s", MinDoltVersion)
	}
	if CompareVersions("2.0.7", MinDoltVersion) != 0 {
		t.Fatalf("2.0.7 should equal MinDoltVersion %s", MinDoltVersion)
	}
	if CompareVersions("2.0.8", MinDoltVersion) <= 0 {
		t.Fatalf("2.0.8 should be above MinDoltVersion %s", MinDoltVersion)
	}
}
