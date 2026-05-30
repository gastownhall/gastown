package beads

import (
	"strings"
	"testing"
)

// --- parseIntField (not covered in beads_test.go) ---

func TestParseIntField(t *testing.T) {
	tests := []struct {
		input   string
		want    int
		wantErr bool
	}{
		{"42", 42, false},
		{"0", 0, false},
		{"-1", -1, false},
		{"abc", 0, true},
		{"", 0, true},
		{"3.14", 3, false}, // Sscanf reads the integer part
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseIntField(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseIntField(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseIntField(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

// --- AttachmentFields Mode round-trip ---

func TestAttachmentFieldsModeRoundTrip(t *testing.T) {
	original := &AttachmentFields{
		AttachedMolecule: "gt-wisp-123",
		AttachedAt:       "2026-02-18T12:00:00Z",
		Mode:             "ralph",
	}

	formatted := FormatAttachmentFields(original)
	if !strings.Contains(formatted, "mode: ralph") {
		t.Errorf("FormatAttachmentFields missing mode field, got:\n%s", formatted)
	}

	issue := &Issue{Description: formatted}
	parsed := ParseAttachmentFields(issue)
	if parsed == nil {
		t.Fatal("round-trip parse returned nil")
	}
	if parsed.Mode != "ralph" {
		t.Errorf("Mode: got %q, want %q", parsed.Mode, "ralph")
	}
	if parsed.AttachedMolecule != "gt-wisp-123" {
		t.Errorf("AttachedMolecule: got %q, want %q", parsed.AttachedMolecule, "gt-wisp-123")
	}
}

func TestSetAttachmentFieldsPreservesMode(t *testing.T) {
	issue := &Issue{
		Description: "mode: ralph\nattached_molecule: gt-wisp-old\nSome other content",
	}
	fields := &AttachmentFields{
		AttachedMolecule: "gt-wisp-new",
		Mode:             "ralph",
	}
	newDesc := SetAttachmentFields(issue, fields)
	if !strings.Contains(newDesc, "mode: ralph") {
		t.Errorf("SetAttachmentFields lost mode field, got:\n%s", newDesc)
	}
	if !strings.Contains(newDesc, "attached_molecule: gt-wisp-new") {
		t.Errorf("SetAttachmentFields lost attached_molecule, got:\n%s", newDesc)
	}
	if !strings.Contains(newDesc, "Some other content") {
		t.Errorf("SetAttachmentFields lost non-attachment content, got:\n%s", newDesc)
	}
}

// --- AgentFields Mode round-trip ---

func TestAgentFieldsModeRoundTrip(t *testing.T) {
	original := &AgentFields{
		RoleType:   "polecat",
		Rig:        "gastown",
		AgentState: "working",
		HookBead:   "gt-abc",
		Mode:       "ralph",
	}

	formatted := FormatAgentDescription("Polecat Test", original)
	if !strings.Contains(formatted, "mode: ralph") {
		t.Errorf("FormatAgentDescription missing mode field, got:\n%s", formatted)
	}

	parsed := ParseAgentFields(formatted)
	if parsed.Mode != "ralph" {
		t.Errorf("Mode: got %q, want %q", parsed.Mode, "ralph")
	}
	if parsed.RoleType != "polecat" {
		t.Errorf("RoleType: got %q, want %q", parsed.RoleType, "polecat")
	}
}

func TestAgentFieldsModeOmittedWhenEmpty(t *testing.T) {
	fields := &AgentFields{
		RoleType:   "polecat",
		Rig:        "gastown",
		AgentState: "working",
		// Mode intentionally empty
	}

	formatted := FormatAgentDescription("Polecat Test", fields)
	if strings.Contains(formatted, "mode:") {
		t.Errorf("FormatAgentDescription should not include mode when empty, got:\n%s", formatted)
	}
}

// --- Convoy fields in AttachmentFields (gt-7b6wf fix) ---

func TestParseAttachmentFieldsConvoy(t *testing.T) {
	tests := []struct {
		name              string
		desc              string
		wantConvoyID      string
		wantMergeStrategy string
	}{
		{
			name:              "convoy_id and merge_strategy",
			desc:              "attached_molecule: gt-wisp-abc\nconvoy_id: hq-cv-xyz\nmerge_strategy: direct",
			wantConvoyID:      "hq-cv-xyz",
			wantMergeStrategy: "direct",
		},
		{
			name:              "hyphenated keys",
			desc:              "convoy-id: hq-cv-123\nmerge-strategy: local",
			wantConvoyID:      "hq-cv-123",
			wantMergeStrategy: "local",
		},
		{
			name:              "convoy key alias",
			desc:              "convoy: hq-cv-456",
			wantConvoyID:      "hq-cv-456",
			wantMergeStrategy: "",
		},
		{
			name:              "only merge_strategy (no convoy_id)",
			desc:              "merge_strategy: mr",
			wantConvoyID:      "",
			wantMergeStrategy: "mr",
		},
		{
			name:              "no convoy fields",
			desc:              "attached_molecule: gt-wisp-abc\ndispatched_by: mayor/",
			wantConvoyID:      "",
			wantMergeStrategy: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{Description: tt.desc}
			fields := ParseAttachmentFields(issue)
			if fields == nil {
				if tt.wantConvoyID != "" || tt.wantMergeStrategy != "" {
					t.Fatal("ParseAttachmentFields() = nil, want non-nil")
				}
				return
			}
			if fields.ConvoyID != tt.wantConvoyID {
				t.Errorf("ConvoyID = %q, want %q", fields.ConvoyID, tt.wantConvoyID)
			}
			if fields.MergeStrategy != tt.wantMergeStrategy {
				t.Errorf("MergeStrategy = %q, want %q", fields.MergeStrategy, tt.wantMergeStrategy)
			}
		})
	}
}

func TestFormatAttachmentFieldsConvoy(t *testing.T) {
	fields := &AttachmentFields{
		AttachedMolecule: "gt-wisp-abc",
		ConvoyID:         "hq-cv-xyz",
		MergeStrategy:    "direct",
		ConvoyOwned:      true,
	}
	got := FormatAttachmentFields(fields)
	if !strings.Contains(got, "convoy_id: hq-cv-xyz") {
		t.Errorf("FormatAttachmentFields missing convoy_id, got:\n%s", got)
	}
	if !strings.Contains(got, "merge_strategy: direct") {
		t.Errorf("FormatAttachmentFields missing merge_strategy, got:\n%s", got)
	}
	if !strings.Contains(got, "convoy_owned: true") {
		t.Errorf("FormatAttachmentFields missing convoy_owned, got:\n%s", got)
	}
}

func TestConvoyFieldsRoundTrip(t *testing.T) {
	original := &AttachmentFields{
		AttachedMolecule: "gt-wisp-abc",
		DispatchedBy:     "mayor/",
		ConvoyID:         "hq-cv-xyz",
		MergeStrategy:    "direct",
		ConvoyOwned:      true,
	}
	formatted := FormatAttachmentFields(original)
	parsed := ParseAttachmentFields(&Issue{Description: formatted})
	if parsed == nil {
		t.Fatal("round-trip parse returned nil")
	}
	if parsed.ConvoyID != original.ConvoyID {
		t.Errorf("ConvoyID: got %q, want %q", parsed.ConvoyID, original.ConvoyID)
	}
	if parsed.MergeStrategy != original.MergeStrategy {
		t.Errorf("MergeStrategy: got %q, want %q", parsed.MergeStrategy, original.MergeStrategy)
	}
	if parsed.AttachedMolecule != original.AttachedMolecule {
		t.Errorf("AttachedMolecule: got %q, want %q", parsed.AttachedMolecule, original.AttachedMolecule)
	}
	if parsed.ConvoyOwned != original.ConvoyOwned {
		t.Errorf("ConvoyOwned: got %v, want %v", parsed.ConvoyOwned, original.ConvoyOwned)
	}
}

func TestConvoyOwnedFalseNotFormatted(t *testing.T) {
	fields := &AttachmentFields{
		ConvoyID:    "hq-cv-xyz",
		ConvoyOwned: false,
	}
	got := FormatAttachmentFields(fields)
	if strings.Contains(got, "convoy_owned") {
		t.Errorf("FormatAttachmentFields should not include convoy_owned when false, got:\n%s", got)
	}
}

func TestSetAttachmentFieldsPreservesConvoy(t *testing.T) {
	issue := &Issue{
		Description: "convoy_id: hq-cv-old\nmerge_strategy: direct\nconvoy_owned: true\nattached_molecule: gt-wisp-old\nSome other content",
	}
	fields := &AttachmentFields{
		AttachedMolecule: "gt-wisp-new",
		ConvoyID:         "hq-cv-new",
		MergeStrategy:    "local",
		ConvoyOwned:      true,
	}
	newDesc := SetAttachmentFields(issue, fields)
	if !strings.Contains(newDesc, "convoy_id: hq-cv-new") {
		t.Errorf("SetAttachmentFields lost convoy_id field, got:\n%s", newDesc)
	}
	if !strings.Contains(newDesc, "merge_strategy: local") {
		t.Errorf("SetAttachmentFields lost merge_strategy field, got:\n%s", newDesc)
	}
	if !strings.Contains(newDesc, "convoy_owned: true") {
		t.Errorf("SetAttachmentFields lost convoy_owned field, got:\n%s", newDesc)
	}
	if !strings.Contains(newDesc, "Some other content") {
		t.Errorf("SetAttachmentFields lost non-attachment content, got:\n%s", newDesc)
	}
}

// --- FormatConvoyFields / SetConvoyFields ---

func TestFormatConvoyFields(t *testing.T) {
	tests := []struct {
		name   string
		fields *ConvoyFields
		want   string
	}{
		{
			name:   "nil fields",
			fields: nil,
			want:   "",
		},
		{
			name:   "empty fields",
			fields: &ConvoyFields{},
			want:   "",
		},
		{
			name:   "all fields",
			fields: &ConvoyFields{Owner: "mayor/", Notify: "witness/", Merge: "direct", Molecule: "gt-wisp-abc"},
			want:   "Owner: mayor/\nNotify: witness/\nMerge: direct\nMolecule: gt-wisp-abc",
		},
		{
			name:   "only merge",
			fields: &ConvoyFields{Merge: "mr"},
			want:   "Merge: mr",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatConvoyFields(tt.fields)
			if got != tt.want {
				t.Errorf("FormatConvoyFields() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSetConvoyFields(t *testing.T) {
	tests := []struct {
		name   string
		issue  *Issue
		fields *ConvoyFields
		want   string
	}{
		{
			name:   "nil issue",
			issue:  nil,
			fields: &ConvoyFields{Owner: "mayor/", Merge: "direct"},
			want:   "Owner: mayor/\nMerge: direct",
		},
		{
			name:   "preserves prose",
			issue:  &Issue{Description: "Convoy tracking 3 issues"},
			fields: &ConvoyFields{Owner: "mayor/", Merge: "mr"},
			want:   "Convoy tracking 3 issues\nOwner: mayor/\nMerge: mr",
		},
		{
			name:   "replaces existing fields",
			issue:  &Issue{Description: "Convoy tracking 3 issues\nOwner: old/\nMerge: local"},
			fields: &ConvoyFields{Owner: "mayor/", Merge: "direct"},
			want:   "Convoy tracking 3 issues\nOwner: mayor/\nMerge: direct",
		},
		{
			name:   "empty fields removes field lines",
			issue:  &Issue{Description: "Convoy tracking 3 issues\nOwner: mayor/\nMerge: direct"},
			fields: &ConvoyFields{},
			want:   "Convoy tracking 3 issues",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SetConvoyFields(tt.issue, tt.fields)
			if got != tt.want {
				t.Errorf("SetConvoyFields() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConvoyFieldsParseFormatRoundTrip(t *testing.T) {
	original := &ConvoyFields{
		Owner:                "mayor/",
		Notify:               "witness/",
		Merge:                "direct",
		Molecule:             "gt-wisp-abc",
		CompletionNotifiedAt: "2026-05-25T02:30:00Z",
	}
	formatted := FormatConvoyFields(original)
	parsed := ParseConvoyFields(&Issue{Description: formatted})
	if parsed == nil {
		t.Fatal("round-trip parse returned nil")
	}
	if parsed.Owner != original.Owner {
		t.Errorf("Owner: got %q, want %q", parsed.Owner, original.Owner)
	}
	if parsed.Notify != original.Notify {
		t.Errorf("Notify: got %q, want %q", parsed.Notify, original.Notify)
	}
	if parsed.Merge != original.Merge {
		t.Errorf("Merge: got %q, want %q", parsed.Merge, original.Merge)
	}
	if parsed.Molecule != original.Molecule {
		t.Errorf("Molecule: got %q, want %q", parsed.Molecule, original.Molecule)
	}
	if parsed.CompletionNotifiedAt != original.CompletionNotifiedAt {
		t.Errorf("CompletionNotifiedAt: got %q, want %q", parsed.CompletionNotifiedAt, original.CompletionNotifiedAt)
	}
}

func TestSetConvoyFieldsWithMixedContent(t *testing.T) {
	issue := &Issue{Description: "Convoy tracking 3 issues\nOwner: old/\nSome prose line\nMerge: local\nAnother line"}
	fields := &ConvoyFields{Owner: "new/", Merge: "direct", Molecule: "gt-mol-xyz"}
	got := SetConvoyFields(issue, fields)

	// Should preserve non-convoy prose
	if !strings.Contains(got, "Some prose line") {
		t.Errorf("lost prose line, got:\n%s", got)
	}
	if !strings.Contains(got, "Another line") {
		t.Errorf("lost another line, got:\n%s", got)
	}
	// Should have new fields
	if !strings.Contains(got, "Owner: new/") {
		t.Errorf("missing new Owner, got:\n%s", got)
	}
	if !strings.Contains(got, "Merge: direct") {
		t.Errorf("missing Merge, got:\n%s", got)
	}
	if !strings.Contains(got, "Molecule: gt-mol-xyz") {
		t.Errorf("missing Molecule, got:\n%s", got)
	}
	// Should NOT have old fields
	if strings.Contains(got, "Owner: old/") {
		t.Errorf("still has old Owner, got:\n%s", got)
	}
	if strings.Contains(got, "Merge: local") {
		t.Errorf("still has old Merge, got:\n%s", got)
	}
}

// --- ParseAgentFields (not covered in beads_test.go) ---

func TestParseAgentFields_AllFields(t *testing.T) {
	desc := "role_type: polecat\nrig: gastown\nagent_state: working\nhook_bead: gt-abc\ncleanup_status: clean\nactive_mr: gt-mr1\nlast_source_issue: gt-src\nnotification_level: verbose"
	got := ParseAgentFields(desc)
	if got.RoleType != "polecat" {
		t.Errorf("RoleType = %q, want %q", got.RoleType, "polecat")
	}
	if got.Rig != "gastown" {
		t.Errorf("Rig = %q, want %q", got.Rig, "gastown")
	}
	if got.AgentState != "working" {
		t.Errorf("AgentState = %q, want %q", got.AgentState, "working")
	}
	if got.HookBead != "gt-abc" {
		t.Errorf("HookBead = %q, want %q", got.HookBead, "gt-abc")
	}
	if got.CleanupStatus != "clean" {
		t.Errorf("CleanupStatus = %q, want %q", got.CleanupStatus, "clean")
	}
	if got.ActiveMR != "gt-mr1" {
		t.Errorf("ActiveMR = %q, want %q", got.ActiveMR, "gt-mr1")
	}
	if got.LastSourceIssue != "gt-src" {
		t.Errorf("LastSourceIssue = %q, want %q", got.LastSourceIssue, "gt-src")
	}
	if got.NotificationLevel != "verbose" {
		t.Errorf("NotificationLevel = %q, want %q", got.NotificationLevel, "verbose")
	}
}

// --- Completion metadata fields (gt-x7t9) ---

func TestAgentFieldsCompletionMetadataRoundTrip(t *testing.T) {
	original := &AgentFields{
		RoleType:        "polecat",
		Rig:             "gastown",
		AgentState:      "done",
		HookBead:        "gt-abc",
		ExitType:        "COMPLETED",
		MRID:            "gt-mr-xyz",
		Branch:          "polecat/nux/gt-abc@hash",
		LastSourceIssue: "gt-abc",
		MRFailed:        false,
		CompletionTime:  "2026-02-28T01:00:00Z",
	}

	formatted := FormatAgentDescription("Polecat nux", original)

	// Verify all completion fields are present
	if !strings.Contains(formatted, "exit_type: COMPLETED") {
		t.Errorf("missing exit_type in formatted output:\n%s", formatted)
	}
	if !strings.Contains(formatted, "mr_id: gt-mr-xyz") {
		t.Errorf("missing mr_id in formatted output:\n%s", formatted)
	}
	if !strings.Contains(formatted, "branch: polecat/nux/gt-abc@hash") {
		t.Errorf("missing branch in formatted output:\n%s", formatted)
	}
	if !strings.Contains(formatted, "last_source_issue: gt-abc") {
		t.Errorf("missing last_source_issue in formatted output:\n%s", formatted)
	}
	if !strings.Contains(formatted, "completion_time: 2026-02-28T01:00:00Z") {
		t.Errorf("missing completion_time in formatted output:\n%s", formatted)
	}
	// mr_failed=false should NOT appear
	if strings.Contains(formatted, "mr_failed") {
		t.Errorf("mr_failed should not appear when false:\n%s", formatted)
	}

	// Parse and verify round-trip
	parsed := ParseAgentFields(formatted)
	if parsed.ExitType != "COMPLETED" {
		t.Errorf("ExitType: got %q, want %q", parsed.ExitType, "COMPLETED")
	}
	if parsed.MRID != "gt-mr-xyz" {
		t.Errorf("MRID: got %q, want %q", parsed.MRID, "gt-mr-xyz")
	}
	if parsed.Branch != "polecat/nux/gt-abc@hash" {
		t.Errorf("Branch: got %q, want %q", parsed.Branch, "polecat/nux/gt-abc@hash")
	}
	if parsed.LastSourceIssue != "gt-abc" {
		t.Errorf("LastSourceIssue: got %q, want %q", parsed.LastSourceIssue, "gt-abc")
	}
	if parsed.MRFailed != false {
		t.Errorf("MRFailed: got %v, want false", parsed.MRFailed)
	}
	if parsed.CompletionTime != "2026-02-28T01:00:00Z" {
		t.Errorf("CompletionTime: got %q, want %q", parsed.CompletionTime, "2026-02-28T01:00:00Z")
	}
	// Verify non-completion fields survive
	if parsed.RoleType != "polecat" {
		t.Errorf("RoleType: got %q, want %q", parsed.RoleType, "polecat")
	}
	if parsed.HookBead != "gt-abc" {
		t.Errorf("HookBead: got %q, want %q", parsed.HookBead, "gt-abc")
	}
}

func TestAgentFieldsMRFailedTrue(t *testing.T) {
	fields := &AgentFields{
		RoleType:   "polecat",
		Rig:        "gastown",
		AgentState: "done",
		ExitType:   "COMPLETED",
		MRFailed:   true,
	}

	formatted := FormatAgentDescription("Polecat nux", fields)
	if !strings.Contains(formatted, "mr_failed: true") {
		t.Errorf("missing mr_failed: true in formatted output:\n%s", formatted)
	}

	parsed := ParseAgentFields(formatted)
	if !parsed.MRFailed {
		t.Errorf("MRFailed: got false, want true")
	}
}

func TestAgentFieldsCompletionOmittedWhenEmpty(t *testing.T) {
	fields := &AgentFields{
		RoleType:   "polecat",
		Rig:        "gastown",
		AgentState: "working",
		// All completion fields intentionally empty
	}

	formatted := FormatAgentDescription("Polecat nux", fields)
	for _, keyword := range []string{"exit_type:", "mr_id:", "branch:", "last_source_issue:", "mr_failed:", "completion_time:"} {
		if strings.Contains(formatted, keyword) {
			t.Errorf("empty completion field %q should not appear in output:\n%s", keyword, formatted)
		}
	}
}

func TestParseAgentFields_WithCompletionMetadata(t *testing.T) {
	desc := "role_type: polecat\nrig: gastown\nagent_state: done\nhook_bead: gt-abc\nexit_type: ESCALATED\nbranch: polecat/nux/gt-abc@hash\nlast_source_issue: gt-abc\nmr_failed: true\ncompletion_time: 2026-02-28T02:00:00Z"
	got := ParseAgentFields(desc)
	if got.ExitType != "ESCALATED" {
		t.Errorf("ExitType = %q, want %q", got.ExitType, "ESCALATED")
	}
	if got.Branch != "polecat/nux/gt-abc@hash" {
		t.Errorf("Branch = %q, want %q", got.Branch, "polecat/nux/gt-abc@hash")
	}
	if !got.MRFailed {
		t.Errorf("MRFailed = false, want true")
	}
	if got.LastSourceIssue != "gt-abc" {
		t.Errorf("LastSourceIssue = %q, want %q", got.LastSourceIssue, "gt-abc")
	}
	if got.CompletionTime != "2026-02-28T02:00:00Z" {
		t.Errorf("CompletionTime = %q, want %q", got.CompletionTime, "2026-02-28T02:00:00Z")
	}
	if got.MRID != "" {
		t.Errorf("MRID = %q, want empty (not in desc)", got.MRID)
	}
}

// --- Convoy watcher tests ---

func TestConvoyFieldsWatchersRoundTrip(t *testing.T) {
	original := &ConvoyFields{
		Owner:         "mayor/",
		Notify:        "witness/",
		Watchers:      "gastown/crew/mel,gastown/crew/tom",
		NudgeWatchers: "gastown/crew/joe",
	}
	formatted := FormatConvoyFields(original)
	parsed := ParseConvoyFields(&Issue{Description: formatted})
	if parsed == nil {
		t.Fatal("round-trip parse returned nil")
	}
	if parsed.Watchers != original.Watchers {
		t.Errorf("Watchers: got %q, want %q", parsed.Watchers, original.Watchers)
	}
	if parsed.NudgeWatchers != original.NudgeWatchers {
		t.Errorf("NudgeWatchers: got %q, want %q", parsed.NudgeWatchers, original.NudgeWatchers)
	}
}

func TestConvoyFieldsAddWatcher(t *testing.T) {
	f := &ConvoyFields{}

	// First add
	if !f.AddWatcher("gastown/crew/mel") {
		t.Error("AddWatcher should return true for new address")
	}
	if f.Watchers != "gastown/crew/mel" {
		t.Errorf("Watchers = %q, want %q", f.Watchers, "gastown/crew/mel")
	}

	// Second add
	if !f.AddWatcher("gastown/crew/tom") {
		t.Error("AddWatcher should return true for new address")
	}
	if f.Watchers != "gastown/crew/mel,gastown/crew/tom" {
		t.Errorf("Watchers = %q, want %q", f.Watchers, "gastown/crew/mel,gastown/crew/tom")
	}

	// Duplicate add
	if f.AddWatcher("gastown/crew/mel") {
		t.Error("AddWatcher should return false for duplicate")
	}
}

func TestConvoyFieldsAddNudgeWatcher(t *testing.T) {
	f := &ConvoyFields{}

	if !f.AddNudgeWatcher("mayor/") {
		t.Error("AddNudgeWatcher should return true for new address")
	}
	if f.NudgeWatchers != "mayor/" {
		t.Errorf("NudgeWatchers = %q, want %q", f.NudgeWatchers, "mayor/")
	}

	if f.AddNudgeWatcher("mayor/") {
		t.Error("AddNudgeWatcher should return false for duplicate")
	}
}

func TestConvoyFieldsRemoveWatcher(t *testing.T) {
	f := &ConvoyFields{Watchers: "a,b,c"}

	if !f.RemoveWatcher("b") {
		t.Error("RemoveWatcher should return true for existing address")
	}
	if f.Watchers != "a,c" {
		t.Errorf("Watchers = %q, want %q", f.Watchers, "a,c")
	}

	if f.RemoveWatcher("d") {
		t.Error("RemoveWatcher should return false for non-existing address")
	}
}

func TestConvoyFieldsRemoveNudgeWatcher(t *testing.T) {
	f := &ConvoyFields{NudgeWatchers: "x,y"}

	if !f.RemoveNudgeWatcher("x") {
		t.Error("RemoveNudgeWatcher should return true for existing address")
	}
	if f.NudgeWatchers != "y" {
		t.Errorf("NudgeWatchers = %q, want %q", f.NudgeWatchers, "y")
	}
}

func TestNotificationAddressesIncludesWatchers(t *testing.T) {
	f := &ConvoyFields{
		Owner:    "mayor/",
		Notify:   "witness/",
		Watchers: "gastown/crew/mel,mayor/", // mayor/ overlaps with Owner
	}
	addrs := f.NotificationAddresses()

	// Should be deduplicated: mayor/, witness/, gastown/crew/mel
	want := map[string]bool{"mayor/": true, "witness/": true, "gastown/crew/mel": true}
	got := make(map[string]bool)
	for _, a := range addrs {
		got[a] = true
	}
	if len(got) != len(want) {
		t.Errorf("NotificationAddresses: got %v, want %v", addrs, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("NotificationAddresses missing %q, got %v", k, addrs)
		}
	}
}

func TestNudgeNotificationAddresses(t *testing.T) {
	f := &ConvoyFields{
		NudgeWatchers: "gastown/crew/mel,gastown/crew/tom",
	}
	addrs := f.NudgeNotificationAddresses()
	if len(addrs) != 2 {
		t.Errorf("NudgeNotificationAddresses: got %d addresses, want 2", len(addrs))
	}
}

func TestSetConvoyFieldsPreservesWatchers(t *testing.T) {
	issue := &Issue{Description: "Some text\nWatchers: a,b\nnudge_watchers: c"}
	fields := &ConvoyFields{
		Owner:         "new/",
		Watchers:      "a,b,d",
		NudgeWatchers: "c,e",
	}
	got := SetConvoyFields(issue, fields)

	if !strings.Contains(got, "Watchers: a,b,d") {
		t.Errorf("missing updated Watchers, got:\n%s", got)
	}
	if !strings.Contains(got, "nudge_watchers: c,e") {
		t.Errorf("missing updated nudge_watchers, got:\n%s", got)
	}
	if !strings.Contains(got, "Some text") {
		t.Errorf("lost prose, got:\n%s", got)
	}
}
