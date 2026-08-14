package dog

import (
	"encoding/json"
	"testing"
	"time"
)

func TestState_Constants(t *testing.T) {
	tests := []struct {
		name  string
		state State
		want  string
	}{
		{"idle state", StateIdle, "idle"},
		{"working state", StateWorking, "working"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.state) != tt.want {
				t.Errorf("State constant = %q, want %q", tt.state, tt.want)
			}
		})
	}
}

func TestDog_ZeroValues(t *testing.T) {
	var dog Dog

	// Test zero value behavior
	if dog.Name != "" {
		t.Errorf("zero value Dog.Name should be empty, got %q", dog.Name)
	}
	if dog.State != "" {
		t.Errorf("zero value Dog.State should be empty, got %q", dog.State)
	}
	if dog.Worktrees == nil {
		// Worktrees is a map, nil is expected for zero value
	} else if len(dog.Worktrees) != 0 {
		t.Errorf("zero value Dog.Worktrees should be empty, got %d items", len(dog.Worktrees))
	}
}

func TestDogState_JSONMarshaling(t *testing.T) {
	now := time.Now().Round(time.Second)
	dogState := DogState{
		Name:       "test-dog",
		State:      StateWorking,
		LastActive: now,
		Work:       "hq-abc123",
		Worktrees:  map[string]string{"gastown": "/path/to/worktree"},
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	// Marshal to JSON
	data, err := json.Marshal(dogState)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	// Unmarshal back
	var unmarshaled DogState
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if unmarshaled.Name != dogState.Name {
		t.Errorf("After round-trip: Name = %q, want %q", unmarshaled.Name, dogState.Name)
	}
	if unmarshaled.State != dogState.State {
		t.Errorf("After round-trip: State = %q, want %q", unmarshaled.State, dogState.State)
	}
	if unmarshaled.Work != dogState.Work {
		t.Errorf("After round-trip: Work = %q, want %q", unmarshaled.Work, dogState.Work)
	}
}

func TestDogState_OmitEmptyFields(t *testing.T) {
	dogState := DogState{
		Name:       "test-dog",
		State:      StateIdle,
		LastActive: time.Now(),
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		// Work and Worktrees left empty to test omitempty
	}

	data, err := json.Marshal(dogState)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	// Check that empty fields are omitted
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal() to map error = %v", err)
	}

	if _, exists := raw["work"]; exists {
		t.Error("Field 'work' should be omitted when empty")
	}
	if _, exists := raw["work_source_id"]; exists {
		t.Error("Field 'work_source_id' should be omitted when empty")
	}
	if _, exists := raw["worktrees"]; exists {
		t.Error("Field 'worktrees' should be omitted when empty")
	}

	// Required fields should be present
	requiredFields := []string{"name", "state", "last_active", "created_at", "updated_at"}
	for _, field := range requiredFields {
		if _, exists := raw[field]; !exists {
			t.Errorf("Required field '%s' should be present", field)
		}
	}
}

func TestDogState_WithWorktrees(t *testing.T) {
	dogState := DogState{
		Name:      "alpha",
		State:     StateWorking,
		Worktrees: map[string]string{"gastown": "/path/1", "beads": "/path/2"},
	}

	data, err := json.Marshal(dogState)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal() to map error = %v", err)
	}

	worktrees, exists := raw["worktrees"]
	if !exists {
		t.Fatal("Field 'worktrees' should be present when non-empty")
	}

	// Verify it's a map
	worktreesMap, ok := worktrees.(map[string]interface{})
	if !ok {
		t.Fatal("worktrees should be a JSON object")
	}

	if len(worktreesMap) != 2 {
		t.Errorf("worktrees should have 2 entries, got %d", len(worktreesMap))
	}
}

func TestCanClearStateOnly(t *testing.T) {
	tests := []struct {
		name string
		work string
		kind WorkKind
		want bool
	}{
		{name: "plugin kind", work: "plugin:rebuild", kind: WorkKindPlugin, want: true},
		{name: "legacy plugin prefix", work: "plugin:rebuild", kind: "", want: true},
		{name: "bead kind", work: "gt-abc", kind: WorkKindBead, want: false},
		{name: "formula kind", work: "mol-dog-reaper", kind: WorkKindFormula, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanClearStateOnly(tt.work, tt.kind); got != tt.want {
				t.Fatalf("CanClearStateOnly(%q, %q) = %v, want %v", tt.work, tt.kind, got, tt.want)
			}
		})
	}
}
