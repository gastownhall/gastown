package cmd

import "testing"

// si-mefr: a check that exists and a check that RUNS are different claims. The
// detection for si-d6kw is worth nothing sitting unregistered, and dropping one
// Register line is invisible to every test in internal/doctor.
func TestDoctorRegistersWorktreeRefSafetyChecks(t *testing.T) {
	want := map[string]bool{
		"worktree-shared-ref":    false,
		"worktree-armed-staging": false,
		"autosave-refusal":       false,
	}
	for _, check := range newDoctorForCommand("").Checks() {
		if _, ok := want[check.Name()]; ok {
			want[check.Name()] = true
		}
	}
	for name, registered := range want {
		if !registered {
			t.Errorf("check %q is not registered with gt doctor, so it never runs", name)
		}
	}
}

func TestDoctorDoesNotRegisterDoltConfigCheck(t *testing.T) {
	d := newDoctorForCommand("")
	for _, check := range d.Checks() {
		if check.Name() == "dolt-config" {
			t.Fatalf("dolt-config check must not be registered; it writes runtime Dolt keys into tracked .beads/config.yaml")
		}
	}
}
