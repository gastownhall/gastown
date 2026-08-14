package cmd

import "testing"

// TestSlingTrimsTrailingSlash verifies that trailing slashes in target arguments
// are trimmed to handle tab-completion artifacts like "slingshot/" -> "slingshot".
// This ensures that "gt sling sl-123 slingshot/" behaves the same as "gt sling sl-123 slingshot".
func TestSlingTrimsTrailingSlash(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"rig with trailing slash", "slingshot/", "slingshot"},
		{"rig without slash", "slingshot", "slingshot"},
		{"path with trailing slash", "gastown/crew/", "gastown/crew"},
		{"path without slash", "gastown/crew", "gastown/crew"},
		{"multiple trailing slashes", "slingshot///", "slingshot"},
		{"just slashes", "///", ""},
		{"empty string", "", ""},
		{"mayor role", "mayor", "mayor"},
		{"deacon role", "deacon", "deacon"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeSlingTarget(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeSlingTarget(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// TestIsRigNameWithTrailingSlash verifies that IsRigName correctly rejects
// targets with trailing slashes (since they'll be trimmed before reaching IsRigName).
func TestIsRigNameWithTrailingSlash(t *testing.T) {
	// Note: In actual usage, trailing slashes are trimmed in runSling before
	// reaching IsRigName. This test verifies IsRigName's current behavior.

	// Create a minimal test - we can't test against real rigs without setup,
	// but we can verify the slash-checking logic.

	tests := []struct {
		name     string
		target   string
		wantName string
		wantOk   bool
	}{
		{
			name:     "deacon/dogs has slash",
			target:   "deacon/dogs",
			wantName: "",
			wantOk:   false,
		},
		{
			name:     "trailing slash makes it look like path",
			target:   "slingshot/",
			wantName: "",
			wantOk:   false,
		},
		{
			name:     "mayor role without slash",
			target:   "mayor",
			wantName: "",
			wantOk:   false, // mayor is a known role, not a rig
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotOk := IsRigName(tt.target)
			if gotName != tt.wantName || gotOk != tt.wantOk {
				t.Errorf("IsRigName(%q) = (%q, %v), want (%q, %v)",
					tt.target, gotName, gotOk, tt.wantName, tt.wantOk)
			}
		})
	}
}
